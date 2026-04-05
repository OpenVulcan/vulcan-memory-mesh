// config.go implements configuration and prompt loading.
// config.go 用于实现配置与提示词加载。
package config

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Duration wraps time.Duration so config files can accept either duration strings or millisecond numbers.
// Duration 用于包装 time.Duration，让配置文件既能接受时长字符串，也能接受毫秒数。
type Duration struct{ time.Duration }

// UnmarshalJSON executes the UnmarshalJSON logic.
// UnmarshalJSON 用于执行 UnmarshalJSON 逻辑。
func (d *Duration) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		dur, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		d.Duration = dur
		return nil
	}
	var ms int64
	if err := json.Unmarshal(data, &ms); err != nil {
		return err
	}
	d.Duration = time.Duration(ms) * time.Millisecond
	return nil
}

// MarshalJSON executes the MarshalJSON logic.
// MarshalJSON 用于执行 MarshalJSON 逻辑。
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Config is the root runtime configuration loaded before the local application starts.
// Config 用于表示本地应用启动前加载的根配置对象。
type Config struct {
	GRPC               GRPCConfig           `json:"grpc"`
	Logging            LoggingConfig        `json:"logging"`
	PII                PIIConfig            `json:"pii"`
	Noise              NoiseConfig          `json:"noise"`
	Storage            StorageConfig        `json:"storage"`
	SQLite             SQLiteConfig         `json:"sqlite"`
	LanceDB            LanceDBConfig        `json:"lancedb"`
	Postgres           PostgresConfig       `json:"postgres"`
	LLM                LLMConfig            `json:"llm"`
	Embedding          EmbeddingConfig      `json:"embedding"`
	Rerank             RerankConfig         `json:"rerank"`
	Vector             VectorConfig         `json:"vector"`
	Relational         RelationalConfig     `json:"relational"`
	PostAction         PostActionConfig     `json:"post_action"`
	PreCheck           PreCheckConfig       `json:"pre_check"`
	MemoryPipeline     MemoryPipelineConfig `json:"memory_pipeline"`
	Retention          RetentionConfig      `json:"retention"`
	MemoryReplaceScope string               `json:"memory_replace_scope,omitempty"`
}

// GRPCConfig holds listener and timeout settings for the inbound gRPC server.
// GRPCConfig 用于保存入站 gRPC 服务的监听地址和超时配置。
type GRPCConfig struct {
	ListenAddr             string             `json:"listen_addr"`
	MaxReceiveMessageBytes int                `json:"max_receive_message_bytes"`
	RequestTimeout         GRPCRequestTimeout `json:"request_timeout"`
	ShutdownTimeout        Duration           `json:"shutdown_timeout"`
}

// GRPCRequestTimeout groups per-method timeout settings used by the gRPC handlers.
// GRPCRequestTimeout 用于收集各个 gRPC 方法使用的超时配置。
type GRPCRequestTimeout struct {
	Workspace  Duration `json:"workspace"`
	PreCheck   Duration `json:"pre_check"`
	PostAction Duration `json:"post_action"`
}

// LoggingConfig holds the structured logging knobs shared by the local runtime.
// LoggingConfig 用于保存本地运行时共享的结构化日志配置项。
type LoggingConfig struct {
	Level                string `json:"level"`
	Format               string `json:"format"`
	DebugRPCPayloads     bool   `json:"debug_rpc_payloads"`
	ProtectPayloads      bool   `json:"protect_payloads"`
	PayloadEncryptionKey string `json:"payload_encryption_key,omitempty"`
}

// PIIConfig holds the default language used by the fixed pii_rules layout.
// PIIConfig 用于保存固定 pii_rules 目录布局所使用的默认语言。
type PIIConfig struct {
	DefaultLanguage string `json:"default_language"`
}

// NoiseConfig holds the memory-admission filter settings used to reject noisy user/assistant turns before persistence.
// NoiseConfig 用于保存写库前拒绝噪声用户/助手轮次的记忆准入过滤配置。
type NoiseConfig struct {
	Enabled           bool    `json:"enabled"`
	DefaultLanguage   string  `json:"default_language"`
	SemanticEnabled   bool    `json:"semantic_enabled"`
	SemanticThreshold float64 `json:"semantic_threshold"`
}

// StorageConfig controls whether the runtime stays on the historical split stores or switches to one combined PostgreSQL-backed store.
// StorageConfig 用于控制运行时继续使用历史分离存储，还是切换到统一的 PostgreSQL 组合库。
type StorageConfig struct {
	Mode             string `json:"mode"`
	CombinedProvider string `json:"combined_provider"`
}

// SQLiteConfig holds the gRPC endpoint used by the local SQLite gateway for durable SQL-backed data.
// SQLiteConfig 用于保存本地 SQLite 网关的 gRPC 地址与超时配置，承载长期 SQL 数据。
type SQLiteConfig struct {
	Address string   `json:"address"`
	Timeout Duration `json:"timeout"`
}

// LanceDBConfig holds the gRPC endpoint and table settings used by the local LanceDB gateway.
// LanceDBConfig 用于保存本地 LanceDB 网关的 gRPC 地址与表配置。
type LanceDBConfig struct {
	Address      string   `json:"address"`
	Timeout      Duration `json:"timeout"`
	TableName    string   `json:"table_name"`
	VectorColumn string   `json:"vector_column"`
}

// PostgresConfig holds the combined-store connection settings shared by the PostgreSQL dialect runtime.
// PostgresConfig 用于保存 PostgreSQL 方言组合库共享的连接与搜索方言配置。
type PostgresConfig struct {
	DSN                     string   `json:"dsn"`
	Schema                  string   `json:"schema"`
	Flavor                  string   `json:"flavor"`
	QueryTimeout            Duration `json:"query_timeout"`
	ConnectTimeout          Duration `json:"connect_timeout"`
	MaxOpenConns            int      `json:"max_open_conns"`
	MinIdleConns            int      `json:"min_idle_conns"`
	AutoCreateExtensions    bool     `json:"auto_create_extensions"`
	BM25IndexConcurrently   bool     `json:"bm25_index_concurrently"`
	BM25IndexName           string   `json:"bm25_index_name"`
	TRGMSimilarityThreshold float64  `json:"trgm_similarity_threshold"`
	VectorLists             int      `json:"vector_lists"`
	VectorProbes            int      `json:"vector_probes"`
	MigrationBatchSize      int      `json:"migration_batch_size"`
}

// LLMConfig holds the provider and model settings used for intent extraction and other LLM tasks.
// LLMConfig 用于保存意图提取等 LLM 任务使用的 provider 和模型配置。
type LLMConfig struct {
	Provider     string                    `json:"provider"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	APIKey       string                    `json:"api_key,omitempty"`
	APIKeys      []string                  `json:"api_keys,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Organization string                    `json:"organization,omitempty"`
	Project      string                    `json:"project,omitempty"`
	Params       map[string]any            `json:"params,omitempty"`
	ModelParams  map[string]map[string]any `json:"model_params,omitempty"`
	KeyFailover  KeyFailoverConfig         `json:"key_failover,omitempty"`
}

// EmbeddingConfig holds the provider and model settings used when generating recall vectors.
// EmbeddingConfig 用于保存生成召回向量时使用的 provider 和模型配置。
type EmbeddingConfig struct {
	Provider     string                    `json:"provider"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	APIKey       string                    `json:"api_key,omitempty"`
	APIKeys      []string                  `json:"api_keys,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Dimension    int                       `json:"dimension,omitempty"`
	Organization string                    `json:"organization,omitempty"`
	Project      string                    `json:"project,omitempty"`
	Params       map[string]any            `json:"params,omitempty"`
	ModelParams  map[string]map[string]any `json:"model_params,omitempty"`
	KeyFailover  KeyFailoverConfig         `json:"key_failover,omitempty"`
}

// RerankConfig holds the optional second-stage rerank settings used to reorder vector recall hits.
// RerankConfig 用于保存可选的第二阶段重排序配置，让系统在向量召回后重新排序候选。
type RerankConfig struct {
	Enabled     bool              `json:"enabled"`
	Provider    string            `json:"provider,omitempty"`
	Endpoint    string            `json:"endpoint,omitempty"`
	APIKey      string            `json:"api_key,omitempty"`
	APIKeys     []string          `json:"api_keys,omitempty"`
	Model       string            `json:"model,omitempty"`
	TopN        int               `json:"top_n,omitempty"`
	Timeout     Duration          `json:"timeout,omitempty"`
	KeyFailover KeyFailoverConfig `json:"key_failover,omitempty"`
}

// KeyFailoverConfig keeps the in-memory API-key rotation policy for one fixed provider/endpoint/model tuple.
// KeyFailoverConfig 用于保存某个固定 provider/endpoint/model 组合在运行时的内存态 API Key 轮换策略。
type KeyFailoverConfig struct {
	Enabled            bool     `json:"enabled"`
	Policy             string   `json:"policy,omitempty"`
	RespectRetryAfter  bool     `json:"respect_retry_after"`
	RateLimitCooldown  Duration `json:"rate_limit_cooldown,omitempty"`
	QuotaCooldown      Duration `json:"quota_cooldown,omitempty"`
	AuthCooldown       Duration `json:"auth_cooldown,omitempty"`
	ProbeAfterCooldown bool     `json:"probe_after_cooldown"`
}

// VectorConfig selects the vector backend used by recall and long-term memory indexing.
// VectorConfig 用于选择记忆召回和长期记忆索引使用的向量后端。
type VectorConfig struct {
	Provider string `json:"provider"`
}

// RelationalConfig selects the durable storage backend used by hierarchy, session, and turn persistence.
// RelationalConfig 用于选择层级、session 和 turn 持久化所使用的长期存储后端。
type RelationalConfig struct {
	Provider string `json:"provider"`
}

// PostActionConfig controls inbound validation strictness plus the async single-turn extraction knobs,
// while retaining a few threshold fields as backward-compatible no-op configuration entries.
// PostActionConfig 用于控制入站校验严格度和异步单轮提炼参数，
// 同时保留少量阈值字段，作为向后兼容的空操作配置项。
type PostActionConfig struct {
	InputMode                     string   `json:"input_mode"`
	SessionAnalysisTurnThreshold  int      `json:"session_analysis_turn_threshold"`
	SessionAnalysisTokenThreshold int      `json:"session_analysis_token_threshold"`
	SessionAnalysisIdleTimeout    Duration `json:"session_analysis_idle_timeout"`
	SessionAnalysisHistoryTurns   int      `json:"session_analysis_history_turns"`
	SessionAnalysisMaxInputTokens int      `json:"session_analysis_max_input_tokens"`
}

// RetentionConfig keeps the cold-data governance knobs for recycle scanning, turn hot-window buffering, and trash retention.
// RetentionConfig 用于保存冷数据治理相关参数，包括回收扫描周期、turn 热窗口缓冲和回收站保留时长。
type RetentionConfig struct {
	Enabled                     bool     `json:"enabled"`
	RecycleScanInterval         Duration `json:"recycle_scan_interval"`
	TurnKeepExtraTurns          int      `json:"turn_keep_extra_turns"`
	SessionIdleRecycleAfter     Duration `json:"session_idle_recycle_after"`
	TrashRetention              Duration `json:"trash_retention"`
	ProtectPriorityFloor        string   `json:"protect_priority_floor,omitempty"`
	ProtectMemoryLevelFloor     string   `json:"protect_memory_level_floor,omitempty"`
	SkipProtectedSharedMemories bool     `json:"skip_protected_shared_memories"`
}

// PreCheckConfig controls timeout and recall window settings for the pre-check workflow.
// PreCheckConfig 用于控制 pre-check 工作流的超时和召回窗口配置。
type PreCheckConfig struct {
	IntentTimeout       Duration `json:"intent_timeout"`
	TopK                int      `json:"top_k"`
	SearchScope         string   `json:"search_scope,omitempty"`
	SimilarityThreshold float64  `json:"similarity_threshold,omitempty"`
}

// MemoryPipelineConfig controls keyword fan-out, hybrid retrieval, read-time decay, and diversity filtering in the memory recall pipeline.
// MemoryPipelineConfig 用于控制记忆召回流水线中的关键词扇出、混合检索、读时衰减和多样性过滤。
type MemoryPipelineConfig struct {
	MaxSearchKeywords        int      `json:"max_search_keywords"`
	MinSimilarityScore       *float64 `json:"min_similarity_score,omitempty"`
	HybridEnabled            bool     `json:"hybrid_enabled"`
	LexicalPreTokenize       bool     `json:"lexical_pre_tokenize"`
	LexicalTopK              int      `json:"lexical_top_k,omitempty"`
	RRFK                     int      `json:"rrf_k,omitempty"`
	MMREnabled               bool     `json:"mmr_enabled"`
	MMRLambda                float64  `json:"mmr_lambda,omitempty"`
	WeibullEnabled           bool     `json:"weibull_enabled"`
	WeibullShape             float64  `json:"weibull_shape,omitempty"`
	WeibullScaleHours        float64  `json:"weibull_scale_hours,omitempty"`
	WeibullMinMultiplier     float64  `json:"weibull_min_multiplier,omitempty"`
	WeibullReinforceWeight   float64  `json:"weibull_reinforce_weight,omitempty"`
	WeibullCrossSessionBoost float64  `json:"weibull_cross_session_boost,omitempty"`
}

// DefaultLocal executes the DefaultLocal logic.
// DefaultLocal 用于执行 DefaultLocal 逻辑。
func DefaultLocal() Config {
	return Config{
		GRPC: GRPCConfig{
			ListenAddr:             ":8080",
			MaxReceiveMessageBytes: 1 << 20,
			RequestTimeout:         GRPCRequestTimeout{Workspace: Duration{15 * time.Second}, PreCheck: Duration{8 * time.Second}, PostAction: Duration{8 * time.Second}},
			ShutdownTimeout:        Duration{10 * time.Second},
		},
		Logging: LoggingConfig{Level: "info", Format: "text", DebugRPCPayloads: false, ProtectPayloads: false},
		PII:     PIIConfig{DefaultLanguage: "zh-CN"},
		Noise:   NoiseConfig{Enabled: true, DefaultLanguage: "zh-CN", SemanticEnabled: true, SemanticThreshold: 0.88},
		Storage: StorageConfig{Mode: "split", CombinedProvider: "postgres"},
		SQLite:  SQLiteConfig{Address: "127.0.0.1:19501", Timeout: Duration{5 * time.Second}},
		LanceDB: LanceDBConfig{Address: "127.0.0.1:19301", Timeout: Duration{5 * time.Second}, TableName: "vmm_memory_vectors", VectorColumn: "vector"},
		Postgres: PostgresConfig{
			Schema:                  "public",
			Flavor:                  "paradedb",
			QueryTimeout:            Duration{5 * time.Second},
			ConnectTimeout:          Duration{5 * time.Second},
			MaxOpenConns:            10,
			MinIdleConns:            1,
			AutoCreateExtensions:    false,
			BM25IndexConcurrently:   true,
			BM25IndexName:           "vmm_memory_nodes_bm25_idx",
			TRGMSimilarityThreshold: 0.2,
			VectorLists:             100,
			VectorProbes:            10,
			MigrationBatchSize:      500,
		},
		LLM: LLMConfig{
			Provider:    "openai",
			Model:       "gpt-4.1-mini",
			KeyFailover: defaultKeyFailoverConfig(),
		},
		Embedding: EmbeddingConfig{
			Provider:    "openai",
			Model:       "text-embedding-3-large",
			Dimension:   1024,
			KeyFailover: defaultKeyFailoverConfig(),
		},
		Rerank: RerankConfig{
			Enabled:     false,
			Provider:    "dashscope",
			Endpoint:    "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
			Model:       "qwen3-vl-rerank",
			TopN:        8,
			Timeout:     Duration{8 * time.Second},
			KeyFailover: defaultKeyFailoverConfig(),
		},
		Vector: VectorConfig{Provider: "lancedb"},
		Relational: RelationalConfig{
			Provider: "sqlite",
		},
		PostAction: PostActionConfig{
			InputMode:                     "compat",
			SessionAnalysisTurnThreshold:  2,
			SessionAnalysisTokenThreshold: 12000,
			SessionAnalysisIdleTimeout:    Duration{15 * time.Minute},
			SessionAnalysisHistoryTurns:   3,
			SessionAnalysisMaxInputTokens: 6000,
		},
		PreCheck: PreCheckConfig{IntentTimeout: Duration{5 * time.Second}, TopK: 5, SearchScope: "space"},
		MemoryPipeline: MemoryPipelineConfig{
			MaxSearchKeywords:        5,
			MinSimilarityScore:       float64Ptr(0.75),
			HybridEnabled:            true,
			LexicalPreTokenize:       true,
			LexicalTopK:              8,
			RRFK:                     60,
			MMREnabled:               true,
			MMRLambda:                0.75,
			WeibullEnabled:           true,
			WeibullShape:             1.35,
			WeibullScaleHours:        2160,
			WeibullMinMultiplier:     0.4,
			WeibullReinforceWeight:   0.18,
			WeibullCrossSessionBoost: 0.12,
		},
		Retention: RetentionConfig{
			Enabled:                     true,
			RecycleScanInterval:         Duration{30 * time.Minute},
			TurnKeepExtraTurns:          5,
			SessionIdleRecycleAfter:     Duration{360 * time.Hour},
			TrashRetention:              Duration{720 * time.Hour},
			ProtectPriorityFloor:        "P1",
			ProtectMemoryLevelFloor:     "stable",
			SkipProtectedSharedMemories: true,
		},
		MemoryReplaceScope: "project",
	}
}

// Load loads related data.
// Load 用于加载相关数据。
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
	if err := loadDotEnv(normalizedPaths); err != nil {
		return Config{}, err
	}

	// Read configuration layers in order so later files override earlier ones.
	// 按顺序读取配置层，让后面的文件覆盖前面的值。
	for _, path := range normalizedPaths {
		body, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		expandedBody := os.ExpandEnv(string(body))
		expandedBytes := []byte(expandedBody)
		if err := applyLayeredAIKeyOverrideReset(&cfg, expandedBytes); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		if err := json.Unmarshal(expandedBytes, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}

	// Apply environment overrides and then finalize normalization plus validation.
	// 应用环境变量覆盖，然后完成归一化与校验。
	applyEnvOverrides(&cfg)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// aiKeyFieldPresence tracks whether one config layer explicitly mentions the legacy single-key field or the newer key-pool field.
// aiKeyFieldPresence 用于记录某一层配置是否显式声明了旧的单值 key 字段或新的 key 池字段。
type aiKeyFieldPresence struct {
	HasAPIKey  bool
	HasAPIKeys bool
}

// applyLayeredAIKeyOverrideReset clears stale lower-priority key fields before one higher-priority config layer is unmarshaled.
// applyLayeredAIKeyOverrideReset 用于在反序列化更高优先级配置层前，清理来自低优先级层的残留 key 字段。
func applyLayeredAIKeyOverrideReset(cfg *Config, body []byte) error {
	if cfg == nil || len(body) == 0 {
		return nil
	}
	root := make(map[string]json.RawMessage)
	if err := json.Unmarshal(body, &root); err != nil {
		return err
	}
	if err := resetAIKeyFieldPair(root["llm"], &cfg.LLM.APIKey, &cfg.LLM.APIKeys); err != nil {
		return fmt.Errorf("parse llm key fields: %w", err)
	}
	if err := resetAIKeyFieldPair(root["embedding"], &cfg.Embedding.APIKey, &cfg.Embedding.APIKeys); err != nil {
		return fmt.Errorf("parse embedding key fields: %w", err)
	}
	if err := resetAIKeyFieldPair(root["rerank"], &cfg.Rerank.APIKey, &cfg.Rerank.APIKeys); err != nil {
		return fmt.Errorf("parse rerank key fields: %w", err)
	}
	return nil
}

// resetAIKeyFieldPair keeps layered config precedence stable by clearing the opposite field only when the current layer chooses exactly one key shape.
// resetAIKeyFieldPair 用于在当前配置层只选择一种 key 写法时清空另一种写法，从而保持分层配置覆盖优先级稳定。
func resetAIKeyFieldPair(sectionBody []byte, single *string, many *[]string) error {
	presence, err := detectAIKeyFieldPresence(sectionBody)
	if err != nil {
		return err
	}
	switch {
	case presence.HasAPIKey && !presence.HasAPIKeys:
		if many != nil {
			*many = nil
		}
	case presence.HasAPIKeys && !presence.HasAPIKey:
		if single != nil {
			*single = ""
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
	return aiKeyFieldPresence{
		HasAPIKey:  hasAPIKey,
		HasAPIKeys: hasAPIKeys,
	}, nil
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

// loadDotEnv loads related data.
// loadDotEnv 用于加载相关数据。
func loadDotEnv(configPaths []string) error {
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
func float64Ptr(v float64) *float64 { return &v }

// defaultKeyFailoverConfig returns the stable API-key failover defaults shared by fixed-model AI clients.
// defaultKeyFailoverConfig 用于返回固定模型 AI 客户端共享的稳定 API Key 容灾默认值。
func defaultKeyFailoverConfig() KeyFailoverConfig {
	return KeyFailoverConfig{
		Enabled:            true,
		Policy:             "ordered_failover",
		RespectRetryAfter:  true,
		RateLimitCooldown:  Duration{5 * time.Minute},
		QuotaCooldown:      Duration{10 * time.Minute},
		AuthCooldown:       Duration{12 * time.Hour},
		ProbeAfterCooldown: true,
	}
}

// normalizeKeyFailoverPolicyValue canonicalizes the in-memory API-key rotation policy so config defaults, env overrides, and runtime wiring all compare one stable token.
// normalizeKeyFailoverPolicyValue 用于规范化内存态 API Key 轮换策略，让默认值、环境变量覆盖和运行时装配始终比较同一份稳定 token。
func normalizeKeyFailoverPolicyValue(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "round_robin":
		return "round_robin"
	default:
		return "ordered_failover"
	}
}

// splitConfigAPIKeys expands one raw config string into individual API keys using comma, semicolon, or newline separators.
// splitConfigAPIKeys 用于把单个原始配置字符串按逗号、分号或换行分隔为多个独立 API Key。
func splitConfigAPIKeys(raw string) []string {
	replacer := strings.NewReplacer("\r\n", "\n", "\r", "\n", ";", "\n", ",", "\n")
	return strings.Split(replacer.Replace(raw), "\n")
}

// trimStringSlice removes surrounding whitespace and drops empty items from one string slice while preserving order.
// trimStringSlice 用于裁剪字符串切片中的首尾空白并去掉空项，同时保持原始顺序不变。
func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

// normalizeAPIKeys merges the legacy single api_key field with the newer api_keys list, trims separators, and removes duplicates while preserving caller order.
// normalizeAPIKeys 用于合并旧的单值 api_key 与新的 api_keys 列表，裁剪分隔符并在保持顺序的前提下去重。
func normalizeAPIKeys(single string, many []string) []string {
	merged := make([]string, 0, len(many)+1)
	for _, value := range many {
		merged = append(merged, splitConfigAPIKeys(value)...)
	}
	merged = append(merged, splitConfigAPIKeys(single)...)
	merged = trimStringSlice(merged)
	if len(merged) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(merged))
	normalized := make([]string, 0, len(merged))
	for _, value := range merged {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

// normalizeKeyFailoverConfig fills safe in-memory cooldown defaults for one fixed-model API-key pool.
// normalizeKeyFailoverConfig 用于为单个固定模型的 API Key 池补齐安全的内存态冷却默认值。
func normalizeKeyFailoverConfig(cfg *KeyFailoverConfig) {
	if cfg == nil {
		return
	}
	defaults := defaultKeyFailoverConfig()
	cfg.Policy = normalizeKeyFailoverPolicyValue(cfg.Policy)
	if cfg.RateLimitCooldown.Duration <= 0 {
		cfg.RateLimitCooldown = defaults.RateLimitCooldown
	}
	if cfg.QuotaCooldown.Duration <= 0 {
		cfg.QuotaCooldown = defaults.QuotaCooldown
	}
	if cfg.AuthCooldown.Duration <= 0 {
		cfg.AuthCooldown = defaults.AuthCooldown
	}
}

// normalizePreCheckSearchScopeValue canonicalizes the pre-check search-scope enum so config defaults, env overrides, and validation all compare the same token.
// normalizePreCheckSearchScopeValue 用于规范化 pre-check 检索作用域枚举，让配置默认值、环境变量覆盖和校验始终比较同一个 token。
func normalizePreCheckSearchScopeValue(scope string) string {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	switch normalized {
	case "team":
		return "team"
	case "project":
		return "project"
	case "space":
		return "space"
	case "":
		return "space"
	default:
		return normalized
	}
}

// isSupportedPreCheckSearchScopeValue reports whether one caller-provided config token is an explicitly supported pre-check search scope before defaults are applied.
// isSupportedPreCheckSearchScopeValue 用于判断调用方提供的配置 token 在默认值介入前，是否属于受支持的 pre-check 检索作用域。
func isSupportedPreCheckSearchScopeValue(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "team", "space", "project":
		return true
	default:
		return false
	}
}

// normalizeMemoryReplaceScopeValue canonicalizes the dedicated memory-replacement scope enum so config defaults, env overrides, and runtime wiring all compare the same token.
// normalizeMemoryReplaceScopeValue 用于规范化专用记忆更替作用域枚举，让配置默认值、环境变量覆盖和运行时装配始终比较同一个 token。
func normalizeMemoryReplaceScopeValue(scope string) string {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	switch normalized {
	case "session":
		return "session"
	case "team":
		return "team"
	case "space":
		return "space"
	case "project", "":
		return "project"
	default:
		return normalized
	}
}

// isSupportedMemoryReplaceScopeValue reports whether one caller-provided config token is an explicitly supported memory-replacement scope before defaults are applied.
// isSupportedMemoryReplaceScopeValue 用于判断调用方提供的配置 token 在默认值介入前，是否属于受支持的记忆更替作用域。
func isSupportedMemoryReplaceScopeValue(scope string) bool {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "session", "team", "space", "project":
		return true
	default:
		return false
	}
}

// normalizeRetentionPriorityFloorValue canonicalizes the retention protection priority floor so future recycle code can compare one stable label.
// normalizeRetentionPriorityFloorValue 用于规范化 retention 保护优先级下限，让后续回收逻辑可以稳定比较同一标签。
func normalizeRetentionPriorityFloorValue(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "P0":
		return "P0"
	case "P2":
		return "P2"
	case "P1", "":
		return "P1"
	default:
		return strings.ToUpper(strings.TrimSpace(level))
	}
}

// isSupportedRetentionPriorityFloorValue reports whether one configured retention priority floor belongs to the explicit supported enum set.
// isSupportedRetentionPriorityFloorValue 用于判断 retention 优先级下限是否属于当前支持的显式枚举集合。
func isSupportedRetentionPriorityFloorValue(level string) bool {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "P0", "P1", "P2":
		return true
	default:
		return false
	}
}

// normalizeRetentionMemoryLevelFloorValue canonicalizes the retention protection memory-level floor so later lifecycle code can compare one stable token.
// normalizeRetentionMemoryLevelFloorValue 用于规范化 retention 保护记忆等级下限，让后续生命周期逻辑可以稳定比较同一 token。
func normalizeRetentionMemoryLevelFloorValue(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session":
		return "session"
	case "phase":
		return "phase"
	case "persistent":
		return "persistent"
	case "stable", "":
		return "stable"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}

// isSupportedRetentionMemoryLevelFloorValue reports whether one configured retention memory-level floor belongs to the explicit supported enum set.
// isSupportedRetentionMemoryLevelFloorValue 用于判断 retention 记忆等级下限是否属于当前支持的显式枚举集合。
func isSupportedRetentionMemoryLevelFloorValue(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session", "phase", "stable", "persistent":
		return true
	default:
		return false
	}
}

// normalizeStorageModeValue canonicalizes the storage-mode token so config defaults, env overrides, and runtime branching all compare one stable value.
// normalizeStorageModeValue 用于规范化存储模式 token，让默认值、环境变量覆盖和运行时分支始终比较同一份稳定值。
func normalizeStorageModeValue(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "combined":
		return "combined"
	case "split", "":
		return "split"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// normalizeCombinedProviderValue canonicalizes the combined-store provider token so the repo can later expand beyond one PostgreSQL-backed implementation without changing validation style.
// normalizeCombinedProviderValue 用于规范化组合库存储提供方 token，让仓库未来即使扩展到更多实现，也无需改动当前校验风格。
func normalizeCombinedProviderValue(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "postgres", "":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// normalizePostgresFlavorValue canonicalizes the PostgreSQL search flavor token so all call sites route on one stable dialect name.
// normalizePostgresFlavorValue 用于规范化 PostgreSQL 搜索 flavor token，确保各处都基于同一方言名称做分支。
func normalizePostgresFlavorValue(flavor string) string {
	switch strings.ToLower(strings.TrimSpace(flavor)) {
	case "standard":
		return "standard"
	case "paradedb", "":
		return "paradedb"
	default:
		return strings.ToLower(strings.TrimSpace(flavor))
	}
}

// Normalize executes the Normalize logic.
// Normalize 用于执行 Normalize 逻辑。
func (c *Config) Normalize() {
	// Canonicalize runtime-facing string fields first so validation, startup, and adapter selection all observe the same trimmed values.
	// 先规范化面向运行时的字符串字段，让校验、启动和适配器选择都看到同一份裁剪后的值。
	c.normalizeRuntimeStrings()

	// Backfill safe defaults for gRPC timeouts and shutdown behavior.
	// 为 gRPC 超时和关闭行为补齐安全默认值。
	if c.GRPC.RequestTimeout.PreCheck.Duration <= 0 {
		c.GRPC.RequestTimeout.PreCheck = Duration{8 * time.Second}
	}
	if c.GRPC.RequestTimeout.Workspace.Duration <= 0 {
		c.GRPC.RequestTimeout.Workspace = Duration{15 * time.Second}
	}
	if c.GRPC.MaxReceiveMessageBytes <= 0 {
		c.GRPC.MaxReceiveMessageBytes = 1 << 20
	}
	if c.GRPC.RequestTimeout.PostAction.Duration <= 0 {
		c.GRPC.RequestTimeout.PostAction = Duration{8 * time.Second}
	}
	if c.GRPC.ShutdownTimeout.Duration <= 0 {
		c.GRPC.ShutdownTimeout = Duration{10 * time.Second}
	}
	if c.PreCheck.IntentTimeout.Duration <= 0 {
		c.PreCheck.IntentTimeout = Duration{5 * time.Second}
	}
	if c.PreCheck.TopK <= 0 {
		c.PreCheck.TopK = 5
	}
	c.PreCheck.SearchScope = normalizePreCheckSearchScopeValue(c.PreCheck.SearchScope)
	if c.PostAction.SessionAnalysisTurnThreshold <= 0 {
		c.PostAction.SessionAnalysisTurnThreshold = 2
	}
	if c.PostAction.SessionAnalysisTokenThreshold <= 0 {
		c.PostAction.SessionAnalysisTokenThreshold = 12000
	}
	if c.PostAction.SessionAnalysisIdleTimeout.Duration <= 0 {
		c.PostAction.SessionAnalysisIdleTimeout = Duration{15 * time.Minute}
	}
	if c.PostAction.SessionAnalysisHistoryTurns <= 0 {
		c.PostAction.SessionAnalysisHistoryTurns = 3
	}
	if c.PostAction.SessionAnalysisMaxInputTokens <= 0 {
		c.PostAction.SessionAnalysisMaxInputTokens = 6000
	}
	c.MemoryReplaceScope = normalizeMemoryReplaceScopeValue(c.MemoryReplaceScope)
	if c.Retention.RecycleScanInterval.Duration <= 0 {
		c.Retention.RecycleScanInterval = Duration{30 * time.Minute}
	}
	if c.Retention.TurnKeepExtraTurns < 0 {
		c.Retention.TurnKeepExtraTurns = 5
	}
	if c.Retention.SessionIdleRecycleAfter.Duration <= 0 {
		c.Retention.SessionIdleRecycleAfter = Duration{360 * time.Hour}
	}
	if c.Retention.TrashRetention.Duration <= 0 {
		c.Retention.TrashRetention = Duration{720 * time.Hour}
	}
	c.Retention.ProtectPriorityFloor = normalizeRetentionPriorityFloorValue(c.Retention.ProtectPriorityFloor)
	c.Retention.ProtectMemoryLevelFloor = normalizeRetentionMemoryLevelFloorValue(c.Retention.ProtectMemoryLevelFloor)

	// Clamp memory pipeline knobs to keep recall fan-out predictable.
	// 对记忆流水线参数做钳制，保持召回扇出可控。
	if c.MemoryPipeline.MaxSearchKeywords <= 0 {
		c.MemoryPipeline.MaxSearchKeywords = 5
	}
	if c.MemoryPipeline.MaxSearchKeywords > 10 {
		c.MemoryPipeline.MaxSearchKeywords = 10
	}
	if c.MemoryPipeline.LexicalTopK <= 0 {
		c.MemoryPipeline.LexicalTopK = 8
	}
	if c.MemoryPipeline.RRFK <= 0 {
		c.MemoryPipeline.RRFK = 60
	}
	if c.MemoryPipeline.MMRLambda <= 0 || c.MemoryPipeline.MMRLambda > 1 {
		c.MemoryPipeline.MMRLambda = 0.75
	}
	if c.MemoryPipeline.WeibullShape <= 0 {
		c.MemoryPipeline.WeibullShape = 1.35
	}
	if c.MemoryPipeline.WeibullScaleHours <= 0 {
		c.MemoryPipeline.WeibullScaleHours = 2160
	}
	if c.MemoryPipeline.WeibullMinMultiplier < 0 || c.MemoryPipeline.WeibullMinMultiplier > 1 {
		c.MemoryPipeline.WeibullMinMultiplier = 0.4
	}
	if c.MemoryPipeline.WeibullReinforceWeight < 0 {
		c.MemoryPipeline.WeibullReinforceWeight = 0.18
	}
	if c.MemoryPipeline.WeibullCrossSessionBoost < 0 {
		c.MemoryPipeline.WeibullCrossSessionBoost = 0.12
	}
	if c.MemoryPipeline.MinSimilarityScore == nil {
		if c.PreCheck.SimilarityThreshold > 0 {
			c.MemoryPipeline.MinSimilarityScore = float64Ptr(c.PreCheck.SimilarityThreshold)
		} else {
			c.MemoryPipeline.MinSimilarityScore = float64Ptr(0.75)
		}
	}
	c.LLM.APIKeys = normalizeAPIKeys(c.LLM.APIKey, c.LLM.APIKeys)
	if len(c.LLM.APIKeys) > 0 {
		c.LLM.APIKey = c.LLM.APIKeys[0]
	}
	normalizeKeyFailoverConfig(&c.LLM.KeyFailover)
	if c.Embedding.Dimension <= 0 && isOpenAIProvider(c.Embedding.Provider) {
		c.Embedding.Dimension = 1024
	}
	c.Embedding.APIKeys = normalizeAPIKeys(c.Embedding.APIKey, c.Embedding.APIKeys)
	if len(c.Embedding.APIKeys) > 0 {
		c.Embedding.APIKey = c.Embedding.APIKeys[0]
	}
	normalizeKeyFailoverConfig(&c.Embedding.KeyFailover)
	if strings.TrimSpace(c.Rerank.Provider) == "" {
		c.Rerank.Provider = "dashscope"
	}
	if strings.TrimSpace(c.Rerank.Endpoint) == "" {
		c.Rerank.Endpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"
	}
	if strings.TrimSpace(c.Rerank.Model) == "" {
		c.Rerank.Model = "qwen3-vl-rerank"
	}
	if c.Rerank.TopN <= 0 {
		c.Rerank.TopN = 8
	}
	if c.Rerank.Timeout.Duration <= 0 {
		c.Rerank.Timeout = Duration{8 * time.Second}
	}
	c.Rerank.APIKeys = normalizeAPIKeys(c.Rerank.APIKey, c.Rerank.APIKeys)
	if len(c.Rerank.APIKeys) > 0 {
		c.Rerank.APIKey = c.Rerank.APIKeys[0]
	}
	normalizeKeyFailoverConfig(&c.Rerank.KeyFailover)
	if strings.TrimSpace(c.Logging.Level) == "" {
		c.Logging.Level = "info"
	}
	if strings.TrimSpace(c.Logging.Format) == "" {
		c.Logging.Format = "text"
	}
	if strings.TrimSpace(c.PII.DefaultLanguage) == "" {
		c.PII.DefaultLanguage = "zh-CN"
	}
	if strings.TrimSpace(c.Noise.DefaultLanguage) == "" {
		c.Noise.DefaultLanguage = c.PII.DefaultLanguage
	}
	if c.Noise.SemanticThreshold <= 0 {
		c.Noise.SemanticThreshold = 0.88
	}
	c.Storage.Mode = normalizeStorageModeValue(c.Storage.Mode)
	c.Storage.CombinedProvider = normalizeCombinedProviderValue(c.Storage.CombinedProvider)
	if strings.TrimSpace(c.SQLite.Address) == "" {
		c.SQLite.Address = "127.0.0.1:19501"
	}
	if c.SQLite.Timeout.Duration <= 0 {
		c.SQLite.Timeout = Duration{5 * time.Second}
	}
	if strings.TrimSpace(c.LanceDB.Address) == "" {
		c.LanceDB.Address = "127.0.0.1:19301"
	}
	if c.LanceDB.Timeout.Duration <= 0 {
		c.LanceDB.Timeout = Duration{5 * time.Second}
	}
	if strings.TrimSpace(c.LanceDB.TableName) == "" {
		c.LanceDB.TableName = "vmm_memory_vectors"
	}
	if strings.TrimSpace(c.LanceDB.VectorColumn) == "" {
		c.LanceDB.VectorColumn = "vector"
	}
	if strings.TrimSpace(c.Postgres.Schema) == "" {
		c.Postgres.Schema = "public"
	}
	c.Postgres.Flavor = normalizePostgresFlavorValue(c.Postgres.Flavor)
	if c.Postgres.QueryTimeout.Duration <= 0 {
		c.Postgres.QueryTimeout = Duration{5 * time.Second}
	}
	if c.Postgres.ConnectTimeout.Duration <= 0 {
		c.Postgres.ConnectTimeout = Duration{5 * time.Second}
	}
	if c.Postgres.MaxOpenConns <= 0 {
		c.Postgres.MaxOpenConns = 10
	}
	if c.Postgres.MinIdleConns < 0 {
		c.Postgres.MinIdleConns = 1
	}
	if strings.TrimSpace(c.Postgres.BM25IndexName) == "" {
		c.Postgres.BM25IndexName = "vmm_memory_nodes_bm25_idx"
	}
	if c.Postgres.TRGMSimilarityThreshold <= 0 {
		c.Postgres.TRGMSimilarityThreshold = 0.2
	}
	if c.Postgres.VectorLists <= 0 {
		c.Postgres.VectorLists = 100
	}
	if c.Postgres.VectorProbes <= 0 {
		c.Postgres.VectorProbes = 10
	}
	if c.Postgres.MigrationBatchSize <= 0 {
		c.Postgres.MigrationBatchSize = 500
	}
	if strings.TrimSpace(c.Vector.Provider) == "" {
		c.Vector.Provider = "lancedb"
	}
	c.PostAction.InputMode = strings.ToLower(strings.TrimSpace(c.PostAction.InputMode))
	if c.PostAction.InputMode == "" {
		c.PostAction.InputMode = "compat"
	}

	// Default durable local data paths to the gateway-backed SQLite implementation only.
	// 为本地持久化数据路径只保留基于网关的 SQLite 实现。
	if strings.TrimSpace(c.Relational.Provider) == "" {
		c.Relational.Provider = "sqlite"
	}
}

// normalizeRuntimeStrings trims user-provided string fields that participate in runtime wiring so config validation and runtime composition stay aligned even when configs contain accidental surrounding whitespace.
// normalizeRuntimeStrings 用于裁剪参与运行时装配的用户字符串字段，保证即使配置里带了意外的首尾空白，配置校验与运行时装配也能保持一致。
func (c *Config) normalizeRuntimeStrings() {
	c.GRPC.ListenAddr = strings.TrimSpace(c.GRPC.ListenAddr)
	c.Logging.Level = strings.TrimSpace(c.Logging.Level)
	c.Logging.Format = strings.TrimSpace(c.Logging.Format)
	c.Logging.PayloadEncryptionKey = strings.TrimSpace(c.Logging.PayloadEncryptionKey)
	c.PII.DefaultLanguage = strings.TrimSpace(c.PII.DefaultLanguage)
	c.Noise.DefaultLanguage = strings.TrimSpace(c.Noise.DefaultLanguage)
	c.Storage.Mode = strings.TrimSpace(c.Storage.Mode)
	c.Storage.CombinedProvider = strings.TrimSpace(c.Storage.CombinedProvider)
	c.SQLite.Address = strings.TrimSpace(c.SQLite.Address)
	c.LanceDB.Address = strings.TrimSpace(c.LanceDB.Address)
	c.LanceDB.TableName = strings.TrimSpace(c.LanceDB.TableName)
	c.LanceDB.VectorColumn = strings.TrimSpace(c.LanceDB.VectorColumn)
	c.Postgres.DSN = strings.TrimSpace(c.Postgres.DSN)
	c.Postgres.Schema = strings.TrimSpace(c.Postgres.Schema)
	c.Postgres.Flavor = strings.TrimSpace(c.Postgres.Flavor)
	c.Postgres.BM25IndexName = strings.TrimSpace(c.Postgres.BM25IndexName)
	c.PreCheck.SearchScope = strings.TrimSpace(c.PreCheck.SearchScope)
	c.MemoryReplaceScope = strings.TrimSpace(c.MemoryReplaceScope)
	c.LLM.Provider = strings.TrimSpace(c.LLM.Provider)
	c.LLM.Endpoint = strings.TrimSpace(c.LLM.Endpoint)
	c.LLM.APIKey = strings.TrimSpace(c.LLM.APIKey)
	c.LLM.APIKeys = trimStringSlice(c.LLM.APIKeys)
	c.LLM.Model = strings.TrimSpace(c.LLM.Model)
	c.LLM.Organization = strings.TrimSpace(c.LLM.Organization)
	c.LLM.Project = strings.TrimSpace(c.LLM.Project)
	c.LLM.KeyFailover.Policy = strings.TrimSpace(c.LLM.KeyFailover.Policy)
	c.Embedding.Provider = strings.TrimSpace(c.Embedding.Provider)
	c.Embedding.Endpoint = strings.TrimSpace(c.Embedding.Endpoint)
	c.Embedding.APIKey = strings.TrimSpace(c.Embedding.APIKey)
	c.Embedding.APIKeys = trimStringSlice(c.Embedding.APIKeys)
	c.Embedding.Model = strings.TrimSpace(c.Embedding.Model)
	c.Embedding.Organization = strings.TrimSpace(c.Embedding.Organization)
	c.Embedding.Project = strings.TrimSpace(c.Embedding.Project)
	c.Embedding.KeyFailover.Policy = strings.TrimSpace(c.Embedding.KeyFailover.Policy)
	c.Rerank.Provider = strings.TrimSpace(c.Rerank.Provider)
	c.Rerank.Endpoint = strings.TrimSpace(c.Rerank.Endpoint)
	c.Rerank.APIKey = strings.TrimSpace(c.Rerank.APIKey)
	c.Rerank.APIKeys = trimStringSlice(c.Rerank.APIKeys)
	c.Rerank.Model = strings.TrimSpace(c.Rerank.Model)
	c.Rerank.KeyFailover.Policy = strings.TrimSpace(c.Rerank.KeyFailover.Policy)
	c.Vector.Provider = strings.TrimSpace(c.Vector.Provider)
	c.Relational.Provider = strings.TrimSpace(c.Relational.Provider)
	c.PostAction.InputMode = strings.TrimSpace(c.PostAction.InputMode)
	c.Retention.ProtectPriorityFloor = strings.TrimSpace(c.Retention.ProtectPriorityFloor)
	c.Retention.ProtectMemoryLevelFloor = strings.TrimSpace(c.Retention.ProtectMemoryLevelFloor)
}

// Validate validates the input value.
// Validate 用于校验输入值。
func (c Config) Validate() error {
	// Verify the minimum runtime contract before the application starts.
	// 在应用启动前验证最小运行时契约。
	if strings.TrimSpace(c.GRPC.ListenAddr) == "" {
		return errors.New("grpc.listen_addr is required")
	}
	if strings.TrimSpace(c.PII.DefaultLanguage) == "" {
		return errors.New("pii.default_language is required")
	}
	if strings.TrimSpace(c.Noise.DefaultLanguage) == "" {
		return errors.New("noise.default_language is required")
	}
	switch normalizeStorageModeValue(c.Storage.Mode) {
	case "split", "combined":
	default:
		return errors.New("storage.mode must be either split or combined")
	}
	if c.GRPC.MaxReceiveMessageBytes <= 0 {
		return errors.New("grpc.max_receive_message_bytes must be > 0")
	}
	switch strings.ToLower(strings.TrimSpace(c.Logging.Level)) {
	case "", "debug", "info", "warn", "warning", "error":
	default:
		return errors.New("logging.level must be one of debug, info, warn, error")
	}
	switch strings.ToLower(strings.TrimSpace(c.Logging.Format)) {
	case "text", "json":
	default:
		return errors.New("logging.format must be either text or json")
	}
	if c.Logging.ProtectPayloads {
		if _, err := validatePayloadEncryptionKey(c.Logging.PayloadEncryptionKey); err != nil {
			return fmt.Errorf("logging.payload_encryption_key is invalid: %w", err)
		}
	}
	if c.PreCheck.TopK <= 0 {
		return errors.New("precheck.top_k must be > 0")
	}
	if scope := strings.TrimSpace(c.PreCheck.SearchScope); scope != "" && !isSupportedPreCheckSearchScopeValue(scope) {
		return errors.New("pre_check.search_scope must be one of team, space, or project")
	}
	if scope := strings.TrimSpace(c.MemoryReplaceScope); scope != "" && !isSupportedMemoryReplaceScopeValue(scope) {
		return errors.New("memory_replace_scope must be one of session, team, space, or project")
	}
	if c.GRPC.RequestTimeout.PreCheck.Duration <= c.PreCheck.IntentTimeout.Duration {
		return errors.New("grpc.request_timeout.pre_check must be greater than pre_check.intent_timeout")
	}
	if c.MemoryPipeline.MaxSearchKeywords <= 0 || c.MemoryPipeline.MaxSearchKeywords > 10 {
		return errors.New("memory_pipeline.max_search_keywords must be in [1,10]")
	}
	if c.MemoryPipeline.LexicalTopK <= 0 {
		return errors.New("memory_pipeline.lexical_top_k must be > 0")
	}
	if c.MemoryPipeline.RRFK <= 0 {
		return errors.New("memory_pipeline.rrf_k must be > 0")
	}
	if c.MemoryPipeline.MMRLambda <= 0 || c.MemoryPipeline.MMRLambda > 1 {
		return errors.New("memory_pipeline.mmr_lambda must be in (0,1]")
	}
	if c.MemoryPipeline.WeibullShape <= 0 {
		return errors.New("memory_pipeline.weibull_shape must be > 0")
	}
	if c.MemoryPipeline.WeibullScaleHours <= 0 {
		return errors.New("memory_pipeline.weibull_scale_hours must be > 0")
	}
	if c.MemoryPipeline.WeibullMinMultiplier < 0 || c.MemoryPipeline.WeibullMinMultiplier > 1 {
		return errors.New("memory_pipeline.weibull_min_multiplier must be in [0,1]")
	}
	if c.MemoryPipeline.WeibullReinforceWeight < 0 {
		return errors.New("memory_pipeline.weibull_reinforce_weight must be >= 0")
	}
	if c.MemoryPipeline.WeibullCrossSessionBoost < 0 {
		return errors.New("memory_pipeline.weibull_cross_session_boost must be >= 0")
	}
	if c.MemoryPipeline.MinSimilarityScore == nil {
		return errors.New("memory_pipeline.min_similarity_score must be set")
	}
	if *c.MemoryPipeline.MinSimilarityScore < 0 || *c.MemoryPipeline.MinSimilarityScore > 1 {
		return errors.New("memory_pipeline.min_similarity_score must be in [0,1]")
	}
	if c.Noise.SemanticThreshold < 0 || c.Noise.SemanticThreshold > 1 {
		return errors.New("noise.semantic_threshold must be in [0,1]")
	}
	if strings.TrimSpace(c.LLM.Provider) == "" || strings.TrimSpace(c.Embedding.Provider) == "" {
		return errors.New("provider fields are required")
	}
	if !isOpenAIProvider(c.LLM.Provider) {
		return errors.New("llm.provider must use one openai-compatible provider")
	}
	if !isOpenAIProvider(c.Embedding.Provider) {
		return errors.New("embedding.provider must use one openai-compatible provider")
	}
	if normalizeStorageModeValue(c.Storage.Mode) == "split" {
		if strings.TrimSpace(c.LanceDB.Address) == "" {
			return errors.New("lancedb.address is required")
		}
		if strings.TrimSpace(c.LanceDB.TableName) == "" {
			return errors.New("lancedb.table_name is required")
		}
		if strings.TrimSpace(c.LanceDB.VectorColumn) == "" {
			return errors.New("lancedb.vector_column is required")
		}
		switch strings.ToLower(strings.TrimSpace(c.Vector.Provider)) {
		case "lancedb":
		default:
			return errors.New("vector.provider must be lancedb")
		}
		switch strings.ToLower(strings.TrimSpace(c.Relational.Provider)) {
		case "sqlite":
			if strings.TrimSpace(c.SQLite.Address) == "" {
				return errors.New("sqlite.address is required")
			}
		default:
			return errors.New("relational.provider must be sqlite")
		}
	} else {
		switch normalizeCombinedProviderValue(c.Storage.CombinedProvider) {
		case "postgres":
		default:
			return errors.New("storage.combined_provider must be postgres")
		}
		if strings.TrimSpace(c.Postgres.DSN) == "" {
			return errors.New("postgres.dsn is required when storage.mode=combined")
		}
		if strings.TrimSpace(c.Postgres.Schema) == "" {
			return errors.New("postgres.schema is required when storage.mode=combined")
		}
		switch normalizePostgresFlavorValue(c.Postgres.Flavor) {
		case "paradedb", "standard":
		default:
			return errors.New("postgres.flavor must be either paradedb or standard")
		}
		if c.Postgres.QueryTimeout.Duration <= 0 {
			return errors.New("postgres.query_timeout must be > 0 when storage.mode=combined")
		}
		if c.Postgres.ConnectTimeout.Duration <= 0 {
			return errors.New("postgres.connect_timeout must be > 0 when storage.mode=combined")
		}
		if c.Postgres.MaxOpenConns <= 0 {
			return errors.New("postgres.max_open_conns must be > 0 when storage.mode=combined")
		}
		if c.Postgres.MinIdleConns < 0 {
			return errors.New("postgres.min_idle_conns must be >= 0 when storage.mode=combined")
		}
		if strings.TrimSpace(c.Postgres.BM25IndexName) == "" {
			return errors.New("postgres.bm25_index_name is required when storage.mode=combined")
		}
		if c.Postgres.TRGMSimilarityThreshold <= 0 || c.Postgres.TRGMSimilarityThreshold > 1 {
			return errors.New("postgres.trgm_similarity_threshold must be in (0,1] when storage.mode=combined")
		}
		if c.Postgres.VectorLists <= 0 {
			return errors.New("postgres.vector_lists must be > 0 when storage.mode=combined")
		}
		if c.Postgres.VectorProbes <= 0 {
			return errors.New("postgres.vector_probes must be > 0 when storage.mode=combined")
		}
		if c.Postgres.MigrationBatchSize <= 0 {
			return errors.New("postgres.migration_batch_size must be > 0 when storage.mode=combined")
		}
	}
	if strings.TrimSpace(c.LLM.Endpoint) == "" {
		return errors.New("llm.endpoint is required")
	}
	if len(normalizeAPIKeys(c.LLM.APIKey, c.LLM.APIKeys)) == 0 {
		return errors.New("llm.api_key or llm.api_keys is required")
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		return errors.New("llm.model is required")
	}
	if strings.TrimSpace(c.Embedding.Endpoint) == "" {
		return errors.New("embedding.endpoint is required")
	}
	if len(normalizeAPIKeys(c.Embedding.APIKey, c.Embedding.APIKeys)) == 0 {
		return errors.New("embedding.api_key or embedding.api_keys is required")
	}
	if strings.TrimSpace(c.Embedding.Model) == "" {
		return errors.New("embedding.model is required")
	}
	if c.Embedding.Dimension <= 0 {
		return errors.New("embedding.dimension must be > 0")
	}
	if c.Rerank.Enabled {
		switch strings.ToLower(strings.TrimSpace(c.Rerank.Provider)) {
		case "dashscope":
		default:
			return errors.New("rerank.provider must be dashscope when rerank is enabled")
		}
		if strings.TrimSpace(c.Rerank.Endpoint) == "" {
			return errors.New("rerank.endpoint is required when rerank is enabled")
		}
		if strings.TrimSpace(c.Rerank.Model) == "" {
			return errors.New("rerank.model is required when rerank is enabled")
		}
		if len(normalizeAPIKeys(c.Rerank.APIKey, c.Rerank.APIKeys)) == 0 {
			return errors.New("rerank.api_key or rerank.api_keys is required when rerank is enabled")
		}
		if c.Rerank.TopN <= 0 {
			return errors.New("rerank.top_n must be > 0 when rerank is enabled")
		}
		if c.Rerank.Timeout.Duration <= 0 {
			return errors.New("rerank.timeout must be > 0 when rerank is enabled")
		}
	}
	switch c.PostAction.InputMode {
	case "strict", "compat":
	default:
		return errors.New("post_action.input_mode must be either strict or compat")
	}
	if c.PostAction.SessionAnalysisTurnThreshold <= 0 {
		return errors.New("post_action.session_analysis_turn_threshold must be > 0")
	}
	if c.PostAction.SessionAnalysisTokenThreshold <= 0 {
		return errors.New("post_action.session_analysis_token_threshold must be > 0")
	}
	if c.PostAction.SessionAnalysisIdleTimeout.Duration <= 0 {
		return errors.New("post_action.session_analysis_idle_timeout must be > 0")
	}
	if c.PostAction.SessionAnalysisHistoryTurns <= 0 {
		return errors.New("post_action.session_analysis_history_turns must be > 0")
	}
	if c.PostAction.SessionAnalysisMaxInputTokens <= 0 {
		return errors.New("post_action.session_analysis_max_input_tokens must be > 0")
	}
	if c.Retention.RecycleScanInterval.Duration <= 0 {
		return errors.New("retention.recycle_scan_interval must be > 0")
	}
	if c.Retention.TurnKeepExtraTurns < 0 {
		return errors.New("retention.turn_keep_extra_turns must be >= 0")
	}
	if c.Retention.SessionIdleRecycleAfter.Duration <= 0 {
		return errors.New("retention.session_idle_recycle_after must be > 0")
	}
	if c.Retention.TrashRetention.Duration <= 0 {
		return errors.New("retention.trash_retention must be > 0")
	}
	if c.Retention.SessionIdleRecycleAfter.Duration < 360*time.Hour {
		return errors.New("retention.session_idle_recycle_after must be >= 360h")
	}
	if level := strings.TrimSpace(c.Retention.ProtectPriorityFloor); level != "" && !isSupportedRetentionPriorityFloorValue(level) {
		return errors.New("retention.protect_priority_floor must be one of P0, P1, or P2")
	}
	if level := strings.TrimSpace(c.Retention.ProtectMemoryLevelFloor); level != "" && !isSupportedRetentionMemoryLevelFloorValue(level) {
		return errors.New("retention.protect_memory_level_floor must be one of session, phase, stable, or persistent")
	}
	return nil
}

// applyEnvOverrides applies the target settings.
// applyEnvOverrides 用于应用目标设置。
func applyEnvOverrides(cfg *Config) {
	// Reapply explicit process-level overrides after file-based expansion.
	// 在文件占位符展开之后，再次应用进程级显式覆盖。
	setString := func(k string, target *string) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = v
		}
	}
	setSingleKey := func(k string, target *string, pool *[]string) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = v
			if pool != nil {
				*pool = nil
			}
		}
	}
	setStringSlice := func(k string, target *[]string, legacy *string) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = splitConfigAPIKeys(v)
			if legacy != nil {
				*legacy = ""
			}
		}
	}
	setInt := func(k string, target *int) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*target = n
			}
		}
	}
	setFloat := func(k string, target *float64) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = n
			}
		}
	}
	setOptionalFloat := func(k string, target **float64) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = float64Ptr(n)
			}
		}
	}
	setBool := func(k string, target *bool) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseBool(v); err == nil {
				*target = n
			}
		}
	}
	setDuration := func(k string, target *Duration) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := time.ParseDuration(v); err == nil {
				target.Duration = n
			}
		}
	}

	setString("VMM_GRPC_LISTEN_ADDR", &cfg.GRPC.ListenAddr)
	setInt("VMM_GRPC_MAX_RECEIVE_MESSAGE_BYTES", &cfg.GRPC.MaxReceiveMessageBytes)
	setDuration("VMM_GRPC_WORKSPACE_TIMEOUT", &cfg.GRPC.RequestTimeout.Workspace)
	setDuration("VMM_GRPC_PRE_CHECK_TIMEOUT", &cfg.GRPC.RequestTimeout.PreCheck)
	setDuration("VMM_GRPC_POST_ACTION_TIMEOUT", &cfg.GRPC.RequestTimeout.PostAction)
	setDuration("VMM_GRPC_SHUTDOWN_TIMEOUT", &cfg.GRPC.ShutdownTimeout)
	setString("VMM_LOG_LEVEL", &cfg.Logging.Level)
	setString("VMM_LOG_FORMAT", &cfg.Logging.Format)
	setBool("VMM_LOG_DEBUG_RPC_PAYLOADS", &cfg.Logging.DebugRPCPayloads)
	setBool("VMM_LOG_PROTECT_PAYLOADS", &cfg.Logging.ProtectPayloads)
	setString("VMM_LOG_PAYLOAD_ENCRYPTION_KEY", &cfg.Logging.PayloadEncryptionKey)
	setString("VMM_PII_DEFAULT_LANGUAGE", &cfg.PII.DefaultLanguage)
	setBool("VMM_NOISE_ENABLED", &cfg.Noise.Enabled)
	setString("VMM_NOISE_DEFAULT_LANGUAGE", &cfg.Noise.DefaultLanguage)
	setBool("VMM_NOISE_SEMANTIC_ENABLED", &cfg.Noise.SemanticEnabled)
	setFloat("VMM_NOISE_SEMANTIC_THRESHOLD", &cfg.Noise.SemanticThreshold)
	setString("VMM_STORAGE_MODE", &cfg.Storage.Mode)
	setString("VMM_STORAGE_COMBINED_PROVIDER", &cfg.Storage.CombinedProvider)
	setString("VMM_SQLITE_ADDRESS", &cfg.SQLite.Address)
	setDuration("VMM_SQLITE_TIMEOUT", &cfg.SQLite.Timeout)
	setString("VMM_LANCEDB_ADDRESS", &cfg.LanceDB.Address)
	setDuration("VMM_LANCEDB_TIMEOUT", &cfg.LanceDB.Timeout)
	setString("VMM_LANCEDB_TABLE_NAME", &cfg.LanceDB.TableName)
	setString("VMM_LANCEDB_VECTOR_COLUMN", &cfg.LanceDB.VectorColumn)
	setString("VMM_POSTGRES_DSN", &cfg.Postgres.DSN)
	setString("VMM_POSTGRES_SCHEMA", &cfg.Postgres.Schema)
	setString("VMM_POSTGRES_FLAVOR", &cfg.Postgres.Flavor)
	setDuration("VMM_POSTGRES_QUERY_TIMEOUT", &cfg.Postgres.QueryTimeout)
	setDuration("VMM_POSTGRES_CONNECT_TIMEOUT", &cfg.Postgres.ConnectTimeout)
	setInt("VMM_POSTGRES_MAX_OPEN_CONNS", &cfg.Postgres.MaxOpenConns)
	setInt("VMM_POSTGRES_MIN_IDLE_CONNS", &cfg.Postgres.MinIdleConns)
	setBool("VMM_POSTGRES_AUTO_CREATE_EXTENSIONS", &cfg.Postgres.AutoCreateExtensions)
	setBool("VMM_POSTGRES_BM25_INDEX_CONCURRENTLY", &cfg.Postgres.BM25IndexConcurrently)
	setString("VMM_POSTGRES_BM25_INDEX_NAME", &cfg.Postgres.BM25IndexName)
	setFloat("VMM_POSTGRES_TRGM_SIMILARITY_THRESHOLD", &cfg.Postgres.TRGMSimilarityThreshold)
	setInt("VMM_POSTGRES_VECTOR_LISTS", &cfg.Postgres.VectorLists)
	setInt("VMM_POSTGRES_VECTOR_PROBES", &cfg.Postgres.VectorProbes)
	setInt("VMM_POSTGRES_MIGRATION_BATCH_SIZE", &cfg.Postgres.MigrationBatchSize)
	setString("VMM_LLM_PROVIDER", &cfg.LLM.Provider)
	setString("VMM_LLM_ENDPOINT", &cfg.LLM.Endpoint)
	setSingleKey("VMM_LLM_API_KEY", &cfg.LLM.APIKey, &cfg.LLM.APIKeys)
	setStringSlice("VMM_LLM_API_KEYS", &cfg.LLM.APIKeys, &cfg.LLM.APIKey)
	setString("VMM_LLM_MODEL", &cfg.LLM.Model)
	setString("VMM_LLM_ORGANIZATION", &cfg.LLM.Organization)
	setString("VMM_LLM_PROJECT", &cfg.LLM.Project)
	setBool("VMM_LLM_KEY_FAILOVER_ENABLED", &cfg.LLM.KeyFailover.Enabled)
	setString("VMM_LLM_KEY_FAILOVER_POLICY", &cfg.LLM.KeyFailover.Policy)
	setBool("VMM_LLM_KEY_FAILOVER_RESPECT_RETRY_AFTER", &cfg.LLM.KeyFailover.RespectRetryAfter)
	setDuration("VMM_LLM_KEY_FAILOVER_RATE_LIMIT_COOLDOWN", &cfg.LLM.KeyFailover.RateLimitCooldown)
	setDuration("VMM_LLM_KEY_FAILOVER_QUOTA_COOLDOWN", &cfg.LLM.KeyFailover.QuotaCooldown)
	setDuration("VMM_LLM_KEY_FAILOVER_AUTH_COOLDOWN", &cfg.LLM.KeyFailover.AuthCooldown)
	setBool("VMM_LLM_KEY_FAILOVER_PROBE_AFTER_COOLDOWN", &cfg.LLM.KeyFailover.ProbeAfterCooldown)
	setString("VMM_EMBED_PROVIDER", &cfg.Embedding.Provider)
	setString("VMM_EMBED_ENDPOINT", &cfg.Embedding.Endpoint)
	setSingleKey("VMM_EMBED_API_KEY", &cfg.Embedding.APIKey, &cfg.Embedding.APIKeys)
	setStringSlice("VMM_EMBED_API_KEYS", &cfg.Embedding.APIKeys, &cfg.Embedding.APIKey)
	setString("VMM_EMBED_MODEL", &cfg.Embedding.Model)
	setInt("VMM_EMBED_DIMENSION", &cfg.Embedding.Dimension)
	setString("VMM_EMBED_ORGANIZATION", &cfg.Embedding.Organization)
	setString("VMM_EMBED_PROJECT", &cfg.Embedding.Project)
	setBool("VMM_EMBED_KEY_FAILOVER_ENABLED", &cfg.Embedding.KeyFailover.Enabled)
	setString("VMM_EMBED_KEY_FAILOVER_POLICY", &cfg.Embedding.KeyFailover.Policy)
	setBool("VMM_EMBED_KEY_FAILOVER_RESPECT_RETRY_AFTER", &cfg.Embedding.KeyFailover.RespectRetryAfter)
	setDuration("VMM_EMBED_KEY_FAILOVER_RATE_LIMIT_COOLDOWN", &cfg.Embedding.KeyFailover.RateLimitCooldown)
	setDuration("VMM_EMBED_KEY_FAILOVER_QUOTA_COOLDOWN", &cfg.Embedding.KeyFailover.QuotaCooldown)
	setDuration("VMM_EMBED_KEY_FAILOVER_AUTH_COOLDOWN", &cfg.Embedding.KeyFailover.AuthCooldown)
	setBool("VMM_EMBED_KEY_FAILOVER_PROBE_AFTER_COOLDOWN", &cfg.Embedding.KeyFailover.ProbeAfterCooldown)
	setBool("VMM_RERANK_ENABLED", &cfg.Rerank.Enabled)
	setString("VMM_RERANK_PROVIDER", &cfg.Rerank.Provider)
	setString("VMM_RERANK_ENDPOINT", &cfg.Rerank.Endpoint)
	setSingleKey("VMM_RERANK_API_KEY", &cfg.Rerank.APIKey, &cfg.Rerank.APIKeys)
	setStringSlice("VMM_RERANK_API_KEYS", &cfg.Rerank.APIKeys, &cfg.Rerank.APIKey)
	setString("VMM_RERANK_MODEL", &cfg.Rerank.Model)
	setInt("VMM_RERANK_TOP_N", &cfg.Rerank.TopN)
	setDuration("VMM_RERANK_TIMEOUT", &cfg.Rerank.Timeout)
	setBool("VMM_RERANK_KEY_FAILOVER_ENABLED", &cfg.Rerank.KeyFailover.Enabled)
	setString("VMM_RERANK_KEY_FAILOVER_POLICY", &cfg.Rerank.KeyFailover.Policy)
	setBool("VMM_RERANK_KEY_FAILOVER_RESPECT_RETRY_AFTER", &cfg.Rerank.KeyFailover.RespectRetryAfter)
	setDuration("VMM_RERANK_KEY_FAILOVER_RATE_LIMIT_COOLDOWN", &cfg.Rerank.KeyFailover.RateLimitCooldown)
	setDuration("VMM_RERANK_KEY_FAILOVER_QUOTA_COOLDOWN", &cfg.Rerank.KeyFailover.QuotaCooldown)
	setDuration("VMM_RERANK_KEY_FAILOVER_AUTH_COOLDOWN", &cfg.Rerank.KeyFailover.AuthCooldown)
	setBool("VMM_RERANK_KEY_FAILOVER_PROBE_AFTER_COOLDOWN", &cfg.Rerank.KeyFailover.ProbeAfterCooldown)
	setString("VMM_VECTOR_PROVIDER", &cfg.Vector.Provider)
	setString("VMM_RELATIONAL_PROVIDER", &cfg.Relational.Provider)
	setString("VMM_POST_ACTION_INPUT_MODE", &cfg.PostAction.InputMode)
	setInt("VMM_POST_ACTION_SESSION_ANALYSIS_TURN_THRESHOLD", &cfg.PostAction.SessionAnalysisTurnThreshold)
	setInt("VMM_POST_ACTION_SESSION_ANALYSIS_TOKEN_THRESHOLD", &cfg.PostAction.SessionAnalysisTokenThreshold)
	setDuration("VMM_POST_ACTION_SESSION_ANALYSIS_IDLE_TIMEOUT", &cfg.PostAction.SessionAnalysisIdleTimeout)
	setInt("VMM_POST_ACTION_SESSION_ANALYSIS_HISTORY_TURNS", &cfg.PostAction.SessionAnalysisHistoryTurns)
	setInt("VMM_POST_ACTION_SESSION_ANALYSIS_MAX_INPUT_TOKENS", &cfg.PostAction.SessionAnalysisMaxInputTokens)
	setString("VMM_MEMORY_REPLACE_SCOPE", &cfg.MemoryReplaceScope)
	setDuration("VMM_PRE_CHECK_INTENT_TIMEOUT", &cfg.PreCheck.IntentTimeout)
	setInt("VMM_PRE_CHECK_TOPK", &cfg.PreCheck.TopK)
	setString("VMM_PRE_CHECK_SEARCH_SCOPE", &cfg.PreCheck.SearchScope)
	setFloat("VMM_PRE_CHECK_SIMILARITY_THRESHOLD", &cfg.PreCheck.SimilarityThreshold)
	setBool("VMM_RETENTION_ENABLED", &cfg.Retention.Enabled)
	setDuration("VMM_RETENTION_RECYCLE_SCAN_INTERVAL", &cfg.Retention.RecycleScanInterval)
	setInt("VMM_RETENTION_TURN_KEEP_EXTRA_TURNS", &cfg.Retention.TurnKeepExtraTurns)
	setDuration("VMM_RETENTION_SESSION_IDLE_RECYCLE_AFTER", &cfg.Retention.SessionIdleRecycleAfter)
	setDuration("VMM_RETENTION_TRASH_RETENTION", &cfg.Retention.TrashRetention)
	setString("VMM_RETENTION_PROTECT_PRIORITY_FLOOR", &cfg.Retention.ProtectPriorityFloor)
	setString("VMM_RETENTION_PROTECT_MEMORY_LEVEL_FLOOR", &cfg.Retention.ProtectMemoryLevelFloor)
	setBool("VMM_RETENTION_SKIP_PROTECTED_SHARED_MEMORIES", &cfg.Retention.SkipProtectedSharedMemories)
	setInt("VMM_MEMORY_MAX_SEARCH_KEYWORDS", &cfg.MemoryPipeline.MaxSearchKeywords)
	setOptionalFloat("VMM_MEMORY_MIN_SIMILARITY_SCORE", &cfg.MemoryPipeline.MinSimilarityScore)
	setBool("VMM_MEMORY_HYBRID_ENABLED", &cfg.MemoryPipeline.HybridEnabled)
	setBool("VMM_MEMORY_LEXICAL_PRETOKENIZE", &cfg.MemoryPipeline.LexicalPreTokenize)
	setInt("VMM_MEMORY_LEXICAL_TOP_K", &cfg.MemoryPipeline.LexicalTopK)
	setInt("VMM_MEMORY_RRF_K", &cfg.MemoryPipeline.RRFK)
	setBool("VMM_MEMORY_MMR_ENABLED", &cfg.MemoryPipeline.MMREnabled)
	setFloat("VMM_MEMORY_MMR_LAMBDA", &cfg.MemoryPipeline.MMRLambda)
	setBool("VMM_MEMORY_WEIBULL_ENABLED", &cfg.MemoryPipeline.WeibullEnabled)
	setFloat("VMM_MEMORY_WEIBULL_SHAPE", &cfg.MemoryPipeline.WeibullShape)
	setFloat("VMM_MEMORY_WEIBULL_SCALE_HOURS", &cfg.MemoryPipeline.WeibullScaleHours)
	setFloat("VMM_MEMORY_WEIBULL_MIN_MULTIPLIER", &cfg.MemoryPipeline.WeibullMinMultiplier)
	setFloat("VMM_MEMORY_WEIBULL_REINFORCE_WEIGHT", &cfg.MemoryPipeline.WeibullReinforceWeight)
	setFloat("VMM_MEMORY_WEIBULL_CROSS_SESSION_BOOST", &cfg.MemoryPipeline.WeibullCrossSessionBoost)
}

// StorageMode returns the normalized runtime storage mode used by composition and tests.
// StorageMode 用于返回运行时装配与测试共用的规范化存储模式。
func (c Config) StorageMode() string {
	return normalizeStorageModeValue(c.Storage.Mode)
}

// UsesCombinedPostgres reports whether the runtime should build the unified PostgreSQL-backed combined store.
// UsesCombinedPostgres 用于判断运行时是否应装配统一的 PostgreSQL 组合库。
func (c Config) UsesCombinedPostgres() bool {
	return c.StorageMode() == "combined" && normalizeCombinedProviderValue(c.Storage.CombinedProvider) == "postgres"
}

// isOpenAIProvider reports whether one provider alias resolves to the supported OpenAI-compatible adapter.
// isOpenAIProvider 用于判断某个 provider 别名是否会落到当前支持的 OpenAI 兼容适配器。
func isOpenAIProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openai_native", "openai_go":
		return true
	default:
		return false
	}
}

// validatePayloadEncryptionKey checks the optional protected-log key format so startup can fail fast instead of silently dropping encrypted payload logging.
// validatePayloadEncryptionKey 用于校验受保护日志密钥格式，让启动流程能尽早失败，而不是静默丢失加密载荷日志能力。
func validatePayloadEncryptionKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("must not be empty when logging.protect_payloads is enabled")
	}
	if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(trimmed) == 32 {
		return []byte(trimmed), nil
	}
	return nil, errors.New("must be 32 raw bytes, 64 hex chars, or base64 for 32 bytes")
}
