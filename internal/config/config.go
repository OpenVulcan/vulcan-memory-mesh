// config.go keeps the configuration model, baked-in defaults, and shared config value types.
// config.go 用于承载配置模型、内建默认值以及共享配置值类型。
package config

import (
	"encoding/json"
	"time"
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

// SQLiteConfig holds local SQLite runtime options for the split-storage relational backend.
// SQLiteConfig 用于保存 split 存储关系后端的本地 SQLite 运行时配置。
type SQLiteConfig struct {
	Address       string   `json:"address"`
	Timeout       Duration `json:"timeout"`
	TokenizerMode string   `json:"tokenizer_mode"`
}

// LanceDBConfig holds local LanceDB runtime options for the split-storage vector backend.
// LanceDBConfig 用于保存 split 存储向量后端的本地 LanceDB 运行时配置。
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
	// ProfileInstruction weights the profile_instruction_main manual profile review chain separately from post-action review.
	// ProfileInstruction 用于为 profile_instruction_main 手工画像评审链路设置独立于 post-action 评审的权重。
	ProfileInstruction *int `json:"profile_instruction,omitempty"`
	Reserve            *int `json:"reserve,omitempty"`
}

// LLMRouteResolvedWeights keeps one fully materialized six-slot weight view so runtime callers do not need to repeat default-value merging on every selection.
// LLMRouteResolvedWeights 用于保存一份已经补齐默认值的六槽位权重视图，让运行时调用方无需在每次选路时重复做默认值合并。
type LLMRouteResolvedWeights struct {
	PreCheckL1   int
	PreCheckL2   int
	PostActionL1 int
	PostActionL2 int
	// ProfileInstruction is the resolved weight for profile_instruction_main manual profile reviews.
	// ProfileInstruction 是 profile_instruction_main 手工画像评审使用的补齐后权重。
	ProfileInstruction int
	Reserve            int
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
	MaxQueueWorkers               int      `json:"max_queue_workers,omitempty"`
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
		SQLite: SQLiteConfig{
			Address:       "127.0.0.1:19501",
			Timeout:       Duration{5 * time.Second},
			TokenizerMode: "jieba",
		},
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
			MaxQueueWorkers:               4,
		},
		PreCheck: PreCheckConfig{IntentTimeout: Duration{5 * time.Second}, TopK: 5, SearchScope: "space"},
		MemoryPipeline: MemoryPipelineConfig{
			MaxSearchKeywords:         5,
			MinSimilarityScore:        float64Ptr(0.75),
			ReplaceMinSimilarityScore: float64Ptr(defaultMemoryReplaceMinSimilarityScore),
			HardDedupeCosineThreshold: float64Ptr(defaultMemoryHardDedupeCosineThreshold),
			HardDedupePoolTopK:        defaultMemoryHardDedupePoolTopK,
			HybridEnabled:             true,
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
