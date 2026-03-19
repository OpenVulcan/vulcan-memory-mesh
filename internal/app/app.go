package app

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"

	httpapi "github.com/openvulcan/vmm/internal/adapters/inbound/http"
	"github.com/openvulcan/vmm/internal/adapters/outbound/aliyun_dashscope"
	"github.com/openvulcan/vmm/internal/adapters/outbound/aliyun_dashvector"
	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/adapters/outbound/postgres_store"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/core/services"
	"github.com/openvulcan/vmm/internal/platform/xid"
)

type Application struct {
	Config    config.Config
	Logger    *log.Logger
	Router    *gin.Engine
	Server    *http.Server
	Shutdowns []ports.Shutdowner
}

func NewLocal(cfg config.Config) (*Application, error) { return newApplication(cfg, true) }
func NewSaaS(cfg config.Config) (*Application, error)  { return newApplication(cfg, false) }

func newApplication(cfg config.Config, local bool) (*Application, error) {
	logger := log.New(os.Stdout, "[vmm] ", log.LstdFlags|log.Lmicroseconds|log.LUTC)
	ids := xid.NewGenerator()

	llm, err := buildLLM(cfg)
	if err != nil { return nil, err }
	embedding, err := buildEmbedding(cfg)
	if err != nil { return nil, err }
	vector, err := buildVector(cfg)
	if err != nil { return nil, err }
	relational, err := buildRelational(cfg)
	if err != nil { return nil, err }

	persona := buildPersona(local)
	pre := services.NewPreCheckService(llm, embedding, vector, persona, logger, cfg.PreCheck.IntentTimeout.Duration, cfg.PreCheck.TopK, cfg.PreCheck.SimilarityThreshold)
	post := services.NewPostActionService(relational, logger)
	seed := services.NewSeedMemoryService(embedding, vector, ids, logger)

	extra := []gin.HandlerFunc{}
	enableSeed := local && cfg.Admin.SeedEnabled
	if !local {
		extra = append(extra, httpapi.AuthPlaceholderMiddleware(), httpapi.TenantPlaceholderMiddleware())
		enableSeed = false
	}
	router := httpapi.NewRouter(httpapi.Dependencies{
		IDs: ids, PreCheck: pre, PostAction: post, SeedMemory: seed, Logger: logger,
		PreCheckTimeout: cfg.HTTP.RequestTimeout.PreCheck.Duration,
		PostActionTimeout: cfg.HTTP.RequestTimeout.PostAction.Duration,
		SeedMemoryTimeout: cfg.HTTP.RequestTimeout.SeedMemory.Duration,
		EnableSeedRoute: enableSeed, ExtraMiddlewares: extra,
	})

	shutdowns := []ports.Shutdowner{}
	if relational != nil { shutdowns = append(shutdowns, relational) }
	if vector != nil { shutdowns = append(shutdowns, vector) }

	return &Application{
		Config: cfg,
		Logger: logger,
		Router: router,
		Server: &http.Server{Addr: cfg.HTTP.ListenAddr, Handler: router},
		Shutdowns: shutdowns,
	}, nil
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

func buildLLM(cfg config.Config) (ports.LLMClient, error) {
	switch strings.ToLower(cfg.LLM.Provider) {
	case "mock", "memory", "memory_mock":
		return memory_mock.NewLLMClient(), nil
	case "aliyun_dashscope":
		return aliyun_dashscope.NewLLMClient(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model), nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %s", cfg.LLM.Provider)
	}
}

func buildEmbedding(cfg config.Config) (ports.EmbeddingClient, error) {
	switch strings.ToLower(cfg.Embedding.Provider) {
	case "mock", "memory", "memory_mock":
		return memory_mock.NewEmbeddingClient(64), nil
	case "aliyun_dashscope":
		return aliyun_dashscope.NewEmbeddingClient(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.Model), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", cfg.Embedding.Provider)
	}
}

func buildVector(cfg config.Config) (ports.VectorStore, error) {
	switch strings.ToLower(cfg.Vector.Provider) {
	case "memory", "mock", "memory_mock":
		return memory_mock.NewVectorStore(), nil
	case "aliyun_dashvector":
		return aliyun_dashvector.NewStore(cfg.Vector.Endpoint, cfg.Vector.APIKey, cfg.Vector.Namespace, cfg.Vector.Collection), nil
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

func buildRelational(cfg config.Config) (ports.RelationalStore, error) {
	switch strings.ToLower(cfg.Relational.Provider) {
	case "memory", "mock", "memory_mock":
		return memory_mock.NewRelationalStore(), nil
	case "postgres":
		db, err := sql.Open("postgres", cfg.Relational.DSN)
		if err != nil { return nil, fmt.Errorf("open postgres db: %w", err) }
		return postgres_store.New(db), nil
	default:
		return nil, fmt.Errorf("unsupported relational provider: %s", cfg.Relational.Provider)
	}
}

func buildPersona(local bool) ports.ContextPersonaProvider {
	_ = local
	return memory_mock.NewPersonaProvider()
}
