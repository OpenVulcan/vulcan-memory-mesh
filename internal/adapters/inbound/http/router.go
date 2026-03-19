package httpapi

import (
	"log"
	nethttp "net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/core/services"
)

type Dependencies struct {
	IDs               ports.IDGenerator
	PreCheck          *services.PreCheckService
	PostAction        *services.PostActionService
	SeedMemory        *services.SeedMemoryService
	Logger            *log.Logger
	PreCheckTimeout   time.Duration
	PostActionTimeout time.Duration
	SeedMemoryTimeout time.Duration
	EnableSeedRoute   bool
	ExtraMiddlewares  []gin.HandlerFunc
}

func NewRouter(deps Dependencies) *gin.Engine {
	engine := gin.New()
	engine.Use(RecoveryMiddleware(deps.Logger), TraceIDMiddleware(deps.IDs), RequestLoggerMiddleware(deps.Logger))
	if len(deps.ExtraMiddlewares) > 0 { engine.Use(deps.ExtraMiddlewares...) }

	handler := NewHandler(deps.PreCheck, deps.PostAction, deps.SeedMemory, deps.PreCheckTimeout, deps.PostActionTimeout, deps.SeedMemoryTimeout, deps.Logger)

	engine.GET("/healthz", handler.Healthz)
	v1 := engine.Group("/v1")
	chat := v1.Group("/chat")
	chat.POST("/pre-check", handler.PreCheck)
	chat.POST("/post-action", handler.PostAction)
	if deps.EnableSeedRoute && deps.SeedMemory != nil {
		admin := v1.Group("/admin")
		admin.POST("/seed-memory", handler.SeedMemory)
	}
	engine.NoRoute(func(c *gin.Context) {
		c.AbortWithStatusJSON(nethttp.StatusNotFound, Envelope{Code: nethttp.StatusNotFound, Msg: "not found", TraceID: traceIDFromContext(c)})
	})
	return engine
}
