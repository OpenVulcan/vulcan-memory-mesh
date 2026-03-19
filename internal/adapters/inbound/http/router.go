// router.go implements the inbound HTTP adapter layer.
// router.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"log"
	"net/http"
	"time"

	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// Dependencies collects everything the HTTP router needs to wire routes and middleware.
// Dependencies 用于收集 HTTP 路由装配所需的全部依赖和中间件配置。
type Dependencies struct {
	IDs               interface{ NewID(prefix string) string }
	PreCheck          usecase.PreCheckExecutor
	PostAction        usecase.PostActionExecutor
	SeedMemory        usecase.SeedMemoryExecutor
	Logger            *log.Logger
	PreCheckTimeout   time.Duration
	PostActionTimeout time.Duration
	SeedMemoryTimeout time.Duration
	EnableSeedRoute   bool
	ExtraMiddlewares  []Middleware
}

// NewRouter creates a Router instance.
// NewRouter 用于创建 Router 实例。
func NewRouter(deps Dependencies) http.Handler {
	handler := NewHandler(deps.PreCheck, deps.PostAction, deps.SeedMemory, deps.PreCheckTimeout, deps.PostActionTimeout, deps.SeedMemoryTimeout, deps.Logger)
	mux := http.NewServeMux()
	mux.Handle("/healthz", methodHandler(http.MethodGet, http.HandlerFunc(handler.Healthz)))
	mux.Handle("/v1/chat/pre-check", methodHandler(http.MethodPost, http.HandlerFunc(handler.PreCheck)))
	mux.Handle("/v1/chat/post-action", methodHandler(http.MethodPost, http.HandlerFunc(handler.PostAction)))
	if deps.EnableSeedRoute && deps.SeedMemory != nil {
		mux.Handle("/v1/admin/seed-memory", methodHandler(http.MethodPost, http.HandlerFunc(handler.SeedMemory)))
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, Envelope{Code: http.StatusNotFound, Msg: "not found", TraceID: traceIDFromContext(r)})
	}))

	middlewares := []Middleware{
		RecoveryMiddleware(deps.Logger),
		TraceIDMiddleware(deps.IDs),
		RequestLoggerMiddleware(deps.Logger),
	}
	middlewares = append(middlewares, deps.ExtraMiddlewares...)
	return chain(mux, middlewares...)
}

// methodHandler executes the methodHandler logic.
// methodHandler 用于执行 methodHandler 逻辑。
func methodHandler(method string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, Envelope{Code: http.StatusMethodNotAllowed, Msg: "method not allowed", TraceID: traceIDFromContext(r)})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chain executes the chain logic.
// chain 用于执行 chain 逻辑。
func chain(next http.Handler, middlewares ...Middleware) http.Handler {
	wrapped := next
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middlewares[i] == nil {
			continue
		}
		wrapped = middlewares[i](wrapped)
	}
	return wrapped
}

// traceIDFromContext executes the traceIDFromContext logic.
// traceIDFromContext 用于执行 traceIDFromContext 逻辑。
func traceIDFromContext(r *http.Request) string {
	if r == nil {
		return ""
	}
	return trace.IDFromContext(r.Context())
}
