// config_validate.go keeps config validation, supported environment overrides, and provider capability helpers.
// config_validate.go 用于承载配置校验、受支持的环境变量覆盖以及 provider 能力辅助逻辑。
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// supportedEnvOverrideValuePaths maps process-level VMM_* overrides to the config fields that are allowed to opt into each override.
// supportedEnvOverrideValuePaths 用于把进程级 VMM_* 覆盖映射到允许显式启用该覆盖的配置字段路径。
var supportedEnvOverrideValuePaths = map[string][]string{
	"VMM_GRPC_LISTEN_ADDR":                              {"grpc.listen_addr"},
	"VMM_GRPC_MAX_RECEIVE_MESSAGE_BYTES":                {"grpc.max_receive_message_bytes"},
	"VMM_GRPC_WORKSPACE_TIMEOUT":                        {"grpc.request_timeout.workspace"},
	"VMM_GRPC_PRE_CHECK_TIMEOUT":                        {"grpc.request_timeout.pre_check"},
	"VMM_GRPC_POST_ACTION_TIMEOUT":                      {"grpc.request_timeout.post_action"},
	"VMM_GRPC_SHUTDOWN_TIMEOUT":                         {"grpc.shutdown_timeout"},
	"VMM_GRPC_KEEPALIVE_ENABLED":                        {"grpc.keepalive.enabled"},
	"VMM_GRPC_KEEPALIVE_TIME":                           {"grpc.keepalive.time"},
	"VMM_GRPC_KEEPALIVE_TIMEOUT":                        {"grpc.keepalive.timeout"},
	"VMM_GRPC_KEEPALIVE_MAX_CONNECTION_IDLE":            {"grpc.keepalive.max_connection_idle"},
	"VMM_GRPC_KEEPALIVE_MAX_CONNECTION_AGE":             {"grpc.keepalive.max_connection_age"},
	"VMM_GRPC_KEEPALIVE_MAX_CONNECTION_AGE_GRACE":       {"grpc.keepalive.max_connection_age_grace"},
	"VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL":              {"grpc.keepalive.min_ping_interval"},
	"VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM":          {"grpc.keepalive.permit_without_stream"},
	"VMM_MANAGEMENT_ENABLED":                            {"management.enabled"},
	"VMM_MANAGEMENT_LISTEN_ADDR":                        {"management.listen_addr"},
	"VMM_MANAGEMENT_ACCESS_TOKEN":                       {"management.access_token"},
	"VMM_MANAGEMENT_MAX_REQUEST_BODY_BYTES":             {"management.max_request_body_bytes"},
	"VMM_MANAGEMENT_DEFAULT_PAGE_SIZE":                  {"management.default_page_size"},
	"VMM_MANAGEMENT_MAX_PAGE_SIZE":                      {"management.max_page_size"},
	"VMM_MANAGEMENT_READ_HEADER_TIMEOUT":                {"management.read_header_timeout"},
	"VMM_MANAGEMENT_REQUEST_TIMEOUT":                    {"management.request_timeout"},
	"VMM_MANAGEMENT_IDLE_TIMEOUT":                       {"management.idle_timeout"},
	"VMM_MANAGEMENT_SHUTDOWN_TIMEOUT":                   {"management.shutdown_timeout"},
	"VMM_LOG_LEVEL":                                     {"logging.level"},
	"VMM_LOG_FORMAT":                                    {"logging.format"},
	"VMM_LOG_DEBUG_RPC_PAYLOADS":                        {"logging.debug_rpc_payloads"},
	"VMM_LOG_LLM_OUTPUT_ENABLED":                        {"logging.llm_output_enabled"},
	"VMM_LOG_PROTECT_PAYLOADS":                          {"logging.protect_payloads"},
	"VMM_LOG_PAYLOAD_ENCRYPTION_KEY":                    {"logging.payload_encryption_key"},
	"VMM_PII_DEFAULT_LANGUAGE":                          {"pii.default_language"},
	"VMM_NOISE_ENABLED":                                 {"noise.enabled"},
	"VMM_NOISE_DEFAULT_LANGUAGE":                        {"noise.default_language"},
	"VMM_NOISE_SEMANTIC_ENABLED":                        {"noise.semantic_enabled"},
	"VMM_NOISE_SEMANTIC_THRESHOLD":                      {"noise.semantic_threshold"},
	"VMM_PROMPTS_PROMPT_LANGUAGE":                       {"prompts.prompt_language"},
	"VMM_STORAGE_MODE":                                  {"storage.mode"},
	"VMM_STORAGE_COMBINED_PROVIDER":                     {"storage.combined_provider"},
	"VMM_SQLITE_ADDRESS":                                {"sqlite.address"},
	"VMM_SQLITE_TIMEOUT":                                {"sqlite.timeout"},
	"VMM_SQLITE_TOKENIZER_MODE":                         {"sqlite.tokenizer_mode"},
	"VMM_SQLITE_NATIVE_PATH":                            {"sqlite.native.path"},
	"VMM_SQLITE_NATIVE_TOKENIZER":                       {"sqlite.native.tokenizer"},
	"VMM_LANCEDB_ADDRESS":                               {"lancedb.address"},
	"VMM_LANCEDB_TIMEOUT":                               {"lancedb.timeout"},
	"VMM_LANCEDB_TABLE_NAME":                            {"lancedb.table_name"},
	"VMM_LANCEDB_VECTOR_COLUMN":                         {"lancedb.vector_column"},
	"VMM_LANCEDB_NATIVE_PATH":                           {"lancedb.native.path"},
	"VMM_LANCEDB_NATIVE_LIBRARY_PATH":                   {"lancedb.native.library_path"},
	"VMM_CONTROLLER_ENDPOINT":                           {"controller.endpoint"},
	"VMM_CONTROLLER_AUTO_SPAWN":                         {"controller.auto_spawn"},
	"VMM_CONTROLLER_EXECUTABLE":                         {"controller.executable"},
	"VMM_CONTROLLER_PROCESS_MODE":                       {"controller.process_mode"},
	"VMM_CONTROLLER_MINIMUM_UPTIME":                     {"controller.minimum_uptime"},
	"VMM_CONTROLLER_IDLE_TIMEOUT":                       {"controller.idle_timeout"},
	"VMM_CONTROLLER_LEASE_TTL":                          {"controller.lease_ttl"},
	"VMM_CONTROLLER_CONNECT_TIMEOUT":                    {"controller.connect_timeout"},
	"VMM_CONTROLLER_STARTUP_TIMEOUT":                    {"controller.startup_timeout"},
	"VMM_CONTROLLER_STARTUP_RETRY_INTERVAL":             {"controller.startup_retry_interval"},
	"VMM_CONTROLLER_LEASE_RENEW_INTERVAL":               {"controller.lease_renew_interval"},
	"VMM_CONTROLLER_REQUEST_TIMEOUT":                    {"controller.request_timeout"},
	"VMM_CONTROLLER_SPACE_ID":                           {"controller.space_id"},
	"VMM_CONTROLLER_SPACE_LABEL":                        {"controller.space_label"},
	"VMM_POSTGRES_DSN":                                  {"postgres.dsn"},
	"VMM_POSTGRES_SCHEMA":                               {"postgres.schema"},
	"VMM_POSTGRES_FLAVOR":                               {"postgres.flavor"},
	"VMM_POSTGRES_QUERY_TIMEOUT":                        {"postgres.query_timeout"},
	"VMM_POSTGRES_CONNECT_TIMEOUT":                      {"postgres.connect_timeout"},
	"VMM_POSTGRES_MAX_OPEN_CONNS":                       {"postgres.max_open_conns"},
	"VMM_POSTGRES_MIN_IDLE_CONNS":                       {"postgres.min_idle_conns"},
	"VMM_POSTGRES_AUTO_CREATE_EXTENSIONS":               {"postgres.auto_create_extensions"},
	"VMM_POSTGRES_BM25_INDEX_CONCURRENTLY":              {"postgres.bm25_index_concurrently"},
	"VMM_POSTGRES_BM25_INDEX_NAME":                      {"postgres.bm25_index_name"},
	"VMM_POSTGRES_TRGM_SIMILARITY_THRESHOLD":            {"postgres.trgm_similarity_threshold"},
	"VMM_POSTGRES_VECTOR_LISTS":                         {"postgres.vector_lists"},
	"VMM_POSTGRES_VECTOR_PROBES":                        {"postgres.vector_probes"},
	"VMM_POSTGRES_MIGRATION_BATCH_SIZE":                 {"postgres.migration_batch_size"},
	"VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT":        {"maintenance_tool.postgres.read_timeout"},
	"VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT":       {"maintenance_tool.postgres.write_timeout"},
	"VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE":    {"maintenance_tool.vector_rebuild_batch_size"},
	"VMM_EMBED_PROVIDER":                                {"embedding.provider"},
	"VMM_EMBED_ENDPOINT":                                {"embedding.endpoint"},
	"VMM_EMBED_API_KEYS":                                {"embedding.api_keys"},
	"VMM_EMBED_RPM":                                     {"embedding.rpm"},
	"VMM_EMBED_TPM":                                     {"embedding.tpm"},
	"VMM_EMBED_RPD":                                     {"embedding.rpd"},
	"VMM_EMBED_MODEL":                                   {"embedding.model"},
	"VMM_EMBED_DIMENSION":                               {"embedding.dimension"},
	"VMM_EMBED_MAX_BATCH_SIZE":                          {"embedding.max_batch_size"},
	"VMM_EMBED_ORGANIZATION":                            {"embedding.organization"},
	"VMM_EMBED_PROJECT":                                 {"embedding.project"},
	"VMM_EMBED_KEY_FAILOVER_ENABLED":                    {"embedding.key_failover.enabled"},
	"VMM_EMBED_KEY_FAILOVER_POLICY":                     {"embedding.key_failover.policy"},
	"VMM_EMBED_KEY_FAILOVER_RESPECT_RETRY_AFTER":        {"embedding.key_failover.respect_retry_after"},
	"VMM_EMBED_KEY_FAILOVER_RATE_LIMIT_COOLDOWN":        {"embedding.key_failover.rate_limit_cooldown"},
	"VMM_EMBED_KEY_FAILOVER_QUOTA_COOLDOWN":             {"embedding.key_failover.quota_cooldown"},
	"VMM_EMBED_KEY_FAILOVER_AUTH_COOLDOWN":              {"embedding.key_failover.auth_cooldown"},
	"VMM_EMBED_KEY_FAILOVER_PROBE_AFTER_COOLDOWN":       {"embedding.key_failover.probe_after_cooldown"},
	"VMM_RERANK_ENABLED":                                {"rerank.enabled"},
	"VMM_RERANK_TOP_N":                                  {"rerank.top_n"},
	"VMM_VECTOR_PROVIDER":                               {"vector.provider"},
	"VMM_RELATIONAL_PROVIDER":                           {"relational.provider"},
	"VMM_POST_ACTION_INPUT_MODE":                        {"post_action.input_mode"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_TURN_THRESHOLD":   {"post_action.session_analysis_turn_threshold"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_TOKEN_THRESHOLD":  {"post_action.session_analysis_token_threshold"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_IDLE_TIMEOUT":     {"post_action.session_analysis_idle_timeout"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_HISTORY_TURNS":    {"post_action.session_analysis_history_turns"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_MAX_INPUT_TOKENS": {"post_action.session_analysis_max_input_tokens"},
	"VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT":          {"post_action.session_analysis_timeout"},
	"VMM_POST_ACTION_FAILURE_PASS_THRESHOLD":            {"post_action.failure_pass_threshold"},
	"VMM_POST_ACTION_MAX_QUEUE_WORKERS":                 {"post_action.max_queue_workers"},
	"VMM_MEMORY_REPLACE_SCOPE":                          {"memory_replace_scope"},
	"VMM_PRE_CHECK_INTENT_TIMEOUT":                      {"pre_check.intent_timeout"},
	"VMM_PRE_CHECK_TOPK":                                {"pre_check.top_k"},
	"VMM_PRE_CHECK_SEARCH_SCOPE":                        {"pre_check.search_scope"},
	"VMM_PRE_CHECK_SIMILARITY_THRESHOLD":                {"pre_check.similarity_threshold"},
	"VMM_RETENTION_ENABLED":                             {"retention.enabled"},
	"VMM_RETENTION_RECYCLE_SCAN_INTERVAL":               {"retention.recycle_scan_interval"},
	"VMM_RETENTION_TURN_KEEP_EXTRA_TURNS":               {"retention.turn_keep_extra_turns"},
	"VMM_RETENTION_SESSION_IDLE_RECYCLE_AFTER":          {"retention.session_idle_recycle_after"},
	"VMM_RETENTION_TRASH_RETENTION":                     {"retention.trash_retention"},
	"VMM_RETENTION_PROTECT_PRIORITY_FLOOR":              {"retention.protect_priority_floor"},
	"VMM_RETENTION_PROTECT_MEMORY_LEVEL_FLOOR":          {"retention.protect_memory_level_floor"},
	"VMM_RETENTION_SKIP_PROTECTED_SHARED_MEMORIES":      {"retention.skip_protected_shared_memories"},
	"VMM_MEMORY_MAX_SEARCH_KEYWORDS":                    {"memory_pipeline.max_search_keywords"},
	"VMM_MEMORY_MIN_SIMILARITY_SCORE":                   {"memory_pipeline.min_similarity_score"},
	"VMM_MEMORY_REPLACE_MIN_SIMILARITY_SCORE":           {"memory_pipeline.replace_min_similarity_score"},
	"VMM_MEMORY_HARD_DEDUPE_COSINE_THRESHOLD":           {"memory_pipeline.hard_dedupe_cosine_threshold"},
	"VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K":                 {"memory_pipeline.hard_dedupe_pool_top_k"},
	"VMM_MEMORY_HYBRID_ENABLED":                         {"memory_pipeline.hybrid_enabled"},
	"VMM_MEMORY_LEXICAL_TOP_K":                          {"memory_pipeline.lexical_top_k"},
	"VMM_MEMORY_RRF_K":                                  {"memory_pipeline.rrf_k"},
	"VMM_MEMORY_MMR_ENABLED":                            {"memory_pipeline.mmr_enabled"},
	"VMM_MEMORY_MMR_LAMBDA":                             {"memory_pipeline.mmr_lambda"},
	"VMM_MEMORY_WEIBULL_ENABLED":                        {"memory_pipeline.weibull_enabled"},
	"VMM_MEMORY_WEIBULL_SHAPE":                          {"memory_pipeline.weibull_shape"},
	"VMM_MEMORY_WEIBULL_SCALE_HOURS":                    {"memory_pipeline.weibull_scale_hours"},
	"VMM_MEMORY_WEIBULL_MIN_MULTIPLIER":                 {"memory_pipeline.weibull_min_multiplier"},
	"VMM_MEMORY_WEIBULL_REINFORCE_WEIGHT":               {"memory_pipeline.weibull_reinforce_weight"},
	"VMM_MEMORY_WEIBULL_CROSS_SESSION_BOOST":            {"memory_pipeline.weibull_cross_session_boost"},
}

// Validate verifies that the normalized config still satisfies the runtime contract before adapters and workflows are wired.
// Validate 用于校验归一化后的配置是否仍满足运行时契约，再进入适配器装配和工作流执行阶段。
func (c Config) Validate() error {
	// Verify the minimum runtime contract before the application starts.
	// 在应用启动前验证最小运行时契约。
	if strings.TrimSpace(c.GRPC.ListenAddr) == "" {
		return errors.New("grpc.listen_addr is required")
	}
	if c.Management.Enabled {
		if strings.TrimSpace(c.Management.ListenAddr) == "" {
			return errors.New("management.listen_addr is required when management.enabled is true")
		}
		if strings.TrimSpace(c.Management.AccessToken) == "" {
			return errors.New("management.access_token is required when management.enabled is true")
		}
		if c.Management.MaxRequestBodyBytes <= 0 {
			return errors.New("management.max_request_body_bytes must be > 0")
		}
		if c.Management.DefaultPageSize <= 0 || c.Management.MaxPageSize <= 0 || c.Management.DefaultPageSize > c.Management.MaxPageSize {
			return errors.New("management page sizes must be positive and default_page_size must not exceed max_page_size")
		}
		if c.Management.ReadHeaderTimeout.Duration <= 0 || c.Management.RequestTimeout.Duration <= 0 || c.Management.IdleTimeout.Duration <= 0 || c.Management.ShutdownTimeout.Duration <= 0 {
			return errors.New("management timeouts must be > 0")
		}
	}
	if strings.TrimSpace(c.PII.DefaultLanguage) == "" {
		return errors.New("pii.default_language is required")
	}
	if strings.TrimSpace(c.Noise.DefaultLanguage) == "" {
		return errors.New("noise.default_language is required")
	}
	storageMode := normalizeStorageModeValue(c.Storage.Mode)
	switch storageMode {
	case "split", "controller", "combined", "native":
	default:
		return errors.New("storage.mode must be one of split, controller, combined, or native")
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
	if c.GRPC.RequestTimeout.PostAction.Duration <= c.PreCheck.IntentTimeout.Duration {
		return errors.New("grpc.request_timeout.post_action must be greater than pre_check.intent_timeout")
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
		return errors.New("embedding.provider must be one of openai, openai_native, openai_go, google_ai_studio, or openrouter")
	}
	if storageMode != "combined" {
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
			if storageMode == "native" {
				if err := validateNativePath("sqlite.native.path", c.SQLite.Native.Path, true); err != nil {
					return err
				}
				if err := validateNativePath("lancedb.native.path", c.LanceDB.Native.Path, true); err != nil {
					return err
				}
				if err := validateNativePath("lancedb.native.library_path", c.LanceDB.Native.LibraryPath, false); err != nil {
					return err
				}
				switch normalizeSQLiteNativeTokenizerValue(c.SQLite.Native.Tokenizer) {
				case "gse", "unicode61":
				default:
					return errors.New("sqlite.native.tokenizer must be one of gse or unicode61 when storage.mode=native")
				}
			} else {
				switch normalizeSQLiteTokenizerModeValue(c.SQLite.TokenizerMode) {
				case "jieba", "none":
				default:
					return errors.New("sqlite.tokenizer_mode must be one of jieba or none")
				}
			}
		default:
			return errors.New("relational.provider must be sqlite")
		}
		if normalizeStorageModeValue(c.Storage.Mode) == "controller" {
			if strings.TrimSpace(c.Controller.Endpoint) == "" {
				return errors.New("controller.endpoint is required when storage.mode=controller")
			}
			if !isLoopbackControllerEndpoint(c.Controller.Endpoint) {
				return errors.New("controller.endpoint must use a loopback host when storage.mode=controller")
			}
			switch strings.ToLower(strings.TrimSpace(c.Controller.ProcessMode)) {
			case "managed", "service":
			default:
				return errors.New("controller.process_mode must be either managed or service")
			}
			if c.Controller.MinimumUptime.Duration <= 0 {
				return errors.New("controller.minimum_uptime must be > 0")
			}
			if c.Controller.IdleTimeout.Duration <= 0 {
				return errors.New("controller.idle_timeout must be > 0")
			}
			if c.Controller.LeaseTTL.Duration <= 0 {
				return errors.New("controller.lease_ttl must be > 0")
			}
			if c.Controller.ConnectTimeout.Duration <= 0 {
				return errors.New("controller.connect_timeout must be > 0")
			}
			if c.Controller.StartupTimeout.Duration <= 0 {
				return errors.New("controller.startup_timeout must be > 0")
			}
			if c.Controller.StartupRetryInterval.Duration <= 0 {
				return errors.New("controller.startup_retry_interval must be > 0")
			}
			if c.Controller.LeaseRenewInterval.Duration <= 0 {
				return errors.New("controller.lease_renew_interval must be > 0")
			}
			if c.Controller.LeaseRenewInterval.Duration >= c.Controller.LeaseTTL.Duration {
				return errors.New("controller.lease_renew_interval must be less than controller.lease_ttl")
			}
			if c.Controller.RequestTimeout.Duration <= 0 {
				return errors.New("controller.request_timeout must be > 0")
			}
			if strings.TrimSpace(c.Controller.SpaceID) == "" {
				return errors.New("controller.space_id is required when storage.mode=controller")
			}
			if strings.TrimSpace(c.Controller.SpaceLabel) == "" {
				return errors.New("controller.space_label is required when storage.mode=controller")
			}
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
	if c.PostAction.SessionAnalysisTimeout.Duration < 2*time.Minute {
		return errors.New("post_action.session_analysis_timeout must be >= 2m")
	}
	if c.PostAction.FailurePassThreshold <= 0 {
		return errors.New("post_action.failure_pass_threshold must be > 0")
	}
	if c.PostAction.MaxQueueWorkers <= 0 {
		return errors.New("post_action.max_queue_workers must be > 0")
	}
	if c.PostAction.MaxQueueWorkers > 64 {
		return errors.New("post_action.max_queue_workers must be <= 64")
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

// validateNativePath rejects path values that cannot be represented by the runtime while leaving existence and root resolution to the application layer.
// validateNativePath 用于拒绝运行时无法表示的路径值，同时把路径存在性和根目录解析交给应用层处理。
func validateNativePath(name, raw string, required bool) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		if required {
			return fmt.Errorf("%s is required when storage.mode=native", name)
		}
		return nil
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains an invalid control character", name)
		}
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

// applyEnvOverrides applies the supported target settings and returns keys whose integer/float/bool/duration values failed to parse.
// applyEnvOverrides 用于应用受支持的目标设置，并返回解析失败的整数/浮点/布尔/时长键列表。
func applyEnvOverrides(cfg *Config, referencedEnvKeys map[string]struct{}) []string {
	// Reapply explicit process-level overrides after file-based expansion.
	// 在文件占位符展开之后，再次应用进程级显式覆盖。
	var parseFailures []string
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
			} else {
				parseFailures = append(parseFailures, k)
			}
		}
	}
	setInt64 := func(k string, target *int64) {
		if !envOverrideAllowed(referencedEnvKeys, k) {
			return
		}
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*target = n
			} else {
				parseFailures = append(parseFailures, k)
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
			} else {
				parseFailures = append(parseFailures, k)
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
			} else {
				parseFailures = append(parseFailures, k)
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
			} else {
				parseFailures = append(parseFailures, k)
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
			} else {
				parseFailures = append(parseFailures, k)
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
	setBool("VMM_MANAGEMENT_ENABLED", &cfg.Management.Enabled)
	setString("VMM_MANAGEMENT_LISTEN_ADDR", &cfg.Management.ListenAddr)
	setString("VMM_MANAGEMENT_ACCESS_TOKEN", &cfg.Management.AccessToken)
	setInt64("VMM_MANAGEMENT_MAX_REQUEST_BODY_BYTES", &cfg.Management.MaxRequestBodyBytes)
	setInt("VMM_MANAGEMENT_DEFAULT_PAGE_SIZE", &cfg.Management.DefaultPageSize)
	setInt("VMM_MANAGEMENT_MAX_PAGE_SIZE", &cfg.Management.MaxPageSize)
	setDuration("VMM_MANAGEMENT_READ_HEADER_TIMEOUT", &cfg.Management.ReadHeaderTimeout)
	setDuration("VMM_MANAGEMENT_REQUEST_TIMEOUT", &cfg.Management.RequestTimeout)
	setDuration("VMM_MANAGEMENT_IDLE_TIMEOUT", &cfg.Management.IdleTimeout)
	setDuration("VMM_MANAGEMENT_SHUTDOWN_TIMEOUT", &cfg.Management.ShutdownTimeout)
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
	setString("VMM_SQLITE_TOKENIZER_MODE", &cfg.SQLite.TokenizerMode)
	setString("VMM_SQLITE_NATIVE_PATH", &cfg.SQLite.Native.Path)
	setString("VMM_SQLITE_NATIVE_TOKENIZER", &cfg.SQLite.Native.Tokenizer)
	setString("VMM_LANCEDB_ADDRESS", &cfg.LanceDB.Address)
	setDuration("VMM_LANCEDB_TIMEOUT", &cfg.LanceDB.Timeout)
	setString("VMM_LANCEDB_TABLE_NAME", &cfg.LanceDB.TableName)
	setString("VMM_LANCEDB_VECTOR_COLUMN", &cfg.LanceDB.VectorColumn)
	setString("VMM_LANCEDB_NATIVE_PATH", &cfg.LanceDB.Native.Path)
	setString("VMM_LANCEDB_NATIVE_LIBRARY_PATH", &cfg.LanceDB.Native.LibraryPath)
	setString("VMM_CONTROLLER_ENDPOINT", &cfg.Controller.Endpoint)
	setBool("VMM_CONTROLLER_AUTO_SPAWN", &cfg.Controller.AutoSpawn)
	setString("VMM_CONTROLLER_EXECUTABLE", &cfg.Controller.Executable)
	setString("VMM_CONTROLLER_PROCESS_MODE", &cfg.Controller.ProcessMode)
	setDuration("VMM_CONTROLLER_MINIMUM_UPTIME", &cfg.Controller.MinimumUptime)
	setDuration("VMM_CONTROLLER_IDLE_TIMEOUT", &cfg.Controller.IdleTimeout)
	setDuration("VMM_CONTROLLER_LEASE_TTL", &cfg.Controller.LeaseTTL)
	setDuration("VMM_CONTROLLER_CONNECT_TIMEOUT", &cfg.Controller.ConnectTimeout)
	setDuration("VMM_CONTROLLER_STARTUP_TIMEOUT", &cfg.Controller.StartupTimeout)
	setDuration("VMM_CONTROLLER_STARTUP_RETRY_INTERVAL", &cfg.Controller.StartupRetryInterval)
	setDuration("VMM_CONTROLLER_LEASE_RENEW_INTERVAL", &cfg.Controller.LeaseRenewInterval)
	setDuration("VMM_CONTROLLER_REQUEST_TIMEOUT", &cfg.Controller.RequestTimeout)
	setString("VMM_CONTROLLER_SPACE_ID", &cfg.Controller.SpaceID)
	setString("VMM_CONTROLLER_SPACE_LABEL", &cfg.Controller.SpaceLabel)
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
	if envOverrideAllowed(referencedEnvKeys, "VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT") && strings.TrimSpace(os.Getenv("VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT")) != "" {
		// The explicit marker prevents a parsed zero duration from being silently replaced during normalization.
		// 显式标记用于防止已解析的零时长在归一化时被静默替换。
		cfg.PostAction.sessionAnalysisTimeoutSet = true
	}
	setDuration("VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT", &cfg.PostAction.SessionAnalysisTimeout)
	if envOverrideAllowed(referencedEnvKeys, "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD") && strings.TrimSpace(os.Getenv("VMM_POST_ACTION_FAILURE_PASS_THRESHOLD")) != "" {
		// The explicit marker preserves invalid zero or negative thresholds for fail-closed validation.
		// 显式标记用于保留无效的零值或负数阈值，以便执行封闭校验。
		cfg.PostAction.failurePassThresholdSet = true
	}
	setInt("VMM_POST_ACTION_FAILURE_PASS_THRESHOLD", &cfg.PostAction.FailurePassThreshold)
	setInt("VMM_POST_ACTION_MAX_QUEUE_WORKERS", &cfg.PostAction.MaxQueueWorkers)
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
			} else {
				parseFailures = append(parseFailures, "VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K")
			}
		}
	}
	setBool("VMM_MEMORY_HYBRID_ENABLED", &cfg.MemoryPipeline.HybridEnabled)
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
	return parseFailures
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

// UsesController reports whether the runtime should route both split-store APIs through one shared vldb-controller process.
// UsesController 用于判断运行时是否应把两套 split 存储 API 统一路由到共享 vldb-controller 进程。
func (c Config) UsesController() bool {
	return c.StorageMode() == "controller"
}

// UsesNative reports whether the runtime should select standalone SQLite and LanceDB implementations.
// UsesNative 用于判断运行时是否应选择独立 SQLite 与 LanceDB 实现。
func (c Config) UsesNative() bool {
	return c.StorageMode() == "native"
}

// UsesCombinedPostgres reports whether the runtime should build the unified PostgreSQL-backed combined store.
// UsesCombinedPostgres 用于判断运行时是否应装配统一的 PostgreSQL 组合库。
func (c Config) UsesCombinedPostgres() bool {
	return c.StorageMode() == "combined" && normalizeCombinedProviderValue(c.Storage.CombinedProvider) == "postgres"
}

// isLoopbackControllerEndpoint verifies an explicit TCP endpoint without resolving arbitrary hostnames over the network.
// isLoopbackControllerEndpoint 在不通过网络解析任意主机名的前提下校验显式 TCP loopback 端点。
func isLoopbackControllerEndpoint(endpoint string) bool {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return false
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User != nil {
		return false
	}
	if parsed.Scheme != "http" {
		return false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || strings.TrimSpace(port) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
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

// isOpenRouterProvider reports whether one provider alias resolves to the native OpenRouter SDK adapter.
// isOpenRouterProvider 用于判断某个 provider 别名是否会落到 OpenRouter SDK 原生适配器。
func isOpenRouterProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openrouter":
		return true
	default:
		return false
	}
}

// isSupportedAIProvider reports whether one provider alias resolves to any currently supported LLM/embedding adapter.
// isSupportedAIProvider 用于判断某个 provider 别名是否会落到当前支持的任一 LLM/embedding 适配器。
func isSupportedAIProvider(provider string) bool {
	return isOpenAIProvider(provider) || isGoogleAIStudioProvider(provider) || isOpenRouterProvider(provider)
}

// providerRequiresEndpoint reports whether the provider expects callers to supply an explicit endpoint instead of relying on the SDK default service root.
// providerRequiresEndpoint 用于判断某个 provider 是否要求调用方显式提供 endpoint，而不是依赖 SDK 默认服务根地址。
func providerRequiresEndpoint(provider string) bool {
	return !isGoogleAIStudioProvider(provider) && !isOpenRouterProvider(provider)
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
