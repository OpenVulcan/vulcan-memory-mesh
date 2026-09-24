// app.go implements the thin application composition root for the local gRPC runtime.
// app.go 用于实现本地 gRPC 运行时的薄型应用组合根。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"google.golang.org/grpc"
)

// Application holds the fully wired local runtime, including the gRPC server and shutdown hooks.
// Application 用于持有完整装配后的本地运行时，包括 gRPC 服务和关闭钩子。
type Application struct {
	Config           config.Config
	Logger           *logx.Logger
	Server           *grpc.Server
	ManagementServer *http.Server
	Shutdowns        []appports.Shutdowner
}

// RuntimeEndpoints contains the operating-system-resolved addresses for every required runtime listener.
// RuntimeEndpoints 用于保存每个必需运行时监听器由操作系统最终解析出的地址。
type RuntimeEndpoints struct {
	GRPCListenAddr       string
	ManagementListenAddr string
}

// NewLocal creates the local application instance.
// NewLocal 用于创建本地应用实例。
func NewLocal(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	return newApplication(cfg, prompts, layout)
}

// newApplication composes the standalone runtime dependencies for the local gRPC server.
// newApplication 用于装配本地 gRPC 服务的独立运行时依赖。
func newApplication(cfg config.Config, prompts appports.PromptSource, layout config.PromptLayout) (*Application, error) {
	// Initialize shared runtime utilities such as logging and ID generation first.
	// 先初始化日志和 ID 生成器等共享运行时能力。
	logDir, err := ResolveRuntimeLogDir(cfg, layout)
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

	// Build the low-level AI and storage foundations before composing the business pipeline.
	// 先构建底层 AI 与存储基础设施，再装配业务 pipeline。
	aiDeps, err := buildRuntimeAIDependencies(cfg)
	if err != nil {
		return nil, err
	}
	storageCaps, err := initRuntimeStorageCapabilities(cfg, logger, layout)
	if err != nil {
		return nil, err
	}
	logRuntimeStorageCapabilities(logger, cfg, layout)
	trackStartupShutdown(storageCaps.Lifecycle)
	trackStartupShutdown(storageCaps.Relational)
	trackStartupShutdown(storageCaps.Vector)

	// Build the shared cross-cutting processors that sit between raw adapters and application use cases.
	// 构建位于原始适配器与应用用例之间的共享横切处理器。
	noiseGate, err := buildNoiseGate(cfg, layout, aiDeps.Embedding, storageCaps.NoiseCache, logger)
	if err != nil {
		return nil, err
	}
	piiScrubber, err := buildPIIScrubber(cfg, layout, logger)
	if err != nil {
		return nil, err
	}

	// Assemble the business use cases and derive the transport-facing dependency bundle from the resulting runtime graph.
	// 装配业务用例，并基于最终运行时图生成传输层依赖集合。
	useCases, grpcDeps, err := composeRuntimeUseCases(cfg, prompts, ids, logger, llmOutputLogger, aiDeps, storageCaps, noiseGate, piiScrubber)
	if err != nil {
		return nil, err
	}
	trackStartupShutdown(useCases.PostAction)
	trackStartupShutdown(useCases.Retention)

	server := buildRuntimeGRPCServer(cfg, grpcDeps)
	var managementServer *http.Server
	if cfg.Management.Enabled {
		managementUseCase, managementErr := usecase.NewManagementUseCase(storageCaps.ManagementStore, cfg.Management.DefaultPageSize, cfg.Management.MaxPageSize)
		if managementErr != nil {
			return nil, fmt.Errorf("initialize management use case: %w", managementErr)
		}
		managementUseCase.ConfigureMutationPolicy(
			cfg.PostAction.SessionAnalysisHistoryTurns+cfg.Retention.TurnKeepExtraTurns,
			cfg.Retention.TrashRetention.Duration,
		)
		if storageCaps.ManageVectorSchema {
			if managementErr := managementUseCase.ConfigureVectorQuarantine(storageCaps.Vector); managementErr != nil {
				return nil, fmt.Errorf("initialize management vector quarantine: %w", managementErr)
			}
		}
		managementServer = buildManagementHTTPServer(cfg, managementUseCase)
	}
	shutdowns := buildUniqueShutdownSequence(fileWriter, llmOutputShutdowner, storageCaps.Lifecycle, storageCaps.Relational, storageCaps.Vector, useCases.PostAction, useCases.Retention)
	initSucceeded = true
	return &Application{
		Config:           cfg,
		Logger:           logger,
		Server:           server,
		ManagementServer: managementServer,
		Shutdowns:        shutdowns,
	}, nil
}

// Run starts the gRPC server and coordinates graceful shutdown against context cancellation, OS signals, and serve failures.
// Run 用于启动 gRPC 服务，并在上下文取消、系统信号和服务错误之间协调优雅停机。
func (a *Application) Run(ctx context.Context) error {
	return a.RunWithReady(ctx, nil)
}

// RunWithReady starts the gRPC server and invokes ready after the listener is bound so service managers can report accurate startup state.
// RunWithReady 用于启动 gRPC 服务，并在监听器绑定成功后调用 ready，让服务管理器可以报告准确的启动状态。
func (a *Application) RunWithReady(ctx context.Context, ready func()) error {
	return a.RunWithReadyAddress(ctx, func(_ string) error {
		if ready != nil {
			ready()
		}
		return nil
	})
}

// RunWithReadyAddress starts the gRPC server and reports the operating-system-resolved listener address.
// RunWithReadyAddress 用于启动 gRPC 服务，并回报由操作系统最终解析出的监听地址。
func (a *Application) RunWithReadyAddress(ctx context.Context, ready func(string) error) error {
	return a.RunWithReadyEndpoints(ctx, func(endpoints RuntimeEndpoints) error {
		if ready == nil {
			return nil
		}
		return ready(endpoints.GRPCListenAddr)
	})
}

// RunWithReadyEndpoints binds every required listener before reporting readiness and serving requests.
// RunWithReadyEndpoints 用于在回报就绪并开始处理请求前绑定每个必需监听器。
func (a *Application) RunWithReadyEndpoints(ctx context.Context, ready func(RuntimeEndpoints) error) error {
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

	// Bind the TCP listeners before starting either server so readiness always represents the complete required endpoint set.
	// 在启动任一服务前先绑定全部 TCP 监听器，确保就绪状态始终代表完整的必需端点集合。
	grpcListener, err := net.Listen("tcp", a.Config.GRPC.ListenAddr)
	if err != nil {
		return fmt.Errorf("bind grpc listener: %w", err)
	}
	var managementListener net.Listener
	if a.ManagementServer != nil {
		managementListener, err = net.Listen("tcp", a.Config.Management.ListenAddr)
		if err != nil {
			_ = grpcListener.Close()
			return fmt.Errorf("bind management listener: %w", err)
		}
	}
	endpoints := RuntimeEndpoints{GRPCListenAddr: grpcListener.Addr().String()}
	if managementListener != nil {
		endpoints.ManagementListenAddr = managementListener.Addr().String()
	}
	if ready != nil {
		if err := ready(endpoints); err != nil {
			_ = grpcListener.Close()
			if managementListener != nil {
				_ = managementListener.Close()
			}
			return fmt.Errorf("ready callback: %w", err)
		}
	}

	serverCount := 1
	if managementListener != nil {
		serverCount++
	}
	errCh := make(chan error, serverCount)
	a.Logger.Info("grpc server listening", "addr", endpoints.GRPCListenAddr)
	go func() {
		if serveErr := a.Server.Serve(grpcListener); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("serve grpc: %w", serveErr)
			return
		}
		errCh <- nil
	}()
	if managementListener != nil {
		a.Logger.Info("management server listening", "addr", endpoints.ManagementListenAddr)
		go func() {
			if serveErr := a.ManagementServer.Serve(managementListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve management http: %w", serveErr)
				return
			}
			errCh <- nil
		}()
	}

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
	var shutdownErrors []error

	// Only stop gRPC when the runtime actually finished wiring the server, so partial construction or test doubles can still reuse the shutdown path safely.
	// 只有在运行时确实完成 gRPC 服务装配时才执行停服，这样部分装配对象或测试替身也能安全复用 shutdown 路径。
	if a.Server != nil {
		grpcShutdownCtx, cancelGRPCShutdown := context.WithTimeout(ctx, timeout)
		stopped := make(chan struct{})
		go func() {
			a.Server.GracefulStop()
			close(stopped)
		}()
		select {
		case <-grpcShutdownCtx.Done():
			a.Server.Stop()
		case <-stopped:
		}
		cancelGRPCShutdown()
	}
	if a.ManagementServer != nil {
		managementTimeout := a.Config.Management.ShutdownTimeout.Duration
		if managementTimeout <= 0 {
			managementTimeout = config.DefaultLocal().Management.ShutdownTimeout.Duration
		}
		managementShutdownCtx, cancelManagementShutdown := context.WithTimeout(ctx, managementTimeout)
		if err := a.ManagementServer.Shutdown(managementShutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown management server: %w", err))
			if closeErr := a.ManagementServer.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("force close management server: %w", closeErr))
			}
		}
		cancelManagementShutdown()
	}

	// Continue draining every dependency even if one shutdown step fails, so later resources do not leak simply because an earlier adapter returned an error.
	// 即便某个关闭步骤失败，也继续释放后续依赖，避免前一个适配器报错后导致后面的资源直接泄漏。
	dependencyShutdownCtx, cancelDependencyShutdown := context.WithTimeout(ctx, timeout)
	defer cancelDependencyShutdown()
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
		if err := shutdowner.Shutdown(dependencyShutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown dependency[%d]: %w", i, err))
		}
	}
	if len(shutdownErrors) > 0 {
		return fmt.Errorf("shutdown completed with dependency errors: %w", errors.Join(shutdownErrors...))
	}
	return nil
}
