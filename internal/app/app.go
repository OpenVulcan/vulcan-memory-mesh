// app.go implements the application composition root for the local gRPC runtime.
// app.go 用于实现本地 gRPC 运行时的应用组合根。
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	grpcapi "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi"
	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/adapters/outbound/dashscope_rerank"
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
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
	logger := logx.New(os.Stdout, logx.Config{Level: cfg.Logging.Level, Format: cfg.Logging.Format})
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
	vector, err := buildVector(cfg)
	if err != nil {
		return nil, err
	}
	relational, err := buildRelational(cfg)
	if err != nil {
		return nil, err
	}
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
	profileStore, ok := relational.(appports.ProfileStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support profile management")
	}
	memoryStore, ok := relational.(appports.MemoryStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support unified memory lookup")
	}
	noiseGate, err := buildNoiseGate(cfg, layout, embedding, noiseCache, logger)
	if err != nil {
		return nil, err
	}

	// Compose use cases on top of processors and outbound ports.
	// 在处理器和出站端口之上装配用例层。
	workspace := usecase.NewWorkspaceUseCase(workspaceStore, vector)
	profiles := usecase.NewProfileUseCase(profileStore, processor.NewManualProfileReviewer(llm, prompts, cfg.LLM.Model), logger)
	memory := usecase.NewMemoryUseCase(profileStore, memoryStore, embedding, vector, logger)
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
	pre := usecase.NewPreCheckUseCase(
		profiles,
		memory,
		relational,
		processor.NewIntentExtractor(llm, prompts, cfg.LLM.Model, cfg.MemoryPipeline.MaxSearchKeywords),
		processor.NewPreCheckMemoryReviewer(llm, prompts, cfg.LLM.Model),
		processor.NewContextAssembler(prompts, cfg.LLM.Model),
		usecase.PreCheckConfig{
			IntentTimeout:      cfg.PreCheck.IntentTimeout.Duration,
			TopK:               cfg.PreCheck.TopK,
			MinSimilarityScore: minSimilarityOrDefault(cfg),
			HistoryTurns:       cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens:     cfg.PostAction.SessionAnalysisMaxInputTokens,
		},
		logger,
	)
	post := usecase.NewPostActionUseCase(
		noiseGate,
		relational,
		embedding,
		vector,
		processor.NewTurnAnalyzer(llm, prompts, cfg.LLM.Model),
		processor.NewProfileReviewer(llm, prompts, cfg.LLM.Model),
		usecase.PostActionAnalysisConfig{
			TurnThreshold:  cfg.PostAction.SessionAnalysisTurnThreshold,
			TokenThreshold: cfg.PostAction.SessionAnalysisTokenThreshold,
			IdleTimeout:    cfg.PostAction.SessionAnalysisIdleTimeout.Duration,
			HistoryTurns:   cfg.PostAction.SessionAnalysisHistoryTurns,
			MaxInputTokens: cfg.PostAction.SessionAnalysisMaxInputTokens,
		},
		logger,
	)

	// Wire gRPC handlers and shutdown dependencies into the application container.
	// 将 gRPC 处理器和关闭依赖接入应用容器。
	deps := grpcapi.Dependencies{
		IDs:               ids,
		Workspace:         workspace,
		Profiles:          profiles,
		Memory:            memory,
		PreCheck:          pre,
		PostAction:        post,
		ScopeResolver:     scopeResolver,
		Logger:            logger,
		Validator:         grpcapi.NewRequestValidator(),
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

	shutdowns := []appports.Shutdowner{relational, vector, post}
	return &Application{Config: cfg, Logger: logger, Server: server, Shutdowns: shutdowns}, nil
}

// Run starts the gRPC server and coordinates graceful shutdown against context cancellation, OS signals, and serve failures.
// Run 用于启动 gRPC 服务，并在上下文取消、系统信号和服务错误之间协调优雅停机。
func (a *Application) Run(ctx context.Context) error {
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
		return err
	}
}

// Shutdown stops the gRPC server and releases downstream resources in reverse construction order.
// Shutdown 用于停止 gRPC 服务，并按构建逆序释放下游资源。
func (a *Application) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, a.Config.GRPC.ShutdownTimeout.Duration)
	defer cancel()
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

	for i := len(a.Shutdowns) - 1; i >= 0; i-- {
		if err := a.Shutdowns[i].Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dependency[%d]: %w", i, err)
		}
	}
	return nil
}

// minSimilarityOrDefault keeps the pre-check runtime aligned with the validated memory-pipeline similarity floor.
// minSimilarityOrDefault 用于让 pre-check 运行时与已校验过的 memory pipeline 相似度下限保持一致。
func minSimilarityOrDefault(cfg config.Config) float64 {
	if cfg.MemoryPipeline.MinSimilarityScore != nil && *cfg.MemoryPipeline.MinSimilarityScore > 0 {
		return *cfg.MemoryPipeline.MinSimilarityScore
	}
	return 0.75
}

// buildLLM selects the configured generation backend used by the debug-stage post-action summary probe and future LLM-driven workflows.
// buildLLM 用于选择当前配置的生成后端，服务调试阶段的 post-action 摘要探测以及未来的 LLM 工作流。
func buildLLM(cfg config.Config) (appports.LLMClient, error) {
	switch strings.ToLower(cfg.LLM.Provider) {
	case "openai", "openai_native", "openai_go":
		return openai_native.NewLLMClient(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.Organization, cfg.LLM.Project, cfg.LLM.Params, cfg.LLM.ModelParams), nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", cfg.LLM.Provider)
	}
}

// buildEmbedding selects the configured real embedding adapter for recall and semantic filtering.
// buildEmbedding 用于为召回和语义过滤选择当前配置的真实 embedding 适配器。
func buildEmbedding(cfg config.Config) (appports.EmbeddingClient, error) {
	switch strings.ToLower(cfg.Embedding.Provider) {
	case "openai", "openai_native", "openai_go":
		return openai_native.NewEmbeddingClient(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimension, cfg.Embedding.Organization, cfg.Embedding.Project, cfg.Embedding.Params, cfg.Embedding.ModelParams), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Embedding.Provider)
	}
}

// buildReranker selects the optional second-stage rerank backend used to reorder first-stage vector recall hits.
// buildReranker 用于选择可选的第二阶段重排序后端，对首轮向量召回结果重新排序。
func buildReranker(cfg config.Config) (appports.RerankerClient, error) {
	if !cfg.Rerank.Enabled {
		return nil, nil
	}
	apiKey := strings.TrimSpace(cfg.Rerank.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(cfg.LLM.APIKey)
	}
	switch strings.ToLower(cfg.Rerank.Provider) {
	case "dashscope":
		return dashscope_rerank.NewClient(cfg.Rerank.Endpoint, apiKey, cfg.Rerank.Model, cfg.Rerank.Timeout.Duration, nil), nil
	default:
		return nil, fmt.Errorf("unsupported rerank provider: %s", cfg.Rerank.Provider)
	}
}

// buildVector selects the configured vector backend used by retrieval and destructive cleanup flows.
// buildVector 用于选择当前配置的向量后端，服务检索和破坏性清理流程。
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	switch strings.ToLower(cfg.Vector.Provider) {
	case "lancedb":
		return vldb_lancedb.NewStore(cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// buildRelational selects the configured durable SQL backend used by workspace/session/turn persistence.
// buildRelational 用于选择当前配置的长期 SQL 后端，服务层级、session 和 turn 持久化。
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	switch strings.ToLower(cfg.Relational.Provider) {
	case "sqlite":
		return vldb_sqlite.NewStore(cfg.SQLite.Address, cfg.SQLite.Timeout.Duration)
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
