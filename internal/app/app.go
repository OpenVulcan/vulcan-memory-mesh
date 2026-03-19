package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	httpapi "github.com/openvulcan/vmm/internal/adapters/inbound/http"
	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/xid"
)

type Application struct {
	Config    config.Config
	Logger    *log.Logger
	Handler   http.Handler
	Server    *http.Server
	Shutdowns []appports.Shutdowner
}

func NewLocal(cfg config.Config, prompts appports.PromptSource) (*Application, error) {
	return newApplication(cfg, prompts)
}

func newApplication(cfg config.Config, prompts appports.PromptSource) (*Application, error) {
	logger := log.New(os.Stdout, "[vmm] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)
	ids := xid.NewGenerator()
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
	persona := buildPersona()
	pre := usecase.NewPreCheckUseCase(processor.NewIntentExtractor(llm, prompts, cfg.LLM.Model, cfg.MemoryPipeline.MaxSearchKeywords), processor.NewContextAssembler(prompts, cfg.LLM.Model), embedding, vector, persona, logger, cfg.PreCheck.IntentTimeout.Duration, cfg.PreCheck.TopK, cfg.MemoryPipeline.MaxSearchKeywords, cfg.MemoryPipeline.MinSimilarityScore, cfg.Embedding.Model, cfg.Embedding.Dimension)
	post := usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), relational, logger)
	seed := usecase.NewSeedMemoryUseCase(embedding, vector, ids, logger, cfg.Embedding.Model, cfg.Embedding.Dimension)
	enableSeed := cfg.Admin.SeedEnabled
	deps := httpapi.Dependencies{IDs: ids, PreCheck: pre, PostAction: post, SeedMemory: seed, Logger: logger, PreCheckTimeout: cfg.HTTP.RequestTimeout.PreCheck.Duration, PostActionTimeout: cfg.HTTP.RequestTimeout.PostAction.Duration, SeedMemoryTimeout: cfg.HTTP.RequestTimeout.SeedMemory.Duration, EnableSeedRoute: enableSeed}
	handler := httpapi.NewRouter(deps)
	shutdowns := []appports.Shutdowner{}
	if relational != nil {
		shutdowns = append(shutdowns, relational)
	}
	if vector != nil {
		shutdowns = append(shutdowns, vector)
	}
	server := &http.Server{Addr: cfg.HTTP.ListenAddr, Handler: handler}
	return &Application{Config: cfg, Logger: logger, Handler: handler, Server: server, Shutdowns: shutdowns}, nil
}

func (a *Application) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		a.Logger.Printf("http server listening on %s", a.Server.Addr)
		if err := a.Server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
		a.Logger.Printf("received signal=%s, starting graceful shutdown", sig.String())
		return a.Shutdown(context.Background())
	case err := <-errCh:
		return err
	}
}
func (a *Application) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, a.Config.HTTP.ShutdownTimeout.Duration)
	defer cancel()
	if err := a.Server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	for i := len(a.Shutdowns) - 1; i >= 0; i-- {
		if err := a.Shutdowns[i].Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dependency[%d]: %w", i, err)
		}
	}
	return nil
}

func buildLLM(cfg config.Config) (appports.LLMClient, error) {
	switch strings.ToLower(cfg.LLM.Provider) {
	case "mock", "memory", "memory_mock":
		return memory_mock.NewLLMClient(), nil
	case "openai", "openai_native", "openai_go":
		return openai_native.NewLLMClient(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model, cfg.LLM.Organization, cfg.LLM.Project), nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", cfg.LLM.Provider)
	}
}
func buildEmbedding(cfg config.Config) (appports.EmbeddingClient, error) {
	switch strings.ToLower(cfg.Embedding.Provider) {
	case "mock", "memory", "memory_mock":
		return memory_mock.NewEmbeddingClient(cfg.Embedding.Dimension), nil
	case "openai", "openai_native", "openai_go":
		return openai_native.NewEmbeddingClient(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimension, cfg.Embedding.Organization, cfg.Embedding.Project), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Embedding.Provider)
	}
}
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	switch strings.ToLower(cfg.Vector.Provider) {
	case "memory", "mock", "memory_mock":
		return memory_mock.NewVectorStore(), nil
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	switch strings.ToLower(cfg.Relational.Provider) {
	case "", "memory", "mock", "memory_mock":
		return memory_mock.NewRelationalStore(), nil
	default:
		return nil, fmt.Errorf("unsupported relational provider: %s", cfg.Relational.Provider)
	}
}
func buildPersona() appports.ContextPersonaProvider {
	return memory_mock.NewPersonaProvider()
}
