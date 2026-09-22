// native_embedding_identity.go protects native vector data from silent embedding-provider changes.
// native_embedding_identity.go 用于防止原生向量数据在 embedding provider 变化后被静默混写。
package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvulcan/vmm/internal/config"
)

// nativeEmbeddingIdentityVersion identifies the sidecar schema and keeps future identity changes fail-closed.
// nativeEmbeddingIdentityVersion 标识 sidecar schema，并让未来 identity 变化以失败关闭处理。
const nativeEmbeddingIdentityVersion = 2

// nativeEmbeddingIdentity is the credential-free sidecar contract paired with one native SQLite database.
// nativeEmbeddingIdentity 是与一份原生 SQLite 数据库配对的无凭据 sidecar 契约。
type nativeEmbeddingIdentity struct {
	Version           int    `json:"version"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	Dimension         int    `json:"dimension"`
	ParamsSHA256      string `json:"params_sha256"`
	ModelParamsSHA256 string `json:"model_params_sha256"`
	EndpointSHA256    string `json:"endpoint_sha256"`
	NodesSHA256       string `json:"nodes_sha256"`
}

// ensureNativeEmbeddingIdentity validates the sidecar before native startup and creates it only for a brand-new database.
// ensureNativeEmbeddingIdentity 在原生启动前校验 sidecar，并且只允许为全新数据库创建它。
//
// When allowChange is true, an existing well-formed identity may differ from the current configuration for an explicit maintenance rebuild; this function never changes the sidecar in that mode.
// 当 allowChange 为 true 时，已有且结构正确的 identity 可以与当前配置不同，以支持显式维护重建；本函数在该模式下绝不修改 sidecar。
func ensureNativeEmbeddingIdentity(cfg config.Config, sqlitePath string, allowChange bool) error {
	expected, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	sqlitePath = strings.TrimSpace(sqlitePath)
	if sqlitePath == "" {
		return fmt.Errorf("native embedding identity requires sqlite path")
	}
	identityPath := nativeEmbeddingIdentityPath(sqlitePath)
	databaseExists, err := nativePathExists(sqlitePath)
	if err != nil {
		return fmt.Errorf("stat native SQLite database: %w", err)
	}
	actual, err := readNativeEmbeddingIdentity(identityPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if allowChange {
			return fmt.Errorf("native embedding identity is required for maintenance: %s", identityPath)
		}
		if databaseExists {
			return fmt.Errorf("native embedding identity is missing for existing SQLite database: %s", sqlitePath)
		}
		// A first startup publishes the sidecar before the native SQLite constructor creates the database, so a crash cannot make an untracked database look valid.
		// 首次启动会在原生 SQLite 构造器创建数据库前发布 sidecar，避免崩溃后出现未登记数据库被误认为有效的情况。
		if err := writeNativeEmbeddingIdentity(identityPath, expected); err != nil {
			return fmt.Errorf("create native embedding identity: %w", err)
		}
		return nil
	}
	if !databaseExists {
		return fmt.Errorf("native embedding identity exists without SQLite database: %s", identityPath)
	}
	if allowChange {
		return nil
	}
	if actual != expected {
		return fmt.Errorf("native embedding identity does not match configured provider/model/dimension or embedding parameters: %s", identityPath)
	}
	return nil
}

// updateNativeEmbeddingIdentity writes the current native identity after a complete vector rebuild has succeeded.
// updateNativeEmbeddingIdentity 在完整向量重建成功后写入当前原生 identity。
func updateNativeEmbeddingIdentity(cfg config.Config) error {
	if !cfg.UsesNative() {
		return fmt.Errorf("native embedding identity update requires storage.mode=native")
	}
	layout, err := resolveNativeStorageLayout(cfg, config.PromptLayout{})
	if err != nil {
		return fmt.Errorf("resolve native storage layout for embedding identity: %w", err)
	}
	databaseExists, err := nativePathExists(layout.SQLiteDatabase)
	if err != nil {
		return fmt.Errorf("stat native SQLite database for embedding identity: %w", err)
	}
	if !databaseExists {
		return fmt.Errorf("cannot update native embedding identity before SQLite database exists: %s", layout.SQLiteDatabase)
	}
	identityPath := nativeEmbeddingIdentityPath(layout.SQLiteDatabase)
	if _, err := readNativeEmbeddingIdentity(identityPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("validate existing native embedding identity before update: %w", err)
	}
	identity, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		return err
	}
	if err := writeNativeEmbeddingIdentity(identityPath, identity); err != nil {
		return fmt.Errorf("update native embedding identity: %w", err)
	}
	return nil
}

// nativeEmbeddingIdentityForConfig derives only the embedding attributes that can change vector values or their interpretation.
// nativeEmbeddingIdentityForConfig 只提取会改变向量值或其解释方式的 embedding 属性。
func nativeEmbeddingIdentityForConfig(cfg config.Config) (nativeEmbeddingIdentity, error) {
	cfg.Normalize()
	provider := strings.ToLower(strings.TrimSpace(cfg.Embedding.Provider))
	model := strings.TrimSpace(cfg.Embedding.Model)
	if provider == "" || model == "" || cfg.Embedding.Dimension <= 0 {
		return nativeEmbeddingIdentity{}, fmt.Errorf("native embedding identity requires provider, model, and positive dimension")
	}
	paramsSHA256, err := nativeEmbeddingParamsSHA256(cfg.Embedding.Params)
	if err != nil {
		return nativeEmbeddingIdentity{}, fmt.Errorf("encode embedding params for identity: %w", err)
	}
	modelParamsSHA256, err := nativeEmbeddingParamsSHA256(cfg.Embedding.ModelParams[model])
	if err != nil {
		return nativeEmbeddingIdentity{}, fmt.Errorf("encode current model params for identity: %w", err)
	}
	endpointSHA256, err := nativeEmbeddingEndpointSHA256(cfg.Embedding.Endpoint)
	if err != nil {
		return nativeEmbeddingIdentity{}, fmt.Errorf("normalize embedding endpoint for identity: %w", err)
	}
	nodesSHA256, err := nativeEmbeddingNodesSHA256(cfg.Embedding.RoutingNodes())
	if err != nil {
		return nativeEmbeddingIdentity{}, fmt.Errorf("encode embedding routing nodes for identity: %w", err)
	}
	return nativeEmbeddingIdentity{
		Version:           nativeEmbeddingIdentityVersion,
		Provider:          provider,
		Model:             model,
		Dimension:         cfg.Embedding.Dimension,
		ParamsSHA256:      paramsSHA256,
		ModelParamsSHA256: modelParamsSHA256,
		EndpointSHA256:    endpointSHA256,
		NodesSHA256:       nodesSHA256,
	}, nil
}

// nativeEmbeddingEndpointSHA256 hashes a canonical endpoint without persisting the endpoint or any credential-bearing URL text.
// nativeEmbeddingEndpointSHA256 对规范化 endpoint 计算摘要，不把 endpoint 或其中可能携带的凭据明文写入 sidecar。
func nativeEmbeddingEndpointSHA256(endpoint string) (string, error) {
	normalized := strings.TrimSpace(endpoint)
	if normalized != "" {
		parsed, err := url.Parse(normalized)
		if err != nil {
			return "", err
		}
		parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
		parsed.Host = strings.ToLower(strings.TrimSpace(parsed.Host))
		escapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
		if parsed.RawPath != "" {
			decodedPath, err := url.PathUnescape(escapedPath)
			if err != nil {
				return "", fmt.Errorf("normalize endpoint path: %w", err)
			}
			parsed.Path = decodedPath
			parsed.RawPath = escapedPath
		} else {
			parsed.Path = strings.TrimRight(parsed.Path, "/")
		}
		parsed.Fragment = ""
		parsed.RawFragment = ""
		if parsed.RawQuery != "" {
			query, err := url.ParseQuery(parsed.RawQuery)
			if err != nil {
				return "", fmt.Errorf("normalize endpoint query: %w", err)
			}
			parsed.RawQuery = query.Encode()
		}
		normalized = parsed.String()
	}
	return nativeEmbeddingParamsSHA256(normalized)
}

// nativeEmbeddingNodeIdentity keeps only ordered routing names because API keys and mutable throughput budgets are not vector-space identity.
// nativeEmbeddingNodeIdentity 只保留有序路由名称，因为 API Key 与可变吞吐预算不属于向量空间身份。
type nativeEmbeddingNodeIdentity struct {
	Name string `json:"name"`
}

// nativeEmbeddingNodesSHA256 summarizes normalized routing topology without persisting keys, limits, or other credentials.
// nativeEmbeddingNodesSHA256 对归一化后的路由拓扑计算摘要，不落盘 key、限额或其他凭据。
func nativeEmbeddingNodesSHA256(nodes []config.AIRoutingNodeConfig) (string, error) {
	identityNodes := make([]nativeEmbeddingNodeIdentity, len(nodes))
	for index, node := range nodes {
		identityNodes[index] = nativeEmbeddingNodeIdentity{Name: strings.TrimSpace(node.Name)}
	}
	return nativeEmbeddingParamsSHA256(identityNodes)
}

// nativeEmbeddingParamsSHA256 produces a stable digest without persisting provider credentials or endpoint metadata.
// nativeEmbeddingParamsSHA256 生成稳定摘要，不落盘 provider 凭据或 endpoint 元数据。
func nativeEmbeddingParamsSHA256(value any) (string, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// nativeEmbeddingIdentityPath returns the sidecar path paired with one SQLite database path.
// nativeEmbeddingIdentityPath 返回与一份 SQLite 数据库路径配对的 sidecar 路径。
func nativeEmbeddingIdentityPath(sqlitePath string) string {
	return strings.TrimSpace(sqlitePath) + ".embedding.json"
}

// nativePathExists distinguishes a missing native resource from filesystem errors that must fail closed.
// nativePathExists 区分原生资源不存在与必须失败关闭的文件系统错误。
func nativePathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// readNativeEmbeddingIdentity strictly decodes and validates one sidecar without accepting unknown fields.
// readNativeEmbeddingIdentity 严格解码并校验一份 sidecar，不接受未知字段。
func readNativeEmbeddingIdentity(path string) (nativeEmbeddingIdentity, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nativeEmbeddingIdentity{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var identity nativeEmbeddingIdentity
	if err := decoder.Decode(&identity); err != nil {
		return nativeEmbeddingIdentity{}, fmt.Errorf("decode native embedding identity: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nativeEmbeddingIdentity{}, fmt.Errorf("decode native embedding identity: multiple JSON values")
		}
		return nativeEmbeddingIdentity{}, fmt.Errorf("decode native embedding identity trailer: %w", err)
	}
	if err := validateNativeEmbeddingIdentity(identity); err != nil {
		return nativeEmbeddingIdentity{}, err
	}
	return identity, nil
}

// validateNativeEmbeddingIdentity rejects malformed or unsupported sidecar structures before they can authorize startup.
// validateNativeEmbeddingIdentity 在 sidecar 授权启动前拒绝格式错误或不支持的结构。
func validateNativeEmbeddingIdentity(identity nativeEmbeddingIdentity) error {
	if identity.Version != nativeEmbeddingIdentityVersion {
		return fmt.Errorf("unsupported native embedding identity version %d", identity.Version)
	}
	if strings.TrimSpace(identity.Provider) == "" || strings.TrimSpace(identity.Model) == "" || identity.Dimension <= 0 {
		return fmt.Errorf("native embedding identity has invalid provider, model, or dimension")
	}
	for name, digest := range map[string]string{
		"params_sha256":       identity.ParamsSHA256,
		"model_params_sha256": identity.ModelParamsSHA256,
		"endpoint_sha256":     identity.EndpointSHA256,
		"nodes_sha256":        identity.NodesSHA256,
	} {
		if len(digest) != sha256.Size*2 {
			return fmt.Errorf("native embedding identity %s must be a SHA-256 digest", name)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return fmt.Errorf("native embedding identity %s is not hexadecimal: %w", name, err)
		}
	}
	return nil
}

// writeNativeEmbeddingIdentity atomically publishes one credential-free sidecar and keeps a failed write from changing the active identity.
// writeNativeEmbeddingIdentity 原子发布一份无凭据 sidecar，写入失败时不会改变当前生效 identity。
func writeNativeEmbeddingIdentity(path string, identity nativeEmbeddingIdentity) (err error) {
	if err := validateNativeEmbeddingIdentity(identity); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode native embedding identity: %w", err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create native embedding identity directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".embedding-identity-*")
	if err != nil {
		return fmt.Errorf("create native embedding identity temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure native embedding identity temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write native embedding identity temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync native embedding identity temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close native embedding identity temporary file: %w", err)
	}
	if err := renameNativeAtomicFile(temporaryPath, path); err != nil {
		return fmt.Errorf("publish native embedding identity: %w", err)
	}
	if err := syncNativeParentDirectory(path); err != nil {
		return fmt.Errorf("sync native embedding identity directory: %w", err)
	}
	return nil
}
