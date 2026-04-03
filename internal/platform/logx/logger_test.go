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
	logger.Error("maintenance failed", "err", errors.New("sqlite prepare failed: resource deadlock would occur"))

	output := logBuf.String()
	if strings.Contains(output, "err：{}") {
		t.Fatalf("expected error message instead of empty JSON object, got %s", output)
	}
	if !strings.Contains(output, "TEXT(err)：\nsqlite prepare failed: resource deadlock would occur\n") {
		t.Fatalf("expected error string output, got %s", output)
	}
}

// TestLoggerLevelFiltersLowerSeverity verifies the configured runtime log level is truly enforced so release environments can suppress info and warn chatter while still keeping error logs.
// TestLoggerLevelFiltersLowerSeverity 用于验证运行时日志级别配置会被真实执行，让 release 环境可以压制 info 和 warn 噪声，同时保留 error 日志。
func TestLoggerLevelFiltersLowerSeverity(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := New(logBuf, Config{Level: "error", Format: "text"})

	logger.Info("info should be dropped")
	logger.Warn("warn should be dropped")
	logger.Error("error should stay visible")

	output := logBuf.String()
	if strings.Contains(output, "info should be dropped") || strings.Contains(output, "warn should be dropped") {
		t.Fatalf("expected error level to suppress lower severities, got %s", output)
	}
	if !strings.Contains(output, "error should stay visible") {
		t.Fatalf("expected error level to keep error logs, got %s", output)
	}
}

// TestLoggerPayloadDebugFlagPropagates verifies the shared payload-debug switch survives child logger creation so business layers can consistently decide whether to log full payloads.
// TestLoggerPayloadDebugFlagPropagates 用于验证共享 payload 调试开关会在子日志器之间继承，确保业务层能够稳定判断是否输出完整载荷。
func TestLoggerPayloadDebugFlagPropagates(t *testing.T) {
	logger := New(&bytes.Buffer{}, Config{Level: "info", Format: "text", DebugPayloads: true}).With("component", "grpcapi")

	if !logger.PayloadDebugEnabled() {
		t.Fatal("expected payload debug flag to propagate through child logger")
	}
}

// TestLoggerPayloadProtectionFlagPropagates verifies the shared protected-payload switch survives child logger creation so business layers can safely add encrypted payload snapshots without re-reading config.
// TestLoggerPayloadProtectionFlagPropagates 用于验证共享受保护载荷开关会在子日志器之间继承，确保业务层无需重新读取配置也能安全追加加密载荷快照。
func TestLoggerPayloadProtectionFlagPropagates(t *testing.T) {
	logger := New(&bytes.Buffer{}, Config{
		Level:                "info",
		Format:               "text",
		ProtectPayloads:      true,
		PayloadEncryptionKey: "0123456789abcdef0123456789abcdef",
	}).With("component", "grpcapi")

	if !logger.PayloadProtectionEnabled() {
		t.Fatal("expected payload protection flag to propagate through child logger")
	}
}

// TestLoggerAppendPayloadFieldsEncryptsPayloadWhenDebugIsDisabled verifies payload fields become encrypted JSON envelopes when plaintext debug logging is off but protected-payload logging is enabled.
// TestLoggerAppendPayloadFieldsEncryptsPayloadWhenDebugIsDisabled 用于验证当明文调试关闭且受保护载荷日志开启时，载荷字段会被加密成 JSON 信封输出。
func TestLoggerAppendPayloadFieldsEncryptsPayloadWhenDebugIsDisabled(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := New(logBuf, Config{
		Level:                "info",
		Format:               "text",
		ProtectPayloads:      true,
		PayloadEncryptionKey: "0123456789abcdef0123456789abcdef",
	})

	fields := logger.AppendPayloadFields([]any{"trace_id", "trc_123"}, "stage_payload", map[string]any{
		"user_content": "用户的真实问题",
		"queries":      []string{"为什么这样改"},
	})
	logger.Info("pre-check intent analyzed", fields...)

	output := logBuf.String()
	if strings.Contains(output, "用户的真实问题") || strings.Contains(output, "为什么这样改") {
		t.Fatalf("expected protected payload logs to hide plaintext, got %s", output)
	}
	if !strings.Contains(output, "JSON(stage_payload_protected)：") ||
		!strings.Contains(output, "\"algorithm\": \"AES-256-GCM\"") ||
		!strings.Contains(output, "\"version\": \"v1\"") ||
		!strings.Contains(output, "\"key_id\":") ||
		!strings.Contains(output, "\"ciphertext\":") {
		t.Fatalf("expected protected payload envelope in logs, got %s", output)
	}
}
