// app.go implements the thin application composition root for the local gRPC runtime.
// app.go 用于实现本地 gRPC 运行时的薄型应用组合根。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"google.golang.org/grpc"
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

// newApplication composes the runtime dependencies for the local gRPC server while delegating adapter, pipeline, and transport construction to focused builders.
// newApplication 用于为本地 gRPC 服务装配运行时依赖，并把适配器、pipeline 与传输层构建委派给更聚焦的 builder。
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
	shutdowns := buildUniqueShutdownSequence(fileWriter, llmOutputShutdowner, storageCaps.Relational, storageCaps.Vector, useCases.PostAction, useCases.Retention)
	initSucceeded = true
	return &Application{
		Config:    cfg,
		Logger:    logger,
		Server:    server,
		Shutdowns: shutdowns,
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
		if ready != nil {
			ready()
		}
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
