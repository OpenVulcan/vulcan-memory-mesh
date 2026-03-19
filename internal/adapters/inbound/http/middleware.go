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

type Middleware func(http.Handler) http.Handler

type contextKey string

const (
	traceIDHeader         = "X-Trace-ID"
	requestBodyContextKey = contextKey("request_body")
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func TraceIDMiddleware(ids appports.IDGenerator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func RecoveryMiddleware(logger *log.Logger) Middleware {
	if logger == nil {
		logger = log.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func RequestLoggerMiddleware(logger *log.Logger) Middleware {
	if logger == nil {
		logger = log.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func captureRequestBody(r *http.Request) (string, *http.Request) {
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
