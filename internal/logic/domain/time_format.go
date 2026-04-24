// time_format.go centralizes caller-facing time formatting helpers so prompt rendering, transport responses, and review payloads share the same local-time contract.
// time_format.go 用于集中管理面向调用方的时间格式化辅助逻辑，让提示词渲染、传输层响应和评审载荷共享一致的本地时间契约。
package domain

import (
	"strings"
	"time"
)

const (
	// displayDateTimeLayout keeps transport and prompt time values human-readable with second precision while following the current runtime's local clock without exposing timezone text.
	// displayDateTimeLayout 用于让传输层和提示词里的时间值保持“秒级精度 + 当前运行系统本地时间且不暴露时区文本”的人类可读格式。
	displayDateTimeLayout = "2006-01-02 15:04:05"

	// displayDateLayout keeps auxiliary date-only fields aligned with the same local-time expansion contract.
	// displayDateLayout 用于让辅助日期字段与同一套本地时间展开契约保持一致。
	displayDateLayout = "2006-01-02"
)

// FormatDisplayDateTime converts one timestamp into the shared caller-facing local datetime format used by prompt payloads and gRPC responses without showing timezone suffixes.
// FormatDisplayDateTime 用于把时间转换成提示词载荷与 gRPC 响应共用的本地 datetime 格式，并且不显示时区后缀。
func FormatDisplayDateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.Local).Format(displayDateTimeLayout)
}

// FormatDisplayDate converts one timestamp into the shared caller-facing local date format so natural-day reasoning does not drift away from datetime fields.
// FormatDisplayDate 用于把时间转换成统一的本地日期格式，避免自然日推理与 datetime 字段产生漂移。
func FormatDisplayDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.Local).Format(displayDateLayout)
}

// FormatDisplayTimeFromUnixMillis converts one Unix-millisecond timestamp into the shared local datetime/date pair while preserving empty zero values.
// FormatDisplayTimeFromUnixMillis 用于把 Unix 毫秒时间戳转换成统一的本地 datetime/date 对，并在零值时保持为空。
func FormatDisplayTimeFromUnixMillis(timestamp int64) (string, string) {
	if timestamp <= 0 {
		return "", ""
	}
	value := time.UnixMilli(timestamp)
	return FormatDisplayDateTime(value), FormatDisplayDate(value)
}

// NormalizeLegacyDisplayDate resolves one caller-facing profile date from the current runtime-local expansion of the supplied anchor, and only falls back to the stored text when no reliable anchor exists.
// NormalizeLegacyDisplayDate 用于基于给定锚点按当前运行系统本地时间解析对外画像日期；只有在没有可靠锚点时才退回到已存文本。
func NormalizeLegacyDisplayDate(displayDate string, anchor time.Time) string {
	normalized := strings.TrimSpace(displayDate)
	if !anchor.IsZero() {
		return FormatDisplayDate(anchor)
	}
	return normalized
}

// NormalizeLegacyDisplayDateWithFallback resolves one reliable date anchor first, then applies the shared runtime-local date expansion rule without forcing every caller to duplicate anchor fallback rules.
// NormalizeLegacyDisplayDateWithFallback 用于先解析一个可靠日期锚点，再应用共享的运行时本地日期展开规则，避免每个调用方重复实现锚点回退逻辑。
func NormalizeLegacyDisplayDateWithFallback(displayDate string, anchor, fallback time.Time) string {
	if anchor.IsZero() {
		anchor = fallback
	}
	return NormalizeLegacyDisplayDate(displayDate, anchor)
}
