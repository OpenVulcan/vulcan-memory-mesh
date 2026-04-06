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

// LLMConfig holds the explicit multi-route LLM configuration used by intent extraction and other generation tasks.
// LLMConfig 用于保存意图提取等生成任务使用的显式多路由 LLM 配置。
type LLMConfig struct {
	Routes []LLMRouteConfig `json:"routes,omitempty"`
}

// EmbeddingConfig holds the single-provider, single-model embedding configuration while still allowing multiple API keys and node-level throughput budgets.
// EmbeddingConfig 用于保存单 provider、单模型的 embedding 配置，同时保留多 API Key 与节点级吞吐预算能力。
type EmbeddingConfig struct {
	Provider     string                    `json:"provider"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	APIKeys      []string                  `json:"api_keys,omitempty"`
	RPM          int                       `json:"rpm,omitempty"`
	TPM          int                       `json:"tpm,omitempty"`
	RPD          int                       `json:"rpd,omitempty"`
	Nodes        []AIRoutingNodeConfig     `json:"nodes,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Dimension    int                       `json:"dimension,omitempty"`
	Organization string                    `json:"organization,omitempty"`
	Project      string                    `json:"project,omitempty"`
	Params       map[string]any            `json:"params,omitempty"`
	ModelParams  map[string]map[string]any `json:"model_params,omitempty"`
	KeyFailover  KeyFailoverConfig         `json:"key_failover,omitempty"`
}

// RerankConfig holds the optional multi-route rerank configuration used to reorder vector recall hits.
// RerankConfig 用于保存可选的多路由 rerank 配置，让系统在向量召回后重新排序候选。
type RerankConfig struct {
	Enabled bool                `json:"enabled"`
	Routes  []RerankRouteConfig `json:"routes,omitempty"`
	TopN    int                 `json:"top_n,omitempty"`
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

// AIRoutingNodeConfig describes one fixed-model routing node that owns one API key pool and assigns the same per-key RPM/TPM/RPD limits to each member.
// AIRoutingNodeConfig 用于描述一个固定模型轮询节点：它拥有一组 API Key 池，并给每个成员分配同一组独立的 RPM/TPM/RPD 限额。
type AIRoutingNodeConfig struct {
	Name    string   `json:"name,omitempty"`
	APIKeys []string `json:"api_keys,omitempty"`
	RPM     int      `json:"rpm,omitempty"`
	TPM     int      `json:"tpm,omitempty"`
	RPD     int      `json:"rpd,omitempty"`
}

// LLMRouteConfig describes one concrete LLM route that owns its provider, endpoint, model, key pool, and node budget policy.
// LLMRouteConfig 用于描述一条具体的 LLM 路由：它自包含 provider、endpoint、model、Key 池与节点预算策略。
type LLMRouteConfig struct {
	Name         string                    `json:"name,omitempty"`
	Priority     int                       `json:"priority,omitempty"`
	Provider     string                    `json:"provider,omitempty"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	APIKeys      []string                  `json:"api_keys,omitempty"`
	RPM          int                       `json:"rpm,omitempty"`
	TPM          int                       `json:"tpm,omitempty"`
	RPD          int                       `json:"rpd,omitempty"`
	Nodes        []AIRoutingNodeConfig     `json:"nodes,omitempty"`
	Model        string                    `json:"model,omitempty"`
	Organization string                    `json:"organization,omitempty"`
	Project      string                    `json:"project,omitempty"`
	Params       map[string]any            `json:"params,omitempty"`
	ModelParams  map[string]map[string]any `json:"model_params,omitempty"`
	KeyFailover  KeyFailoverConfig         `json:"key_failover,omitempty"`
}

// RerankRouteConfig describes one concrete rerank route that switches provider, endpoint, model, and key pool as one ordered failover step.
// RerankRouteConfig 用于描述一条具体的 rerank 路由：它把 provider、endpoint、model 与 Key 池作为一个有序容灾单元进行切换。
type RerankRouteConfig struct {
	Name        string                `json:"name,omitempty"`
	Priority    int                   `json:"priority,omitempty"`
	Provider    string                `json:"provider,omitempty"`
	Endpoint    string                `json:"endpoint,omitempty"`
	APIKeys     []string              `json:"api_keys,omitempty"`
	RPM         int                   `json:"rpm,omitempty"`
	TPM         int                   `json:"tpm,omitempty"`
	RPD         int                   `json:"rpd,omitempty"`
	Nodes       []AIRoutingNodeConfig `json:"nodes,omitempty"`
	Model       string                `json:"model,omitempty"`
	Timeout     Duration              `json:"timeout,omitempty"`
	KeyFailover KeyFailoverConfig     `json:"key_failover,omitempty"`
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
			Routes: []LLMRouteConfig{{
				Provider:    "openai",
				Model:       "gpt-4.1-mini",
				KeyFailover: defaultKeyFailoverConfig(),
			}},
		},
		Embedding: EmbeddingConfig{
			Provider:    "openai",
			Model:       "text-embedding-3-large",
			Dimension:   1024,
			KeyFailover: defaultKeyFailoverConfig(),
		},
		Rerank: RerankConfig{
			Enabled: false,
			TopN:    8,
			Routes: []RerankRouteConfig{{
				Provider:    "dashscope",
				Endpoint:    "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
				Model:       "qwen3-vl-rerank",
				Timeout:     Duration{8 * time.Second},
				KeyFailover: defaultKeyFailoverConfig(),
			}},
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
	if err := validateRemovedAIEnvOverrides(); err != nil {
		return Config{}, err
	}
	applyEnvOverrides(&cfg)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
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

// rejectRemovedAIConfigModes blocks deprecated AI config shapes at load time so runtime code only needs to reason about the new route-only or multi-key-only contracts.
// rejectRemovedAIConfigModes 用于在加载期阻断已废弃的 AI 配置形态，让运行时代码只需要处理新的 route-only 或 multi-key-only 契约。
func rejectRemovedAIConfigModes(root map[string]json.RawMessage) error {
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

// rejectRemovedLLMConfigFields rejects top-level legacy single-route LLM fields and the removed singular api_key shape inside routes or nodes.
// rejectRemovedLLMConfigFields 用于拒绝顶层 legacy 单路由 LLM 字段，以及 routes 或 nodes 中已移除的单值 api_key 写法。
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
	if err := rejectRemovedAPIKeyInRoutes("llm.routes", fields["routes"]); err != nil {
		return err
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

// RoutingNodes returns the normalized embedding routing nodes, synthesizing one default node from top-level api_keys when explicit nodes are absent.
// RoutingNodes 用于返回归一化后的 embedding 轮询节点；当未显式声明 nodes 时，会把顶层 api_keys 折叠成一个默认节点。
func (c EmbeddingConfig) RoutingNodes() []AIRoutingNodeConfig {
	return normalizeAIRoutingNodes(c.APIKeys, c.RPM, c.TPM, c.RPD, c.Nodes)
}

// ProviderRoutes returns the normalized explicit LLM route list.
// ProviderRoutes 用于返回归一化后的显式 LLM 路由列表。
func (c LLMConfig) ProviderRoutes() []LLMRouteConfig {
	return normalizeLLMRouteConfigs(c.Routes)
}

// PrimaryRoute returns the route that should represent the default runtime model identity for LLM-side prompt assembly and diagnostics.
// PrimaryRoute 用于返回应该代表默认运行时模型身份的 LLM 路由，供提示词装配与诊断逻辑使用。
func (c LLMConfig) PrimaryRoute() (LLMRouteConfig, bool) {
	return primaryLLMRoute(c.ProviderRoutes())
}

// PrimaryModel returns the primary LLM model chosen from the highest-priority declared route.
// PrimaryModel 用于返回从最高优先级声明路由中选出的主 LLM 模型名称。
func (c LLMConfig) PrimaryModel() string {
	route, ok := c.PrimaryRoute()
	if !ok {
		return ""
	}
	return route.Model
}

// ProviderRoutes returns the normalized explicit rerank route list.
// ProviderRoutes 用于返回归一化后的显式 rerank 路由列表。
func (c RerankConfig) ProviderRoutes() []RerankRouteConfig {
	return normalizeRerankRouteConfigs(c.Routes)
}

// primaryLLMRoute returns the highest-priority LLM route while preserving declaration order for equal priorities.
// primaryLLMRoute 用于返回最高优先级的 LLM 路由；若优先级相同，则保持声明顺序。
func primaryLLMRoute(routes []LLMRouteConfig) (LLMRouteConfig, bool) {
	if len(routes) == 0 {
		return LLMRouteConfig{}, false
	}
	best := routes[0]
	for _, route := range routes[1:] {
		if route.Priority > best.Priority {
			best = route
		}
	}
	return best, true
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

// normalizeAPIKeys expands raw api_keys declarations, trims separators, and removes duplicates while preserving caller order.
// normalizeAPIKeys 用于展开原始 api_keys 声明，裁剪分隔符并在保持顺序的前提下去重。
func normalizeAPIKeys(values []string) []string {
	merged := make([]string, 0, len(values))
	for _, value := range values {
		merged = append(merged, splitConfigAPIKeys(value)...)
	}
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

// normalizeAIRoutingNodes canonicalizes routing-node declarations and can synthesize one default node from top-level api_keys plus shared budgets.
// normalizeAIRoutingNodes 用于规范化轮询节点声明，并可根据顶层 api_keys 与共享预算合成一个默认节点。
func normalizeAIRoutingNodes(apiKeys []string, rpm, tpm, rpd int, nodes []AIRoutingNodeConfig) []AIRoutingNodeConfig {
	if len(nodes) == 0 {
		apiKeys = normalizeAPIKeys(apiKeys)
		if len(apiKeys) == 0 {
			return nil
		}
		return []AIRoutingNodeConfig{{
			APIKeys: apiKeys,
			RPM:     rpm,
			TPM:     tpm,
			RPD:     rpd,
		}}
	}
	normalized := make([]AIRoutingNodeConfig, 0, len(nodes))
	for _, node := range nodes {
		current := AIRoutingNodeConfig{
			Name:    strings.TrimSpace(node.Name),
			APIKeys: normalizeAPIKeys(node.APIKeys),
			RPM:     node.RPM,
			TPM:     node.TPM,
			RPD:     node.RPD,
		}
		normalized = append(normalized, current)
	}
	return normalized
}

// normalizeAIRoutingNodeFields trims routing-node strings in place so later validation and runtime wiring see stable values even before full normalization runs.
// normalizeAIRoutingNodeFields 用于原地裁剪轮询节点里的字符串字段，让后续校验和运行时装配在完整归一化前也能看到稳定值。
func normalizeAIRoutingNodeFields(nodes []AIRoutingNodeConfig) []AIRoutingNodeConfig {
	if len(nodes) == 0 {
		return nil
	}
	normalized := make([]AIRoutingNodeConfig, 0, len(nodes))
	for _, node := range nodes {
		node.Name = strings.TrimSpace(node.Name)
		node.APIKeys = trimStringSlice(node.APIKeys)
		normalized = append(normalized, node)
	}
	return normalized
}

// normalizeLLMRouteConfig trims one LLM route, normalizes its API key pool, folds legacy key pools into routing nodes, and fills safe key-failover defaults for the route itself.
// normalizeLLMRouteConfig 用于裁剪单条 LLM 路由，规范化其 API Key 池，把旧版 key 池折叠成轮询节点，并为该路由补齐安全的 key-failover 默认值。
func normalizeLLMRouteConfig(route LLMRouteConfig) LLMRouteConfig {
	route.Name = strings.TrimSpace(route.Name)
	route.Provider = strings.TrimSpace(route.Provider)
	route.Endpoint = strings.TrimSpace(route.Endpoint)
	route.APIKeys = trimStringSlice(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
	route.Model = strings.TrimSpace(route.Model)
	route.Organization = strings.TrimSpace(route.Organization)
	route.Project = strings.TrimSpace(route.Project)
	route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
	route.APIKeys = normalizeAPIKeys(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodes(route.APIKeys, route.RPM, route.TPM, route.RPD, route.Nodes)
	normalizeKeyFailoverConfig(&route.KeyFailover)
	return route
}

// normalizeRerankRouteConfig trims one rerank route, normalizes its API key pool, folds legacy key pools into routing nodes, and fills safe key-failover defaults for the route itself.
// normalizeRerankRouteConfig 用于裁剪单条 rerank 路由，规范化其 API Key 池，把旧版 key 池折叠成轮询节点，并为该路由补齐安全的 key-failover 默认值。
func normalizeRerankRouteConfig(route RerankRouteConfig) RerankRouteConfig {
	route.Name = strings.TrimSpace(route.Name)
	route.Provider = strings.TrimSpace(route.Provider)
	if route.Provider == "" {
		route.Provider = "dashscope"
	}
	route.Endpoint = strings.TrimSpace(route.Endpoint)
	if route.Endpoint == "" {
		route.Endpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"
	}
	route.APIKeys = trimStringSlice(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
	route.Model = strings.TrimSpace(route.Model)
	if route.Model == "" {
		route.Model = "qwen3-vl-rerank"
	}
	if route.Timeout.Duration <= 0 {
		route.Timeout = Duration{8 * time.Second}
	}
	route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
	route.APIKeys = normalizeAPIKeys(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodes(route.APIKeys, route.RPM, route.TPM, route.RPD, route.Nodes)
	normalizeKeyFailoverConfig(&route.KeyFailover)
	return route
}

// normalizeLLMRouteConfigs normalizes one explicit LLM route slice in declaration order so config loading and runtime wiring share the same route view.
// normalizeLLMRouteConfigs 用于按声明顺序规范化显式 LLM 路由切片，让配置加载与运行时装配共享同一份路由视图。
func normalizeLLMRouteConfigs(routes []LLMRouteConfig) []LLMRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	normalized := make([]LLMRouteConfig, 0, len(routes))
	for _, route := range routes {
		normalized = append(normalized, normalizeLLMRouteConfig(route))
	}
	return normalized
}

// normalizeRerankRouteConfigs normalizes one explicit rerank route slice in declaration order so config loading and runtime wiring share the same route view.
// normalizeRerankRouteConfigs 用于按声明顺序规范化显式 rerank 路由切片，让配置加载与运行时装配共享同一份路由视图。
func normalizeRerankRouteConfigs(routes []RerankRouteConfig) []RerankRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	normalized := make([]RerankRouteConfig, 0, len(routes))
	for _, route := range routes {
		normalized = append(normalized, normalizeRerankRouteConfig(route))
	}
	return normalized
}

// trimLLMRouteFields trims only runtime-facing string fields inside one explicit LLM route slice, keeping the rest of Normalize responsible for full canonicalization.
// trimLLMRouteFields 用于只裁剪显式 LLM 路由切片中的运行时字符串字段，把完整规范化职责继续留给后续 Normalize 阶段。
func trimLLMRouteFields(routes []LLMRouteConfig) []LLMRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	trimmed := make([]LLMRouteConfig, 0, len(routes))
	for _, route := range routes {
		route.Name = strings.TrimSpace(route.Name)
		route.Provider = strings.TrimSpace(route.Provider)
		route.Endpoint = strings.TrimSpace(route.Endpoint)
		route.APIKeys = trimStringSlice(route.APIKeys)
		route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
		route.Model = strings.TrimSpace(route.Model)
		route.Organization = strings.TrimSpace(route.Organization)
		route.Project = strings.TrimSpace(route.Project)
		route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
		trimmed = append(trimmed, route)
	}
	return trimmed
}

// trimRerankRouteFields trims only runtime-facing string fields inside one explicit rerank route slice, keeping the rest of Normalize responsible for full canonicalization.
// trimRerankRouteFields 用于只裁剪显式 rerank 路由切片中的运行时字符串字段，把完整规范化职责继续留给后续 Normalize 阶段。
func trimRerankRouteFields(routes []RerankRouteConfig) []RerankRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	trimmed := make([]RerankRouteConfig, 0, len(routes))
	for _, route := range routes {
		route.Name = strings.TrimSpace(route.Name)
		route.Provider = strings.TrimSpace(route.Provider)
		route.Endpoint = strings.TrimSpace(route.Endpoint)
		route.APIKeys = trimStringSlice(route.APIKeys)
		route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
		route.Model = strings.TrimSpace(route.Model)
		route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
		trimmed = append(trimmed, route)
	}
	return trimmed
}

// validateAIRoutingNodes verifies that each routing node exposes at least one key and only non-negative budget limits.
// validateAIRoutingNodes 用于校验每个轮询节点都至少暴露一个 Key，并且预算限制必须是非负数。
func validateAIRoutingNodes(serviceName string, nodes []AIRoutingNodeConfig) error {
	for idx, node := range nodes {
		label := fmt.Sprintf("%s.nodes[%d]", serviceName, idx)
		if len(node.APIKeys) == 0 {
			return fmt.Errorf("%s.api_keys is required", label)
		}
		if node.RPM < 0 {
			return fmt.Errorf("%s.rpm must be >= 0", label)
		}
		if node.TPM < 0 {
			return fmt.Errorf("%s.tpm must be >= 0", label)
		}
		if node.RPD < 0 {
			return fmt.Errorf("%s.rpd must be >= 0", label)
		}
	}
	return nil
}

// validateLLMRouteConfigs verifies each explicit LLM route remains self-contained so multi-route failover never falls back to partially inherited runtime wiring.
// validateLLMRouteConfigs 用于校验每条显式 LLM 路由都保持自包含，避免多路由容灾退化成依赖部分隐式继承的运行时装配。
func validateLLMRouteConfigs(routes []LLMRouteConfig) error {
	if len(routes) == 0 {
		return errors.New("llm.routes must contain at least one route when declared")
	}
	for idx, route := range routes {
		label := fmt.Sprintf("llm.routes[%d]", idx)
		if strings.TrimSpace(route.Provider) == "" {
			return fmt.Errorf("%s.provider is required", label)
		}
		if !isOpenAIProvider(route.Provider) {
			return fmt.Errorf("%s.provider must use one openai-compatible provider", label)
		}
		if strings.TrimSpace(route.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required", label)
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("%s.model is required", label)
		}
		if len(route.Nodes) == 0 {
			return fmt.Errorf("%s.api_keys or %s.nodes is required", label, label)
		}
		if err := validateAIRoutingNodes(label, route.Nodes); err != nil {
			return err
		}
	}
	return nil
}

// validateRerankRouteConfigs verifies each explicit rerank route stays self-contained so route failover can swap providers or models without guessing missing runtime fields.
// validateRerankRouteConfigs 用于校验每条显式 rerank 路由都保持自包含，确保路由容灾在切 provider 或 model 时不必猜测缺失的运行时字段。
func validateRerankRouteConfigs(routes []RerankRouteConfig) error {
	if len(routes) == 0 {
		return errors.New("rerank.routes must contain at least one route when declared")
	}
	for idx, route := range routes {
		label := fmt.Sprintf("rerank.routes[%d]", idx)
		switch strings.ToLower(strings.TrimSpace(route.Provider)) {
		case "dashscope":
		default:
			return fmt.Errorf("%s.provider must be dashscope", label)
		}
		if strings.TrimSpace(route.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required", label)
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("%s.model is required", label)
		}
		if len(route.Nodes) == 0 {
			return fmt.Errorf("%s.api_keys or %s.nodes is required", label, label)
		}
		if err := validateAIRoutingNodes(label, route.Nodes); err != nil {
			return err
		}
		if route.Timeout.Duration <= 0 {
			return fmt.Errorf("%s.timeout must be > 0", label)
		}
	}
	return nil
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
	c.LLM.Routes = normalizeLLMRouteConfigs(c.LLM.Routes)
	if c.Embedding.Dimension <= 0 && isOpenAIProvider(c.Embedding.Provider) {
		c.Embedding.Dimension = 1024
	}
	c.Embedding.APIKeys = normalizeAPIKeys(c.Embedding.APIKeys)
	c.Embedding.Nodes = normalizeAIRoutingNodes(c.Embedding.APIKeys, c.Embedding.RPM, c.Embedding.TPM, c.Embedding.RPD, c.Embedding.Nodes)
	normalizeKeyFailoverConfig(&c.Embedding.KeyFailover)
	if c.Rerank.TopN <= 0 {
		c.Rerank.TopN = 8
	}
	c.Rerank.Routes = normalizeRerankRouteConfigs(c.Rerank.Routes)
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
	c.LLM.Routes = trimLLMRouteFields(c.LLM.Routes)
	c.Embedding.Provider = strings.TrimSpace(c.Embedding.Provider)
	c.Embedding.Endpoint = strings.TrimSpace(c.Embedding.Endpoint)
	c.Embedding.APIKeys = trimStringSlice(c.Embedding.APIKeys)
	c.Embedding.Nodes = normalizeAIRoutingNodeFields(c.Embedding.Nodes)
	c.Embedding.Model = strings.TrimSpace(c.Embedding.Model)
	c.Embedding.Organization = strings.TrimSpace(c.Embedding.Organization)
	c.Embedding.Project = strings.TrimSpace(c.Embedding.Project)
	c.Embedding.KeyFailover.Policy = strings.TrimSpace(c.Embedding.KeyFailover.Policy)
	c.Rerank.Routes = trimRerankRouteFields(c.Rerank.Routes)
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
	if strings.TrimSpace(c.Embedding.Provider) == "" {
		return errors.New("embedding.provider is required")
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
	if len(c.LLM.Routes) == 0 {
		return errors.New("llm.routes must contain at least one route")
	}
	if err := validateLLMRouteConfigs(c.LLM.Routes); err != nil {
		return err
	}
	if strings.TrimSpace(c.Embedding.Endpoint) == "" {
		return errors.New("embedding.endpoint is required")
	}
	embeddingNodes := c.Embedding.RoutingNodes()
	if len(embeddingNodes) == 0 {
		return errors.New("embedding.api_keys is required")
	}
	if err := validateAIRoutingNodes("embedding", embeddingNodes); err != nil {
		return err
	}
	if strings.TrimSpace(c.Embedding.Model) == "" {
		return errors.New("embedding.model is required")
	}
	if c.Embedding.Dimension <= 0 {
		return errors.New("embedding.dimension must be > 0")
	}
	if c.Rerank.Enabled {
		if len(c.Rerank.Routes) == 0 {
			return errors.New("rerank.routes must contain at least one route when rerank is enabled")
		}
		if err := validateRerankRouteConfigs(c.Rerank.Routes); err != nil {
			return err
		}
		if c.Rerank.TopN <= 0 {
			return errors.New("rerank.top_n must be > 0 when rerank is enabled")
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

// validateRemovedAIEnvOverrides rejects deprecated AI environment variables so runtime startup no longer silently mixes removed single-route semantics into the new config contract.
// validateRemovedAIEnvOverrides 用于拒绝已废弃的 AI 环境变量，避免运行时把已移除的单路由语义静默混入新的配置契约。
func validateRemovedAIEnvOverrides() error {
	for _, key := range []string{
		"VMM_LLM_PROVIDER",
		"VMM_LLM_ENDPOINT",
		"VMM_LLM_API_KEY",
		"VMM_LLM_API_KEYS",
		"VMM_LLM_RPM",
		"VMM_LLM_TPM",
		"VMM_LLM_RPD",
		"VMM_LLM_MODEL",
		"VMM_LLM_ORGANIZATION",
		"VMM_LLM_PROJECT",
		"VMM_LLM_KEY_FAILOVER_ENABLED",
		"VMM_LLM_KEY_FAILOVER_POLICY",
		"VMM_LLM_KEY_FAILOVER_RESPECT_RETRY_AFTER",
		"VMM_LLM_KEY_FAILOVER_RATE_LIMIT_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_QUOTA_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_AUTH_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_PROBE_AFTER_COOLDOWN",
		"VMM_RERANK_PROVIDER",
		"VMM_RERANK_ENDPOINT",
		"VMM_RERANK_API_KEY",
		"VMM_RERANK_API_KEYS",
		"VMM_RERANK_RPM",
		"VMM_RERANK_TPM",
		"VMM_RERANK_RPD",
		"VMM_RERANK_MODEL",
		"VMM_RERANK_TIMEOUT",
		"VMM_RERANK_KEY_FAILOVER_ENABLED",
		"VMM_RERANK_KEY_FAILOVER_POLICY",
		"VMM_RERANK_KEY_FAILOVER_RESPECT_RETRY_AFTER",
		"VMM_RERANK_KEY_FAILOVER_RATE_LIMIT_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_QUOTA_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_AUTH_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_PROBE_AFTER_COOLDOWN",
		"VMM_EMBED_API_KEY",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Errorf("%s has been removed; please migrate AI runtime settings into config files", key)
		}
	}
	return nil
}

// applyEnvOverrides applies the supported target settings.
// applyEnvOverrides 用于应用仍然受支持的目标设置。
func applyEnvOverrides(cfg *Config) {
	// Reapply explicit process-level overrides after file-based expansion.
	// 在文件占位符展开之后，再次应用进程级显式覆盖。
	setString := func(k string, target *string) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = v
		}
	}
	setStringSlice := func(k string, target *[]string, nodes *[]AIRoutingNodeConfig) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = splitConfigAPIKeys(v)
			if nodes != nil {
				*nodes = nil
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
	setString("VMM_EMBED_PROVIDER", &cfg.Embedding.Provider)
	setString("VMM_EMBED_ENDPOINT", &cfg.Embedding.Endpoint)
	setStringSlice("VMM_EMBED_API_KEYS", &cfg.Embedding.APIKeys, &cfg.Embedding.Nodes)
	setInt("VMM_EMBED_RPM", &cfg.Embedding.RPM)
	setInt("VMM_EMBED_TPM", &cfg.Embedding.TPM)
	setInt("VMM_EMBED_RPD", &cfg.Embedding.RPD)
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
	setInt("VMM_RERANK_TOP_N", &cfg.Rerank.TopN)
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
