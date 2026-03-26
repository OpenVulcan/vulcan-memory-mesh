// app.go implements the application composition root.
// app.go 用于实现应用组合根。
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
	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldg_dockdb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldg_lancedb"
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

// NewLocal creates a Local instance.
// NewLocal 用于创建 Local 实例。
func NewLocal(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	return newApplication(cfg, prompts, layout)
}

// newApplication creates a Application instance.
// newApplication 用于创建 Application 实例。
func newApplication(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	// Initialize shared runtime utilities such as logging and ID generation.
	// 初始化日志和 ID 生成器等共享运行时能力。
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
	vector, err := buildVector(cfg)
	if err != nil {
		return nil, err
	}
	relational, err := buildRelational(cfg)
	if err != nil {
		return nil, err
	}
	archiveStore, err := buildArchiveStore(cfg)
	if err != nil {
		return nil, err
	}
	scrubber, err := buildScrubber(cfg, layout)
	if err != nil {
		return nil, err
	}
	var noiseCache appports.NoiseEmbeddingCache
	if candidate, ok := archiveStore.(appports.NoiseEmbeddingCache); ok {
		noiseCache = candidate
	}
	noiseGate, err := buildNoiseGate(cfg, layout, embedding, noiseCache, logger)
	if err != nil {
		return nil, err
	}
	persona := buildPersona()

	// Compose use cases on top of processors and outbound ports.
	// 在处理器和出站端口之上装配用例层。
	chat := usecase.NewChatUseCase(scrubber, archiveStore, ids, logger)
	pre := usecase.NewPreCheckUseCase(processor.NewIntentExtractor(llm, prompts, cfg.LLM.Model, cfg.MemoryPipeline.MaxSearchKeywords), processor.NewContextAssembler(prompts, cfg.LLM.Model), embedding, vector, persona, logger, cfg.PreCheck.IntentTimeout.Duration, cfg.PreCheck.TopK, cfg.MemoryPipeline.MaxSearchKeywords, cfg.MemoryPipeline.MinSimilarityScore, cfg.Embedding.Model, cfg.Embedding.Dimension)
	post := usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), noiseGate, relational, logger)
	seed := usecase.NewSeedMemoryUseCase(embedding, vector, ids, logger, cfg.Embedding.Model, cfg.Embedding.Dimension)
	enableSeed := cfg.Admin.SeedEnabled

	// Wire gRPC handlers and shutdown dependencies into the application container.
	// 将 gRPC 处理器和关闭依赖接入应用容器。
	deps := grpcapi.Dependencies{
		IDs:               ids,
		Chat:              chat,
		PreCheck:          pre,
		PostAction:        post,
		SeedMemory:        seed,
		Logger:            logger,
		Validator:         grpcapi.NewRequestValidator(cfg.PostAction.InputMode),
		ChatTimeout:       cfg.GRPC.RequestTimeout.Chat.Duration,
		PreCheckTimeout:   cfg.GRPC.RequestTimeout.PreCheck.Duration,
		PostActionTimeout: cfg.GRPC.RequestTimeout.PostAction.Duration,
		SeedMemoryTimeout: cfg.GRPC.RequestTimeout.SeedMemory.Duration,
	}
	grpcOptions := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveMessageBytes),
		grpc.ChainUnaryInterceptor(grpcapi.BuildUnaryInterceptors(deps)...),
	}
	server := grpc.NewServer(grpcOptions...)
	vmmv1.RegisterVMMServiceServer(server, grpcapi.NewServer(deps))

	// Register server reflection so grpcurl and other local debugging tools can inspect services directly.
	// 注册服务反射，让 grpcurl 等本地调试工具可以直接发现服务定义。
	reflection.Register(server)
	shutdowns := []appports.Shutdowner{}
	if archiveStore != nil {
		shutdowns = append(shutdowns, archiveStore)
	}
	if relational != nil {
		shutdowns = append(shutdowns, relational)
	}
	if vector != nil {
		shutdowns = append(shutdowns, vector)
	}
	if !enableSeed {
		logger.Info("seed-memory rpc disabled")
	}
	return &Application{Config: cfg, Logger: logger, Server: server, Shutdowns: shutdowns}, nil
}

// Run executes the Run logic.
// Run 用于执行 Run 逻辑。
func (a *Application) Run(ctx context.Context) error {
	// Start the gRPC server asynchronously so shutdown signals can be observed.
	// 异步启动 gRPC 服务，以便同时监听关闭信号。
	errCh := make(chan error, 1)
	go func() {
		// Bind the TCP listener at start time so Caddy or other proxies can terminate TLS in front of gRPC.
		// 在启动时绑定 TCP 监听器，让 Caddy 或其他代理在 gRPC 前面终止 TLS。
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

	// Coordinate graceful shutdown across context cancellation, OS signals, and server failures.
	// 在上下文取消、系统信号和服务异常之间协调优雅停机。
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

// Shutdown executes the Shutdown logic.
// Shutdown 用于执行 Shutdown 逻辑。
func (a *Application) Shutdown(ctx context.Context) error {
	// Stop accepting new gRPC requests before tearing down downstream resources.
	// 先停止接收新的 gRPC 请求，再销毁下游资源。
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

	// Release dependencies in reverse order to match the construction sequence.
	// 按构建的逆序释放依赖，保证关闭顺序稳定。
	for i := len(a.Shutdowns) - 1; i >= 0; i-- {
		if err := a.Shutdowns[i].Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dependency[%d]: %w", i, err)
		}
	}
	return nil
}

// buildLLM selects the configured real LLM adapter for intent extraction and other generation flows.
// buildLLM 用于为意图提取等生成链路选择当前配置的真实 LLM 适配器。
func buildLLM(cfg config.Config) (appports.LLMClient, error) {
	// Select the LLM adapter according to the configured provider.
	// 根据配置的 provider 选择对应的 LLM 适配器。
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
	// Select the embedding adapter according to the configured provider.
	// 根据配置的 provider 选择对应的 embedding 适配器。
	switch strings.ToLower(cfg.Embedding.Provider) {
	case "openai", "openai_native", "openai_go":
		return openai_native.NewEmbeddingClient(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimension, cfg.Embedding.Organization, cfg.Embedding.Project, cfg.Embedding.Params, cfg.Embedding.ModelParams), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Embedding.Provider)
	}
}

// buildVector builds the target dependency.
// buildVector 用于构建目标依赖。
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	// Select the vector store adapter according to the configured provider.
	// 根据配置的 provider 选择对应的向量存储适配器。
	switch strings.ToLower(cfg.Vector.Provider) {
	case "lancedb":
		return vldg_lancedb.NewStore(cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	case "memory", "mock", "memory_mock":
		return memory_mock.NewVectorStore(), nil
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// buildRelational builds the target dependency.
// buildRelational 用于构建目标依赖。
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	// Select the relational store adapter according to the configured provider.
	// 根据配置的 provider 选择对应的关系存储适配器。
	switch strings.ToLower(cfg.Relational.Provider) {
	case "dockdb":
		return vldg_dockdb.NewStore(cfg.DockDB.Address, cfg.DockDB.Timeout.Duration)
	case "", "memory", "mock", "memory_mock":
		return memory_mock.NewRelationalStore(), nil
	default:
		return nil, fmt.Errorf("unsupported relational provider: %s", cfg.Relational.Provider)
	}
}

// buildArchiveStore builds the archive store used by the /chat scrub-and-store flow.
// buildArchiveStore 用于构建 /chat 脱敏归档流程使用的存储后端。
func buildArchiveStore(cfg config.Config) (appports.MemoryArchiveStore, error) {
	switch strings.ToLower(cfg.Archive.Provider) {
	case "dockdb":
		return vldg_dockdb.NewStore(cfg.DockDB.Address, cfg.DockDB.Timeout.Duration)
	default:
		return nil, fmt.Errorf("unsupported archive provider: %s", cfg.Archive.Provider)
	}
}

// buildPersona builds the target dependency.
// buildPersona 用于构建目标依赖。
func buildPersona() appports.ContextPersonaProvider {
	// Use the local persona provider for the OSS local runtime.
	// 在 OSS 本地运行时中使用本地画像提供器。
	return memory_mock.NewPersonaProvider()
}

// buildScrubber builds the multi-language scrubber used by the local chat archive route.
// buildScrubber 用于构建本地聊天归档路由使用的多语言脱敏器。
func buildScrubber(cfg config.Config, layout config.PromptLayout) (appports.TextScrubber, error) {
	return pii.NewEngine(layout.SystemPIIRulesDir(), layout.UserPIIRulesDir(), cfg.PII.DefaultLanguage)
}

// buildNoiseGate builds the pre-persistence admission gate that blocks noisy turns from entering long-term memory.
// buildNoiseGate 用于构建写库前阻断噪声轮次进入长期记忆的准入门控器。
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
