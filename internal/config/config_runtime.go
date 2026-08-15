// config_runtime.go keeps runtime-facing normalization helpers that canonicalize config values before validation and composition.
// config_runtime.go 用于承载运行时配置归一化辅助逻辑，在校验与装配前把配置值规范化。
package config

import (
	"strings"
	"time"
)

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
	case "controller":
		return "controller"
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

// normalizeSQLiteTokenizerModeValue canonicalizes the SQLite tokenizer mode so runtime wiring can deterministically switch between no-op and Chinese tokenization.
// normalizeSQLiteTokenizerModeValue 用于规范化 SQLite 分词模式，让运行时可以稳定地在关闭分词与中文分词之间切换。
func normalizeSQLiteTokenizerModeValue(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "jieba":
		return "jieba"
	case "none":
		return "none"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// Normalize backfills safe defaults and canonicalizes all runtime-facing config values
// so validation, startup, and adapter selection all observe one stable normalized view.
// Normalize 用于补齐安全默认值并规范化所有面向运行时的配置值，
// 让校验、启动和适配器选择都基于同一份稳定归一化后的视图。
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
	if c.Management.MaxRequestBodyBytes <= 0 {
		c.Management.MaxRequestBodyBytes = 1 << 20
	}
	if c.Management.DefaultPageSize <= 0 {
		c.Management.DefaultPageSize = 30
	}
	if c.Management.MaxPageSize <= 0 {
		c.Management.MaxPageSize = 100
	}
	if c.Management.DefaultPageSize > c.Management.MaxPageSize {
		c.Management.DefaultPageSize = c.Management.MaxPageSize
	}
	if c.Management.ReadHeaderTimeout.Duration <= 0 {
		c.Management.ReadHeaderTimeout = Duration{5 * time.Second}
	}
	if c.Management.RequestTimeout.Duration <= 0 {
		c.Management.RequestTimeout = Duration{30 * time.Second}
	}
	if c.Management.IdleTimeout.Duration <= 0 {
		c.Management.IdleTimeout = Duration{60 * time.Second}
	}
	if c.Management.ShutdownTimeout.Duration <= 0 {
		c.Management.ShutdownTimeout = Duration{10 * time.Second}
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
	if !c.PostAction.sessionAnalysisTimeoutSet && c.PostAction.SessionAnalysisTimeout.Duration <= 0 {
		c.PostAction.SessionAnalysisTimeout = Duration{3 * time.Minute}
	}
	if !c.PostAction.failurePassThresholdSet && c.PostAction.FailurePassThreshold <= 0 {
		c.PostAction.FailurePassThreshold = 5
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
	if c.SQLite.Timeout.Duration <= 0 {
		c.SQLite.Timeout = Duration{5 * time.Second}
	}
	c.SQLite.TokenizerMode = normalizeSQLiteTokenizerModeValue(c.SQLite.TokenizerMode)
	if c.LanceDB.Timeout.Duration <= 0 {
		c.LanceDB.Timeout = Duration{5 * time.Second}
	}
	if strings.TrimSpace(c.LanceDB.TableName) == "" {
		c.LanceDB.TableName = "vmm_memory_vectors"
	}
	if strings.TrimSpace(c.LanceDB.VectorColumn) == "" {
		c.LanceDB.VectorColumn = "vector"
	}
	if strings.TrimSpace(c.Controller.Endpoint) == "" {
		c.Controller.Endpoint = "http://127.0.0.1:19801"
	}
	if strings.TrimSpace(c.Controller.ProcessMode) == "" {
		c.Controller.ProcessMode = "managed"
	}
	if c.Controller.MinimumUptime.Duration <= 0 {
		c.Controller.MinimumUptime = Duration{5 * time.Minute}
	}
	if c.Controller.IdleTimeout.Duration <= 0 {
		c.Controller.IdleTimeout = Duration{15 * time.Minute}
	}
	if c.Controller.LeaseTTL.Duration <= 0 {
		c.Controller.LeaseTTL = Duration{2 * time.Minute}
	}
	if c.Controller.ConnectTimeout.Duration <= 0 {
		c.Controller.ConnectTimeout = Duration{5 * time.Second}
	}
	if c.Controller.StartupTimeout.Duration <= 0 {
		c.Controller.StartupTimeout = Duration{15 * time.Second}
	}
	if c.Controller.StartupRetryInterval.Duration <= 0 {
		c.Controller.StartupRetryInterval = Duration{250 * time.Millisecond}
	}
	if c.Controller.LeaseRenewInterval.Duration <= 0 {
		c.Controller.LeaseRenewInterval = Duration{30 * time.Second}
	}
	if c.Controller.RequestTimeout.Duration <= 0 {
		c.Controller.RequestTimeout = Duration{30 * time.Second}
	}
	if strings.TrimSpace(c.Controller.SpaceID) == "" {
		c.Controller.SpaceID = "vmm-local-default"
	}
	if strings.TrimSpace(c.Controller.SpaceLabel) == "" {
		c.Controller.SpaceLabel = "VulcanMemoryMesh"
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
	c.Management.ListenAddr = strings.TrimSpace(c.Management.ListenAddr)
	c.Management.AccessToken = strings.TrimSpace(c.Management.AccessToken)
	c.Logging.Level = strings.TrimSpace(c.Logging.Level)
	c.Logging.Format = strings.TrimSpace(c.Logging.Format)
	c.Logging.PayloadEncryptionKey = strings.TrimSpace(c.Logging.PayloadEncryptionKey)
	c.PII.DefaultLanguage = strings.TrimSpace(c.PII.DefaultLanguage)
	c.Noise.DefaultLanguage = strings.TrimSpace(c.Noise.DefaultLanguage)
	c.Prompts.PromptLanguage = normalizePromptBundleName(c.Prompts.PromptLanguage)
	c.Storage.Mode = strings.TrimSpace(c.Storage.Mode)
	c.Storage.CombinedProvider = strings.TrimSpace(c.Storage.CombinedProvider)
	c.SQLite.Address = strings.TrimSpace(c.SQLite.Address)
	c.SQLite.TokenizerMode = strings.TrimSpace(c.SQLite.TokenizerMode)
	c.LanceDB.Address = strings.TrimSpace(c.LanceDB.Address)
	c.LanceDB.TableName = strings.TrimSpace(c.LanceDB.TableName)
	c.LanceDB.VectorColumn = strings.TrimSpace(c.LanceDB.VectorColumn)
	c.Controller.Endpoint = strings.TrimSpace(c.Controller.Endpoint)
	c.Controller.Executable = strings.TrimSpace(c.Controller.Executable)
	c.Controller.ProcessMode = strings.ToLower(strings.TrimSpace(c.Controller.ProcessMode))
	c.Controller.SpaceID = strings.TrimSpace(c.Controller.SpaceID)
	c.Controller.SpaceLabel = strings.TrimSpace(c.Controller.SpaceLabel)
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
