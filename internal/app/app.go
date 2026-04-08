// app.go implements the application composition root for the local gRPC runtime.
// app.go 用于实现本地 gRPC 运行时的应用组合根。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"

	grpcapi "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi"
	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_postgres"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/pii"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Application holds the fully wired local runtime, including the gRPC server and shutdown hooks.
// Application 用于持有完整装配后的本地运行时，包括 gRPC 服务和关闭钩子。
type Application struct {
	Config    config.Config
	Logger    *logx.Logger
	Server    *grpc.Server
	Shutdowns []appports.Shutdowner
}

// storageDependencies bundles the relational and vector ports selected for one runtime mode together with the startup schema workflow the composition root must apply.
// storageDependencies 用于打包某个运行模式选中的关系端口、向量端口，以及组合根启动时需要执行的 schema 工作流。
type storageDependencies struct {
	Relational         appports.RelationalStore
	Vector             appports.VectorStore
	ManageVectorSchema bool
}

// routeFailoverAwareLLMClient preserves prompt-side model routing while clearing request-level model pins in multi-route mode so processor calls can still fan out across heterogeneous llm.routes.
// routeFailoverAwareLLMClient 用于在保留提示词侧模型路由的同时，于多路由模式清空请求级模型固定值，确保处理器调用仍能在不同模型名的 llm.routes 之间扩散容灾。
type routeFailoverAwareLLMClient struct {
	upstream appports.LLMClient
}

// runtimePIIScrubber adapts the shared PII engine into the narrow use-case interface so pre-check and post-action can redact text without depending on engine-specific language-routing details.
// runtimePIIScrubber 用于把共享 PII 引擎适配成用例层需要的最小接口，让 pre-check 与 post-action 可以执行脱敏，而不直接依赖引擎的语言路由细节。
type runtimePIIScrubber struct {
	engine          *pii.Engine
	defaultLanguage string
}

// Scrub redacts one text fragment with the configured default runtime language so live request paths reuse the same ruleset as the standalone PII tester.
// Scrub 用于使用运行时配置好的默认语言对单段文本执行脱敏，让实时请求链路复用与独立 PII 测试器相同的一套规则。
func (s runtimePIIScrubber) Scrub(text string) string {
	if s.engine == nil {
		return text
	}
	return s.engine.Scrub(text, s.defaultLanguage)
}

// Generate forwards one processor-originated LLM request after dropping the fixed model field that would otherwise block heterogeneous route failover.
// Generate 用于转发一条来自处理器的 LLM 请求，并在转发前移除会阻断异构路由容灾的固定模型字段。
func (c routeFailoverAwareLLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.upstream == nil {
		return appports.LLMResponse{}, fmt.Errorf("route failover aware llm client is nil")
	}
	req.Model = ""
	return c.upstream.Generate(ctx, req)
}

// selectProcessorLLMModel chooses the primary model label for one business call tier without coupling prompt selection to model-specific prompt folders.
// selectProcessorLLMModel 用于为某个业务调用层级选择主模型标识，同时避免再把提示词选择耦合到模型专属提示词目录。
func selectProcessorLLMModel(cfg config.Config, selectionLevel appports.LLMRouteSelectionLevel) string {
	return cfg.LLM.PrimaryModelForSelection(string(selectionLevel))
}

// NewLocal creates the local application instance.
// NewLocal 用于创建本地应用实例。
func NewLocal(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	return newApplication(cfg, prompts, layout)
}

// newApplication composes the runtime dependencies for the local gRPC server.
// newApplication 用于为本地 gRPC 服务装配运行时依赖。
func newApplication(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	// Initialize shared runtime utilities such as logging and ID generation first.
	// 先初始化日志和 ID 生成器等共享运行时能力。
	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		return nil, err
	}
	fileWriter, err := logx.NewHourlyFileWriter(logDir)
	if err != nil {
		return nil, fmt.Errorf("init runtime log file writer: %w", err)
	}
	llmOutputLogger, llmOutputShutdowner, err := buildLLMOutputLogger(cfg, logDir)
	if err != nil {
		return nil, fmt.Errorf("init llm output log file writer: %w", err)
	}
	// Close eager startup resources again when later dependency wiring fails, so a half-built runtime does not leave file handles, pools, or workers behind.
	// 当后续依赖装配失败时，及时关闭这些提前创建的启动资源，避免半装配运行时留下文件句柄、连接池或后台工作器。
	initSucceeded := false
	startupShutdowns := []appports.Shutdowner{}
	trackStartupShutdown := func(shutdowner appports.Shutdowner) {
		startupShutdowns = appendUniqueShutdowner(startupShutdowns, shutdowner)
	}
	trackStartupShutdown(fileWriter)
	trackStartupShutdown(llmOutputShutdowner)
	defer func() {
		if initSucceeded {
			return
		}
		for i := len(startupShutdowns) - 1; i >= 0; i-- {
			_ = startupShutdowns[i].Shutdown(context.Background())
		}
	}()
	logger := logx.New(io.MultiWriter(os.Stdout, fileWriter), logx.Config{
		Level:                cfg.Logging.Level,
		Format:               cfg.Logging.Format,
		DebugPayloads:        cfg.Logging.DebugRPCPayloads,
		ProtectPayloads:      cfg.Logging.ProtectPayloads,
		PayloadEncryptionKey: cfg.Logging.PayloadEncryptionKey,
	})
	ids := xid.NewGenerator()

	// Build outbound dependencies from the active configuration.
	// 根据当前配置构建出站依赖。
	llm, err := buildLLM(cfg)
	if err != nil {
		return nil, err
	}
	embedding, err := buildEmbedding(cfg)
	if err != nil {
		return nil, err
	}
	reranker, err := buildReranker(cfg)
	if err != nil {
		return nil, err
	}
	storageDeps, err := buildStorageDependencies(cfg)
	if err != nil {
		return nil, err
	}
	relational := storageDeps.Relational
	vector := storageDeps.Vector
	trackStartupShutdown(relational)
	trackStartupShutdown(vector)
	noiseCache, ok := relational.(appports.NoiseEmbeddingCache)
	if !ok {
		return nil, fmt.Errorf("relational store does not support noise embedding cache")
	}
	scopeResolver, ok := relational.(appports.RequestScopeResolver)
	if !ok {
		return nil, fmt.Errorf("relational store does not support request scope resolution")
	}
	workspaceStore, ok := relational.(appports.WorkspaceStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support workspace management")
	}
	schemaVersions, ok := relational.(appports.SchemaVersionStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support schema version tracking")
	}
	profileStore, ok := relational.(appports.ProfileStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support profile management")
	}
	memoryStore, ok := relational.(appports.MemoryStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support unified memory lookup")
	}
	chatCompactStore, ok := relational.(usecase.ChatCompactStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support session compact updates")
	}
	scratchpadStore, ok := relational.(appports.ScratchpadStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support scratchpad management")
	}
	scratchpadMaintenanceStore, ok := relational.(appports.ScratchpadMaintenanceStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support scratchpad maintenance")
	}
	retentionStore, err := usecase.EnsureRetentionStore(relational)
	if err != nil {
		return nil, err
	}
	if storageDeps.ManageVectorSchema {
		if err := ensureVectorSchema(context.Background(), schemaVersions, workspaceStore, vector, logger); err != nil {
			return nil, err
		}
	}
	noiseGate, err := buildNoiseGate(cfg, layout, embedding, noiseCache, logger)
	if err != nil {
		return nil, err
	}
	piiScrubber, err := buildPIIScrubber(cfg, layout, logger)
	if err != nil {
		return nil, err
	}

	// Compose use cases on top of processors and outbound ports.
	// 在处理器和出站端口之上装配用例层。
	workspace := usecase.NewWorkspaceUseCase(workspaceStore, vector)
	reservePromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelReserve)
	preCheckL1PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL1)
	preCheckL2PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL2)
	postActionL1PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPostActionL1)
	postActionL2PromptModel := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPostActionL2)
	processorLLM := adaptLLMForProcessorRoutes(cfg, llm)
	processorLLM = wrapLLMWithOutputLogger(processorLLM, llmOutputLogger)
	profiles := usecase.NewProfileUseCase(profileStore, processor.NewManualProfileReviewer(processorLLM, prompts, reservePromptModel), logger)
	memory := usecase.NewMemoryUseCase(profileStore, memoryStore, embedding, vector, logger)
	memory.ConfigurePIIScrubber(piiScrubber)
	scratchpad := usecase.NewScratchpadUseCase(scratchpadStore)
	chatCompact := usecase.NewChatCompactUseCase(chatCompactStore)
	candidateReviewer := processor.NewPostActionCandidateReviewer(processorLLM, prompts, postActionL2PromptModel)
	memory.ConfigureHybrid(cfg.MemoryPipeline.HybridEnabled, cfg.MemoryPipeline.LexicalTopK, cfg.MemoryPipeline.RRFK)
	memory.ConfigureMMR(cfg.MemoryPipeline.MMREnabled, cfg.MemoryPipeline.MMRLambda)
	memory.ConfigureDecay(
		cfg.MemoryPipeline.WeibullEnabled,
		cfg.MemoryPipeline.WeibullShape,
		cfg.MemoryPipeline.WeibullScaleHours,
		cfg.MemoryPipeline.WeibullMinMultiplier,
		cfg.MemoryPipeline.WeibullReinforceWeight,
		cfg.MemoryPipeline.WeibullCrossSessionBoost,
	)
	memory.ConfigureRerank(reranker, cfg.Rerank.TopN)
	memory.ConfigureHardDedupePoolTopK(hardDedupePoolTopKOrDefault(cfg))
	memory.ConfigureMemoryReplace(
		candidateReviewer,
		cfg.PreCheck.TopK,
		cfg.MemoryReplaceScope,
		replaceMinSimilarityOrDefault(cfg),
		hardDedupeCosineThresholdOrDefault(cfg),
	)
	pre := usecase.NewPreCheckUseCase(
		memory,
		relational,
		processor.NewIntentExtractor(processorLLM, prompts, preCheckL1PromptModel, cfg.MemoryPipeline.MaxSearchKeywords),
		processor.NewPreCheckMemoryReviewer(processorLLM, prompts, preCheckL2PromptModel),
		processor.NewContextAssembler(),
		usecase.PreCheckConfig{
			IntentTimeout:      cfg.PreCheck.IntentTimeout.Duration,
			TopK:               cfg.PreCheck.TopK,
			MinSimilarityScore: minSimilarityOrDefault(cfg),
			SearchScope:        cfg.PreCheck.SearchScope,
			HistoryTurns:       cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens:     cfg.PostAction.SessionAnalysisMaxInputTokens,
		},
		logger,
	)
	pre.ConfigurePIIScrubber(piiScrubber)
	post := usecase.NewPostActionUseCase(
		noiseGate,
		relational,
		embedding,
		vector,
		processor.NewTurnAnalyzer(processorLLM, prompts, postActionL1PromptModel),
		memory,
		candidateReviewer,
		usecase.PostActionAnalysisConfig{
			TurnThreshold:             cfg.PostAction.SessionAnalysisTurnThreshold,
			TokenThreshold:            cfg.PostAction.SessionAnalysisTokenThreshold,
			IdleTimeout:               cfg.PostAction.SessionAnalysisIdleTimeout.Duration,
			HistoryTurns:              cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens:            cfg.PostAction.SessionAnalysisMaxInputTokens,
			DedupeSearchTopK:          cfg.PreCheck.TopK,
			MemoryReplaceScope:        cfg.MemoryReplaceScope,
			DedupeMinSimilarity:       replaceMinSimilarityOrDefault(cfg),
			HardDedupeCosineThreshold: hardDedupeCosineThresholdOrDefault(cfg),
		},
		logger,
	)
	post.ConfigurePIIScrubber(piiScrubber)
	// Retention uses the same shared history-turn knob currently consumed by both PreCheck and PostAction, so the configured hot window is that shared base plus the extra keep turns.
	// 当前运行时里 PreCheck 与 PostAction 共用同一组历史轮数配置，因此 retention 的热窗口等于这组共享基线再加额外保留轮数。
	retentionTurnHotWindowSize := cfg.PostAction.SessionAnalysisHistoryTurns + cfg.Retention.TurnKeepExtraTurns
	retention := usecase.NewRetentionUseCase(retentionStore, vector, usecase.RetentionConfig{
		Enabled:                     cfg.Retention.Enabled,
		RecycleScanInterval:         cfg.Retention.RecycleScanInterval.Duration,
		SessionIdleRecycleAfter:     cfg.Retention.SessionIdleRecycleAfter.Duration,
		TurnHotWindowSize:           retentionTurnHotWindowSize,
		TrashRetention:              cfg.Retention.TrashRetention.Duration,
		ProtectPriorityFloor:        cfg.Retention.ProtectPriorityFloor,
		ProtectMemoryLevelFloor:     cfg.Retention.ProtectMemoryLevelFloor,
		SkipProtectedSharedMemories: cfg.Retention.SkipProtectedSharedMemories,
	}, logger)
	retention.ConfigureScratchpadMaintenanceStore(scratchpadMaintenanceStore)
	trackStartupShutdown(post)
	trackStartupShutdown(retention)

	// Wire gRPC handlers and shutdown dependencies into the application container.
	// 将 gRPC 处理器和关闭依赖接入应用容器。
	deps := grpcapi.Dependencies{
		IDs:               ids,
		Workspace:         workspace,
		Profiles:          profiles,
		Memory:            memory,
		Scratchpad:        scratchpad,
		ChatCompact:       chatCompact,
		PreCheck:          pre,
		PostAction:        post,
		ScopeResolver:     scopeResolver,
		Logger:            logger,
		Validator:         grpcapi.NewRequestValidator(),
		DebugRPCPayloads:  cfg.Logging.DebugRPCPayloads,
		WorkspaceTimeout:  cfg.GRPC.RequestTimeout.Workspace.Duration,
		PreCheckTimeout:   cfg.GRPC.RequestTimeout.PreCheck.Duration,
		PostActionTimeout: cfg.GRPC.RequestTimeout.PostAction.Duration,
	}
	grpcOptions := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveMessageBytes),
		grpc.ChainUnaryInterceptor(grpcapi.BuildUnaryInterceptors(deps)...),
	}
	server := grpc.NewServer(grpcOptions...)
	vmmv1.RegisterVMMServiceServer(server, grpcapi.NewServer(deps))
	reflection.Register(server)

	shutdowns := buildUniqueShutdownSequence(fileWriter, llmOutputShutdowner, relational, vector, post, retention)
	initSucceeded = true
	return &Application{Config: cfg, Logger: logger, Server: server, Shutdowns: shutdowns}, nil
}

// Run starts the gRPC server and coordinates graceful shutdown against context cancellation, OS signals, and serve failures.
// Run 用于启动 gRPC 服务，并在上下文取消、系统信号和服务错误之间协调优雅停机。
func (a *Application) Run(ctx context.Context) error {
	// Fail fast on obviously incomplete runtime state so callers receive one deterministic error instead of a goroutine panic from grpc.Server.
	// 对明显不完整的运行时状态提前失败，让调用方拿到确定性错误，而不是在 goroutine 里被 grpc.Server 触发 panic。
	if a == nil {
		return errors.New("application is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.Server == nil {
		return errors.New("grpc server is not initialized")
	}

	errCh := make(chan error, 1)
	go func() {
		// Bind the TCP listener at start time so TLS can be terminated by an external proxy such as Caddy.
		// 在启动时绑定 TCP 监听器，让 TLS 由外部代理如 Caddy 终止。
		listener, err := net.Listen("tcp", a.Config.GRPC.ListenAddr)
		if err != nil {
			errCh <- err
			return
		}
		a.Logger.Info("grpc server listening", "addr", listener.Addr().String())
		if err := a.Server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-ctx.Done():
		return a.Shutdown(context.Background())
	case sig := <-sigCh:
		a.Logger.Info("received shutdown signal", "signal", sig.String())
		return a.Shutdown(context.Background())
	case err := <-errCh:
		shutdownErr := a.Shutdown(context.Background())
		if err != nil {
			if shutdownErr != nil {
				return errors.Join(err, shutdownErr)
			}
			return err
		}
		return shutdownErr
	}
}

// Shutdown stops the gRPC server and releases downstream resources in reverse construction order.
// Shutdown 用于停止 gRPC 服务，并按构建逆序释放下游资源。
func (a *Application) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := a.Config.GRPC.ShutdownTimeout.Duration
	if timeout <= 0 {
		timeout = config.DefaultLocal().GRPC.ShutdownTimeout.Duration
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Only stop gRPC when the runtime actually finished wiring the server, so partial construction or test doubles can still reuse the shutdown path safely.
	// 只有在运行时确实完成 gRPC 服务装配时才执行停服，这样部分装配对象或测试替身也能安全复用 shutdown 路径。
	if a.Server != nil {
		stopped := make(chan struct{})
		go func() {
			a.Server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-shutdownCtx.Done():
			a.Server.Stop()
		case <-stopped:
		}
	}

	// Continue draining every dependency even if one shutdown step fails, so later resources do not leak simply because an earlier adapter returned an error.
	// 即便某个关闭步骤失败，也继续释放后续依赖，避免前一个适配器报错后导致后面的资源直接泄漏。
	var shutdownErrors []error
	seenShutdowners := map[string]struct{}{}
	for i := len(a.Shutdowns) - 1; i >= 0; i-- {
		shutdowner := a.Shutdowns[i]
		if shutdowner == nil {
			continue
		}
		id := shutdownerIdentity(shutdowner)
		if _, seen := seenShutdowners[id]; seen {
			continue
		}
		seenShutdowners[id] = struct{}{}
		if err := shutdowner.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown dependency[%d]: %w", i, err))
		}
	}
	if len(shutdownErrors) > 0 {
		return fmt.Errorf("shutdown completed with dependency errors: %w", errors.Join(shutdownErrors...))
	}
	return nil
}

// buildUniqueShutdownSequence preserves construction order while collapsing duplicate shutdown hooks that point at the same runtime dependency.
// buildUniqueShutdownSequence 用于在保留构建顺序的同时折叠指向同一运行时依赖的重复 shutdown 钩子。
func buildUniqueShutdownSequence(shutdowners ...appports.Shutdowner) []appports.Shutdowner {
	unique := make([]appports.Shutdowner, 0, len(shutdowners))
	for _, shutdowner := range shutdowners {
		unique = appendUniqueShutdowner(unique, shutdowner)
	}
	return unique
}

// appendUniqueShutdowner appends one shutdown hook only when it does not already exist in the current construction list.
// appendUniqueShutdowner 用于仅在当前构建列表里尚不存在时追加一条 shutdown 钩子。
func appendUniqueShutdowner(shutdowners []appports.Shutdowner, shutdowner appports.Shutdowner) []appports.Shutdowner {
	if shutdowner == nil {
		return shutdowners
	}
	id := shutdownerIdentity(shutdowner)
	for _, existing := range shutdowners {
		if shutdownerIdentity(existing) == id {
			return shutdowners
		}
	}
	return append(shutdowners, shutdowner)
}

// shutdownerIdentity derives one stable in-process identity for duplicate-suppression across startup cleanup and graceful shutdown.
// shutdownerIdentity 用于为启动清理与优雅停机的重复抑制推导一条稳定的进程内标识。
func shutdownerIdentity(shutdowner appports.Shutdowner) string {
	if shutdowner == nil {
		return ""
	}
	value := reflect.ValueOf(shutdowner)
	typeName := reflect.TypeOf(shutdowner).String()
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		if value.IsNil() {
			return typeName + ":nil"
		}
		return fmt.Sprintf("%s:%x", typeName, value.Pointer())
	default:
		return fmt.Sprintf("%s:%#v", typeName, shutdowner)
	}
}

// minSimilarityOrDefault keeps the pre-check runtime aligned with the validated memory-pipeline similarity floor.
// minSimilarityOrDefault 用于让 pre-check 运行时与已校验过的 memory pipeline 相似度下限保持一致。
func minSimilarityOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.MinSimilarityScore != nil && *cfg.MemoryPipeline.MinSimilarityScore > 0 {
		return *cfg.MemoryPipeline.MinSimilarityScore
	}
	return 0.75
}

// replaceMinSimilarityOrDefault keeps post-action/direct-write duplicate review aligned with the dedicated reviewer-entry similarity floor instead of the older hard-coded clamp.
// replaceMinSimilarityOrDefault 用于让 post-action/主动写记忆重复评审与专用 reviewer 入口相似度下限保持一致，而不再依赖旧的硬编码钳制。
func replaceMinSimilarityOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.ReplaceMinSimilarityScore != nil && *cfg.MemoryPipeline.ReplaceMinSimilarityScore >= 0 {
		return *cfg.MemoryPipeline.ReplaceMinSimilarityScore
	}
	return 0.80
}

// hardDedupeCosineThresholdOrDefault keeps the pre-review vector hard-dedupe gate aligned with validated config while still allowing callers to disable it by setting 0.
// hardDedupeCosineThresholdOrDefault 用于让 reviewer 前向量硬排重门槛与已校验配置保持一致，同时允许调用方通过设置 0 显式关闭。
func hardDedupeCosineThresholdOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.HardDedupeCosineThreshold != nil && *cfg.MemoryPipeline.HardDedupeCosineThreshold >= 0 {
		return *cfg.MemoryPipeline.HardDedupeCosineThreshold
	}
	return 0.99
}

// hardDedupePoolTopKOrDefault keeps the reviewer-front hard-dedupe scan window aligned with validated config while guaranteeing a non-zero bounded expansion target for the search pipeline.
// hardDedupePoolTopKOrDefault 用于让 reviewer 前硬排重扫描窗口与已校验配置保持一致，并保证检索流水线总能拿到一个非零的扩展目标值。
func hardDedupePoolTopKOrDefault(cfg config.Config) int {
	if cfg.MemoryPipeline.HardDedupePoolTopK > 0 {
		return cfg.MemoryPipeline.HardDedupePoolTopK
	}
	return 16
}

// normalizeProviderAlias keeps runtime adapter selection aligned with config validation by trimming accidental surrounding whitespace before lower-casing provider aliases.
// normalizeProviderAlias 用于在 provider 别名转小写前先裁掉意外的首尾空白，让运行时适配器选择与配置校验保持一致。
func normalizeProviderAlias(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// buildLLM selects the configured generation backend used by the debug-stage post-action summary probe and future LLM-driven workflows.
// buildLLM 用于选择当前配置的生成后端，服务调试阶段的 post-action 摘要探测以及未来的 LLM 工作流。
func buildLLM(cfg config.Config) (appports.LLMClient, error) {
	cfg.Normalize()
	routes := cfg.LLM.ProviderRoutes()
	if len(routes) == 0 {
		return nil, fmt.Errorf("llm.routes must contain at least one route")
	}
	if len(routes) == 1 {
		return buildOneLLMRouteClient(routes[0], 0)
	}
	routeOptions := make([]ai_key_failover.LLMRouteOptions, 0, len(routes))
	for idx, route := range routes {
		resolvedWeights := route.ResolvedWeights()
		routeOptions = append(routeOptions, ai_key_failover.LLMRouteOptions{
			Name: buildRouteName("llm", idx, route.Name),
			SelectionWeights: ai_key_failover.LLMRouteSelectionWeights{
				PreCheckL1:   resolvedWeights.PreCheckL1,
				PreCheckL2:   resolvedWeights.PreCheckL2,
				PostActionL1: resolvedWeights.PostActionL1,
				PostActionL2: resolvedWeights.PostActionL2,
				Reserve:      resolvedWeights.Reserve,
			},
			Provider:     route.Provider,
			Endpoint:     route.Endpoint,
			Model:        route.Model,
			Organization: route.Organization,
			Project:      route.Project,
			APIKeys:      append([]string(nil), route.APIKeys...),
			Params:       route.Params,
			ModelParams:  route.ModelParams,
			Options:      buildKeyFailoverOptions(buildRouteName("llm", idx, route.Name), route.Nodes, route.KeyFailover),
		})
	}
	return ai_key_failover.NewLLMMultiRouteClient(routeOptions)
}

// adaptLLMForProcessorRoutes keeps prompt routing anchored to the primary model while freeing processor-originated requests from route-blocking model pins in multi-route mode.
// adaptLLMForProcessorRoutes 用于让提示词路由继续锚定主模型，同时在多路由模式下解除处理器请求对特定模型名的硬固定，避免阻断路由级容灾。
func adaptLLMForProcessorRoutes(cfg config.Config, client appports.LLMClient) appports.LLMClient {
	if len(cfg.LLM.ProviderRoutes()) <= 1 {
		return client
	}
	return routeFailoverAwareLLMClient{upstream: client}
}

// buildOneLLMRouteClient materializes one concrete fixed-model LLM route that will handle provider-specific key failover inside its own key pool.
// buildOneLLMRouteClient 用于实例化一条具体的固定模型 LLM 路由，让它在自己的 Key 池内部处理 provider 专属的 Key 容灾。
func buildOneLLMRouteClient(route config.LLMRouteConfig, routeIndex int) (appports.LLMClient, error) {
	return ai_key_failover.NewProviderLLMClient(
		route.Provider,
		route.Endpoint,
		route.Model,
		route.Organization,
		route.Project,
		route.APIKeys,
		route.Params,
		route.ModelParams,
		buildKeyFailoverOptions(buildRouteName("llm", routeIndex, route.Name), route.Nodes, route.KeyFailover),
	)
}

// buildEmbedding selects the configured real embedding adapter for recall and semantic filtering.
// buildEmbedding 用于为召回和语义过滤选择当前配置的真实 embedding 适配器。
func buildEmbedding(cfg config.Config) (appports.EmbeddingClient, error) {
	cfg.Normalize()
	return ai_key_failover.NewProviderEmbeddingClient(
		cfg.Embedding.Provider,
		cfg.Embedding.Endpoint,
		cfg.Embedding.Model,
		cfg.Embedding.Dimension,
		cfg.Embedding.MaxBatchSize,
		cfg.Embedding.Organization,
		cfg.Embedding.Project,
		cfg.Embedding.APIKeys,
		cfg.Embedding.Params,
		cfg.Embedding.ModelParams,
		buildKeyFailoverOptions("embedding", cfg.Embedding.RoutingNodes(), cfg.Embedding.KeyFailover),
	)
}

// buildReranker selects the optional second-stage rerank backend used to reorder first-stage vector recall hits.
// buildReranker 用于选择可选的第二阶段重排序后端，对首轮向量召回结果重新排序。
func buildReranker(cfg config.Config) (appports.RerankerClient, error) {
	cfg.Normalize()
	if !cfg.Rerank.Enabled {
		return nil, nil
	}
	routes := cfg.Rerank.ProviderRoutes()
	if len(routes) == 0 {
		return nil, fmt.Errorf("rerank.routes must contain at least one route when rerank is enabled")
	}
	if len(routes) == 1 {
		return buildOneRerankRouteClient(routes[0], 0)
	}
	routeOptions := make([]ai_key_failover.RerankRouteOptions, 0, len(routes))
	for idx, route := range routes {
		routeOptions = append(routeOptions, ai_key_failover.RerankRouteOptions{
			Name:     buildRouteName("rerank", idx, route.Name),
			Priority: route.Priority,
			Provider: route.Provider,
			Endpoint: route.Endpoint,
			Model:    route.Model,
			Timeout:  route.Timeout.Duration,
			APIKeys:  append([]string(nil), route.APIKeys...),
			Options:  buildKeyFailoverOptions(buildRouteName("rerank", idx, route.Name), route.Nodes, route.KeyFailover),
		})
	}
	return ai_key_failover.NewRerankMultiRouteClient(routeOptions)
}

// buildOneRerankRouteClient materializes one concrete fixed-model rerank route that will handle provider-specific key failover inside its own key pool.
// buildOneRerankRouteClient 用于实例化一条具体的固定模型 rerank 路由，让它在自己的 Key 池内部处理 provider 专属的 Key 容灾。
func buildOneRerankRouteClient(route config.RerankRouteConfig, routeIndex int) (appports.RerankerClient, error) {
	return ai_key_failover.NewProviderRerankerClient(
		route.Provider,
		route.Endpoint,
		route.Model,
		route.Timeout.Duration,
		route.APIKeys,
		buildKeyFailoverOptions(buildRouteName("rerank", routeIndex, route.Name), route.Nodes, route.KeyFailover),
	)
}

// buildRouteName returns one stable route label so route-level failover logs and selector error messages can point back to the configured route order.
// buildRouteName 用于返回稳定的路由标签，让路由级容灾日志和选择器错误信息都能回指到配置里的路由顺序。
func buildRouteName(serviceName string, routeIndex int, routeName string) string {
	trimmed := strings.TrimSpace(routeName)
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-route-%d", serviceName, routeIndex+1)
}

// buildKeyFailoverOptions converts one normalized runtime config into the fixed-model API-key failover options consumed by outbound wrappers.
// buildKeyFailoverOptions 用于把一份已归一化的运行时配置转换为出站包装器消费的固定模型 API Key 容灾参数。
func buildKeyFailoverOptions(serviceName string, nodes []config.AIRoutingNodeConfig, cfg config.KeyFailoverConfig) ai_key_failover.Options {
	routingNodes := make([]ai_key_failover.NodeOptions, 0, len(nodes))
	totalCandidates := 0
	for idx, node := range nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("%s-node-%d", serviceName, idx+1)
		}
		apiKeys := append([]string(nil), node.APIKeys...)
		totalCandidates += len(apiKeys)
		routingNodes = append(routingNodes, ai_key_failover.NodeOptions{
			Name:    name,
			APIKeys: apiKeys,
			RPM:     node.RPM,
			TPM:     node.TPM,
			RPD:     node.RPD,
		})
	}
	return ai_key_failover.Options{
		ServiceName:        serviceName,
		Enabled:            cfg.Enabled && totalCandidates > 1,
		Policy:             strings.TrimSpace(cfg.Policy),
		Nodes:              routingNodes,
		RespectRetryAfter:  cfg.RespectRetryAfter,
		RateLimitCooldown:  cfg.RateLimitCooldown.Duration,
		QuotaCooldown:      cfg.QuotaCooldown.Duration,
		AuthCooldown:       cfg.AuthCooldown.Duration,
		ProbeAfterCooldown: cfg.ProbeAfterCooldown,
	}
}

// buildStorageDependencies selects either the historical split stores or the unified PostgreSQL combined store and returns the matching runtime ports.
// buildStorageDependencies 用于选择历史分离存储或统一 PostgreSQL 组合库，并返回对应的运行时端口集合。
func buildStorageDependencies(cfg config.Config) (storageDependencies, error) {
	if cfg.UsesCombinedPostgres() {
		combined, err := buildCombinedStore(cfg)
		if err != nil {
			return storageDependencies{}, err
		}
		return storageDependencies{
			Relational:         combined,
			Vector:             combined,
			ManageVectorSchema: false,
		}, nil
	}
	relational, err := buildRelational(cfg)
	if err != nil {
		return storageDependencies{}, err
	}
	vector, err := buildVector(cfg)
	if err != nil {
		return storageDependencies{}, err
	}
	return storageDependencies{
		Relational:         relational,
		Vector:             vector,
		ManageVectorSchema: true,
	}, nil
}

// buildCombinedStore selects the unified PostgreSQL-backed combined store used by the new dialect pattern runtime.
// buildCombinedStore 用于选择 Dialect Pattern 运行时使用的统一 PostgreSQL 组合库。
func buildCombinedStore(cfg config.Config) (*vldb_postgres.Store, error) {
	if !cfg.UsesCombinedPostgres() {
		return nil, fmt.Errorf("combined postgres store is disabled for storage.mode=%s", cfg.StorageMode())
	}
	return vldb_postgres.NewStore(vldb_postgres.Config{
		DSN:                     cfg.Postgres.DSN,
		Schema:                  cfg.Postgres.Schema,
		Flavor:                  cfg.Postgres.Flavor,
		QueryTimeout:            cfg.Postgres.QueryTimeout.Duration,
		MaintenanceReadTimeout:  cfg.MaintenanceTool.Postgres.ReadTimeout.Duration,
		MaintenanceWriteTimeout: cfg.MaintenanceTool.Postgres.WriteTimeout.Duration,
		ConnectTimeout:          cfg.Postgres.ConnectTimeout.Duration,
		MaxOpenConns:            cfg.Postgres.MaxOpenConns,
		MinIdleConns:            cfg.Postgres.MinIdleConns,
		AutoCreateExtensions:    cfg.Postgres.AutoCreateExtensions,
		BM25IndexConcurrently:   cfg.Postgres.BM25IndexConcurrently,
		BM25IndexName:           cfg.Postgres.BM25IndexName,
		TRGMSimilarityThreshold: cfg.Postgres.TRGMSimilarityThreshold,
		VectorLists:             cfg.Postgres.VectorLists,
		VectorProbes:            cfg.Postgres.VectorProbes,
		MigrationBatchSize:      cfg.Postgres.MigrationBatchSize,
		EmbeddingDimension:      cfg.Embedding.Dimension,
	})
}

// buildVector selects the configured vector backend used by retrieval and destructive cleanup flows.
// buildVector 用于选择当前配置的向量后端，服务检索和破坏性清理流程。
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	switch normalizeProviderAlias(cfg.Vector.Provider) {
	case "lancedb":
		return vldb_lancedb.NewStore(cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// buildRelational selects the configured durable SQL backend used by workspace/session/turn persistence.
// buildRelational 用于选择当前配置的长期 SQL 后端，服务层级、session 和 turn 持久化。
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	switch normalizeProviderAlias(cfg.Relational.Provider) {
	case "sqlite":
		return vldb_sqlite.NewStore(cfg.SQLite.Address, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{
			LexicalPreTokenize: cfg.MemoryPipeline.LexicalPreTokenize,
		})
	default:
		return nil, fmt.Errorf("unsupported relational provider: %s", cfg.Relational.Provider)
	}
}

// buildNoiseGate builds the pre-persistence admission gate that blocks noisy single-round turns from entering long-term memory.
// buildNoiseGate 用于构建写库前的准入门控器，阻止噪声单轮问答进入长期记忆。
func buildNoiseGate(cfg config.Config, layout config.PromptLayout, embedding appports.EmbeddingClient, cache appports.NoiseEmbeddingCache, logger *logx.Logger) (*processor.NoiseGate, error) {
	return processor.NewNoiseGate(context.Background(), embedding, logger, processor.NoiseGateConfig{
		SystemDir:         layout.SystemNoiseRulesDir(),
		UserDir:           layout.UserNoiseRulesDir(),
		DefaultLanguage:   cfg.Noise.DefaultLanguage,
		Enabled:           cfg.Noise.Enabled,
		SemanticEnabled:   cfg.Noise.SemanticEnabled,
		SemanticThreshold: cfg.Noise.SemanticThreshold,
		Model:             cfg.Embedding.Model,
		Dimension:         cfg.Embedding.Dimension,
		Cache:             cache,
	})
}

// buildPIIScrubber builds the shared runtime PII scrubber used by pre-check and post-action so both entry chains reuse the packaged system rules and user override bundle roots.
// buildPIIScrubber 用于构建 pre-check 与 post-action 共用的运行时 PII 脱敏器，让两条入口链路复用打包后的系统规则目录和用户覆盖规则目录。
func buildPIIScrubber(cfg config.Config, layout config.PromptLayout, logger *logx.Logger) (usecase.PIIScrubber, error) {
	engine, err := pii.NewEngineWithLogger(layout.SystemPIIRulesDir(), layout.UserPIIRulesDir(), cfg.PII.DefaultLanguage, logger)
	if err != nil {
		return nil, err
	}
	return runtimePIIScrubber{
		engine:          engine,
		defaultLanguage: cfg.PII.DefaultLanguage,
	}, nil
}
