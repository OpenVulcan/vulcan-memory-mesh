package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	nethttp "net/http"
	"strings"
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
		requestBody := captureRequestBody(c)
		c.Next()
		if strings.EqualFold(c.Request.Method, nethttp.MethodPost) && requestBody != "" {
			logger.Printf("trace_id=%s method=%s path=%s status=%d latency=%s client_ip=%s request_body=%s", traceIDFromContext(c), c.Request.Method, c.FullPath(), c.Writer.Status(), time.Since(start), c.ClientIP(), requestBody)
			return
		}
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

func captureRequestBody(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return ""
	}
	if !strings.EqualFold(c.Request.Method, nethttp.MethodPost) {
		return ""
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Request.Body = io.NopCloser(bytes.NewReader(nil))
		return "<read_body_error>"
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return compactBody(raw)
}

func compactBody(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err == nil {
		return buf.String()
	}
	return trimmed
}
