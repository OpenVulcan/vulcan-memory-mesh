// config_test.go verifies normalization, validation, and layered loading against the current gRPC-only runtime contract.
// config_test.go 用于围绕当前仅 gRPC 运行时契约，验证配置归一化、校验和分层加载行为。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesCurrentDefaults verifies the active runtime fills gRPC, provider, and storage defaults expected by the latest local build.
// TestConfigNormalizeAppliesCurrentDefaults 用于验证当前运行时会补齐最新本地构建所需的 gRPC、provider 和存储默认值。
func TestConfigNormalizeAppliesCurrentDefaults(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.MaxReceiveMessageBytes = 0
	cfg.GRPC.RequestTimeout.Workspace = Duration{}
	cfg.GRPC.RequestTimeout.PreCheck = Duration{}
	cfg.GRPC.RequestTimeout.PostAction = Duration{}
	cfg.PreCheck.IntentTimeout = Duration{}
	cfg.PostAction.InputMode = ""
	cfg.PostAction.SessionAnalysisTurnThreshold = 0
	cfg.PostAction.SessionAnalysisTokenThreshold = 0
	cfg.PostAction.SessionAnalysisIdleTimeout = Duration{}
	cfg.PostAction.SessionAnalysisHistoryTurns = 0
	cfg.PostAction.SessionAnalysisMaxInputTokens = 0
	cfg.Vector.Provider = ""
	cfg.Relational.Provider = ""
	cfg.SQLite.Address = ""
	cfg.LanceDB.Address = ""
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.MemoryPipeline.LexicalTopK = 0
	cfg.MemoryPipeline.RRFK = 0
	cfg.MemoryPipeline.MMRLambda = 0
	cfg.MemoryPipeline.WeibullShape = 0
	cfg.MemoryPipeline.WeibullScaleHours = 0
	cfg.MemoryPipeline.WeibullMinMultiplier = -1
	cfg.MemoryPipeline.WeibullReinforceWeight = -1
	cfg.MemoryPipeline.WeibullCrossSessionBoost = -1
	cfg.Rerank.Provider = ""
	cfg.Rerank.Endpoint = ""
	cfg.Rerank.Model = ""
	cfg.Rerank.TopN = 0
	cfg.Rerank.Timeout = Duration{}
	cfg.PreCheck.SimilarityThreshold = 0.82

	cfg.Normalize()

	if cfg.GRPC.MaxReceiveMessageBytes != 1<<20 {
		t.Fatalf("max receive message bytes = %d", cfg.GRPC.MaxReceiveMessageBytes)
	}
	if cfg.GRPC.RequestTimeout.Workspace.Duration != 15*time.Second {
		t.Fatalf("workspace timeout = %v", cfg.GRPC.RequestTimeout.Workspace.Duration)
	}
	if cfg.GRPC.RequestTimeout.PreCheck.Duration != 8*time.Second {
		t.Fatalf("pre-check timeout = %v", cfg.GRPC.RequestTimeout.PreCheck.Duration)
	}
	if cfg.GRPC.RequestTimeout.PostAction.Duration != 8*time.Second {
		t.Fatalf("post-action timeout = %v", cfg.GRPC.RequestTimeout.PostAction.Duration)
	}
	if cfg.PreCheck.IntentTimeout.Duration != 5*time.Second {
		t.Fatalf("pre-check intent timeout = %v", cfg.PreCheck.IntentTimeout.Duration)
	}
	if cfg.PreCheck.SearchScope != "space" {
		t.Fatalf("pre-check search scope = %q", cfg.PreCheck.SearchScope)
	}
	if cfg.MemoryReplaceScope != "project" {
		t.Fatalf("memory replace scope = %q", cfg.MemoryReplaceScope)
	}
	if cfg.PostAction.InputMode != "compat" {
		t.Fatalf("post action input mode = %q", cfg.PostAction.InputMode)
	}
	if cfg.PostAction.SessionAnalysisTurnThreshold != 2 {
		t.Fatalf("post action session analysis turn threshold = %d", cfg.PostAction.SessionAnalysisTurnThreshold)
	}
	if cfg.PostAction.SessionAnalysisTokenThreshold != 12000 {
		t.Fatalf("post action session analysis token threshold = %d", cfg.PostAction.SessionAnalysisTokenThreshold)
	}
	if cfg.PostAction.SessionAnalysisIdleTimeout.Duration != 15*time.Minute {
		t.Fatalf("post action session analysis idle timeout = %v", cfg.PostAction.SessionAnalysisIdleTimeout.Duration)
	}
	if cfg.PostAction.SessionAnalysisHistoryTurns != 3 {
		t.Fatalf("post action session analysis history turns = %d", cfg.PostAction.SessionAnalysisHistoryTurns)
	}
	if cfg.PostAction.SessionAnalysisMaxInputTokens != 6000 {
		t.Fatalf("post action session analysis max input tokens = %d", cfg.PostAction.SessionAnalysisMaxInputTokens)
	}
	if cfg.Vector.Provider != "lancedb" {
		t.Fatalf("vector provider = %q", cfg.Vector.Provider)
	}
	if cfg.Relational.Provider != "sqlite" {
		t.Fatalf("relational provider = %q", cfg.Relational.Provider)
	}
	if cfg.SQLite.Address != "127.0.0.1:19501" {
		t.Fatalf("sqlite address = %q", cfg.SQLite.Address)
	}
	if cfg.LanceDB.Address != "127.0.0.1:19301" {
		t.Fatalf("lancedb address = %q", cfg.LanceDB.Address)
	}
	if cfg.Storage.Mode != "split" {
		t.Fatalf("storage mode = %q", cfg.Storage.Mode)
	}
	if cfg.Storage.CombinedProvider != "postgres" {
		t.Fatalf("storage combined provider = %q", cfg.Storage.CombinedProvider)
	}
	if cfg.Postgres.Schema != "public" {
		t.Fatalf("postgres schema = %q", cfg.Postgres.Schema)
	}
	if cfg.Postgres.Flavor != "paradedb" {
		t.Fatalf("postgres flavor = %q", cfg.Postgres.Flavor)
	}
	if cfg.Postgres.QueryTimeout.Duration != 5*time.Second {
		t.Fatalf("postgres query timeout = %v", cfg.Postgres.QueryTimeout.Duration)
	}
	if cfg.Postgres.ConnectTimeout.Duration != 5*time.Second {
		t.Fatalf("postgres connect timeout = %v", cfg.Postgres.ConnectTimeout.Duration)
	}
	if cfg.Postgres.MaxOpenConns != 10 {
		t.Fatalf("postgres max open conns = %d", cfg.Postgres.MaxOpenConns)
	}
	if cfg.Postgres.MinIdleConns != 1 {
		t.Fatalf("postgres min idle conns = %d", cfg.Postgres.MinIdleConns)
	}
	if !cfg.Postgres.BM25IndexConcurrently {
		t.Fatal("expected postgres bm25 index creation to stay concurrent by default")
	}
	if cfg.Postgres.BM25IndexName != "vmm_memory_nodes_bm25_idx" {
		t.Fatalf("postgres bm25 index name = %q", cfg.Postgres.BM25IndexName)
	}
	if cfg.Postgres.TRGMSimilarityThreshold != 0.2 {
		t.Fatalf("postgres trgm similarity threshold = %v", cfg.Postgres.TRGMSimilarityThreshold)
	}
	if cfg.Postgres.VectorLists != 100 {
		t.Fatalf("postgres vector lists = %d", cfg.Postgres.VectorLists)
	}
	if cfg.Postgres.VectorProbes != 10 {
		t.Fatalf("postgres vector probes = %d", cfg.Postgres.VectorProbes)
	}
	if cfg.Postgres.MigrationBatchSize != 500 {
		t.Fatalf("postgres migration batch size = %d", cfg.Postgres.MigrationBatchSize)
	}
	if !cfg.Retention.Enabled {
		t.Fatal("expected retention to stay enabled by default")
	}
	if cfg.Retention.RecycleScanInterval.Duration != 30*time.Minute {
		t.Fatalf("retention recycle scan interval = %v", cfg.Retention.RecycleScanInterval.Duration)
	}
	if cfg.Retention.TurnKeepExtraTurns != 5 {
		t.Fatalf("retention turn keep extra turns = %d", cfg.Retention.TurnKeepExtraTurns)
	}
	if cfg.Retention.SessionIdleRecycleAfter.Duration != 360*time.Hour {
		t.Fatalf("retention session idle recycle after = %v", cfg.Retention.SessionIdleRecycleAfter.Duration)
	}
	if cfg.Retention.TrashRetention.Duration != 720*time.Hour {
		t.Fatalf("retention trash retention = %v", cfg.Retention.TrashRetention.Duration)
	}
	if cfg.Retention.ProtectPriorityFloor != "P1" {
		t.Fatalf("retention protect priority floor = %q", cfg.Retention.ProtectPriorityFloor)
	}
	if cfg.Retention.ProtectMemoryLevelFloor != "stable" {
		t.Fatalf("retention protect memory level floor = %q", cfg.Retention.ProtectMemoryLevelFloor)
	}
	if !cfg.Retention.SkipProtectedSharedMemories {
		t.Fatal("expected retention to skip protected shared memories by default")
	}
	if cfg.MemoryPipeline.MaxSearchKeywords != 5 {
		t.Fatalf("max search keywords = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0.82 {
		t.Fatalf("min similarity score = %#v", cfg.MemoryPipeline.MinSimilarityScore)
	}
	if !cfg.MemoryPipeline.HybridEnabled {
		t.Fatal("expected hybrid retrieval to stay enabled by default")
	}
	if !cfg.MemoryPipeline.LexicalPreTokenize {
		t.Fatal("expected lexical pre-tokenization to stay enabled by default")
	}
	if cfg.MemoryPipeline.LexicalTopK != 8 {
		t.Fatalf("lexical top_k = %d", cfg.MemoryPipeline.LexicalTopK)
	}
	if cfg.MemoryPipeline.RRFK != 60 {
		t.Fatalf("rrf_k = %d", cfg.MemoryPipeline.RRFK)
	}
	if !cfg.MemoryPipeline.MMREnabled {
		t.Fatal("expected mmr to stay enabled by default")
	}
	if cfg.MemoryPipeline.MMRLambda != 0.75 {
		t.Fatalf("mmr_lambda = %v", cfg.MemoryPipeline.MMRLambda)
	}
	if !cfg.MemoryPipeline.WeibullEnabled {
		t.Fatal("expected weibull decay to stay enabled by default")
	}
	if cfg.MemoryPipeline.WeibullShape != 1.35 {
		t.Fatalf("weibull_shape = %v", cfg.MemoryPipeline.WeibullShape)
	}
	if cfg.MemoryPipeline.WeibullScaleHours != 2160 {
		t.Fatalf("weibull_scale_hours = %v", cfg.MemoryPipeline.WeibullScaleHours)
	}
	if cfg.MemoryPipeline.WeibullMinMultiplier != 0.4 {
		t.Fatalf("weibull_min_multiplier = %v", cfg.MemoryPipeline.WeibullMinMultiplier)
	}
	if cfg.MemoryPipeline.WeibullReinforceWeight != 0.18 {
		t.Fatalf("weibull_reinforce_weight = %v", cfg.MemoryPipeline.WeibullReinforceWeight)
	}
	if cfg.MemoryPipeline.WeibullCrossSessionBoost != 0.12 {
		t.Fatalf("weibull_cross_session_boost = %v", cfg.MemoryPipeline.WeibullCrossSessionBoost)
	}
	if cfg.Rerank.Provider != "dashscope" {
		t.Fatalf("rerank provider = %q", cfg.Rerank.Provider)
	}
	if cfg.Rerank.Endpoint == "" {
		t.Fatal("expected rerank endpoint default")
	}
	if cfg.Rerank.Model != "qwen3-vl-rerank" {
		t.Fatalf("rerank model = %q", cfg.Rerank.Model)
	}
	if cfg.Rerank.TopN != 8 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if cfg.Rerank.Timeout.Duration != 8*time.Second {
		t.Fatalf("rerank timeout = %v", cfg.Rerank.Timeout.Duration)
	}
	if cfg.Logging.DebugRPCPayloads {
		t.Fatal("expected debug rpc payload logs to stay disabled by default")
	}
	if cfg.Logging.ProtectPayloads {
		t.Fatal("expected protected payload logging to stay disabled by default")
	}
}

// TestConfigNormalizeClampsSearchKeywordFanOut verifies the recall keyword fan-out remains capped even when callers provide an excessive value.
// TestConfigNormalizeClampsSearchKeywordFanOut 用于验证即使调用方提供过大的值，召回关键词扇出仍会被钳制在上限内。
func TestConfigNormalizeClampsSearchKeywordFanOut(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.MaxSearchKeywords = 99
	cfg.Normalize()
	if cfg.MemoryPipeline.MaxSearchKeywords != 10 {
		t.Fatalf("max search keywords after clamp = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
}

// TestConfigNormalizeTrimsRuntimeStrings verifies Normalize trims runtime-facing string fields so values accepted by validation do not later fail during listener binding or adapter composition because of surrounding whitespace.
// TestConfigNormalizeTrimsRuntimeStrings 用于验证 Normalize 会裁剪面向运行时的字符串字段，避免已经通过校验的值因首尾空白而在监听绑定或适配器装配阶段再失败。
func TestConfigNormalizeTrimsRuntimeStrings(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.ListenAddr = " 127.0.0.1:8080 "
	cfg.SQLite.Address = " 127.0.0.1:19501 "
	cfg.LanceDB.Address = " 127.0.0.1:19301 "
	cfg.LanceDB.TableName = " vmm_memory_vectors "
	cfg.LanceDB.VectorColumn = " vector "
	cfg.Storage.Mode = " combined "
	cfg.Storage.CombinedProvider = " postgres "
	cfg.Postgres.DSN = " postgres://user:pass@localhost:5432/vmm "
	cfg.Postgres.Schema = " vmm "
	cfg.Postgres.Flavor = " paradedb "
	cfg.Postgres.BM25IndexName = " vmm_memory_nodes_bm25_idx "
	cfg.MemoryReplaceScope = " team "
	cfg.Logging.PayloadEncryptionKey = " 0123456789abcdef0123456789abcdef "
	cfg.LLM.Provider = " openai "
	cfg.LLM.Endpoint = " https://api.openai.com/v1 "
	cfg.Embedding.Provider = " openai "
	cfg.Embedding.Endpoint = " https://api.openai.com/v1 "
	cfg.Rerank.Provider = " dashscope "
	cfg.Rerank.Endpoint = " https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank "
	cfg.Vector.Provider = " lancedb "
	cfg.Relational.Provider = " sqlite "
	cfg.PostAction.InputMode = " compat "
	cfg.Retention.ProtectPriorityFloor = " p0 "
	cfg.Retention.ProtectMemoryLevelFloor = " PERSISTENT "

	cfg.Normalize()

	if cfg.GRPC.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("grpc listen addr = %q", cfg.GRPC.ListenAddr)
	}
	if cfg.SQLite.Address != "127.0.0.1:19501" {
		t.Fatalf("sqlite address = %q", cfg.SQLite.Address)
	}
	if cfg.LanceDB.Address != "127.0.0.1:19301" {
		t.Fatalf("lancedb address = %q", cfg.LanceDB.Address)
	}
	if cfg.LanceDB.TableName != "vmm_memory_vectors" {
		t.Fatalf("lancedb table name = %q", cfg.LanceDB.TableName)
	}
	if cfg.LanceDB.VectorColumn != "vector" {
		t.Fatalf("lancedb vector column = %q", cfg.LanceDB.VectorColumn)
	}
	if cfg.Storage.Mode != "combined" {
		t.Fatalf("storage mode = %q", cfg.Storage.Mode)
	}
	if cfg.Storage.CombinedProvider != "postgres" {
		t.Fatalf("storage combined provider = %q", cfg.Storage.CombinedProvider)
	}
	if cfg.Postgres.DSN != "postgres://user:pass@localhost:5432/vmm" {
		t.Fatalf("postgres dsn = %q", cfg.Postgres.DSN)
	}
	if cfg.Postgres.Schema != "vmm" {
		t.Fatalf("postgres schema = %q", cfg.Postgres.Schema)
	}
	if cfg.Postgres.Flavor != "paradedb" {
		t.Fatalf("postgres flavor = %q", cfg.Postgres.Flavor)
	}
	if cfg.Postgres.BM25IndexName != "vmm_memory_nodes_bm25_idx" {
		t.Fatalf("postgres bm25 index name = %q", cfg.Postgres.BM25IndexName)
	}
	if cfg.MemoryReplaceScope != "team" {
		t.Fatalf("memory replace scope = %q", cfg.MemoryReplaceScope)
	}
	if cfg.Logging.PayloadEncryptionKey != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("payload encryption key = %q", cfg.Logging.PayloadEncryptionKey)
	}
	if cfg.LLM.Provider != "openai" {
		t.Fatalf("llm provider = %q", cfg.LLM.Provider)
	}
	if cfg.LLM.Endpoint != "https://api.openai.com/v1" {
		t.Fatalf("llm endpoint = %q", cfg.LLM.Endpoint)
	}
	if cfg.Embedding.Provider != "openai" {
		t.Fatalf("embedding provider = %q", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Endpoint != "https://api.openai.com/v1" {
		t.Fatalf("embedding endpoint = %q", cfg.Embedding.Endpoint)
	}
	if cfg.Rerank.Provider != "dashscope" {
		t.Fatalf("rerank provider = %q", cfg.Rerank.Provider)
	}
	if cfg.Rerank.Endpoint != "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank" {
		t.Fatalf("rerank endpoint = %q", cfg.Rerank.Endpoint)
	}
	if cfg.Vector.Provider != "lancedb" {
		t.Fatalf("vector provider = %q", cfg.Vector.Provider)
	}
	if cfg.Relational.Provider != "sqlite" {
		t.Fatalf("relational provider = %q", cfg.Relational.Provider)
	}
	if cfg.PostAction.InputMode != "compat" {
		t.Fatalf("post action input mode = %q", cfg.PostAction.InputMode)
	}
	if cfg.Retention.ProtectPriorityFloor != "P0" {
		t.Fatalf("retention protect priority floor = %q", cfg.Retention.ProtectPriorityFloor)
	}
	if cfg.Retention.ProtectMemoryLevelFloor != "persistent" {
		t.Fatalf("retention protect memory level floor = %q", cfg.Retention.ProtectMemoryLevelFloor)
	}
}

// TestConfigValidateRejectsInvalidHybridRetrievalKnobs verifies the new lexical recall and RRF parameters stay strictly positive once configured.
// TestConfigValidateRejectsInvalidHybridRetrievalKnobs 用于验证新增的 lexical 召回和 RRF 参数一旦配置后必须保持严格正数。
func TestConfigValidateRejectsInvalidHybridRetrievalKnobs(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.LexicalTopK = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.lexical_top_k must be > 0" {
		t.Fatalf("unexpected lexical_top_k validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.RRFK = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.rrf_k must be > 0" {
		t.Fatalf("unexpected rrf_k validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.MMRLambda = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.mmr_lambda must be in (0,1]" {
		t.Fatalf("unexpected mmr_lambda validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.WeibullShape = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.weibull_shape must be > 0" {
		t.Fatalf("unexpected weibull_shape validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.WeibullMinMultiplier = 2
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.weibull_min_multiplier must be in [0,1]" {
		t.Fatalf("unexpected weibull_min_multiplier validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnknownPostActionMode verifies the new string-only post-action contract still rejects unsupported validation modes.
// TestConfigValidateRejectsUnknownPostActionMode 用于验证新的纯字符串 post-action 契约仍会拒绝不支持的校验模式。
func TestConfigValidateRejectsUnknownPostActionMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.InputMode = "broken"
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.input_mode must be either strict or compat" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnknownLoggingLevel verifies the runtime config only accepts the documented log levels so release deployments can rely on error-only output without ambiguous fallback behavior.
// TestConfigValidateRejectsUnknownLoggingLevel 用于验证运行时配置只接受文档声明的日志级别，让 release 部署可以稳定依赖 error-only 输出，而不是落到含糊的隐式回退行为。
func TestConfigValidateRejectsUnknownLoggingLevel(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Logging.Level = "verbose"
	if err := cfg.Validate(); err == nil || err.Error() != "logging.level must be one of debug, info, warn, error" {
		t.Fatalf("unexpected logging level validate error: %v", err)
	}
}

// TestConfigValidateRejectsInvalidPayloadEncryptionKey verifies protected payload logging cannot start with one malformed key that would silently disable encrypted audit fields at runtime.
// TestConfigValidateRejectsInvalidPayloadEncryptionKey 用于验证受保护载荷日志不能在错误密钥下启动，避免运行时静默丢失加密审计字段。
func TestConfigValidateRejectsInvalidPayloadEncryptionKey(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Logging.ProtectPayloads = true
	cfg.Logging.PayloadEncryptionKey = "bad-key"
	if err := cfg.Validate(); err == nil || err.Error() != "logging.payload_encryption_key is invalid: must be 32 raw bytes, 64 hex chars, or base64 for 32 bytes" {
		t.Fatalf("unexpected payload encryption key validate error: %v", err)
	}
}

// TestConfigValidateRejectsInvalidPostActionAnalysisThresholds verifies the future session-analysis trigger thresholds must stay positive.
// TestConfigValidateRejectsInvalidPostActionAnalysisThresholds 用于验证未来 session 分析触发阈值必须保持正数。
func TestConfigValidateRejectsInvalidPostActionAnalysisThresholds(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.SessionAnalysisTurnThreshold = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_turn_threshold must be > 0" {
		t.Fatalf("unexpected turn-threshold validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisTokenThreshold = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_token_threshold must be > 0" {
		t.Fatalf("unexpected token-threshold validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisIdleTimeout = Duration{}
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_idle_timeout must be > 0" {
		t.Fatalf("unexpected idle-timeout validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisHistoryTurns = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_history_turns must be > 0" {
		t.Fatalf("unexpected history-turns validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisMaxInputTokens = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_max_input_tokens must be > 0" {
		t.Fatalf("unexpected max-input-tokens validate error: %v", err)
	}
}

// TestConfigValidateRejectsPreCheckTimeoutBudget verifies the outer pre-check RPC budget must stay larger than the internal intent step timeout.
// TestConfigValidateRejectsPreCheckTimeoutBudget 用于验证外层 pre-check RPC 预算必须大于内部意图步骤超时。
func TestConfigValidateRejectsPreCheckTimeoutBudget(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.RequestTimeout.PreCheck = Duration{5 * time.Second}
	cfg.PreCheck.IntentTimeout = Duration{5 * time.Second}
	if err := cfg.Validate(); err == nil || err.Error() != "grpc.request_timeout.pre_check must be greater than pre_check.intent_timeout" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnsupportedPreCheckSearchScope verifies startup validation rejects unsupported pre-check scope tokens instead of silently broadening recall.
// TestConfigValidateRejectsUnsupportedPreCheckSearchScope 用于验证启动校验会拒绝不受支持的 pre-check 作用域 token，而不是静默放宽召回范围。
func TestConfigValidateRejectsUnsupportedPreCheckSearchScope(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PreCheck.SearchScope = "workspace"
	cfg.Normalize()

	err := cfg.Validate()
	if err == nil || err.Error() != "pre_check.search_scope must be one of team, space, or project" {
		t.Fatalf("unexpected pre-check search scope error: %v", err)
	}
}

// TestConfigValidateRejectsUnsupportedMemoryReplaceScope verifies startup validation rejects unsupported memory-replacement scope tokens instead of silently broadening supersede reach.
// TestConfigValidateRejectsUnsupportedMemoryReplaceScope 用于验证启动校验会拒绝不受支持的记忆更替作用域 token，而不是静默放宽 supersede 边界。
func TestConfigValidateRejectsUnsupportedMemoryReplaceScope(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryReplaceScope = "workspace"
	cfg.Normalize()

	err := cfg.Validate()
	if err == nil || err.Error() != "memory_replace_scope must be one of session, team, space, or project" {
		t.Fatalf("unexpected memory replace scope error: %v", err)
	}
}

// TestConfigValidateRejectsRemovedProviders verifies the runtime no longer accepts removed fallback providers and only keeps SQLite as the relational backend.
// TestConfigValidateRejectsRemovedProviders 用于验证运行时已经不再接受被移除的回退 provider，并且关系库存储只保留 SQLite。
func TestConfigValidateRejectsRemovedProviders(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Vector.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "vector.provider must be lancedb" {
		t.Fatalf("unexpected vector validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.Relational.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "relational.provider must be sqlite" {
		t.Fatalf("unexpected relational validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnsupportedStorageMode verifies the new storage-mode selector fails fast on unknown values before runtime composition starts.
// TestConfigValidateRejectsUnsupportedStorageMode 用于验证新的存储模式选择器会在运行时装配前快速拒绝未知取值。
func TestConfigValidateRejectsUnsupportedStorageMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "hybrid"
	if err := cfg.Validate(); err == nil || err.Error() != "storage.mode must be either split or combined" {
		t.Fatalf("unexpected storage mode validate error: %v", err)
	}
}

// TestConfigValidateAllowsUnusedCombinedProviderInSplitMode verifies split mode ignores stale combined-provider overrides because the PostgreSQL combined path is not active there.
// TestConfigValidateAllowsUnusedCombinedProviderInSplitMode 用于验证 split 模式会忽略陈旧的 combined-provider 覆盖值，因为此时 PostgreSQL 组合路径并未启用。
func TestConfigValidateAllowsUnusedCombinedProviderInSplitMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "split"
	cfg.Storage.CombinedProvider = "mysql"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("split mode should ignore unused combined provider, got: %v", err)
	}
}

// TestConfigValidateRequiresPostgresDSNInCombinedMode verifies the combined PostgreSQL mode cannot start without an explicit DSN.
// TestConfigValidateRequiresPostgresDSNInCombinedMode 用于验证 PostgreSQL 组合模式缺少 DSN 时不能启动。
func TestConfigValidateRequiresPostgresDSNInCombinedMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "combined"
	cfg.Postgres.DSN = ""
	if err := cfg.Validate(); err == nil || err.Error() != "postgres.dsn is required when storage.mode=combined" {
		t.Fatalf("unexpected combined postgres dsn validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnsupportedPostgresFlavor verifies the PostgreSQL dialect selector only accepts the documented paradeDB and standard flavors.
// TestConfigValidateRejectsUnsupportedPostgresFlavor 用于验证 PostgreSQL 方言选择器只接受文档声明的 paradedb 与 standard。
func TestConfigValidateRejectsUnsupportedPostgresFlavor(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "combined"
	cfg.Postgres.DSN = "postgres://user:pass@localhost:5432/vmm"
	cfg.Postgres.Flavor = "bm25-plus"
	if err := cfg.Validate(); err == nil || err.Error() != "postgres.flavor must be either paradedb or standard" {
		t.Fatalf("unexpected combined postgres flavor validate error: %v", err)
	}
}

// TestLoadExpandsEnvPlaceholdersFromDotEnv verifies the layered loader expands placeholders from the nearest resolved .env file.
// TestLoadExpandsEnvPlaceholdersFromDotEnv 用于验证分层加载器会从最近解析到的 .env 文件里展开占位符。
func TestLoadExpandsEnvPlaceholdersFromDotEnv(t *testing.T) {
	const key = "TEST_CONFIG_API_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "from-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadTrimsConfigPathWhitespace verifies one config path with surrounding whitespace still resolves its colocated config file and root-level .env.
// TestLoadTrimsConfigPathWhitespace 用于验证单个带首尾空白的配置路径仍能解析同目录配置文件和根目录 .env。
func TestLoadTrimsConfigPathWhitespace(t *testing.T) {
	const key = "TEST_TRIMMED_LOAD_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=trimmed-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("  "+configPath+"  ", DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "trimmed-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadPathsMergesSystemAndOverrideConfigs verifies later config files override earlier ones while their colocated .env files also override earlier values.
// TestLoadPathsMergesSystemAndOverrideConfigs 用于验证后面的配置文件会覆盖前面的配置，同时其同目录的 .env 也会覆盖更早的值。
func TestLoadPathsMergesSystemAndOverrideConfigs(t *testing.T) {
	const key = "TEST_LOAD_PATHS_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	systemConfigDir := filepath.Join(rootDir, "configs")
	overrideDir := filepath.Join(rootDir, "user")
	if err := os.MkdirAll(systemConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=system-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, ".env"), []byte(key+"=user-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	systemConfig := filepath.Join(systemConfigDir, "local.json")
	overrideConfig := filepath.Join(overrideDir, "local.json")
	if err := os.WriteFile(systemConfig, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideConfig, []byte(`{"llm":{"model":"user-model"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{systemConfig, overrideConfig}, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "user-model" {
		t.Fatalf("llm model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "user-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadPathsTrimsConfigPathWhitespace verifies layered loading trims surrounding whitespace before deduplicating config paths and resolving neighboring .env files.
// TestLoadPathsTrimsConfigPathWhitespace 用于验证分层加载会在配置路径去重和相邻 .env 解析前先裁剪首尾空白。
func TestLoadPathsTrimsConfigPathWhitespace(t *testing.T) {
	const key = "TEST_TRIMMED_LOAD_PATHS_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	systemConfigDir := filepath.Join(rootDir, "configs")
	overrideDir := filepath.Join(rootDir, "user")
	if err := os.MkdirAll(systemConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=system-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, ".env"), []byte(key+"=user-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	systemConfig := filepath.Join(systemConfigDir, "local.json")
	overrideConfig := filepath.Join(overrideDir, "local.json")
	if err := os.WriteFile(systemConfig, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideConfig, []byte(`{"llm":{"model":"trimmed-user-model"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{"  " + systemConfig + "  ", "\n" + overrideConfig + "\t"}, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "trimmed-user-model" {
		t.Fatalf("llm model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "user-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadExpandsModelSpecificProviderParams verifies provider parameter maps still support environment-expanded model keys.
// TestLoadExpandsModelSpecificProviderParams 用于验证 provider 参数映射仍支持带环境变量展开的模型键名。
func TestLoadExpandsModelSpecificProviderParams(t *testing.T) {
	const modelKey = "TEST_MODEL_NAME"
	const apiKey = "TEST_MODEL_PARAMS_API_KEY"
	restoreEnv(t, modelKey)
	restoreEnv(t, apiKey)
	t.Setenv(modelKey, "qwen3.5-flash")
	t.Setenv(apiKey, "test-key")

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"workspace":"15s","pre_check":"8s","post_action":"8s"},"shutdown_timeout":"10s"},
		"sqlite":{"address":"127.0.0.1:19501","timeout":"5s"},
		"lancedb":{"address":"127.0.0.1:19301","timeout":"5s","table_name":"vmm_memory_vectors","vector_column":"vector"},
		"llm":{
			"provider":"openai",
			"endpoint":"https://api.openai.com/v1",
			"api_key":"${` + apiKey + `}",
			"model":"${` + modelKey + `}",
			"params":{"reasoning_effort":"low"},
			"model_params":{"${` + modelKey + `}":{"enable_thinking":false}}
		},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + apiKey + `}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"sqlite"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75}
	}`
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Params["reasoning_effort"] != "low" {
		t.Fatalf("llm.params.reasoning_effort = %#v", cfg.LLM.Params["reasoning_effort"])
	}
	modelParams, ok := cfg.LLM.ModelParams["qwen3.5-flash"]
	if !ok {
		t.Fatalf("expected model params for qwen3.5-flash, got %#v", cfg.LLM.ModelParams)
	}
	if enabled, ok := modelParams["enable_thinking"].(bool); !ok || enabled {
		t.Fatalf("llm.model_params.enable_thinking = %#v", modelParams["enable_thinking"])
	}
}

// TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds verifies process-level overrides can tune the future session-analysis trigger thresholds.
// TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds 用于验证进程级环境变量可以覆盖未来 session 分析触发阈值。
func TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_TURN_THRESHOLD", "33")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_TOKEN_THRESHOLD", "24000")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_IDLE_TIMEOUT", "25m")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_HISTORY_TURNS", "5")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_MAX_INPUT_TOKENS", "7200")

	applyEnvOverrides(&cfg)

	if cfg.PostAction.SessionAnalysisTurnThreshold != 33 {
		t.Fatalf("post action session analysis turn threshold = %d", cfg.PostAction.SessionAnalysisTurnThreshold)
	}
	if cfg.PostAction.SessionAnalysisTokenThreshold != 24000 {
		t.Fatalf("post action session analysis token threshold = %d", cfg.PostAction.SessionAnalysisTokenThreshold)
	}
	if cfg.PostAction.SessionAnalysisIdleTimeout.Duration != 25*time.Minute {
		t.Fatalf("post action session analysis idle timeout = %v", cfg.PostAction.SessionAnalysisIdleTimeout.Duration)
	}
	if cfg.PostAction.SessionAnalysisHistoryTurns != 5 {
		t.Fatalf("post action session analysis history turns = %d", cfg.PostAction.SessionAnalysisHistoryTurns)
	}
	if cfg.PostAction.SessionAnalysisMaxInputTokens != 7200 {
		t.Fatalf("post action session analysis max input tokens = %d", cfg.PostAction.SessionAnalysisMaxInputTokens)
	}
}

// TestApplyEnvOverridesSetsPreCheckSearchScope verifies process-level overrides can widen or narrow the pre-check recall scope without editing the base JSON config.
// TestApplyEnvOverridesSetsPreCheckSearchScope 用于验证进程级环境变量可以在不修改基础 JSON 配置的前提下调整 pre-check 召回作用域。
func TestApplyEnvOverridesSetsPreCheckSearchScope(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_PRE_CHECK_SEARCH_SCOPE", "team")

	applyEnvOverrides(&cfg)
	cfg.Normalize()

	if cfg.PreCheck.SearchScope != "team" {
		t.Fatalf("pre-check search scope = %q", cfg.PreCheck.SearchScope)
	}
}

// TestApplyEnvOverridesSetsMemoryReplaceAndRetentionKnobs verifies process-level overrides can tune the dedicated replacement scope and retention defaults without editing the base JSON config.
// TestApplyEnvOverridesSetsMemoryReplaceAndRetentionKnobs 用于验证进程级环境变量可以在不修改基础 JSON 配置的前提下调整专用更替作用域和 retention 参数。
func TestApplyEnvOverridesSetsMemoryReplaceAndRetentionKnobs(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_MEMORY_REPLACE_SCOPE", "session")
	t.Setenv("VMM_RETENTION_ENABLED", "false")
	t.Setenv("VMM_RETENTION_RECYCLE_SCAN_INTERVAL", "45m")
	t.Setenv("VMM_RETENTION_TURN_KEEP_EXTRA_TURNS", "8")
	t.Setenv("VMM_RETENTION_SESSION_IDLE_RECYCLE_AFTER", "480h")
	t.Setenv("VMM_RETENTION_TRASH_RETENTION", "960h")
	t.Setenv("VMM_RETENTION_PROTECT_PRIORITY_FLOOR", "P0")
	t.Setenv("VMM_RETENTION_PROTECT_MEMORY_LEVEL_FLOOR", "persistent")
	t.Setenv("VMM_RETENTION_SKIP_PROTECTED_SHARED_MEMORIES", "false")

	applyEnvOverrides(&cfg)
	cfg.Normalize()

	if cfg.MemoryReplaceScope != "session" {
		t.Fatalf("memory replace scope = %q", cfg.MemoryReplaceScope)
	}
	if cfg.Retention.Enabled {
		t.Fatal("expected retention enabled override to be false")
	}
	if cfg.Retention.RecycleScanInterval.Duration != 45*time.Minute {
		t.Fatalf("retention recycle scan interval = %v", cfg.Retention.RecycleScanInterval.Duration)
	}
	if cfg.Retention.TurnKeepExtraTurns != 8 {
		t.Fatalf("retention turn keep extra turns = %d", cfg.Retention.TurnKeepExtraTurns)
	}
	if cfg.Retention.SessionIdleRecycleAfter.Duration != 480*time.Hour {
		t.Fatalf("retention session idle recycle after = %v", cfg.Retention.SessionIdleRecycleAfter.Duration)
	}
	if cfg.Retention.TrashRetention.Duration != 960*time.Hour {
		t.Fatalf("retention trash retention = %v", cfg.Retention.TrashRetention.Duration)
	}
	if cfg.Retention.ProtectPriorityFloor != "P0" {
		t.Fatalf("retention protect priority floor = %q", cfg.Retention.ProtectPriorityFloor)
	}
	if cfg.Retention.ProtectMemoryLevelFloor != "persistent" {
		t.Fatalf("retention protect memory level floor = %q", cfg.Retention.ProtectMemoryLevelFloor)
	}
	if cfg.Retention.SkipProtectedSharedMemories {
		t.Fatal("expected retention skip protected shared memories override to be false")
	}
}

// TestApplyEnvOverridesSetsCombinedPostgresConfig verifies process-level overrides can switch the runtime into combined PostgreSQL mode without editing the base JSON config.
// TestApplyEnvOverridesSetsCombinedPostgresConfig 用于验证进程级环境变量可以在不修改基础 JSON 配置的前提下切换到 PostgreSQL 组合模式。
func TestApplyEnvOverridesSetsCombinedPostgresConfig(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_STORAGE_MODE", "combined")
	t.Setenv("VMM_STORAGE_COMBINED_PROVIDER", "postgres")
	t.Setenv("VMM_POSTGRES_DSN", "postgres://user:pass@localhost:5432/vmm")
	t.Setenv("VMM_POSTGRES_SCHEMA", "vmm")
	t.Setenv("VMM_POSTGRES_FLAVOR", "standard")
	t.Setenv("VMM_POSTGRES_QUERY_TIMEOUT", "7s")
	t.Setenv("VMM_POSTGRES_CONNECT_TIMEOUT", "6s")
	t.Setenv("VMM_POSTGRES_MAX_OPEN_CONNS", "16")
	t.Setenv("VMM_POSTGRES_MIN_IDLE_CONNS", "2")
	t.Setenv("VMM_POSTGRES_AUTO_CREATE_EXTENSIONS", "true")
	t.Setenv("VMM_POSTGRES_BM25_INDEX_CONCURRENTLY", "false")
	t.Setenv("VMM_POSTGRES_BM25_INDEX_NAME", "memory_bm25_idx")
	t.Setenv("VMM_POSTGRES_TRGM_SIMILARITY_THRESHOLD", "0.35")
	t.Setenv("VMM_POSTGRES_VECTOR_LISTS", "64")
	t.Setenv("VMM_POSTGRES_VECTOR_PROBES", "8")
	t.Setenv("VMM_POSTGRES_MIGRATION_BATCH_SIZE", "128")

	applyEnvOverrides(&cfg)
	cfg.Normalize()

	if cfg.Storage.Mode != "combined" {
		t.Fatalf("storage mode = %q", cfg.Storage.Mode)
	}
	if cfg.Storage.CombinedProvider != "postgres" {
		t.Fatalf("storage combined provider = %q", cfg.Storage.CombinedProvider)
	}
	if cfg.Postgres.DSN != "postgres://user:pass@localhost:5432/vmm" {
		t.Fatalf("postgres dsn = %q", cfg.Postgres.DSN)
	}
	if cfg.Postgres.Schema != "vmm" {
		t.Fatalf("postgres schema = %q", cfg.Postgres.Schema)
	}
	if cfg.Postgres.Flavor != "standard" {
		t.Fatalf("postgres flavor = %q", cfg.Postgres.Flavor)
	}
	if cfg.Postgres.QueryTimeout.Duration != 7*time.Second {
		t.Fatalf("postgres query timeout = %v", cfg.Postgres.QueryTimeout.Duration)
	}
	if cfg.Postgres.ConnectTimeout.Duration != 6*time.Second {
		t.Fatalf("postgres connect timeout = %v", cfg.Postgres.ConnectTimeout.Duration)
	}
	if cfg.Postgres.MaxOpenConns != 16 {
		t.Fatalf("postgres max open conns = %d", cfg.Postgres.MaxOpenConns)
	}
	if cfg.Postgres.MinIdleConns != 2 {
		t.Fatalf("postgres min idle conns = %d", cfg.Postgres.MinIdleConns)
	}
	if !cfg.Postgres.AutoCreateExtensions {
		t.Fatal("expected postgres auto-create extensions to be enabled")
	}
	if cfg.Postgres.BM25IndexConcurrently {
		t.Fatal("expected postgres bm25 index concurrently flag to be disabled")
	}
	if cfg.Postgres.BM25IndexName != "memory_bm25_idx" {
		t.Fatalf("postgres bm25 index name = %q", cfg.Postgres.BM25IndexName)
	}
	if cfg.Postgres.TRGMSimilarityThreshold != 0.35 {
		t.Fatalf("postgres trgm similarity threshold = %v", cfg.Postgres.TRGMSimilarityThreshold)
	}
	if cfg.Postgres.VectorLists != 64 {
		t.Fatalf("postgres vector lists = %d", cfg.Postgres.VectorLists)
	}
	if cfg.Postgres.VectorProbes != 8 {
		t.Fatalf("postgres vector probes = %d", cfg.Postgres.VectorProbes)
	}
	if cfg.Postgres.MigrationBatchSize != 128 {
		t.Fatalf("postgres migration batch size = %d", cfg.Postgres.MigrationBatchSize)
	}
}

// TestNormalizePreservesExplicitZeroPostgresMinIdleConns verifies Normalize keeps an explicit zero min-idle setting so operators can disable prewarmed idle PostgreSQL connections.
// TestNormalizePreservesExplicitZeroPostgresMinIdleConns 用于验证 Normalize 会保留显式配置的 PostgreSQL 最小空闲连接数 0，便于运维关闭预热空闲连接。
func TestNormalizePreservesExplicitZeroPostgresMinIdleConns(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Postgres.MinIdleConns = 0

	cfg.Normalize()

	if cfg.Postgres.MinIdleConns != 0 {
		t.Fatalf("postgres min idle conns = %d", cfg.Postgres.MinIdleConns)
	}
}

// TestApplyEnvOverridesSetsRPCPayloadLogging verifies process-level overrides can explicitly enable shared RPC payload debug logging for local troubleshooting without changing the base config file.
// TestApplyEnvOverridesSetsRPCPayloadLogging 用于验证进程级环境变量可以在不修改基础配置文件的前提下显式开启共享 RPC 载荷调试日志。
func TestApplyEnvOverridesSetsRPCPayloadLogging(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_LOG_DEBUG_RPC_PAYLOADS", "true")

	applyEnvOverrides(&cfg)

	if !cfg.Logging.DebugRPCPayloads {
		t.Fatal("expected debug rpc payload logs to be enabled by env override")
	}
}

// TestApplyEnvOverridesSetsProtectedPayloadLogging verifies process-level overrides can enable protected payload logging and inject its encryption key without editing the base config file.
// TestApplyEnvOverridesSetsProtectedPayloadLogging 用于验证进程级环境变量可以在不修改基础配置文件的前提下启用受保护载荷日志并注入加密密钥。
func TestApplyEnvOverridesSetsProtectedPayloadLogging(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_LOG_PROTECT_PAYLOADS", "true")
	t.Setenv("VMM_LOG_PAYLOAD_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")

	applyEnvOverrides(&cfg)

	if !cfg.Logging.ProtectPayloads {
		t.Fatal("expected protected payload logging to be enabled by env override")
	}
	if cfg.Logging.PayloadEncryptionKey != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("payload encryption key = %q", cfg.Logging.PayloadEncryptionKey)
	}
}

// TestApplyEnvOverridesSetsRerankSettings verifies process-level overrides can enable DashScope rerank without editing the base config file.
// TestApplyEnvOverridesSetsRerankSettings 用于验证进程级环境变量可以在不修改基础配置文件的前提下启用 DashScope rerank。
func TestApplyEnvOverridesSetsRerankSettings(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_RERANK_ENABLED", "true")
	t.Setenv("VMM_RERANK_PROVIDER", "dashscope")
	t.Setenv("VMM_RERANK_ENDPOINT", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank")
	t.Setenv("VMM_RERANK_API_KEY", "dashscope-key")
	t.Setenv("VMM_RERANK_MODEL", "qwen3-vl-rerank")
	t.Setenv("VMM_RERANK_TOP_N", "6")
	t.Setenv("VMM_RERANK_TIMEOUT", "9s")

	applyEnvOverrides(&cfg)

	if !cfg.Rerank.Enabled {
		t.Fatal("expected rerank to be enabled")
	}
	if cfg.Rerank.Provider != "dashscope" {
		t.Fatalf("rerank provider = %q", cfg.Rerank.Provider)
	}
	if cfg.Rerank.APIKey != "dashscope-key" {
		t.Fatalf("rerank api key = %q", cfg.Rerank.APIKey)
	}
	if cfg.Rerank.Model != "qwen3-vl-rerank" {
		t.Fatalf("rerank model = %q", cfg.Rerank.Model)
	}
	if cfg.Rerank.TopN != 6 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if cfg.Rerank.Timeout.Duration != 9*time.Second {
		t.Fatalf("rerank timeout = %v", cfg.Rerank.Timeout.Duration)
	}
}

// TestApplyEnvOverridesSetsHybridRetrievalSettings verifies process-level overrides can tune lexical recall and RRF without editing the base config file.
// TestApplyEnvOverridesSetsHybridRetrievalSettings 用于验证进程级环境变量可以在不改基础配置文件的前提下调整 lexical 召回和 RRF 参数。
func TestApplyEnvOverridesSetsHybridRetrievalSettings(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_MEMORY_HYBRID_ENABLED", "false")
	t.Setenv("VMM_MEMORY_LEXICAL_PRETOKENIZE", "false")
	t.Setenv("VMM_MEMORY_LEXICAL_TOP_K", "11")
	t.Setenv("VMM_MEMORY_RRF_K", "77")
	t.Setenv("VMM_MEMORY_MMR_ENABLED", "false")
	t.Setenv("VMM_MEMORY_MMR_LAMBDA", "0.66")
	t.Setenv("VMM_MEMORY_WEIBULL_ENABLED", "false")
	t.Setenv("VMM_MEMORY_WEIBULL_SHAPE", "1.8")
	t.Setenv("VMM_MEMORY_WEIBULL_SCALE_HOURS", "1440")
	t.Setenv("VMM_MEMORY_WEIBULL_MIN_MULTIPLIER", "0.3")
	t.Setenv("VMM_MEMORY_WEIBULL_REINFORCE_WEIGHT", "0.22")
	t.Setenv("VMM_MEMORY_WEIBULL_CROSS_SESSION_BOOST", "0.15")

	applyEnvOverrides(&cfg)

	if cfg.MemoryPipeline.HybridEnabled {
		t.Fatal("expected hybrid retrieval to be disabled by env override")
	}
	if cfg.MemoryPipeline.LexicalPreTokenize {
		t.Fatal("expected lexical pre-tokenization to be disabled by env override")
	}
	if cfg.MemoryPipeline.LexicalTopK != 11 {
		t.Fatalf("memory pipeline lexical top_k = %d", cfg.MemoryPipeline.LexicalTopK)
	}
	if cfg.MemoryPipeline.RRFK != 77 {
		t.Fatalf("memory pipeline rrf_k = %d", cfg.MemoryPipeline.RRFK)
	}
	if cfg.MemoryPipeline.MMREnabled {
		t.Fatal("expected mmr to be disabled by env override")
	}
	if cfg.MemoryPipeline.MMRLambda != 0.66 {
		t.Fatalf("memory pipeline mmr_lambda = %v", cfg.MemoryPipeline.MMRLambda)
	}
	if cfg.MemoryPipeline.WeibullEnabled {
		t.Fatal("expected weibull decay to be disabled by env override")
	}
	if cfg.MemoryPipeline.WeibullShape != 1.8 {
		t.Fatalf("memory pipeline weibull_shape = %v", cfg.MemoryPipeline.WeibullShape)
	}
	if cfg.MemoryPipeline.WeibullScaleHours != 1440 {
		t.Fatalf("memory pipeline weibull_scale_hours = %v", cfg.MemoryPipeline.WeibullScaleHours)
	}
	if cfg.MemoryPipeline.WeibullMinMultiplier != 0.3 {
		t.Fatalf("memory pipeline weibull_min_multiplier = %v", cfg.MemoryPipeline.WeibullMinMultiplier)
	}
	if cfg.MemoryPipeline.WeibullReinforceWeight != 0.22 {
		t.Fatalf("memory pipeline weibull_reinforce_weight = %v", cfg.MemoryPipeline.WeibullReinforceWeight)
	}
	if cfg.MemoryPipeline.WeibullCrossSessionBoost != 0.15 {
		t.Fatalf("memory pipeline weibull_cross_session_boost = %v", cfg.MemoryPipeline.WeibullCrossSessionBoost)
	}
}

// restoreEnv clears one environment variable for the test duration and then restores the prior value.
// restoreEnv 用于在测试期间清空某个环境变量，并在结束后恢复原值。
func restoreEnv(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !existed {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, value)
	})
}

// currentTestConfigBody returns one minimal latest-format config body used by placeholder and layered-load tests.
// currentTestConfigBody 用于返回占位符与分层加载测试使用的最小最新格式配置体。
func currentTestConfigBody(apiKeyExpr string) string {
	return `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"workspace":"15s","pre_check":"8s","post_action":"8s"},"shutdown_timeout":"10s"},
		"sqlite":{"address":"127.0.0.1:19501","timeout":"5s"},
		"lancedb":{"address":"127.0.0.1:19301","timeout":"5s","table_name":"vmm_memory_vectors","vector_column":"vector"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"` + apiKeyExpr + `","model":"test-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"` + apiKeyExpr + `","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"sqlite"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75}
	}`
}

// newValidConfigForTest returns one minimal fully valid config so focused validation tests fail only on the target field.
// newValidConfigForTest 用于返回一份最小且完整的有效配置，让聚焦校验测试只在目标字段上失败。
func newValidConfigForTest() Config {
	cfg := DefaultLocal()
	cfg.GRPC.ListenAddr = "127.0.0.1:8080"
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.Embedding.Dimension = 1024
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"
	return cfg
}
