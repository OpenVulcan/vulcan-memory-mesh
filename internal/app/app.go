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
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldg_duckdb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldg_lancedb"
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
	noiseGate, err := buildNoiseGate(cfg, layout, embedding, noiseCache, logger)
	if err != nil {
		return nil, err
	}

	// Compose use cases on top of processors and outbound ports.
	// 在处理器和出站端口之上装配用例层。
	workspace := usecase.NewWorkspaceUseCase(workspaceStore, vector)
	pre := usecase.NewPreCheckUseCase(logger)
	post := usecase.NewPostActionUseCase(noiseGate, relational, logger)

	// Wire gRPC handlers and shutdown dependencies into the application container.
	// 将 gRPC 处理器和关闭依赖接入应用容器。
	deps := grpcapi.Dependencies{
		IDs:               ids,
		Workspace:         workspace,
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

	shutdowns := []appports.Shutdowner{relational, vector}
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

// buildVector selects the configured vector backend used by retrieval and destructive cleanup flows.
// buildVector 用于选择当前配置的向量后端，服务检索和破坏性清理流程。
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	switch strings.ToLower(cfg.Vector.Provider) {
	case "lancedb":
		return vldg_lancedb.NewStore(cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// buildRelational selects the configured durable SQL backend used by workspace/session/turn persistence.
// buildRelational 用于选择当前配置的长期 SQL 后端，服务层级、session 和 turn 持久化。
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	switch strings.ToLower(cfg.Relational.Provider) {
	case "duckdb":
		return vldg_duckdb.NewStore(cfg.DuckDB.Address, cfg.DuckDB.Timeout.Duration)
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
