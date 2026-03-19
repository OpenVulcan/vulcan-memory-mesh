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

// WithTraceID executes the WithTraceID logic.
// WithTraceID 用于执行 WithTraceID 逻辑。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// IDFromContext executes the IDFromContext logic.
// IDFromContext 用于执行 IDFromContext 逻辑。
func IDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}
