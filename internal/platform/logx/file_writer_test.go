// file_writer_test.go verifies the runtime file log writer keeps the expected day/hour directory structure and rotates safely across hour boundaries.
// file_writer_test.go 用于验证运行时文件日志写入器会保持预期的按天/小时目录结构，并在跨小时后安全滚动。
package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHourlyFileWriterCreatesDayAndHourLog verifies the writer eagerly creates the current day directory and hourly log file so runtime startup can immediately fail on invalid paths instead of silently dropping logs later.
// TestHourlyFileWriterCreatesDayAndHourLog 用于验证写入器会在启动时立即创建当天目录和当前小时日志文件，让运行时在路径异常时尽早失败，而不是之后静默丢日志。
func TestHourlyFileWriterCreatesDayAndHourLog(t *testing.T) {
	now := time.Date(2026, 4, 3, 13, 25, 0, 0, time.FixedZone("CST", 8*3600))
	rootDir := filepath.Join(t.TempDir(), "logs")

	writer, err := newHourlyFileWriter(rootDir, func() time.Time { return now })
	if err != nil {
		t.Fatalf("new hourly file writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	if _, err := writer.Write([]byte("hello log\n")); err != nil {
		t.Fatalf("write log: %v", err)
	}

	logPath := filepath.Join(rootDir, "20260403", "2026040313.log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(body), "hello log") {
		t.Fatalf("expected written body in log file, got %q", string(body))
	}
}

// TestHourlyFileWriterRotatesAcrossHourBoundary verifies a later write after the local hour changes lands in the next hourly file without overwriting the previous bucket.
// TestHourlyFileWriterRotatesAcrossHourBoundary 用于验证本地小时变化后的后续写入会进入下一小时文件，并且不会覆盖前一个小时桶。
func TestHourlyFileWriterRotatesAcrossHourBoundary(t *testing.T) {
	current := time.Date(2026, 4, 3, 13, 59, 0, 0, time.FixedZone("CST", 8*3600))
	rootDir := filepath.Join(t.TempDir(), "logs")
	writer, err := newHourlyFileWriter(rootDir, func() time.Time { return current })
	if err != nil {
		t.Fatalf("new hourly file writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	if _, err := writer.Write([]byte("hour 13\n")); err != nil {
		t.Fatalf("write hour 13: %v", err)
	}
	current = current.Add(time.Hour)
	if _, err := writer.Write([]byte("hour 14\n")); err != nil {
		t.Fatalf("write hour 14: %v", err)
	}

	firstPath := filepath.Join(rootDir, "20260403", "2026040313.log")
	secondPath := filepath.Join(rootDir, "20260403", "2026040314.log")

	firstBody, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatalf("read first log file: %v", err)
	}
	secondBody, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatalf("read second log file: %v", err)
	}
	if !strings.Contains(string(firstBody), "hour 13") {
		t.Fatalf("expected first-hour log body, got %q", string(firstBody))
	}
	if !strings.Contains(string(secondBody), "hour 14") {
		t.Fatalf("expected second-hour log body, got %q", string(secondBody))
	}
}

// TestHourlyPrefixedFileWriterUsesPrefixedHourlyFile verifies prefixed writers keep the same day bucket layout while materializing the hourly file name with the requested prefix.
// TestHourlyPrefixedFileWriterUsesPrefixedHourlyFile 用于验证带前缀的写入器会保持同样的按天目录布局，并把小时日志文件名改成请求的前缀格式。
func TestHourlyPrefixedFileWriterUsesPrefixedHourlyFile(t *testing.T) {
	now := time.Date(2026, 4, 3, 13, 25, 0, 0, time.FixedZone("CST", 8*3600))
	rootDir := filepath.Join(t.TempDir(), "logs")

	writer, err := newHourlyFileWriterWithPrefix(rootDir, "LLM-", func() time.Time { return now })
	if err != nil {
		t.Fatalf("new prefixed hourly file writer: %v", err)
	}
	defer func() { _ = writer.Close() }()

	if _, err := writer.Write([]byte("llm output\n")); err != nil {
		t.Fatalf("write prefixed log: %v", err)
	}

	logPath := filepath.Join(rootDir, "20260403", "LLM-2026040313.log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read prefixed log file: %v", err)
	}
	if !strings.Contains(string(body), "llm output") {
		t.Fatalf("expected prefixed log body, got %q", string(body))
	}
}
