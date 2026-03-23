// app.go implements the application composition root.
// app.go 用于实现应用组合根。
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	httpapi "github.com/openvulcan/vmm/internal/adapters/inbound/http"
	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	"github.com/openvulcan/vmm/internal/adapters/outbound/sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/pii"
	"github.com/openvulcan/vmm/internal/platform/xid"
)

// Application holds the fully wired local runtime, including the HTTP server and shutdown hooks.
// Application 用于持有完整装配后的本地运行时，包括 HTTP 服务和关闭钩子。
type Application struct {
	Config    config.Config
	Logger    *logx.Logger
	Handler   http.Handler
	Server    *http.Server
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
	noiseGate, err := buildNoiseGate(cfg, layout, embedding, logger)
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

	// Wire HTTP handlers and shutdown dependencies into the application container.
	// 将 HTTP 处理器和关闭依赖接入应用容器。
	deps := httpapi.Dependencies{
		IDs:                 ids,
		Chat:                chat,
		PreCheck:            pre,
		PostAction:          post,
		SeedMemory:          seed,
		Logger:              logger,
		Validator:           httpapi.NewRequestValidator(cfg.PostAction.InputMode),
		ChatTimeout:         cfg.HTTP.RequestTimeout.Chat.Duration,
		PreCheckTimeout:     cfg.HTTP.RequestTimeout.PreCheck.Duration,
		PostActionTimeout:   cfg.HTTP.RequestTimeout.PostAction.Duration,
		SeedMemoryTimeout:   cfg.HTTP.RequestTimeout.SeedMemory.Duration,
		MaxRequestBodyBytes: cfg.HTTP.MaxRequestBodyBytes,
		LogRequestBodies:    cfg.Logging.LogRequestBodies,
		EnableSeedRoute:     enableSeed,
	}
	handler := httpapi.NewRouter(deps)
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
	server := &http.Server{Addr: cfg.HTTP.ListenAddr, Handler: handler}
	return &Application{Config: cfg, Logger: logger, Handler: handler, Server: server, Shutdowns: shutdowns}, nil
}

// Run executes the Run logic.
// Run 用于执行 Run 逻辑。
func (a *Application) Run(ctx context.Context) error {
	// Start the HTTP server asynchronously so shutdown signals can be observed.
	// 异步启动 HTTP 服务，以便同时监听关闭信号。
	errCh := make(chan error, 1)
	go func() {
		protocol := "http"
		var err error
		if a.Config.HTTP.TLS.Enabled {
			protocol = "https"
			a.Logger.Info("http server listening", "protocol", protocol, "addr", a.Server.Addr, "tls_cert_file", a.Config.HTTP.TLS.CertFile)
			err = a.Server.ListenAndServeTLS(a.Config.HTTP.TLS.CertFile, a.Config.HTTP.TLS.KeyFile)
		} else {
			a.Logger.Info("http server listening", "protocol", protocol, "addr", a.Server.Addr)
			err = a.Server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
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
	// Stop accepting new HTTP requests before tearing down downstream resources.
	// 先停止接收新的 HTTP 请求，再销毁下游资源。
	shutdownCtx, cancel := context.WithTimeout(ctx, a.Config.HTTP.ShutdownTimeout.Duration)
	defer cancel()
	if err := a.Server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
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
		return openai_native.NewLLMClient(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.Organization, cfg.LLM.Project), nil
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
		return openai_native.NewEmbeddingClient(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimension, cfg.Embedding.Organization, cfg.Embedding.Project), nil
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
	case "sqlite":
		return sqlite.NewStore(resolveRuntimePath(cfg.Archive.Path))
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
func buildNoiseGate(cfg config.Config, layout config.PromptLayout, embedding appports.EmbeddingClient, logger *logx.Logger) (*processor.NoiseGate, error) {
	return processor.NewNoiseGate(context.Background(), embedding, logger, processor.NoiseGateConfig{
		SystemDir:         layout.SystemNoiseRulesDir(),
		UserDir:           layout.UserNoiseRulesDir(),
		DefaultLanguage:   cfg.Noise.DefaultLanguage,
		Enabled:           cfg.Noise.Enabled,
		SemanticEnabled:   cfg.Noise.SemanticEnabled,
		SemanticThreshold: cfg.Noise.SemanticThreshold,
		Model:             cfg.Embedding.Model,
		Dimension:         cfg.Embedding.Dimension,
	})
}

// resolveRuntimePath resolves one relative runtime path against the executable directory.
// resolveRuntimePath 用于把运行时相对路径解析到可执行文件所在目录。
func resolveRuntimePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return path
	}
	exePath, err := os.Executable()
	if err != nil {
		return filepath.Clean(path)
	}
	exeDir := filepath.Dir(exePath)
	return filepath.Clean(filepath.Join(exeDir, path))
}
