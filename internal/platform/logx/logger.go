// logger.go implements structured logging helpers used across the local runtime.
// logger.go 用于实现本地运行时复用的结构化日志助手。
package logx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config carries the logging knobs used to build one structured runtime logger.
// Config 用于承载构建结构化运行时日志器时使用的配置项。
type Config struct {
	Level         string
	Format        string
	DebugPayloads bool
}

// Logger wraps slog.Logger so adapters and use cases can share a small logging surface.
// Logger 用于包装 slog.Logger，让适配层和用例层共享一套轻量日志接口。
type Logger struct {
	base          *slog.Logger
	debugPayloads bool
}

// New creates a Logger instance backed by a text or JSON slog handler.
// New 用于创建一个由 text 或 JSON slog handler 驱动的 Logger 实例。
func New(w io.Writer, cfg Config) *Logger {
	if w == nil {
		w = os.Stdout
	}
	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = newMultilineTextHandler(w, opts)
	}
	return &Logger{base: slog.New(handler), debugPayloads: cfg.DebugPayloads}
}

// Default creates a Logger instance with stdout text output at info level.
// Default 用于创建一个输出到 stdout 且级别为 info 的默认 Logger。
func Default() *Logger {
	return New(os.Stdout, Config{Level: "info", Format: "text"})
}

// With returns a child logger that always carries the provided structured attributes.
// With 用于返回一个始终携带指定结构化字段的子日志器。
func (l *Logger) With(args ...any) *Logger {
	if l == nil {
		return Default().With(args...)
	}
	return &Logger{base: l.base.With(args...), debugPayloads: l.debugPayloads}
}

// Debug writes one debug-level log entry.
// Debug 用于输出一条 debug 级别日志。
func (l *Logger) Debug(msg string, args ...any) {
	logger := resolve(l)
	logger.base.Debug(msg, args...)
}

// Info writes one info-level log entry.
// Info 用于输出一条 info 级别日志。
func (l *Logger) Info(msg string, args ...any) {
	logger := resolve(l)
	logger.base.Info(msg, args...)
}

// Warn writes one warn-level log entry.
// Warn 用于输出一条 warn 级别日志。
func (l *Logger) Warn(msg string, args ...any) {
	logger := resolve(l)
	logger.base.Warn(msg, args...)
}

// Error writes one error-level log entry.
// Error 用于输出一条 error 级别日志。
func (l *Logger) Error(msg string, args ...any) {
	logger := resolve(l)
	logger.base.Error(msg, args...)
}

// Debugf formats and writes one debug-level log entry.
// Debugf 用于格式化并输出一条 debug 级别日志。
func (l *Logger) Debugf(format string, args ...any) {
	resolve(l).Debug(fmt.Sprintf(format, args...))
}

// Infof formats and writes one info-level log entry.
// Infof 用于格式化并输出一条 info 级别日志。
func (l *Logger) Infof(format string, args ...any) {
	resolve(l).Info(fmt.Sprintf(format, args...))
}

// Warnf formats and writes one warn-level log entry.
// Warnf 用于格式化并输出一条 warn 级别日志。
func (l *Logger) Warnf(format string, args ...any) {
	resolve(l).Warn(fmt.Sprintf(format, args...))
}

// Errorf formats and writes one error-level log entry.
// Errorf 用于格式化并输出一条 error 级别日志。
func (l *Logger) Errorf(format string, args ...any) {
	resolve(l).Error(fmt.Sprintf(format, args...))
}

// Enabled reports whether the target level is currently enabled.
// Enabled 用于判断目标级别当前是否启用。
func (l *Logger) Enabled(ctx context.Context, level slog.Level) bool {
	logger := resolve(l)
	return logger.base.Enabled(ctx, level)
}

// PayloadDebugEnabled reports whether the current logger should emit full payload-oriented debug fields instead of the default redacted summaries.
// PayloadDebugEnabled 用于判断当前日志器是否应输出完整载荷类调试字段，而不是默认的脱敏摘要。
func (l *Logger) PayloadDebugEnabled() bool {
	logger := resolve(l)
	return logger.debugPayloads
}

// parseLevel normalizes string levels into slog levels.
// parseLevel 用于把字符串级别归一为 slog 级别。
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// resolve guarantees that callers always receive a non-nil logger.
// resolve 用于保证调用方始终获得非空日志器。
func resolve(logger *Logger) *Logger {
	if logger != nil {
		return logger
	}
	return Default()
}
