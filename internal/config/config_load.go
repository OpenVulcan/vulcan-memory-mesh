// config_load.go keeps layered config decoding, YAML compatibility, and environment-backed loading helpers.
// config_load.go 用于承载分层配置解码、YAML 兼容与环境变量加载辅助逻辑。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Load preserves the single-path helper while delegating to the layered multi-path loader.
// Load 用于保留单路径辅助入口，并委托给支持分层覆盖的多路径加载器。
func Load(path string, fallback Config) (Config, error) {
	return LoadPaths([]string{path}, fallback)
}

// LoadPaths loads related data.
// LoadPaths 用于加载相关数据。
func LoadPaths(paths []string, fallback Config) (Config, error) {
	// Normalize the configured paths and preload layered .env files.
	// 规范化配置路径并预加载分层的 .env 文件。
	cfg := fallback
	normalizedPaths := normalizeConfigPaths(paths)
	layerBodies := make(map[string][]byte, len(normalizedPaths))
	referencedEnvKeys, err := collectReferencedEnvKeysFromConfigPaths(normalizedPaths)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	for _, path := range normalizedPaths {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return Config{}, fmt.Errorf("read config: %w", readErr)
		}
		layerBodies[path] = body
	}
	if err := loadDotEnv(normalizedPaths, referencedEnvKeys); err != nil {
		return Config{}, err
	}

	// Read configuration layers in order so later files override earlier ones.
	// 按顺序读取配置层，让后面的文件覆盖前面的值。
	for _, path := range normalizedPaths {
		body := layerBodies[path]
		expandedBody := os.ExpandEnv(string(body))
		expandedBytes, err := decodeConfigLayer(path, []byte(expandedBody))
		if err != nil {
			return Config{}, fmt.Errorf("parse config layer %q: %w", path, err)
		}
		if err := applyLayeredAIKeyOverrideReset(&cfg, expandedBytes); err != nil {
			return Config{}, fmt.Errorf("apply AI key override reset in config layer %q: %w", path, err)
		}
		if err := json.Unmarshal(expandedBytes, &cfg); err != nil {
			return Config{}, fmt.Errorf("unmarshal config layer %q: %w", path, err)
		}
	}

	// Apply environment overrides and then finalize normalization plus validation.
	// 应用环境变量覆盖，然后完成归一化与校验。
	if err := validateRemovedAIEnvOverrides(referencedEnvKeys); err != nil {
		return Config{}, err
	}
	applyEnvOverrides(&cfg, referencedEnvKeys)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// decodeConfigLayer converts one JSON or YAML config layer into JSON bytes so the rest of the loader can keep one validation and merge path.
// decodeConfigLayer 用于把单层 JSON 或 YAML 配置统一转换成 JSON 字节，让后续校验与合并逻辑共用同一条路径。
func decodeConfigLayer(path string, body []byte) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".yaml", ".yml":
		return convertYAMLToJSON(body)
	default:
		return body, nil
	}
}

// convertYAMLToJSON decodes one YAML document and re-encodes it as JSON so json.RawMessage-based layered validation continues to work.
// convertYAMLToJSON 用于把一份 YAML 文档解码后重新编码成 JSON，以便继续复用基于 json.RawMessage 的分层校验逻辑。
func convertYAMLToJSON(body []byte) ([]byte, error) {
	var raw any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	normalized, err := normalizeYAMLValue(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// normalizeYAMLValue recursively rewrites YAML decoder output into JSON-compatible maps and lists with string keys only.
// normalizeYAMLValue 用于递归把 YAML 解码结果改写成只包含字符串键的 JSON 兼容 map/list 结构。
func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("yaml object key %v is not a string", key)
			}
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			out[keyString] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			out = append(out, normalized)
		}
		return out, nil
	default:
		return value, nil
	}
}

// aiKeyFieldPresence tracks whether one config layer explicitly mentions api_keys or nodes so layered embedding overrides can clear stale lower-priority shapes before unmarshal.
// aiKeyFieldPresence 用于记录某一层配置是否显式声明了 api_keys 或 nodes，让 embedding 分层覆盖可以在反序列化前清理低优先级残留形态。
type aiKeyFieldPresence struct {
	HasAPIKey  bool
	HasAPIKeys bool
	HasNodes   bool
}

// applyLayeredAIKeyOverrideReset validates removed AI config modes early and clears stale lower-priority embedding key shapes before one higher-priority config layer is unmarshaled.
// applyLayeredAIKeyOverrideReset 用于在反序列化更高优先级配置层前，提前拒绝已移除的 AI 配置模式，并清理 embedding 的低优先级残留 key 形态。
func applyLayeredAIKeyOverrideReset(cfg *Config, body []byte) error {
	if cfg == nil || len(body) == 0 {
		return nil
	}
	root := make(map[string]json.RawMessage)
	if err := json.Unmarshal(body, &root); err != nil {
		return err
	}
	if err := rejectRemovedAIConfigModes(root); err != nil {
		return err
	}
	if err := resetAIKeyFieldPair(root["embedding"], &cfg.Embedding.APIKeys, &cfg.Embedding.Nodes); err != nil {
		return fmt.Errorf("parse embedding key fields: %w", err)
	}
	return nil
}

// resetAIKeyFieldPair keeps layered config precedence stable by clearing the opposite field only when the current layer chooses exactly one embedding key shape.
// resetAIKeyFieldPair 用于在当前配置层只选择一种 embedding key 写法时清空另一种写法，从而保持分层配置覆盖优先级稳定。
func resetAIKeyFieldPair(sectionBody []byte, many *[]string, nodes *[]AIRoutingNodeConfig) error {
	presence, err := detectAIKeyFieldPresence(sectionBody)
	if err != nil {
		return err
	}
	switch {
	case presence.HasNodes && !presence.HasAPIKeys:
		if many != nil {
			*many = nil
		}
	case presence.HasAPIKeys && !presence.HasNodes:
		if nodes != nil {
			*nodes = nil
		}
	}
	return nil
}

// detectAIKeyFieldPresence inspects one nested config section and reports whether api_key or api_keys was explicitly present in that layer.
// detectAIKeyFieldPresence 用于检查单个嵌套配置段，并报告该层是否显式写入了 api_key 或 api_keys。
func detectAIKeyFieldPresence(sectionBody []byte) (aiKeyFieldPresence, error) {
	if len(sectionBody) == 0 {
		return aiKeyFieldPresence{}, nil
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(sectionBody, &fields); err != nil {
		return aiKeyFieldPresence{}, err
	}
	_, hasAPIKey := fields["api_key"]
	_, hasAPIKeys := fields["api_keys"]
	_, hasNodes := fields["nodes"]
	return aiKeyFieldPresence{
		HasAPIKey:  hasAPIKey,
		HasAPIKeys: hasAPIKeys,
		HasNodes:   hasNodes,
	}, nil
}

// rejectRemovedAIConfigModes blocks deprecated AI config shapes at load time so runtime code only needs to reason about the current prompt-bundle and explicit route contracts.
// rejectRemovedAIConfigModes 用于在加载期阻断已废弃的 AI 配置形态，让运行时代码只需要处理当前的提示词包选择与显式路由契约。
func rejectRemovedAIConfigModes(root map[string]json.RawMessage) error {
	if err := rejectRemovedPromptConfigFields(root["prompts"]); err != nil {
		return err
	}
	if err := rejectRemovedLLMConfigFields(root["llm"]); err != nil {
		return err
	}
	if err := rejectRemovedEmbeddingConfigFields(root["embedding"]); err != nil {
		return err
	}
	if err := rejectRemovedRerankConfigFields(root["rerank"]); err != nil {
		return err
	}
	return nil
}

// rejectRemovedPromptConfigFields rejects the removed prompts.routes map so startup fails fast instead of silently ignoring legacy model-to-folder routing config.
// rejectRemovedPromptConfigFields 用于拒绝已移除的 prompts.routes 映射，避免启动时静默忽略旧的“模型到提示词目录”路由配置。
func rejectRemovedPromptConfigFields(sectionBody []byte) error {
	if len(sectionBody) == 0 {
		return nil
	}
	fields, err := parseSectionFields(sectionBody)
	if err != nil {
		return err
	}
	if _, ok := fields["routes"]; ok {
		return errors.New("prompts.routes has been removed; please use prompts.prompt_language to select one prompt bundle")
	}
	return nil
}

// rejectRemovedLLMConfigFields rejects top-level legacy single-route LLM fields plus removed route-level compatibility fields such as api_key and priority.
// rejectRemovedLLMConfigFields 用于拒绝顶层 legacy 单路由 LLM 字段，以及 route 内已移除的 api_key、priority 等兼容字段。
func rejectRemovedLLMConfigFields(sectionBody []byte) error {
	if len(sectionBody) == 0 {
		return nil
	}
	fields, err := parseSectionFields(sectionBody)
	if err != nil {
		return err
	}
	for _, field := range []string{"provider", "endpoint", "api_key", "api_keys", "rpm", "tpm", "rpd", "nodes", "model", "organization", "project", "params", "model_params", "key_failover"} {
		if _, ok := fields[field]; ok {
			return fmt.Errorf("llm.%s has been removed; please move llm runtime settings into llm.routes[*]", field)
		}
	}
	if err := rejectRemovedLLMRouteFields(fields["routes"]); err != nil {
		return err
	}
	return nil
}

// rejectRemovedLLMRouteFields rejects removed llm-route fields so route-only configs cannot silently keep dead compatibility branches.
// rejectRemovedLLMRouteFields 用于拒绝 llm route 内部已移除的字段，避免 route-only 配置静默保留失效的兼容分支。
func rejectRemovedLLMRouteFields(routesBody []byte) error {
	if len(routesBody) == 0 {
		return nil
	}
	var routes []map[string]json.RawMessage
	if err := json.Unmarshal(routesBody, &routes); err != nil {
		return err
	}
	for idx, route := range routes {
		if _, ok := route["api_key"]; ok {
			return fmt.Errorf("llm.routes[%d].api_key has been removed; please use llm.routes[%d].api_keys", idx, idx)
		}
		if _, ok := route["priority"]; ok {
			return fmt.Errorf("llm.routes[%d].priority has been removed; please use llm.routes[%d].weights.*", idx, idx)
		}
		if err := rejectRemovedAPIKeyInNodes(fmt.Sprintf("llm.routes[%d].nodes", idx), route["nodes"]); err != nil {
			return err
		}
	}
	return nil
}

// rejectRemovedEmbeddingConfigFields rejects the removed singular embedding api_key shape at the top level or inside nodes.
// rejectRemovedEmbeddingConfigFields 用于拒绝 embedding 顶层或节点内部已移除的单值 api_key 写法。
func rejectRemovedEmbeddingConfigFields(sectionBody []byte) error {
	if len(sectionBody) == 0 {
		return nil
	}
	fields, err := parseSectionFields(sectionBody)
	if err != nil {
		return err
	}
	if _, ok := fields["api_key"]; ok {
		return errors.New("embedding.api_key has been removed; please use embedding.api_keys")
	}
	if err := rejectRemovedAPIKeyInNodes("embedding.nodes", fields["nodes"]); err != nil {
		return err
	}
	return nil
}

// rejectRemovedRerankConfigFields rejects top-level legacy single-route rerank fields and the removed singular api_key shape inside routes or nodes.
// rejectRemovedRerankConfigFields 用于拒绝顶层 legacy 单路由 rerank 字段，以及 routes 或 nodes 中已移除的单值 api_key 写法。
func rejectRemovedRerankConfigFields(sectionBody []byte) error {
	if len(sectionBody) == 0 {
		return nil
	}
	fields, err := parseSectionFields(sectionBody)
	if err != nil {
		return err
	}
	for _, field := range []string{"provider", "endpoint", "api_key", "api_keys", "rpm", "tpm", "rpd", "nodes", "model", "timeout", "key_failover"} {
		if _, ok := fields[field]; ok {
			return fmt.Errorf("rerank.%s has been removed; please move rerank runtime settings into rerank.routes[*]", field)
		}
	}
	if err := rejectRemovedAPIKeyInRoutes("rerank.routes", fields["routes"]); err != nil {
		return err
	}
	return nil
}

// rejectRemovedAPIKeyInRoutes rejects the removed singular api_key field inside one route array and inside each nested node list.
// rejectRemovedAPIKeyInRoutes 用于拒绝 route 数组内部以及其嵌套节点列表内部已移除的单值 api_key 字段。
func rejectRemovedAPIKeyInRoutes(label string, routesBody []byte) error {
	if len(routesBody) == 0 {
		return nil
	}
	var routes []map[string]json.RawMessage
	if err := json.Unmarshal(routesBody, &routes); err != nil {
		return err
	}
	for idx, route := range routes {
		if _, ok := route["api_key"]; ok {
			return fmt.Errorf("%s[%d].api_key has been removed; please use %s[%d].api_keys", label, idx, label, idx)
		}
		if err := rejectRemovedAPIKeyInNodes(fmt.Sprintf("%s[%d].nodes", label, idx), route["nodes"]); err != nil {
			return err
		}
	}
	return nil
}

// rejectRemovedAPIKeyInNodes rejects the removed singular api_key field inside one node array.
// rejectRemovedAPIKeyInNodes 用于拒绝节点数组内部已移除的单值 api_key 字段。
func rejectRemovedAPIKeyInNodes(label string, nodesBody []byte) error {
	if len(nodesBody) == 0 {
		return nil
	}
	var nodes []map[string]json.RawMessage
	if err := json.Unmarshal(nodesBody, &nodes); err != nil {
		return err
	}
	for idx, node := range nodes {
		if _, ok := node["api_key"]; ok {
			return fmt.Errorf("%s[%d].api_key has been removed; please use %s[%d].api_keys", label, idx, label, idx)
		}
	}
	return nil
}

// parseSectionFields decodes one nested config object into a raw field map so layered-loading helpers can distinguish “field absent” from “field explicitly provided as zero value”.
// parseSectionFields 用于把单个嵌套配置对象解码成原始字段表，让分层加载辅助逻辑能够区分“字段缺失”和“字段被显式写成零值”。
func parseSectionFields(sectionBody []byte) (map[string]json.RawMessage, error) {
	if len(sectionBody) == 0 {
		return nil, nil
	}
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(sectionBody, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

// normalizeConfigPaths executes the normalizeConfigPaths logic.
// normalizeConfigPaths 用于执行 normalizeConfigPaths 逻辑。
func normalizeConfigPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	for _, raw := range paths {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		cleaned := filepath.Clean(trimmed)
		duplicate := false
		for _, existing := range normalized {
			if existing == cleaned {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		normalized = append(normalized, cleaned)
	}
	return normalized
}

// collectReferencedEnvKeysFromConfigPaths walks every raw config layer and records which ${ENV_NAME} placeholders were explicitly referenced in values.
// collectReferencedEnvKeysFromConfigPaths 用于扫描所有原始配置层中的值，记录哪些 ${ENV_NAME} 占位符被显式引用。
func collectReferencedEnvKeysFromConfigPaths(paths []string) (map[string]struct{}, error) {
	referenced := map[string]struct{}{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		layerValue, err := decodeRawConfigLayer(path, body)
		if err != nil {
			return nil, err
		}
		collectEnvReferencesInValue(layerValue, referenced)
	}
	return referenced, nil
}

// decodeRawConfigLayer decodes one raw JSON/YAML config layer without environment expansion so placeholder discovery can inspect only real config values, not comments.
// decodeRawConfigLayer 用于在不展开环境变量的前提下解码原始 JSON/YAML 配置层，让占位符发现逻辑只检查真实配置值而不是注释文本。
func decodeRawConfigLayer(path string, body []byte) (any, error) {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".yaml", ".yml":
		var raw any
		if err := yaml.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		return normalizeYAMLValue(raw)
	default:
		var raw any
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
}

// collectEnvReferencesInValue recursively visits config values so only placeholders that appear in actual scalar content can opt a field into environment-backed loading.
// collectEnvReferencesInValue 用于递归遍历配置值，让只有真实标量内容里出现的占位符才能把对应字段显式加入环境变量加载白名单。
func collectEnvReferencesInValue(value any, referenced map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			collectEnvReferencesInValue(child, referenced)
		}
	case []any:
		for _, child := range typed {
			collectEnvReferencesInValue(child, referenced)
		}
	case string:
		collectEnvReferencesInString(typed, referenced)
	}
}

// collectEnvReferencesInString extracts ${ENV_NAME} markers from one string so environment participation becomes an explicit opt-in in config values.
// collectEnvReferencesInString 用于从单个字符串中提取 ${ENV_NAME} 标记，使环境变量参与配置解析变成显式 opt-in 行为。
func collectEnvReferencesInString(raw string, referenced map[string]struct{}) {
	if referenced == nil || strings.TrimSpace(raw) == "" {
		return
	}
	for idx := 0; idx < len(raw); idx++ {
		if raw[idx] != '$' || idx+1 >= len(raw) || raw[idx+1] != '{' {
			continue
		}
		start := idx + 2
		end := start
		for end < len(raw) && raw[end] != '}' {
			end++
		}
		if end >= len(raw) {
			return
		}
		key := strings.TrimSpace(raw[start:end])
		if key != "" {
			referenced[key] = struct{}{}
		}
		idx = end
	}
}

// loadDotEnv loads related data.
// loadDotEnv 用于加载相关数据。
func loadDotEnv(configPaths []string, referencedEnvKeys map[string]struct{}) error {
	if len(referencedEnvKeys) == 0 {
		return nil
	}
	// Merge .env files according to the resolved config search order.
	// 按解析后的配置搜索顺序合并 .env 文件。
	mergedEnv := map[string]string{}
	for _, configPath := range configPaths {
		for _, candidate := range dotEnvCandidates(configPath) {
			envMap, err := godotenv.Read(candidate)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("load .env %q: %w", candidate, err)
			}
			for key, value := range envMap {
				if _, ok := referencedEnvKeys[key]; !ok {
					continue
				}
				mergedEnv[key] = value
			}
		}
	}

	// Materialize merged values only when the process environment has not provided them.
	// 仅当进程环境未显式提供值时，才落入合并后的 .env 变量。
	for key, value := range mergedEnv {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %q from .env: %w", key, err)
		}
	}
	return nil
}

// dotEnvCandidates executes the dotEnvCandidates logic.
// dotEnvCandidates 用于执行 dotEnvCandidates 逻辑。
func dotEnvCandidates(configPath string) []string {
	candidates := make([]string, 0, 2)
	seen := map[string]struct{}{}
	addCandidate := func(path string) {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return
		}
		cleaned := filepath.Clean(trimmed)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	if strings.TrimSpace(configPath) != "" {
		if absPath, err := filepath.Abs(configPath); err == nil {
			configDir := filepath.Dir(absPath)
			if strings.EqualFold(filepath.Base(configDir), "configs") {
				addCandidate(filepath.Join(configDir, "..", ".env"))
			}
			addCandidate(filepath.Join(configDir, ".env"))
		}
	}
	return candidates
}

// float64Ptr executes the float64Ptr logic.
// float64Ptr 用于执行 float64Ptr 逻辑。
