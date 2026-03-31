// logger_test.go verifies the custom multiline text logger keeps long text and JSON payloads readable during debugging.
// logger_test.go 用于验证自定义多行文本日志器会把长文本和 JSON 载荷以易读形式输出，方便调试。
package logx

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestTextLoggerFormatsJSONAndLongText verifies JSON string fields become pretty-printed blocks and long text stays unescaped.
// TestTextLoggerFormatsJSONAndLongText 用于验证 JSON 字符串字段会被格式化展开，长文本也会以无转义形式直接显示。
func TestTextLoggerFormatsJSONAndLongText(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := New(logBuf, Config{Level: "info", Format: "text"})

	// Emit one realistic post-action style record so the assertions cover both TEXT and JSON blocks in one log entry.
	// 输出一条接近 post-action 的真实记录，让断言同时覆盖 TEXT 和 JSON 两种块状输出。
	logger.Info(
		"post-action turn analysis result",
		"trace_id", "trc_123",
		"user_content", "第一行\n第二行",
		"analysis", `{"Details":"abc","MemoryNodes":[{"Category":4,"Abstract":"hello"}]}`,
		"vector_count", 2,
	)

	output := logBuf.String()
	if !strings.Contains(output, "========================================") {
		t.Fatalf("expected record separator, got %s", output)
	}
	if !strings.Contains(output, "TYPE：\"INFO\"") {
		t.Fatalf("expected TYPE header, got %s", output)
	}
	if !strings.Contains(output, "MSG：\"post-action turn analysis result\"") {
		t.Fatalf("expected MSG header, got %s", output)
	}
	if !strings.Contains(output, "trace_id：\"trc_123\"") {
		t.Fatalf("expected scalar field output, got %s", output)
	}
	if !strings.Contains(output, "TEXT(user_content)：\n第一行\n第二行\n") {
		t.Fatalf("expected multiline text block without escapes, got %s", output)
	}
	if !strings.Contains(output, "JSON(analysis)：\n{") ||
		!strings.Contains(output, "\"Details\": \"abc\"") ||
		!strings.Contains(output, "\"MemoryNodes\": [") ||
		!strings.Contains(output, "\"Category\": 4") ||
		!strings.Contains(output, "\"Abstract\": \"hello\"") {
		t.Fatalf("expected pretty JSON block without escaped quotes, got %s", output)
	}
	if !strings.Contains(output, "vector_count：2") {
		t.Fatalf("expected numeric scalar output, got %s", output)
	}
}

// TestTextLoggerPreservesWithAttrs verifies child loggers created by With still emit inherited attrs in the multiline format.
// TestTextLoggerPreservesWithAttrs 用于验证通过 With 创建的子日志器仍会在多行格式里输出继承字段。
func TestTextLoggerPreservesWithAttrs(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := New(logBuf, Config{Level: "info", Format: "text"}).With("component", "grpcapi")

	// Emit one small record to prove inherited attrs remain visible even after the formatter switched away from slog's default text handler.
	// 输出一条简短日志，证明切换自定义格式器后，继承属性仍然可见。
	logger.Info("grpc request", "code", "OK")

	output := logBuf.String()
	if !strings.Contains(output, "component：\"grpcapi\"") {
		t.Fatalf("expected inherited With attribute, got %s", output)
	}
	if !strings.Contains(output, "code：\"OK\"") {
		t.Fatalf("expected record attribute, got %s", output)
	}
}

// TestTextLoggerRendersErrorsAsMessages verifies error values are rendered through Error() instead of being marshaled into empty JSON objects.
// TestTextLoggerRendersErrorsAsMessages 用于验证 error 值会通过 Error() 文本输出，而不是被错误地序列化成空 JSON 对象。
func TestTextLoggerRendersErrorsAsMessages(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := New(logBuf, Config{Level: "info", Format: "text"})

	// Emit one realistic error value so the formatter proves it keeps the original error message visible for operator troubleshooting.
	// 输出一条真实 error 值，验证格式器会保留原始错误文本，便于运维定位问题。
	logger.Error("maintenance failed", "err", errors.New("duckdb prepare failed: resource deadlock would occur"))

	output := logBuf.String()
	if strings.Contains(output, "err：{}") {
		t.Fatalf("expected error message instead of empty JSON object, got %s", output)
	}
	if !strings.Contains(output, "TEXT(err)：\nduckdb prepare failed: resource deadlock would occur\n") {
		t.Fatalf("expected error string output, got %s", output)
	}
}
