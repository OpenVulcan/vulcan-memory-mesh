// managed.go defines and loads the single-file Vulcan Code managed-runtime contract.
// managed.go 用于定义并加载由 Vulcan Code 提供的单文件托管运行时契约。
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// ManagedContractVersion is the only managed-runtime manifest version accepted by this build.
	// ManagedContractVersion 是当前构建唯一接受的托管运行时清单版本。
	ManagedContractVersion = 2

	// ManagedContractOwner identifies Vulcan Code as the authoritative managed-runtime owner.
	// ManagedContractOwner 用于把 Vulcan Code 标识为托管运行时的权威所有者。
	ManagedContractOwner = "vulcan-code"

	// ManagedInferenceMode selects the Vulcan Code inference bridge instead of provider-specific VMM adapters.
	// ManagedInferenceMode 用于选择 Vulcan Code 推理桥接，而不是 VMM 自身的供应商适配器。
	ManagedInferenceMode = "vulcan_code"
)

// ManagedParent identifies the exact Vulcan Code process that owns one managed VMM generation.
// ManagedParent 用于标识拥有某一代托管 VMM 的精确 Vulcan Code 进程。
type ManagedParent struct {
	ProcessID       int   `json:"process_id" yaml:"process_id"`
	StartedAtUnixMS int64 `json:"started_at_unix_ms" yaml:"started_at_unix_ms"`
}

// ManagedAssets identifies the packaged and optional user-managed VMM asset roots.
// ManagedAssets 用于标识 VMM 打包资源根与可选的用户托管资源根。
type ManagedAssets struct {
	SystemRoot       string `json:"system_root" yaml:"system_root"`
	UserOverrideRoot string `json:"user_override_root,omitempty" yaml:"user_override_root,omitempty"`
}

// ManagedGRPCConfig contains listener settings that are safe for a child process to consume.
// ManagedGRPCConfig 用于保存子进程可安全消费的监听设置。
type ManagedGRPCConfig struct {
	PreferredListenAddr    string              `json:"preferred_listen_addr" yaml:"preferred_listen_addr"`
	AllowEphemeralFallback bool                `json:"allow_ephemeral_fallback" yaml:"allow_ephemeral_fallback"`
	AccessToken            string              `json:"access_token" yaml:"access_token"`
	MaxReceiveMessageBytes int                 `json:"max_receive_message_bytes" yaml:"max_receive_message_bytes"`
	RequestTimeout         GRPCRequestTimeout  `json:"request_timeout" yaml:"request_timeout"`
	ShutdownTimeout        Duration            `json:"shutdown_timeout" yaml:"shutdown_timeout"`
	Keepalive              GRPCKeepaliveConfig `json:"keepalive" yaml:"keepalive"`
}

// ManagedManagementConfig contains the isolated management listener and limits supplied by the owning Vulcan Code process.
// ManagedManagementConfig 用于保存由所属 Vulcan Code 进程提供的隔离管理监听器与限制。
type ManagedManagementConfig struct {
	PreferredListenAddr    string   `json:"preferred_listen_addr" yaml:"preferred_listen_addr"`
	AllowEphemeralFallback bool     `json:"allow_ephemeral_fallback" yaml:"allow_ephemeral_fallback"`
	AccessToken            string   `json:"access_token" yaml:"access_token"`
	MaxRequestBodyBytes    int64    `json:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	DefaultPageSize        int      `json:"default_page_size" yaml:"default_page_size"`
	MaxPageSize            int      `json:"max_page_size" yaml:"max_page_size"`
	ReadHeaderTimeout      Duration `json:"read_header_timeout" yaml:"read_header_timeout"`
	RequestTimeout         Duration `json:"request_timeout" yaml:"request_timeout"`
	IdleTimeout            Duration `json:"idle_timeout" yaml:"idle_timeout"`
	ShutdownTimeout        Duration `json:"shutdown_timeout" yaml:"shutdown_timeout"`
}

// ManagedStorageConfig binds VMM to the already-running shared controller while retaining an isolated space.
// ManagedStorageConfig 用于把 VMM 绑定到已运行的共享 Controller，同时保留隔离 Space。
type ManagedStorageConfig struct {
	Mode                 string           `json:"mode" yaml:"mode"`
	ControllerEndpoint   string           `json:"controller_endpoint" yaml:"controller_endpoint"`
	ControllerAutoSpawn  bool             `json:"controller_auto_spawn" yaml:"controller_auto_spawn"`
	ControllerSpaceID    string           `json:"controller_space_id" yaml:"controller_space_id"`
	ControllerSpaceLabel string           `json:"controller_space_label" yaml:"controller_space_label"`
	ControllerLease      ControllerConfig `json:"controller_lease" yaml:"controller_lease"`
}

// ManagedInferenceConfig selects the protected Vulcan Code inference discovery contract.
// ManagedInferenceConfig 用于选择受保护的 Vulcan Code 推理发现契约。
type ManagedInferenceConfig struct {
	Mode               string   `json:"mode" yaml:"mode"`
	DiscoveryFile      string   `json:"discovery_file" yaml:"discovery_file"`
	ExpectedCallerID   string   `json:"expected_caller_id" yaml:"expected_caller_id"`
	ConsumerProfileID  string   `json:"consumer_profile_id" yaml:"consumer_profile_id"`
	StartupTimeout     Duration `json:"startup_timeout" yaml:"startup_timeout"`
	EmbeddingDimension int      `json:"embedding_dimension" yaml:"embedding_dimension"`
}

// ManagedPreCheckConfig preserves every host-authored pre-check field, including an explicit zero threshold.
// ManagedPreCheckConfig 用于保留宿主写入的每一项预检字段，包括显式的零阈值。
type ManagedPreCheckConfig struct {
	IntentTimeout       Duration `json:"intent_timeout" yaml:"intent_timeout"`
	TopK                int      `json:"top_k" yaml:"top_k"`
	SearchScope         string   `json:"search_scope" yaml:"search_scope"`
	SimilarityThreshold float64  `json:"similarity_threshold" yaml:"similarity_threshold"`
}

// ManagedRuntime contains the complete runtime snapshot consumed without the normal layered config chain.
// ManagedRuntime 用于保存不经过普通分层配置链而直接消费的完整运行时快照。
type ManagedRuntime struct {
	DataRoot       string                  `json:"data_root" yaml:"data_root"`
	StatusFile     string                  `json:"status_file" yaml:"status_file"`
	ShutdownFile   string                  `json:"shutdown_file" yaml:"shutdown_file"`
	GRPC           ManagedGRPCConfig       `json:"grpc" yaml:"grpc"`
	Management     ManagedManagementConfig `json:"management" yaml:"management"`
	Storage        ManagedStorageConfig    `json:"storage" yaml:"storage"`
	Inference      ManagedInferenceConfig  `json:"inference" yaml:"inference"`
	Logging        LoggingConfig           `json:"logging" yaml:"logging"`
	PII            PIIConfig               `json:"pii" yaml:"pii"`
	Noise          NoiseConfig             `json:"noise" yaml:"noise"`
	Prompts        PromptConfig            `json:"prompts" yaml:"prompts"`
	Vector         VectorConfig            `json:"vector" yaml:"vector"`
	Relational     RelationalConfig        `json:"relational" yaml:"relational"`
	PreCheck       ManagedPreCheckConfig   `json:"pre_check" yaml:"pre_check"`
	PostAction     PostActionConfig        `json:"post_action" yaml:"post_action"`
	MemoryPipeline MemoryPipelineConfig    `json:"memory_pipeline" yaml:"memory_pipeline"`
	Retention      RetentionConfig         `json:"retention" yaml:"retention"`
}

// ManagedConfig is the versioned single-document contract generated by Vulcan Code for one child-process generation.
// ManagedConfig 是 Vulcan Code 为某一代子进程生成的版本化单文档契约。
type ManagedConfig struct {
	ContractVersion int            `json:"contract_version" yaml:"contract_version"`
	Owner           string         `json:"owner" yaml:"owner"`
	InstanceID      string         `json:"instance_id" yaml:"instance_id"`
	Generation      uint64         `json:"generation" yaml:"generation"`
	Parent          ManagedParent  `json:"parent" yaml:"parent"`
	Assets          ManagedAssets  `json:"assets" yaml:"assets"`
	Runtime         ManagedRuntime `json:"runtime" yaml:"runtime"`
	Digest          string         `json:"-" yaml:"-"`
}

// ManagedRuntimeBundle contains the validated manifest plus derived normal-runtime values.
// ManagedRuntimeBundle 用于保存已校验清单以及派生出的普通运行时值。
type ManagedRuntimeBundle struct {
	Manifest ManagedConfig
	Config   Config
	Layout   PromptLayout
	DataRoot string
}

// LoadVulcanManagedConfig loads exactly one strict manifest and never consults normal config or environment layers.
// LoadVulcanManagedConfig 只加载一份严格清单，且绝不读取普通配置层或环境变量层。
func LoadVulcanManagedConfig(path string) (ManagedRuntimeBundle, error) {
	absolutePath, err := validateManagedConfigPath(path)
	if err != nil {
		return ManagedRuntimeBundle{}, err
	}
	body, err := os.ReadFile(absolutePath)
	if err != nil {
		return ManagedRuntimeBundle{}, fmt.Errorf("read managed config: %w", err)
	}
	normalizedBody, err := managedDocumentJSON(absolutePath, body)
	if err != nil {
		return ManagedRuntimeBundle{}, fmt.Errorf("parse managed config: %w", err)
	}
	var manifest ManagedConfig
	decoder := json.NewDecoder(bytes.NewReader(normalizedBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return ManagedRuntimeBundle{}, fmt.Errorf("decode managed config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return ManagedRuntimeBundle{}, errors.New("managed config must contain one document")
		}
		return ManagedRuntimeBundle{}, fmt.Errorf("decode managed config trailer: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return ManagedRuntimeBundle{}, err
	}
	digest := sha256.Sum256(normalizedBody)
	manifest.Digest = hex.EncodeToString(digest[:])
	runtimeConfig := manifest.RuntimeConfig()
	layout := PromptLayout{
		SystemDir: manifest.Assets.SystemRoot,
		UserDir:   manifest.Assets.UserOverrideRoot,
	}
	return ManagedRuntimeBundle{
		Manifest: manifest,
		Config:   runtimeConfig,
		Layout:   layout,
		DataRoot: filepath.Clean(manifest.Runtime.DataRoot),
	}, nil
}

// Validate rejects any manifest that could escape the loopback, single-owner, or shared-controller boundaries.
// Validate 用于拒绝任何可能越过回环、单一所有者或共享 Controller 边界的清单。
func (c ManagedConfig) Validate() error {
	if c.ContractVersion != ManagedContractVersion {
		return fmt.Errorf("unsupported managed contract version %d", c.ContractVersion)
	}
	if c.Owner != ManagedContractOwner {
		return fmt.Errorf("managed config owner must be %q", ManagedContractOwner)
	}
	if strings.TrimSpace(c.InstanceID) == "" || c.Generation == 0 {
		return errors.New("managed config instance_id and generation are required")
	}
	if c.Parent.ProcessID <= 0 || c.Parent.StartedAtUnixMS <= 0 {
		return errors.New("managed config parent identity is incomplete")
	}
	if err := requireAbsoluteDirectory("assets.system_root", c.Assets.SystemRoot); err != nil {
		return err
	}
	if strings.TrimSpace(c.Assets.UserOverrideRoot) != "" {
		if err := requireAbsoluteDirectory("assets.user_override_root", c.Assets.UserOverrideRoot); err != nil {
			return err
		}
	}
	if err := requireAbsolutePath("runtime.data_root", c.Runtime.DataRoot); err != nil {
		return err
	}
	if err := requireAbsolutePath("runtime.status_file", c.Runtime.StatusFile); err != nil {
		return err
	}
	if err := requireAbsolutePath("runtime.shutdown_file", c.Runtime.ShutdownFile); err != nil {
		return err
	}
	if err := requireLoopbackAddress("runtime.grpc.preferred_listen_addr", c.Runtime.GRPC.PreferredListenAddr); err != nil {
		return err
	}
	if strings.TrimSpace(c.Runtime.GRPC.AccessToken) == "" {
		return errors.New("runtime.grpc.access_token is required")
	}
	if c.Runtime.GRPC.MaxReceiveMessageBytes <= 0 {
		return errors.New("runtime.grpc.max_receive_message_bytes must be positive")
	}
	if err := requireLoopbackAddress("runtime.management.preferred_listen_addr", c.Runtime.Management.PreferredListenAddr); err != nil {
		return err
	}
	if strings.TrimSpace(c.Runtime.Management.AccessToken) == "" {
		return errors.New("runtime.management.access_token is required")
	}
	if c.Runtime.Management.AccessToken == c.Runtime.GRPC.AccessToken {
		return errors.New("runtime management and grpc access tokens must differ")
	}
	if c.Runtime.Management.MaxRequestBodyBytes <= 0 || c.Runtime.Management.DefaultPageSize <= 0 || c.Runtime.Management.MaxPageSize <= 0 {
		return errors.New("runtime management request and page limits must be positive")
	}
	if c.Runtime.Management.DefaultPageSize > c.Runtime.Management.MaxPageSize {
		return errors.New("runtime.management.default_page_size must not exceed max_page_size")
	}
	if c.Runtime.Management.ReadHeaderTimeout.Duration <= 0 ||
		c.Runtime.Management.RequestTimeout.Duration <= 0 ||
		c.Runtime.Management.IdleTimeout.Duration <= 0 ||
		c.Runtime.Management.ShutdownTimeout.Duration <= 0 {
		return errors.New("runtime management timeouts must be positive")
	}
	if c.Runtime.Storage.Mode != "controller" || c.Runtime.Storage.ControllerAutoSpawn {
		return errors.New("managed storage must use controller mode with controller_auto_spawn=false")
	}
	if err := requireLoopbackEndpoint("runtime.storage.controller_endpoint", c.Runtime.Storage.ControllerEndpoint); err != nil {
		return err
	}
	if strings.TrimSpace(c.Runtime.Storage.ControllerSpaceID) == "" || strings.TrimSpace(c.Runtime.Storage.ControllerSpaceLabel) == "" {
		return errors.New("managed controller space identity is required")
	}
	if c.Runtime.Inference.Mode != ManagedInferenceMode {
		return fmt.Errorf("managed inference mode must be %q", ManagedInferenceMode)
	}
	if err := requireAbsolutePath("runtime.inference.discovery_file", c.Runtime.Inference.DiscoveryFile); err != nil {
		return err
	}
	if strings.TrimSpace(c.Runtime.Inference.ExpectedCallerID) == "" || strings.TrimSpace(c.Runtime.Inference.ConsumerProfileID) == "" {
		return errors.New("managed inference caller and consumer profile are required")
	}
	if c.Runtime.Inference.EmbeddingDimension <= 0 {
		return errors.New("managed inference embedding_dimension must be positive")
	}
	if c.Runtime.Inference.StartupTimeout.Duration <= 0 {
		return errors.New("runtime.inference.startup_timeout must be positive")
	}
	runtimeConfig := c.rawRuntimeConfig()
	if err := validateManagedRuntimeConfig(runtimeConfig); err != nil {
		return fmt.Errorf("managed runtime config is invalid: %w", err)
	}
	return nil
}

// validateManagedRuntimeConfig validates every non-provider setting admitted by the managed manifest.
// validateManagedRuntimeConfig 用于校验托管清单允许写入的每一项非供应商设置。
//
// The managed contract deliberately omits VMM-native provider routes and credentials, so this
// validator mirrors only the runtime, storage, pipeline, and retention invariants that remain
// authoritative in managed mode.
// 托管契约刻意省略 VMM 原生供应商路由与凭据，因此本校验器只覆盖托管模式下仍然权威的
// 运行时、存储、流水线与保留策略不变量。
//
// Parameters:
// 参数：
//   - c: the normalized runtime configuration derived only from the managed manifest.
//   - c：只从托管清单派生并完成归一化的运行时配置。
//
// Returns:
// 返回值：
//   - error: nil when every managed invariant is valid, otherwise one deterministic field error.
//   - error：全部托管不变量有效时为空，否则返回一个确定性字段错误。
func validateManagedRuntimeConfig(c Config) error {
	if strings.TrimSpace(c.GRPC.ListenAddr) == "" {
		return errors.New("grpc.listen_addr is required")
	}
	if c.GRPC.MaxReceiveMessageBytes <= 0 {
		return errors.New("grpc.max_receive_message_bytes must be > 0")
	}
	if c.GRPC.RequestTimeout.Workspace.Duration <= 0 ||
		c.GRPC.RequestTimeout.PreCheck.Duration <= 0 ||
		c.GRPC.RequestTimeout.PostAction.Duration <= 0 {
		return errors.New("grpc request timeouts must be > 0")
	}
	if c.GRPC.ShutdownTimeout.Duration <= 0 {
		return errors.New("grpc.shutdown_timeout must be > 0")
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
	if strings.TrimSpace(c.PII.DefaultLanguage) == "" {
		return errors.New("pii.default_language is required")
	}
	if strings.TrimSpace(c.Noise.DefaultLanguage) == "" {
		return errors.New("noise.default_language is required")
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
	if c.MemoryPipeline.MinSimilarityScore == nil ||
		*c.MemoryPipeline.MinSimilarityScore < 0 ||
		*c.MemoryPipeline.MinSimilarityScore > 1 {
		return errors.New("memory_pipeline.min_similarity_score must be in [0,1]")
	}
	if c.MemoryPipeline.ReplaceMinSimilarityScore == nil ||
		*c.MemoryPipeline.ReplaceMinSimilarityScore < 0 ||
		*c.MemoryPipeline.ReplaceMinSimilarityScore > 1 {
		return errors.New("memory_pipeline.replace_min_similarity_score must be in [0,1]")
	}
	if c.MemoryPipeline.HardDedupeCosineThreshold == nil ||
		*c.MemoryPipeline.HardDedupeCosineThreshold < 0 ||
		*c.MemoryPipeline.HardDedupeCosineThreshold > 1 {
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
	if normalizeStorageModeValue(c.Storage.Mode) != "controller" {
		return errors.New("managed storage.mode must be controller")
	}
	if strings.TrimSpace(c.LanceDB.TableName) == "" || strings.TrimSpace(c.LanceDB.VectorColumn) == "" {
		return errors.New("managed lancedb table_name and vector_column are required")
	}
	if strings.ToLower(strings.TrimSpace(c.Vector.Provider)) != "lancedb" {
		return errors.New("managed vector.provider must be lancedb")
	}
	if strings.ToLower(strings.TrimSpace(c.Relational.Provider)) != "sqlite" {
		return errors.New("managed relational.provider must be sqlite")
	}
	switch normalizeSQLiteTokenizerModeValue(c.SQLite.TokenizerMode) {
	case "jieba", "none":
	default:
		return errors.New("sqlite.tokenizer_mode must be one of jieba or none")
	}
	if strings.TrimSpace(c.Controller.Endpoint) == "" || !isLoopbackControllerEndpoint(c.Controller.Endpoint) {
		return errors.New("controller.endpoint must use a loopback host when storage.mode=controller")
	}
	if c.Controller.AutoSpawn || strings.TrimSpace(c.Controller.Executable) != "" {
		return errors.New("managed controller must not auto-spawn or declare an executable")
	}
	switch strings.ToLower(strings.TrimSpace(c.Controller.ProcessMode)) {
	case "managed", "service":
	default:
		return errors.New("controller.process_mode must be either managed or service")
	}
	if c.Controller.MinimumUptime.Duration <= 0 ||
		c.Controller.IdleTimeout.Duration <= 0 ||
		c.Controller.LeaseTTL.Duration <= 0 ||
		c.Controller.ConnectTimeout.Duration <= 0 ||
		c.Controller.StartupTimeout.Duration <= 0 ||
		c.Controller.StartupRetryInterval.Duration <= 0 ||
		c.Controller.LeaseRenewInterval.Duration <= 0 ||
		c.Controller.RequestTimeout.Duration <= 0 {
		return errors.New("managed controller durations must be > 0")
	}
	if c.Controller.LeaseRenewInterval.Duration >= c.Controller.LeaseTTL.Duration {
		return errors.New("controller.lease_renew_interval must be less than controller.lease_ttl")
	}
	if strings.TrimSpace(c.Controller.SpaceID) == "" || strings.TrimSpace(c.Controller.SpaceLabel) == "" {
		return errors.New("managed controller space identity is required")
	}
	if c.Embedding.Dimension <= 0 || c.Embedding.MaxBatchSize <= 0 {
		return errors.New("managed embedding dimension and max_batch_size must be > 0")
	}
	if c.PostAction.InputMode != "strict" && c.PostAction.InputMode != "compat" {
		return errors.New("post_action.input_mode must be either strict or compat")
	}
	if c.PostAction.SessionAnalysisTurnThreshold <= 0 ||
		c.PostAction.SessionAnalysisTokenThreshold <= 0 ||
		c.PostAction.SessionAnalysisIdleTimeout.Duration <= 0 ||
		c.PostAction.SessionAnalysisHistoryTurns <= 0 ||
		c.PostAction.SessionAnalysisMaxInputTokens <= 0 ||
		c.PostAction.MaxQueueWorkers <= 0 ||
		c.PostAction.MaxQueueWorkers > 64 {
		return errors.New("managed post_action limits are invalid")
	}
	if c.Retention.RecycleScanInterval.Duration <= 0 ||
		c.Retention.TurnKeepExtraTurns < 0 ||
		c.Retention.SessionIdleRecycleAfter.Duration < 360*time.Hour ||
		c.Retention.TrashRetention.Duration <= 0 {
		return errors.New("managed retention limits are invalid")
	}
	if level := strings.TrimSpace(c.Retention.ProtectPriorityFloor); level != "" && !isSupportedRetentionPriorityFloorValue(level) {
		return errors.New("retention.protect_priority_floor must be one of P0, P1, or P2")
	}
	if level := strings.TrimSpace(c.Retention.ProtectMemoryLevelFloor); level != "" && !isSupportedRetentionMemoryLevelFloorValue(level) {
		return errors.New("retention.protect_memory_level_floor must be one of session, phase, stable, or persistent")
	}
	return nil
}

// requireLoopbackEndpoint validates one HTTP or bare host-and-port Controller endpoint.
// requireLoopbackEndpoint 用于校验一个 HTTP 或裸主机端口形式的 Controller 端点。
func requireLoopbackEndpoint(name string, endpoint string) error {
	trimmed := strings.TrimSpace(endpoint)
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		if parsed.Scheme != "http" || parsed.Path != "" && parsed.Path != "/" ||
			parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("%s must be a plain loopback HTTP origin", name)
		}
		return requireLoopbackAddress(name, parsed.Host)
	}
	return requireLoopbackAddress(name, trimmed)
}

// RuntimeConfig derives the existing application configuration without exposing provider credentials in the managed contract.
// RuntimeConfig 用于派生现有应用配置，同时不在托管契约中暴露供应商凭据。
func (c ManagedConfig) RuntimeConfig() Config {
	cfg := c.rawRuntimeConfig()
	cfg.Normalize()
	return cfg
}

// rawRuntimeConfig projects the manifest onto audited standalone defaults without normalizing explicit values.
// rawRuntimeConfig 用于把清单投影到已审计的独立运行默认值，同时不归一化显式值。
//
// Validation consumes this raw projection first so an invalid explicit zero cannot be rewritten into
// a default before the managed contract rejects it.
// 校验会先消费这份原始投影，避免无效的显式零值在托管契约拒绝前被改写成默认值。
//
// Returns:
// 返回值：
//   - Config: one credential-free runtime projection retaining every explicit managed value.
//   - Config：保留每一项显式托管值且不含凭据的运行时投影。
func (c ManagedConfig) rawRuntimeConfig() Config {
	cfg := DefaultLocal()
	cfg.GRPC.ListenAddr = c.Runtime.GRPC.PreferredListenAddr
	cfg.GRPC.MaxReceiveMessageBytes = c.Runtime.GRPC.MaxReceiveMessageBytes
	cfg.GRPC.RequestTimeout = c.Runtime.GRPC.RequestTimeout
	cfg.GRPC.ShutdownTimeout = c.Runtime.GRPC.ShutdownTimeout
	cfg.GRPC.Keepalive = c.Runtime.GRPC.Keepalive
	cfg.Management = ManagementConfig{
		Enabled:             true,
		ListenAddr:          c.Runtime.Management.PreferredListenAddr,
		AccessToken:         c.Runtime.Management.AccessToken,
		MaxRequestBodyBytes: c.Runtime.Management.MaxRequestBodyBytes,
		DefaultPageSize:     c.Runtime.Management.DefaultPageSize,
		MaxPageSize:         c.Runtime.Management.MaxPageSize,
		ReadHeaderTimeout:   c.Runtime.Management.ReadHeaderTimeout,
		RequestTimeout:      c.Runtime.Management.RequestTimeout,
		IdleTimeout:         c.Runtime.Management.IdleTimeout,
		ShutdownTimeout:     c.Runtime.Management.ShutdownTimeout,
	}
	cfg.Storage.Mode = "controller"
	cfg.Controller = c.Runtime.Storage.ControllerLease
	cfg.Controller.Endpoint = c.Runtime.Storage.ControllerEndpoint
	cfg.Controller.AutoSpawn = false
	cfg.Controller.Executable = ""
	cfg.Controller.SpaceID = c.Runtime.Storage.ControllerSpaceID
	cfg.Controller.SpaceLabel = c.Runtime.Storage.ControllerSpaceLabel
	cfg.Logging = c.Runtime.Logging
	cfg.PII = c.Runtime.PII
	cfg.Noise = c.Runtime.Noise
	cfg.Prompts = c.Runtime.Prompts
	cfg.Vector = c.Runtime.Vector
	cfg.Relational = c.Runtime.Relational
	cfg.PreCheck = PreCheckConfig{
		IntentTimeout:       c.Runtime.PreCheck.IntentTimeout,
		TopK:                c.Runtime.PreCheck.TopK,
		SearchScope:         c.Runtime.PreCheck.SearchScope,
		SimilarityThreshold: c.Runtime.PreCheck.SimilarityThreshold,
	}
	cfg.PostAction = c.Runtime.PostAction
	cfg.MemoryPipeline = c.Runtime.MemoryPipeline
	cfg.Retention = c.Runtime.Retention
	cfg.Embedding.Dimension = c.Runtime.Inference.EmbeddingDimension
	return cfg
}

// validateManagedConfigPath enforces an absolute regular-file path without symlink traversal.
// validateManagedConfigPath 用于强制要求绝对普通文件路径，并禁止任何符号链接遍历。
func validateManagedConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", errors.New("managed config path must be absolute")
	}
	cleaned := filepath.Clean(path)
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect managed config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("managed config must be a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve managed config: %w", err)
	}
	if !samePath(cleaned, resolved) {
		return "", errors.New("managed config path must not traverse symbolic links")
	}
	return cleaned, nil
}

// samePath compares two resolved paths with the platform's case semantics.
// samePath 用于按照当前平台的大小写语义比较两个已解析路径。
func samePath(left string, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// managedDocumentJSON normalizes YAML or JSON into one strict JSON document without environment expansion.
// managedDocumentJSON 用于把 YAML 或 JSON 规范化为一份严格 JSON 文档，且不执行环境变量展开。
func managedDocumentJSON(path string, body []byte) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		var value any
		if err := yaml.Unmarshal(body, &value); err != nil {
			return nil, err
		}
		normalized, err := normalizeYAMLValue(value)
		if err != nil {
			return nil, err
		}
		return json.Marshal(normalized)
	case ".json":
		return body, nil
	default:
		return nil, errors.New("managed config must use .json, .yaml, or .yml")
	}
}

// requireAbsolutePath validates one non-empty absolute path.
// requireAbsolutePath 用于校验非空绝对路径。
func requireAbsolutePath(name string, path string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path", name)
	}
	return nil
}

// requireAbsoluteDirectory validates one existing absolute directory.
// requireAbsoluteDirectory 用于校验一个已存在的绝对目录。
func requireAbsoluteDirectory(name string, path string) error {
	if err := requireAbsolutePath(name, path); err != nil {
		return err
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory", name)
	}
	return nil
}

// requireLoopbackAddress validates one TCP endpoint with an explicit loopback host and numeric port.
// requireLoopbackAddress 用于校验带有显式回环主机与数字端口的 TCP 端点。
func requireLoopbackAddress(name string, address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("%s must be a host:port address: %w", name, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || strings.TrimSpace(port) == "" {
		return fmt.Errorf("%s must use an explicit loopback IP and port", name)
	}
	return nil
}
