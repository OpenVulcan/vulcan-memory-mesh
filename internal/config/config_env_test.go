// Behavioral tests grouped by config env test.
// 配置层环境变量投影行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced verifies explicitly referenced typed environment overrides report parse failures instead of silently falling back to existing values.
// TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced 用于验证显式引用的类型化环境变量覆盖会报告解析失败，而不是静默回退到已有值。
func TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "int", key: "VMM_PRE_CHECK_TOPK", value: "not-int"},
		{name: "float", key: "VMM_PRE_CHECK_SIMILARITY_THRESHOLD", value: "not-float"},
		{name: "optional-float", key: "VMM_MEMORY_MIN_SIMILARITY_SCORE", value: "not-float"},
		{name: "bool", key: "VMM_NOISE_ENABLED", value: "not-bool"},
		{name: "duration", key: "VMM_GRPC_PRE_CHECK_TIMEOUT", value: "not-duration"},
		{name: "dedupe-pool-int", key: "VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K", value: "not-int"},
		{name: "postaction-timeout", key: "VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT", value: "not-duration"},
		{name: "postaction-failure-threshold", key: "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD", value: "not-int"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreEnv(t, tc.key)
			t.Setenv(tc.key, tc.value)

			cfg := newValidConfigForTest()
			failures := applyEnvOverrides(&cfg, map[string]struct{}{
				tc.key: {},
			})
			if !slices.Contains(failures, tc.key) {
				t.Fatalf("parse failures = %#v, want %q", failures, tc.key)
			}
		})
	}
}

// TestPostActionReliabilityConfigRejectsUnsafeValues verifies explicit invalid values fail closed instead of being normalized back to defaults.
// TestPostActionReliabilityConfigRejectsUnsafeValues 用于验证显式无效值会封闭失败，而不是被归一化回默认值。
func TestPostActionReliabilityConfigRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "analysis timeout below minimum",
			mutate: func(cfg *Config) {
				cfg.PostAction.SessionAnalysisTimeout = Duration{Duration: 119 * time.Second}
			},
			wantErr: "post_action.session_analysis_timeout must be >= 2m",
		},
		{
			name: "zero failure threshold",
			mutate: func(cfg *Config) {
				cfg.PostAction.FailurePassThreshold = 0
			},
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
		{
			name: "negative failure threshold",
			mutate: func(cfg *Config) {
				cfg.PostAction.FailurePassThreshold = -1
			},
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newValidConfigForTest()
			cfg.Normalize()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues verifies typed environment overrides cannot be normalized back to defaults after parsing.
// TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues 用于验证类型化环境变量覆盖在解析后不会被归一化回默认值。
func TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{
			name:    "zero analysis timeout",
			key:     "VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT",
			value:   "0s",
			wantErr: "post_action.session_analysis_timeout must be >= 2m",
		},
		{
			name:    "zero failure threshold",
			key:     "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD",
			value:   "0",
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
		{
			name:    "negative failure threshold",
			key:     "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD",
			value:   "-1",
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			cfg := newValidConfigForTest()
			if failures := applyEnvOverrides(&cfg, map[string]struct{}{tc.key: {}}); len(failures) != 0 {
				t.Fatalf("applyEnvOverrides() failures = %#v", failures)
			}
			cfg.Normalize()
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys verifies the path-gating table stays in lockstep with every supported process-level VMM override.
// TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys 用于验证路径门控表与所有受支持的进程级 VMM 覆盖保持同步。
func TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys(t *testing.T) {
	body, err := os.ReadFile("config_validate.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	supported := map[string]struct{}{}
	for _, match := range regexp.MustCompile(`set(?:String|StringSlice|Int|Int64|Float|OptionalFloat|Bool|Duration)\("([A-Z0-9_]+)"`).FindAllStringSubmatch(source, -1) {
		supported[match[1]] = struct{}{}
	}
	for _, match := range regexp.MustCompile(`envOverrideAllowed\(referencedEnvKeys, "([A-Z0-9_]+)"`).FindAllStringSubmatch(source, -1) {
		supported[match[1]] = struct{}{}
	}
	mapped := map[string]struct{}{}
	for key := range supportedEnvOverrideValuePaths {
		mapped[key] = struct{}{}
	}

	missing := []string{}
	for key := range supported {
		if _, ok := mapped[key]; !ok {
			missing = append(missing, key)
		}
	}
	extra := []string{}
	for key := range mapped {
		if _, ok := supported[key]; !ok {
			extra = append(extra, key)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("env override path table mismatch: missing=%v extra=%v", missing, extra)
	}
}

// TestSupportedEnvOverrideValuePathsExistInConfigSchema verifies every path-gating entry points at a real JSON-tagged Config field.
// TestSupportedEnvOverrideValuePathsExistInConfigSchema 用于验证每个路径门控项都指向真实存在的 Config JSON 标签字段。
func TestSupportedEnvOverrideValuePathsExistInConfigSchema(t *testing.T) {
	rootType := reflect.TypeOf(Config{})
	invalid := []string{}
	for key, paths := range supportedEnvOverrideValuePaths {
		for _, path := range paths {
			if configSchemaPathExists(rootType, path) {
				continue
			}
			invalid = append(invalid, key+"="+path)
		}
	}
	slices.Sort(invalid)
	if len(invalid) > 0 {
		t.Fatalf("env override path table contains unknown config paths: %v", invalid)
	}
}

// configSchemaPathExists walks one dot-separated JSON-tag path through the Config struct schema.
// configSchemaPathExists 用于沿着 Config 结构体 schema 检查一个点分 JSON 标签路径是否存在。
func configSchemaPathExists(rootType reflect.Type, path string) bool {
	current := rootType
	for _, segment := range strings.Split(path, ".") {
		if strings.TrimSpace(segment) == "" {
			return false
		}
		for current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct {
			return false
		}
		field, ok := jsonTaggedFieldByName(current, segment)
		if !ok {
			return false
		}
		current = field.Type
	}
	return true
}

// jsonTaggedFieldByName returns the struct field whose JSON tag owns one config path segment.
// jsonTaggedFieldByName 用于返回 JSON 标签归属于某个配置路径片段的结构体字段。
func jsonTaggedFieldByName(structType reflect.Type, segment string) (reflect.StructField, bool) {
	for idx := 0; idx < structType.NumField(); idx++ {
		field := structType.Field(idx)
		tagName := strings.Split(field.Tag.Get("json"), ",")[0]
		if tagName == "" || tagName == "-" {
			continue
		}
		if tagName == segment {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// TestValidateRemovedAIEnvOverridesOnlyWhenReferenced verifies removed AI env overrides are only rejected after the config explicitly opts into the corresponding placeholder.
// TestValidateRemovedAIEnvOverridesOnlyWhenReferenced 用于验证已移除的 AI 环境变量只会在配置显式引用对应占位符后才被拒绝。
func TestValidateRemovedAIEnvOverridesOnlyWhenReferenced(t *testing.T) {
	restoreEnv(t, "VMM_LLM_API_KEYS")
	t.Setenv("VMM_LLM_API_KEYS", "legacy-key")
	if err := validateRemovedAIEnvOverrides(nil); err != nil {
		t.Fatalf("expected unreferenced removed env override to be ignored, got %v", err)
	}
	err := validateRemovedAIEnvOverrides(map[string]struct{}{"VMM_LLM_API_KEYS": {}})
	if err == nil || !strings.Contains(err.Error(), "VMM_LLM_API_KEYS has been removed") {
		t.Fatalf("unexpected removed env error after explicit reference: %v", err)
	}
}

// TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced verifies supported embedding env overrides still inject multi-key pools and shared node budgets after the config explicitly opts into that placeholder.
// TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced 用于验证配置显式引用对应占位符后，embedding 环境变量覆盖仍会注入多 key 池与共享节点预算。
func TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_EMBED_API_KEYS", "embed-a,embed-b")
	t.Setenv("VMM_EMBED_RPM", "7")
	t.Setenv("VMM_EMBED_TPM", "700")
	t.Setenv("VMM_EMBED_RPD", "70")
	t.Setenv("VMM_EMBED_MAX_BATCH_SIZE", "12")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_EMBED_API_KEYS":       {},
		"VMM_EMBED_RPM":            {},
		"VMM_EMBED_TPM":            {},
		"VMM_EMBED_RPD":            {},
		"VMM_EMBED_MAX_BATCH_SIZE": {},
	})
	cfg.Normalize()

	if got, want := len(cfg.Embedding.APIKeys), 2; got != want {
		t.Fatalf("embedding api key pool size = %d, want %d (%#v)", got, want, cfg.Embedding.APIKeys)
	}
	nodes := cfg.Embedding.RoutingNodes()
	if got, want := len(nodes), 1; got != want {
		t.Fatalf("embedding node count = %d, want %d (%#v)", got, want, nodes)
	}
	if nodes[0].RPM != 7 || nodes[0].TPM != 700 || nodes[0].RPD != 70 {
		t.Fatalf("embedding node limits = %#v", nodes[0])
	}
	if got, want := cfg.Embedding.MaxBatchSize, 12; got != want {
		t.Fatalf("embedding max batch size = %d, want %d", got, want)
	}
}

// TestApplyEnvOverridesSkipsUnreferencedValues verifies plain YAML values no longer get silently replaced by supported env overrides when the config did not explicitly reference those placeholders.
// TestApplyEnvOverridesSkipsUnreferencedValues 用于验证当配置未显式引用对应占位符时，字面量 YAML 值不会再被支持的环境变量覆盖静默替换。
func TestApplyEnvOverridesSkipsUnreferencedValues(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.RequestTimeout.PreCheck = Duration{15 * time.Second}
	t.Setenv("VMM_GRPC_PRE_CHECK_TIMEOUT", "8s")

	_ = applyEnvOverrides(&cfg, nil)

	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, 15*time.Second; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
	}
}

// TestApplyEnvOverridesSetsPromptLanguageWhenReferenced verifies prompt-language env overrides only take effect after the config explicitly opts into that placeholder.
// TestApplyEnvOverridesSetsPromptLanguageWhenReferenced 用于验证只有在配置显式引用对应占位符后，提示词语言环境变量覆盖才会生效。
func TestApplyEnvOverridesSetsPromptLanguageWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_PROMPTS_PROMPT_LANGUAGE", "zh-CN")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_PROMPTS_PROMPT_LANGUAGE": {},
	})
	cfg.Normalize()

	if got, want := cfg.Prompts.PromptLanguage, defaultChinesePromptBundle; got != want {
		t.Fatalf("prompt language = %q, want %q", got, want)
	}
}

// TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced verifies the dedicated LLM output log switch only changes when the config explicitly opts into the environment override.
// TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced 用于验证只有在配置显式引用对应环境变量时，专用 LLM 输出日志开关才会被环境覆盖。
func TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_LOG_LLM_OUTPUT_ENABLED", "true")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_LOG_LLM_OUTPUT_ENABLED": {},
	})
	cfg.Normalize()

	if !cfg.Logging.LLMOutputEnabled {
		t.Fatal("expected llm output logging to be enabled by env override")
	}
}

// TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced verifies maintenance-tool timeout env overrides stay isolated from the regular postgres runtime node after the config explicitly references those placeholders.
// TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced 用于验证配置显式引用对应占位符后，维护工具超时环境变量覆盖会写入独立 maintenance_tool 节点，而不会污染常规 postgres 运行时节点。
func TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT", "45s")
	t.Setenv("VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT", "12m")
	t.Setenv("VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE", "24")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT":     {},
		"VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT":    {},
		"VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE": {},
	})
	cfg.Normalize()

	if got, want := cfg.MaintenanceTool.Postgres.ReadTimeout.Duration, 45*time.Second; got != want {
		t.Fatalf("maintenance tool postgres read timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.Postgres.WriteTimeout.Duration, 12*time.Minute; got != want {
		t.Fatalf("maintenance tool postgres write timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.VectorRebuildBatchSize, 24; got != want {
		t.Fatalf("maintenance tool vector rebuild batch size = %d, want %d", got, want)
	}
	if got, want := cfg.Postgres.QueryTimeout.Duration, 5*time.Second; got != want {
		t.Fatalf("postgres query timeout = %v, want %v", got, want)
	}
}

// TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced verifies explicit env placeholders can tune the new gRPC keepalive block without mutating unrelated transport defaults.
// TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced 用于验证在显式引用环境变量占位符后，可以调整新的 gRPC keepalive 配置块，同时不污染无关的传输层默认值。
func TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.Keepalive.Enabled = false
	originalPreCheckTimeout := cfg.GRPC.RequestTimeout.PreCheck.Duration
	t.Setenv("VMM_GRPC_KEEPALIVE_ENABLED", "true")
	t.Setenv("VMM_GRPC_KEEPALIVE_TIME", "45s")
	t.Setenv("VMM_GRPC_KEEPALIVE_TIMEOUT", "12s")
	t.Setenv("VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL", "30s")
	t.Setenv("VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM", "false")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_GRPC_KEEPALIVE_ENABLED":               {},
		"VMM_GRPC_KEEPALIVE_TIME":                  {},
		"VMM_GRPC_KEEPALIVE_TIMEOUT":               {},
		"VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL":     {},
		"VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM": {},
	})
	cfg.Normalize()

	if !cfg.GRPC.Keepalive.Enabled {
		t.Fatal("expected grpc keepalive to be enabled by env override")
	}
	if got, want := cfg.GRPC.Keepalive.Time.Duration, 45*time.Second; got != want {
		t.Fatalf("grpc keepalive time = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.Timeout.Duration, 12*time.Second; got != want {
		t.Fatalf("grpc keepalive timeout = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.MinPingInterval.Duration, 30*time.Second; got != want {
		t.Fatalf("grpc keepalive min ping interval = %v, want %v", got, want)
	}
	if cfg.GRPC.Keepalive.PermitWithoutStream {
		t.Fatal("expected grpc keepalive permit_without_stream to be disabled by env override")
	}
	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, originalPreCheckTimeout; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
	}
}
