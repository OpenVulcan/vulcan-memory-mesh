// middleware.go implements the inbound HTTP adapter layer.
// middleware.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// Middleware represents one reusable HTTP wrapper in the inbound request pipeline.
// Middleware 用于表示入站请求链路中的一个可复用 HTTP 包装器。
type Middleware func(http.Handler) http.Handler

// contextKey is the private key type used to stash adapter-only values in request contexts.
// contextKey 用于作为私有键类型，把适配层内部值安全存入请求上下文。
type contextKey string

// Constants centralize HTTP adapter keys that must stay consistent across middleware and handlers.
// Constants 用于集中声明 HTTP 适配层在中间件和处理器之间共享的关键常量。
const (
	traceIDHeader         = "X-Trace-ID"
	requestBodyContextKey = contextKey("request_body")
)

// statusRecorder wraps ResponseWriter so the logger can observe the final response status code.
// statusRecorder 用于包装 ResponseWriter，让日志层能够观察最终响应状态码。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader executes the WriteHeader logic.
// WriteHeader 用于执行 WriteHeader 逻辑。
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// TraceIDMiddleware executes the TraceIDMiddleware logic.
// TraceIDMiddleware 用于执行 TraceIDMiddleware 逻辑。
func TraceIDMiddleware(ids appports.IDGenerator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reuse the inbound trace ID when present, otherwise generate one for this request.
			// 有上游 trace ID 时直接复用，否则为当前请求生成新的 trace ID。
			traceID := strings.TrimSpace(r.Header.Get(traceIDHeader))
			if traceID == "" && ids != nil {
				traceID = ids.NewID("trc")
			}
			ctx := trace.WithTraceID(r.Context(), traceID)
			if traceID != "" {
				w.Header().Set(traceIDHeader, traceID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RecoveryMiddleware executes the RecoveryMiddleware logic.
// RecoveryMiddleware 用于执行 RecoveryMiddleware 逻辑。
func RecoveryMiddleware(logger *log.Logger) Middleware {
	if logger == nil {
		logger = log.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Convert unexpected panics into a stable 500 response with trace context.
			// 将未预期的 panic 转成携带 trace 上下文的稳定 500 响应。
			defer func() {
				if rec := recover(); rec != nil {
					logger.Printf("panic recovered trace_id=%s err=%v", trace.IDFromContext(r.Context()), rec)
					writeJSON(w, http.StatusInternalServerError, Envelope{Code: http.StatusInternalServerError, Msg: "internal server error", TraceID: trace.IDFromContext(r.Context())})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLoggerMiddleware executes the RequestLoggerMiddleware logic.
// RequestLoggerMiddleware 用于执行 RequestLoggerMiddleware 逻辑。
func RequestLoggerMiddleware(logger *log.Logger) Middleware {
	if logger == nil {
		logger = log.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture request metadata and optionally compact the POST body for diagnostics.
			// 采集请求元信息，并在需要时压缩 POST 请求体用于诊断。
			start := time.Now()
			body, req := captureRequestBody(r)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, req)
			if strings.EqualFold(req.Method, http.MethodPost) && body != "" {
				logger.Printf("trace_id=%s method=%s path=%s status=%d latency=%s client_ip=%s request_body=%s", trace.IDFromContext(req.Context()), req.Method, req.URL.Path, rec.status, time.Since(start), clientIP(req), body)
				return
			}
			logger.Printf("trace_id=%s method=%s path=%s status=%d latency=%s client_ip=%s", trace.IDFromContext(req.Context()), req.Method, req.URL.Path, rec.status, time.Since(start), clientIP(req))
		})
	}
}

// captureRequestBody executes the captureRequestBody logic.
// captureRequestBody 用于执行 captureRequestBody 逻辑。
func captureRequestBody(r *http.Request) (string, *http.Request) {
	// Read the request body once and reattach it so downstream handlers can read it again.
	// 读取一次请求体并重新挂回去，确保下游处理器仍可继续读取。
	if r == nil || r.Body == nil || !strings.EqualFold(r.Method, http.MethodPost) {
		return "", r
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(nil))
		return "<read_body_error>", r
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	body := compactBody(raw)
	ctx := context.WithValue(r.Context(), requestBodyContextKey, body)
	return body, r.WithContext(ctx)
}

// compactBody executes the compactBody logic.
// compactBody 用于执行 compactBody 逻辑。
func compactBody(raw []byte) string {
	// Compact valid JSON for logs and fall back to the raw trimmed payload otherwise.
	// 对合法 JSON 做压缩日志输出，否则退回原始裁剪后的载荷。
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

// clientIP executes the clientIP logic.
// clientIP 用于执行 clientIP 逻辑。
func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if rip := strings.TrimSpace(r.Header.Get("X-Real-IP")); rip != "" {
		return rip
	}
	return r.RemoteAddr
}
