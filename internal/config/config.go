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
	"gopkg.in/yaml.v3"
)

const (
	// defaultDashScopeRerankEndpoint keeps the built-in DashScope rerank URL aligned across baked defaults, normalization, and docs.
	// defaultDashScopeRerankEndpoint 用于在内建默认值、归一化和文档之间保持 DashScope rerank 地址一致。
	defaultDashScopeRerankEndpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"

	// defaultDashScopeRerankModel keeps the built-in DashScope rerank model aligned across baked defaults, normalization, and docs.
	// defaultDashScopeRerankModel 用于在内建默认值、归一化和文档之间保持 DashScope rerank 模型一致。
	defaultDashScopeRerankModel = "qwen3-vl-rerank"

	// defaultSiliconFlowRerankEndpoint keeps the built-in SiliconFlow rerank URL aligned across normalization and examples when callers pick the SiliconFlow provider.
	// defaultSiliconFlowRerankEndpoint 用于在调用方选择 SiliconFlow provider 时，让归一化和示例共享同一套内建 SiliconFlow rerank 地址。
	defaultSiliconFlowRerankEndpoint = "https://api.siliconflow.cn/v1/rerank"

	// defaultSiliconFlowRerankModel keeps the built-in SiliconFlow rerank model aligned with the official provider example used by this repository.
	// defaultSiliconFlowRerankModel 用于保持仓库内建的 SiliconFlow rerank 模型与当前采用的官方示例一致。
	defaultSiliconFlowRerankModel = "BAAI/bge-reranker-v2-m3"

	// defaultLLMRouteSelectionWeight keeps every LLM route slot stable when callers omit the new per-scene weights.
	// defaultLLMRouteSelectionWeight 用于在调用方省略新的分场景权重时，为每个 LLM 路由槽位提供稳定默认值。
	defaultLLMRouteSelectionWeight = 100

	// defaultEmbeddingMaxBatchSize keeps the historical provider-safe embedding batch width while still allowing deployments to raise or lower it explicitly per model.
	// defaultEmbeddingMaxBatchSize 用于保持历史上 provider 安全的 embedding 批宽默认值，同时允许部署按模型显式调高或调低。
	defaultEmbeddingMaxBatchSize = 10

	// defaultMaintenanceToolPostgresReadTimeout keeps one-shot maintenance fact scans bounded while still allowing full-project durable exports to run longer than online requests by default.
	// defaultMaintenanceToolPostgresReadTimeout 用于为一次性维护事实扫描提供默认上限，让整项目 durable 导出默认可以长于在线请求，但仍保持有界。
	defaultMaintenanceToolPostgresReadTimeout = 30 * time.Second

	// defaultMaintenanceToolPostgresWriteTimeout keeps destructive PostgreSQL maintenance transactions alive long enough to replay active rows and rebuild indexes under larger datasets.
	// defaultMaintenanceToolPostgresWriteTimeout 用于为 PostgreSQL 破坏性维护事务提供更长默认预算，支撑大数据量场景下的 active 行回填与索引重建。
	defaultMaintenanceToolPostgresWriteTimeout = 10 * time.Minute

	// defaultMaintenanceToolVectorRebuildBatchSize keeps vector rebuild materialization bounded without coupling offline maintenance memory usage to the provider-facing embedding batch contract.
	// defaultMaintenanceToolVectorRebuildBatchSize 用于给向量重建的 materialize 阶段提供有界批次，避免离线维护的内存占用被 provider 侧 embedding 批契约直接绑死。
	defaultMaintenanceToolVectorRebuildBatchSize = 10

	// defaultMemoryReplaceMinSimilarityScore keeps post-action/direct-write duplicate review broad enough to surface near-duplicate durable memories without inheriting the old hard-coded 0.90 clamp.
	// defaultMemoryReplaceMinSimilarityScore 用于给 post-action/主动写记忆的重复评审提供较宽的默认召回阈值，避免继续继承旧的 0.90 硬钳制。
	defaultMemoryReplaceMinSimilarityScore = 0.80

	// defaultMemoryHardDedupeCosineThreshold keeps the new pre-review hard-dedupe gate conservative by only short-circuiting obviously equivalent vectors unless callers tune it explicitly.
	// defaultMemoryHardDedupeCosineThreshold 用于让新的 reviewer 前硬排重默认保持保守，只在向量极其接近时才短路后续 LLM，除非调用方显式调参。
	defaultMemoryHardDedupeCosineThreshold = 0.99

	// defaultMemoryHardDedupePoolTopK controls how many pre-MMR durable memories the hard-dedupe shortcut may inspect before the reviewer runs.
	// defaultMemoryHardDedupePoolTopK 用于控制 reviewer 运行前，硬排重捷径最多可以扫描多少条 MMR 之前的长期记忆候选。
	defaultMemoryHardDedupePoolTopK = 16
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
	GRPC               GRPCConfig            `json:"grpc"`
	Logging            LoggingConfig         `json:"logging"`
	PII                PIIConfig             `json:"pii"`
	Noise              NoiseConfig           `json:"noise"`
	Prompts            PromptConfig          `json:"prompts,omitempty"`
	Storage            StorageConfig         `json:"storage"`
	SQLite             SQLiteConfig          `json:"sqlite"`
	LanceDB            LanceDBConfig         `json:"lancedb"`
	Postgres           PostgresConfig        `json:"postgres"`
	MaintenanceTool    MaintenanceToolConfig `json:"maintenance_tool"`
	LLM                LLMConfig             `json:"llm"`
	Embedding          EmbeddingConfig       `json:"embedding"`
	Rerank             RerankConfig          `json:"rerank"`
	Vector             VectorConfig          `json:"vector"`
	Relational         RelationalConfig      `json:"relational"`
	PostAction         PostActionConfig      `json:"post_action"`
	PreCheck           PreCheckConfig        `json:"pre_check"`
	MemoryPipeline     MemoryPipelineConfig  `json:"memory_pipeline"`
	Retention          RetentionConfig       `json:"retention"`
	MemoryReplaceScope string                `json:"memory_replace_scope,omitempty"`
}

// PromptConfig keeps the selected prompt bundle token inside the main config tree so runtime prompt behavior stays explicit and no longer depends on model-name routing.
// PromptConfig 用于把选中的提示词包标识纳入主配置树，确保运行时提示词行为显式可控，不再依赖模型名路由。
type PromptConfig struct {
	PromptLanguage string `json:"prompt_language,omitempty"`
}

// GRPCConfig holds listener and timeout settings for the inbound gRPC server.
// GRPCConfig 用于保存入站 gRPC 服务的监听地址、超时配置和连接治理配置。
type GRPCConfig struct {
	ListenAddr             string              `json:"listen_addr"`
	MaxReceiveMessageBytes int                 `json:"max_receive_message_bytes"`
	RequestTimeout         GRPCRequestTimeout  `json:"request_timeout"`
	ShutdownTimeout        Duration            `json:"shutdown_timeout"`
	Keepalive              GRPCKeepaliveConfig `json:"keepalive"`
}

// GRPCKeepaliveConfig holds server-side keepalive and connection-lifecycle settings that improve unary gRPC connection stability without changing the public proto contract.
// GRPCKeepaliveConfig 用于保存服务端 keepalive 与连接生命周期设置，在不改变对外 proto 契约的前提下提升一元 gRPC 连接稳定性。
type GRPCKeepaliveConfig struct {
	Enabled               bool     `json:"enabled"`
	Time                  Duration `json:"time"`
	Timeout               Duration `json:"timeout"`
	MaxConnectionIdle     Duration `json:"max_connection_idle"`
	MaxConnectionAge      Duration `json:"max_connection_age"`
	MaxConnectionAgeGrace Duration `json:"max_connection_age_grace"`
	MinPingInterval       Duration `json:"min_ping_interval"`
	PermitWithoutStream   bool     `json:"permit_without_stream"`
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
	LLMOutputEnabled     bool   `json:"llm_output_enabled"`
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

// MaintenanceToolConfig groups one-shot admin/maintenance command settings so offline rebuild budgets do not leak into normal online runtime nodes.
// MaintenanceToolConfig 用于收拢一次性管理/维护命令的配置，让离线重建预算不会混入正常在线运行时节点。
type MaintenanceToolConfig struct {
	Postgres               MaintenanceToolPostgresConfig `json:"postgres"`
	VectorRebuildBatchSize int                           `json:"vector_rebuild_batch_size,omitempty"`
}

// MaintenanceToolPostgresConfig keeps PostgreSQL-only maintenance timeouts for offline rebuild, migration, and durable export flows.
// MaintenanceToolPostgresConfig 用于保存 PostgreSQL 专属的维护超时，服务离线重建、迁移和 durable 导出链路。
type MaintenanceToolPostgresConfig struct {
	ReadTimeout  Duration `json:"read_timeout"`
	WriteTimeout Duration `json:"write_timeout"`
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
	MaxBatchSize int                       `json:"max_batch_size,omitempty"`
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

// LLMRouteWeightConfig keeps the per-scene scheduling weights for one LLM route, which is now the only runtime ordering source for shared LLM route selection.
// LLMRouteWeightConfig 用于保存单条 LLM 路由按场景拆分后的调度权重；它现在也是共享 LLM 路由选择时唯一生效的运行时排序来源。
type LLMRouteWeightConfig struct {
	PreCheckL1   *int `json:"precheck_l1,omitempty"`
	PreCheckL2   *int `json:"precheck_l2,omitempty"`
	PostActionL1 *int `json:"postaction_l1,omitempty"`
	PostActionL2 *int `json:"postaction_l2,omitempty"`
	Reserve      *int `json:"reserve,omitempty"`
}

// LLMRouteResolvedWeights keeps one fully materialized five-slot weight view so runtime callers do not need to repeat default-value merging on every selection.
// LLMRouteResolvedWeights 用于保存一份已经补齐默认值的五槽位权重视图，让运行时调用方无需在每次选路时重复做默认值合并。
type LLMRouteResolvedWeights struct {
	PreCheckL1   int
	PreCheckL2   int
	PostActionL1 int
	PostActionL2 int
	Reserve      int
}

// LLMRouteConfig describes one concrete LLM route that owns its provider, endpoint, model, key pool, and node budget policy.
// LLMRouteConfig 用于描述一条具体的 LLM 路由：它自包含 provider、endpoint、model、Key 池与节点预算策略。
type LLMRouteConfig struct {
	Name         string                    `json:"name,omitempty"`
	Weights      LLMRouteWeightConfig      `json:"weights,omitempty"`
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
	MaxSearchKeywords         int      `json:"max_search_keywords"`
	MinSimilarityScore        *float64 `json:"min_similarity_score,omitempty"`
	ReplaceMinSimilarityScore *float64 `json:"replace_min_similarity_score,omitempty"`
	HardDedupeCosineThreshold *float64 `json:"hard_dedupe_cosine_threshold,omitempty"`
	HardDedupePoolTopK        int      `json:"hard_dedupe_pool_top_k,omitempty"`
	HybridEnabled             bool     `json:"hybrid_enabled"`
	LexicalPreTokenize        bool     `json:"lexical_pre_tokenize"`
	LexicalTopK               int      `json:"lexical_top_k,omitempty"`
	RRFK                      int      `json:"rrf_k,omitempty"`
	MMREnabled                bool     `json:"mmr_enabled"`
	MMRLambda                 float64  `json:"mmr_lambda,omitempty"`
	WeibullEnabled            bool     `json:"weibull_enabled"`
	WeibullShape              float64  `json:"weibull_shape,omitempty"`
	WeibullScaleHours         float64  `json:"weibull_scale_hours,omitempty"`
	WeibullMinMultiplier      float64  `json:"weibull_min_multiplier,omitempty"`
	WeibullReinforceWeight    float64  `json:"weibull_reinforce_weight,omitempty"`
	WeibullCrossSessionBoost  float64  `json:"weibull_cross_session_boost,omitempty"`

	hardDedupePoolTopKSet bool `json:"-"`
}

// UnmarshalJSON keeps layered config merges working while tracking whether hard_dedupe_pool_top_k was explicitly provided, so Normalize can distinguish "missing" from "present but invalid".
// UnmarshalJSON 用于在保持分层配置合并语义的同时，追踪 hard_dedupe_pool_top_k 是否被显式提供，让 Normalize 能区分“缺省未配置”和“显式给了非法值”。
func (c *MemoryPipelineConfig) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}
	type memoryPipelineConfigAlias MemoryPipelineConfig
	rawFields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	next := memoryPipelineConfigAlias(*c)
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	*c = MemoryPipelineConfig(next)
	if _, ok := rawFields["hard_dedupe_pool_top_k"]; ok {
		c.hardDedupePoolTopKSet = true
	}
	return nil
}

// DefaultBase returns the baked-in fallback defaults that mirror the shipped base.yaml template.
// DefaultBase 用于返回与随仓库分发的 base.yaml 对齐的内建兜底默认值。
func DefaultBase() Config {
	return Config{
		GRPC: GRPCConfig{
			ListenAddr:             ":8080",
			MaxReceiveMessageBytes: 1 << 20,
			RequestTimeout:         GRPCRequestTimeout{Workspace: Duration{15 * time.Second}, PreCheck: Duration{8 * time.Second}, PostAction: Duration{8 * time.Second}},
			ShutdownTimeout:        Duration{10 * time.Second},
			Keepalive: GRPCKeepaliveConfig{
				Enabled:               true,
				Time:                  Duration{30 * time.Second},
				Timeout:               Duration{10 * time.Second},
				MaxConnectionIdle:     Duration{},
				MaxConnectionAge:      Duration{},
				MaxConnectionAgeGrace: Duration{},
				MinPingInterval:       Duration{20 * time.Second},
				PermitWithoutStream:   true,
			},
		},
		Logging: LoggingConfig{Level: "info", Format: "text", DebugRPCPayloads: false, LLMOutputEnabled: false, ProtectPayloads: false},
		PII:     PIIConfig{DefaultLanguage: "zh-CN"},
		Noise:   NoiseConfig{Enabled: true, DefaultLanguage: "zh-CN", SemanticEnabled: true, SemanticThreshold: 0.88},
		Prompts: PromptConfig{
			PromptLanguage: defaultPromptBundle,
		},
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
		MaintenanceTool: MaintenanceToolConfig{
			Postgres: MaintenanceToolPostgresConfig{
				ReadTimeout:  Duration{defaultMaintenanceToolPostgresReadTimeout},
				WriteTimeout: Duration{defaultMaintenanceToolPostgresWriteTimeout},
			},
			VectorRebuildBatchSize: defaultMaintenanceToolVectorRebuildBatchSize,
		},
		LLM: LLMConfig{
			Routes: []LLMRouteConfig{{
				Provider:    "openai",
				Model:       "gpt-4.1-mini",
				KeyFailover: defaultKeyFailoverConfig(),
			}},
		},
		Embedding: EmbeddingConfig{
			Provider:     "openai",
			Model:        "text-embedding-3-large",
			Dimension:    1024,
			MaxBatchSize: defaultEmbeddingMaxBatchSize,
			KeyFailover:  defaultKeyFailoverConfig(),
		},
		Rerank: RerankConfig{
			Enabled: false,
			TopN:    8,
			Routes: []RerankRouteConfig{{
				Provider:    "dashscope",
				Endpoint:    defaultDashScopeRerankEndpoint,
				Model:       defaultDashScopeRerankModel,
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
			MaxSearchKeywords:         5,
			MinSimilarityScore:        float64Ptr(0.75),
			ReplaceMinSimilarityScore: float64Ptr(defaultMemoryReplaceMinSimilarityScore),
			HardDedupeCosineThreshold: float64Ptr(defaultMemoryHardDedupeCosineThreshold),
			HardDedupePoolTopK:        defaultMemoryHardDedupePoolTopK,
			HybridEnabled:             true,
			LexicalPreTokenize:        true,
			LexicalTopK:               8,
			RRFK:                      60,
			MMREnabled:                true,
			MMRLambda:                 0.75,
			WeibullEnabled:            true,
			WeibullShape:              1.35,
			WeibullScaleHours:         2160,
			WeibullMinMultiplier:      0.4,
			WeibullReinforceWeight:    0.18,
			WeibullCrossSessionBoost:  0.12,
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

// DefaultLocal preserves the legacy helper name while delegating to the new base-config fallback defaults.
// DefaultLocal 用于保留旧的辅助函数名，并委托给新的 base 配置兜底默认值。
func DefaultLocal() Config {
	return DefaultBase()
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
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		if err := applyLayeredAIKeyOverrideReset(&cfg, expandedBytes); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		if err := json.Unmarshal(expandedBytes, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
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

// ResolvedMaxBatchSize returns the effective embedding batch size after defaulting so runtime batching code can stay aligned with config normalization.
// ResolvedMaxBatchSize 用于返回补齐默认值后的 embedding 批大小，让运行时批处理逻辑与配置归一化保持一致。
func (c EmbeddingConfig) ResolvedMaxBatchSize() int {
	if c.MaxBatchSize > 0 {
		return c.MaxBatchSize
	}
	return defaultEmbeddingMaxBatchSize
}

// ProviderRoutes returns the normalized explicit LLM route list.
// ProviderRoutes 用于返回归一化后的显式 LLM 路由列表。
func (c LLMConfig) ProviderRoutes() []LLMRouteConfig {
	return normalizeLLMRouteConfigs(c.Routes)
}

// PrimaryRoute returns the default-route view used when callers do not request one explicit business selection level.
// PrimaryRoute 用于返回未显式指定业务选择层级时采用的默认路由视图。
func (c LLMConfig) PrimaryRoute() (LLMRouteConfig, bool) {
	return c.PrimaryRouteForSelection("")
}

// PrimaryRouteForSelection returns the highest-weight LLM route for one business call tier while preserving declaration order for ties.
// PrimaryRouteForSelection 用于返回某个业务调用层级下权重最高的 LLM 路由；若权重相同，则保持声明顺序。
func (c LLMConfig) PrimaryRouteForSelection(selectionLevel string) (LLMRouteConfig, bool) {
	return primaryLLMRouteForSelection(c.ProviderRoutes(), selectionLevel)
}

// PrimaryModel returns the default primary LLM model chosen from the reserve-weight route view.
// PrimaryModel 用于返回基于 reserve 权重视图选出的默认主 LLM 模型名称。
func (c LLMConfig) PrimaryModel() string {
	return c.PrimaryModelForSelection("")
}

// PrimaryModelForSelection returns the route model chosen for one business call tier after applying per-scene route weights.
// PrimaryModelForSelection 用于在应用分场景路由权重后，返回某个业务调用层级选中的路由模型。
func (c LLMConfig) PrimaryModelForSelection(selectionLevel string) string {
	route, ok := c.PrimaryRouteForSelection(selectionLevel)
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

// primaryLLMRouteForSelection returns the highest-weight LLM route for one business call tier while preserving declaration order for ties.
// primaryLLMRouteForSelection 用于返回某个业务调用层级下权重最高的 LLM 路由；若权重相同，则保持声明顺序。
func primaryLLMRouteForSelection(routes []LLMRouteConfig, selectionLevel string) (LLMRouteConfig, bool) {
	if len(routes) == 0 {
		return LLMRouteConfig{}, false
	}
	best := routes[0]
	bestWeight := best.SelectionWeight(selectionLevel)
	for _, route := range routes[1:] {
		if weight := route.SelectionWeight(selectionLevel); weight > bestWeight {
			best = route
			bestWeight = weight
		}
	}
	return best, true
}

// ResolvedWeights expands one LLM route's optional per-scene overrides into a full five-slot weight table backed by the shared default weight.
// ResolvedWeights 用于把单条 LLM 路由的可选分场景覆盖项展开成完整的五槽位权重表；未声明槽位统一回退到共享默认权重。
func (c LLMRouteConfig) ResolvedWeights() LLMRouteResolvedWeights {
	return LLMRouteResolvedWeights{
		PreCheckL1:   resolveLLMRouteWeight(c.Weights.PreCheckL1, defaultLLMRouteSelectionWeight),
		PreCheckL2:   resolveLLMRouteWeight(c.Weights.PreCheckL2, defaultLLMRouteSelectionWeight),
		PostActionL1: resolveLLMRouteWeight(c.Weights.PostActionL1, defaultLLMRouteSelectionWeight),
		PostActionL2: resolveLLMRouteWeight(c.Weights.PostActionL2, defaultLLMRouteSelectionWeight),
		Reserve:      resolveLLMRouteWeight(c.Weights.Reserve, defaultLLMRouteSelectionWeight),
	}
}

// SelectionWeight returns the concrete weight that one business call tier should use when ordering LLM routes.
// SelectionWeight 用于返回某个业务调用层级在排序 LLM 路由时应采用的具体权重。
func (c LLMRouteConfig) SelectionWeight(selectionLevel string) int {
	weights := c.ResolvedWeights()
	switch strings.ToLower(strings.TrimSpace(selectionLevel)) {
	case "precheck_l1":
		return weights.PreCheckL1
	case "precheck_l2":
		return weights.PreCheckL2
	case "postaction_l1":
		return weights.PostActionL1
	case "postaction_l2":
		return weights.PostActionL2
	default:
		return weights.Reserve
	}
}

// resolveLLMRouteWeight prefers one explicit per-scene override and otherwise falls back to the shared default weight source.
// resolveLLMRouteWeight 用于优先采用显式分场景覆盖值；如果缺失，则回退到共享默认权重来源。
func resolveLLMRouteWeight(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
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
	route.Provider = normalizeRerankProviderValue(route.Provider)
	route.Endpoint = strings.TrimSpace(route.Endpoint)
	if route.Endpoint == "" {
		route.Endpoint = rerankProviderDefaultEndpoint(route.Provider)
	}
	route.APIKeys = trimStringSlice(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
	route.Model = strings.TrimSpace(route.Model)
	if route.Model == "" {
		route.Model = rerankProviderDefaultModel(route.Provider)
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
		if !isSupportedAIProvider(route.Provider) {
			return fmt.Errorf("%s.provider must be one of openai, openai_native, openai_go, or google_ai_studio", label)
		}
		if providerRequiresEndpoint(route.Provider) && strings.TrimSpace(route.Endpoint) == "" {
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
		if err := validateLLMRouteWeightConfig(label, route.Weights); err != nil {
			return err
		}
	}
	return nil
}

// validateLLMRouteWeightConfig rejects negative per-scene route weights so runtime ordering never has to reason about malformed slots.
// validateLLMRouteWeightConfig 用于拒绝负数的分场景路由权重，避免运行时排序阶段处理格式错误的槽位。
func validateLLMRouteWeightConfig(label string, weights LLMRouteWeightConfig) error {
	if weights.PreCheckL1 != nil && *weights.PreCheckL1 < 0 {
		return fmt.Errorf("%s.weights.precheck_l1 must be >= 0", label)
	}
	if weights.PreCheckL2 != nil && *weights.PreCheckL2 < 0 {
		return fmt.Errorf("%s.weights.precheck_l2 must be >= 0", label)
	}
	if weights.PostActionL1 != nil && *weights.PostActionL1 < 0 {
		return fmt.Errorf("%s.weights.postaction_l1 must be >= 0", label)
	}
	if weights.PostActionL2 != nil && *weights.PostActionL2 < 0 {
		return fmt.Errorf("%s.weights.postaction_l2 must be >= 0", label)
	}
	if weights.Reserve != nil && *weights.Reserve < 0 {
		return fmt.Errorf("%s.weights.reserve must be >= 0", label)
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
		switch normalizeRerankProviderValue(route.Provider) {
		case "dashscope", "siliconflow":
		default:
			return fmt.Errorf("%s.provider must be one of dashscope, siliconflow", label)
		}
		if strings.TrimSpace(route.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required", label)
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("%s.model is required", label)
		}
		if route.Priority < 0 {
			return fmt.Errorf("%s.priority must be >= 0", label)
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

// normalizeRerankProviderValue canonicalizes rerank provider aliases so config defaults, validation, and runtime wiring all compare one stable token.
// normalizeRerankProviderValue 用于规范化 rerank provider 别名，让配置默认值、校验和运行时装配始终比较同一份稳定 token。
func normalizeRerankProviderValue(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "dashscope":
		return "dashscope"
	case "siliconflow":
		return "siliconflow"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// rerankProviderDefaultEndpoint returns the provider-specific rerank endpoint used when one explicit route omits an override.
// rerankProviderDefaultEndpoint 用于返回 provider 专属的 rerank 默认地址，在显式路由未覆盖 endpoint 时补齐。
func rerankProviderDefaultEndpoint(provider string) string {
	switch normalizeRerankProviderValue(provider) {
	case "siliconflow":
		return defaultSiliconFlowRerankEndpoint
	default:
		return defaultDashScopeRerankEndpoint
	}
}

// rerankProviderDefaultModel returns the provider-specific rerank model used when one explicit route omits an override.
// rerankProviderDefaultModel 用于返回 provider 专属的 rerank 默认模型，在显式路由未覆盖 model 时补齐。
func rerankProviderDefaultModel(provider string) string {
	switch normalizeRerankProviderValue(provider) {
	case "siliconflow":
		return defaultSiliconFlowRerankModel
	default:
		return defaultDashScopeRerankModel
	}
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
	if c.GRPC.Keepalive.Time.Duration <= 0 {
		c.GRPC.Keepalive.Time = Duration{30 * time.Second}
	}
	if c.GRPC.Keepalive.Timeout.Duration <= 0 {
		c.GRPC.Keepalive.Timeout = Duration{10 * time.Second}
	}
	if c.GRPC.Keepalive.MaxConnectionIdle.Duration < 0 {
		c.GRPC.Keepalive.MaxConnectionIdle = Duration{}
	}
	if c.GRPC.Keepalive.MaxConnectionAge.Duration < 0 {
		c.GRPC.Keepalive.MaxConnectionAge = Duration{}
	}
	if c.GRPC.Keepalive.MaxConnectionAgeGrace.Duration < 0 {
		c.GRPC.Keepalive.MaxConnectionAgeGrace = Duration{}
	}
	if c.GRPC.Keepalive.MinPingInterval.Duration <= 0 {
		c.GRPC.Keepalive.MinPingInterval = Duration{20 * time.Second}
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
	if c.MemoryPipeline.ReplaceMinSimilarityScore == nil {
		c.MemoryPipeline.ReplaceMinSimilarityScore = float64Ptr(defaultMemoryReplaceMinSimilarityScore)
	}
	if c.MemoryPipeline.HardDedupeCosineThreshold == nil {
		c.MemoryPipeline.HardDedupeCosineThreshold = float64Ptr(defaultMemoryHardDedupeCosineThreshold)
	}
	if !c.MemoryPipeline.hardDedupePoolTopKSet && c.MemoryPipeline.HardDedupePoolTopK <= 0 {
		c.MemoryPipeline.HardDedupePoolTopK = defaultMemoryHardDedupePoolTopK
	}
	c.Prompts.PromptLanguage = normalizePromptBundleName(c.Prompts.PromptLanguage)
	c.LLM.Routes = normalizeLLMRouteConfigs(c.LLM.Routes)
	if c.Embedding.Dimension <= 0 && isOpenAIProvider(c.Embedding.Provider) {
		c.Embedding.Dimension = 1024
	}
	if c.Embedding.MaxBatchSize == 0 {
		c.Embedding.MaxBatchSize = defaultEmbeddingMaxBatchSize
	}
	if c.MaintenanceTool.VectorRebuildBatchSize == 0 {
		c.MaintenanceTool.VectorRebuildBatchSize = defaultMaintenanceToolVectorRebuildBatchSize
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
	if c.MaintenanceTool.Postgres.ReadTimeout.Duration <= 0 {
		c.MaintenanceTool.Postgres.ReadTimeout = Duration{defaultMaintenanceToolPostgresReadTimeout}
	}
	if c.MaintenanceTool.Postgres.WriteTimeout.Duration <= 0 {
		c.MaintenanceTool.Postgres.WriteTimeout = Duration{defaultMaintenanceToolPostgresWriteTimeout}
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
	c.Prompts.PromptLanguage = normalizePromptBundleName(c.Prompts.PromptLanguage)
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
	if c.GRPC.Keepalive.Enabled {
		if c.GRPC.Keepalive.Time.Duration <= 0 {
			return errors.New("grpc.keepalive.time must be > 0 when grpc.keepalive.enabled is true")
		}
		if c.GRPC.Keepalive.Timeout.Duration <= 0 {
			return errors.New("grpc.keepalive.timeout must be > 0 when grpc.keepalive.enabled is true")
		}
		if c.GRPC.Keepalive.MinPingInterval.Duration <= 0 {
			return errors.New("grpc.keepalive.min_ping_interval must be > 0 when grpc.keepalive.enabled is true")
		}
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
	if c.MemoryPipeline.ReplaceMinSimilarityScore == nil {
		return errors.New("memory_pipeline.replace_min_similarity_score must be set")
	}
	if *c.MemoryPipeline.ReplaceMinSimilarityScore < 0 || *c.MemoryPipeline.ReplaceMinSimilarityScore > 1 {
		return errors.New("memory_pipeline.replace_min_similarity_score must be in [0,1]")
	}
	if c.MemoryPipeline.HardDedupeCosineThreshold == nil {
		return errors.New("memory_pipeline.hard_dedupe_cosine_threshold must be set")
	}
	if *c.MemoryPipeline.HardDedupeCosineThreshold < 0 || *c.MemoryPipeline.HardDedupeCosineThreshold > 1 {
		return errors.New("memory_pipeline.hard_dedupe_cosine_threshold must be in [0,1]")
	}
	if c.MemoryPipeline.HardDedupePoolTopK <= 0 {
		return errors.New("memory_pipeline.hard_dedupe_pool_top_k must be > 0")
	}
	if c.Noise.SemanticThreshold < 0 || c.Noise.SemanticThreshold > 1 {
		return errors.New("noise.semantic_threshold must be in [0,1]")
	}
	if strings.TrimSpace(c.Prompts.PromptLanguage) == "" {
		return errors.New("prompts.prompt_language must not be empty")
	}
	if strings.TrimSpace(c.Embedding.Provider) == "" {
		return errors.New("embedding.provider is required")
	}
	if !isSupportedAIProvider(c.Embedding.Provider) {
		return errors.New("embedding.provider must be one of openai, openai_native, openai_go, or google_ai_studio")
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
		if c.MaintenanceTool.Postgres.ReadTimeout.Duration <= 0 {
			return errors.New("maintenance_tool.postgres.read_timeout must be > 0 when storage.mode=combined")
		}
		if c.MaintenanceTool.Postgres.WriteTimeout.Duration <= 0 {
			return errors.New("maintenance_tool.postgres.write_timeout must be > 0 when storage.mode=combined")
		}
	}
	if len(c.LLM.Routes) == 0 {
		return errors.New("llm.routes must contain at least one route")
	}
	if err := validateLLMRouteConfigs(c.LLM.Routes); err != nil {
		return err
	}
	if providerRequiresEndpoint(c.Embedding.Provider) && strings.TrimSpace(c.Embedding.Endpoint) == "" {
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
	if c.Embedding.MaxBatchSize <= 0 {
		return errors.New("embedding.max_batch_size must be > 0")
	}
	if c.MaintenanceTool.VectorRebuildBatchSize <= 0 {
		return errors.New("maintenance_tool.vector_rebuild_batch_size must be > 0")
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

// validateRemovedAIEnvOverrides rejects deprecated AI environment variables only when the current config explicitly references those placeholders.
// validateRemovedAIEnvOverrides 用于仅在当前配置显式引用对应占位符时，拒绝已废弃的 AI 环境变量，避免运行时把已移除的单路由语义静默混入新的配置契约。
func validateRemovedAIEnvOverrides(referencedEnvKeys map[string]struct{}) error {
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
		if !envOverrideAllowed(referencedEnvKeys, key) {
			continue
		}
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return fmt.Errorf("%s has been removed; please migrate AI runtime settings into config files", key)
		}
	}
	return nil
}

// applyEnvOverrides applies the supported target settings.
// applyEnvOverrides 用于应用仍然受支持的目标设置。
func applyEnvOverrides(cfg *Config, referencedEnvKeys map[string]struct{}) {
	// Reapply explicit process-level overrides after file-based expansion.
	// 在文件占位符展开之后，再次应用进程级显式覆盖。
	setString := func(k string, target *string) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = v
		}
	}
	setStringSlice := func(k string, target *[]string, nodes *[]AIRoutingNodeConfig) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = splitConfigAPIKeys(v)
			if nodes != nil {
				*nodes = nil
			}
		}
	}
	setInt := func(k string, target *int) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*target = n
			}
		}
	}
	setFloat := func(k string, target *float64) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = n
			}
		}
	}
	setOptionalFloat := func(k string, target **float64) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = float64Ptr(n)
			}
		}
	}
	setBool := func(k string, target *bool) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseBool(v); err == nil {
				*target = n
			}
		}
	}
	setDuration := func(k string, target *Duration) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
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
	setBool("VMM_GRPC_KEEPALIVE_ENABLED", &cfg.GRPC.Keepalive.Enabled)
	setDuration("VMM_GRPC_KEEPALIVE_TIME", &cfg.GRPC.Keepalive.Time)
	setDuration("VMM_GRPC_KEEPALIVE_TIMEOUT", &cfg.GRPC.Keepalive.Timeout)
	setDuration("VMM_GRPC_KEEPALIVE_MAX_CONNECTION_IDLE", &cfg.GRPC.Keepalive.MaxConnectionIdle)
	setDuration("VMM_GRPC_KEEPALIVE_MAX_CONNECTION_AGE", &cfg.GRPC.Keepalive.MaxConnectionAge)
	setDuration("VMM_GRPC_KEEPALIVE_MAX_CONNECTION_AGE_GRACE", &cfg.GRPC.Keepalive.MaxConnectionAgeGrace)
	setDuration("VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL", &cfg.GRPC.Keepalive.MinPingInterval)
	setBool("VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM", &cfg.GRPC.Keepalive.PermitWithoutStream)
	setString("VMM_LOG_LEVEL", &cfg.Logging.Level)
	setString("VMM_LOG_FORMAT", &cfg.Logging.Format)
	setBool("VMM_LOG_DEBUG_RPC_PAYLOADS", &cfg.Logging.DebugRPCPayloads)
	setBool("VMM_LOG_LLM_OUTPUT_ENABLED", &cfg.Logging.LLMOutputEnabled)
	setBool("VMM_LOG_PROTECT_PAYLOADS", &cfg.Logging.ProtectPayloads)
	setString("VMM_LOG_PAYLOAD_ENCRYPTION_KEY", &cfg.Logging.PayloadEncryptionKey)
	setString("VMM_PII_DEFAULT_LANGUAGE", &cfg.PII.DefaultLanguage)
	setBool("VMM_NOISE_ENABLED", &cfg.Noise.Enabled)
	setString("VMM_NOISE_DEFAULT_LANGUAGE", &cfg.Noise.DefaultLanguage)
	setBool("VMM_NOISE_SEMANTIC_ENABLED", &cfg.Noise.SemanticEnabled)
	setFloat("VMM_NOISE_SEMANTIC_THRESHOLD", &cfg.Noise.SemanticThreshold)
	setString("VMM_PROMPTS_PROMPT_LANGUAGE", &cfg.Prompts.PromptLanguage)
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
	setDuration("VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT", &cfg.MaintenanceTool.Postgres.ReadTimeout)
	setDuration("VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT", &cfg.MaintenanceTool.Postgres.WriteTimeout)
	setString("VMM_EMBED_PROVIDER", &cfg.Embedding.Provider)
	setString("VMM_EMBED_ENDPOINT", &cfg.Embedding.Endpoint)
	setStringSlice("VMM_EMBED_API_KEYS", &cfg.Embedding.APIKeys, &cfg.Embedding.Nodes)
	setInt("VMM_EMBED_RPM", &cfg.Embedding.RPM)
	setInt("VMM_EMBED_TPM", &cfg.Embedding.TPM)
	setInt("VMM_EMBED_RPD", &cfg.Embedding.RPD)
	setString("VMM_EMBED_MODEL", &cfg.Embedding.Model)
	setInt("VMM_EMBED_DIMENSION", &cfg.Embedding.Dimension)
	setInt("VMM_EMBED_MAX_BATCH_SIZE", &cfg.Embedding.MaxBatchSize)
	setString("VMM_EMBED_ORGANIZATION", &cfg.Embedding.Organization)
	setInt("VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE", &cfg.MaintenanceTool.VectorRebuildBatchSize)
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
	setOptionalFloat("VMM_MEMORY_REPLACE_MIN_SIMILARITY_SCORE", &cfg.MemoryPipeline.ReplaceMinSimilarityScore)
	setOptionalFloat("VMM_MEMORY_HARD_DEDUPE_COSINE_THRESHOLD", &cfg.MemoryPipeline.HardDedupeCosineThreshold)
	if envOverrideAllowed(referencedEnvKeys, "VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K") {
		if v := strings.TrimSpace(os.Getenv("VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K")); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.MemoryPipeline.HardDedupePoolTopK = n
				cfg.MemoryPipeline.hardDedupePoolTopKSet = true
			}
		}
	}
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

// envOverrideAllowed reports whether one supported environment override key was explicitly referenced by the loaded config layers.
// envOverrideAllowed 用于判断某个受支持的环境变量覆盖键是否被已加载的配置层显式引用。
func envOverrideAllowed(referencedEnvKeys map[string]struct{}, key string) bool {
	if len(referencedEnvKeys) == 0 {
		return false
	}
	_, ok := referencedEnvKeys[key]
	return ok
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

// isGoogleAIStudioProvider reports whether one provider alias resolves to the native Google AI Studio adapter.
// isGoogleAIStudioProvider 用于判断某个 provider 别名是否会落到原生 Google AI Studio 适配器。
func isGoogleAIStudioProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "google_ai_studio":
		return true
	default:
		return false
	}
}

// isSupportedAIProvider reports whether one provider alias resolves to any currently supported LLM/embedding adapter.
// isSupportedAIProvider 用于判断某个 provider 别名是否会落到当前支持的任一 LLM/embedding 适配器。
func isSupportedAIProvider(provider string) bool {
	return isOpenAIProvider(provider) || isGoogleAIStudioProvider(provider)
}

// providerRequiresEndpoint reports whether the provider expects callers to supply an explicit endpoint instead of relying on the SDK default service root.
// providerRequiresEndpoint 用于判断某个 provider 是否要求调用方显式提供 endpoint，而不是依赖 SDK 默认服务根地址。
func providerRequiresEndpoint(provider string) bool {
	return !isGoogleAIStudioProvider(provider)
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
