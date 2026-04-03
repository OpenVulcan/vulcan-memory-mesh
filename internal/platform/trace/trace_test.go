// trace_test.go verifies the shared trace helpers stay safe when direct tests or manual integrations pass incomplete request contexts.
// trace_test.go 用于验证共享 trace 辅助逻辑在直接测试或手工集成传入不完整请求上下文时仍然安全。
package trace

import "testing"

// TestWithTraceIDAllowsNilContext verifies the trace helper falls back to a background context instead of panicking on a nil parent context.
// TestWithTraceIDAllowsNilContext 用于验证 trace helper 在父 context 为 nil 时会回退到 background context，而不是直接 panic。
func TestWithTraceIDAllowsNilContext(t *testing.T) {
	ctx := WithTraceID(nil, "trace-z")
	if got := IDFromContext(ctx); got != "trace-z" {
		t.Fatalf("trace id = %q", got)
	}
}
