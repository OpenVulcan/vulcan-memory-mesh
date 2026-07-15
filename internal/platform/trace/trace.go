// trace.go implements shared platform helpers.
// trace.go 用于实现共享平台辅助能力。
package trace

import "context"

// contextKey is the private context key type used to avoid collisions with other packages.
// contextKey 用于作为私有上下文键类型，避免与其他包发生键冲突。
type contextKey string

// traceIDKey is the concrete key used to store trace ids inside request contexts.
// traceIDKey 用于表示在请求上下文中存储 trace id 的具体键。
const traceIDKey contextKey = "trace_id"

// WithTraceID stores one trace id in the current request context and falls back to context.Background when direct tests or manual integrations pass a nil context.
// WithTraceID 用于把 trace id 写入当前请求上下文，并在直接测试或手工集成传入 nil context 时回退到 context.Background。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceIDKey, traceID)
}

// IDFromContext returns the trace identifier stored on a context, or an empty string when none is present.
// IDFromContext 用于返回上下文中保存的追踪标识；不存在时返回空字符串。
func IDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}
