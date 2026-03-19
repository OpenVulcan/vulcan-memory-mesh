package httpapi

import (
	"log"
	nethttp "net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

const traceIDKey = "trace_id"

func TraceIDMiddleware(ids ports.IDGenerator) gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" { traceID = ids.NewID("trc") }
		ctx := trace.WithTraceID(c.Request.Context(), traceID)
		c.Request = c.Request.WithContext(ctx)
		c.Set(traceIDKey, traceID)
		c.Header("X-Trace-ID", traceID)
		c.Next()
	}
}

func RecoveryMiddleware(logger *log.Logger) gin.HandlerFunc {
	if logger == nil { logger = log.Default() }
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Printf("panic recovered trace_id=%s err=%v", traceIDFromContext(c), rec)
				c.AbortWithStatusJSON(nethttp.StatusInternalServerError, Envelope{Code: nethttp.StatusInternalServerError, Msg: "internal server error", TraceID: traceIDFromContext(c)})
			}
		}()
		c.Next()
	}
}

func RequestLoggerMiddleware(logger *log.Logger) gin.HandlerFunc {
	if logger == nil { logger = log.Default() }
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Printf("trace_id=%s method=%s path=%s status=%d latency=%s client_ip=%s", traceIDFromContext(c), c.Request.Method, c.FullPath(), c.Writer.Status(), time.Since(start), c.ClientIP())
	}
}

func AuthPlaceholderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if token := c.GetHeader("Authorization"); token != "" { c.Set("auth_token", token) }
		c.Next()
	}
}

func TenantPlaceholderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if tenantID := c.GetHeader("X-Tenant-ID"); tenantID != "" { c.Set("tenant_id", tenantID) }
		c.Next()
	}
}

func traceIDFromContext(c *gin.Context) string {
	if v, ok := c.Get(traceIDKey); ok {
		if traceID, ok := v.(string); ok { return traceID }
	}
	return trace.IDFromContext(c.Request.Context())
}
