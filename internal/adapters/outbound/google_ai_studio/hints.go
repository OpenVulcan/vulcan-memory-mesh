// hints.go implements provider-hint helpers shared by the Google AI Studio native adapters.
// hints.go 用于实现 Google AI Studio 原生适配器共享的 provider 参数辅助逻辑。
package google_ai_studio

import (
	"fmt"
	"strconv"
	"strings"
)

// stringHint normalizes one generic value into a non-empty string when the provider hint is text-like.
// stringHint 用于在 provider hint 为文本形态时，把通用值规范化成非空字符串。
func stringHint(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		return trimmed, trimmed != ""
	case fmt.Stringer:
		trimmed := strings.TrimSpace(typed.String())
		return trimmed, trimmed != ""
	default:
		return "", false
	}
}

// boolHint normalizes one generic value into a boolean when the provider hint uses a boolean-compatible representation.
// boolHint 用于在 provider hint 使用布尔兼容表达时，把通用值规范化成布尔值。
func boolHint(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

// intHint normalizes one generic value into an integer when the provider hint encodes one count-like field.
// intHint 用于在 provider hint 表示计数型字段时，把通用值规范化成整数。
func intHint(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// floatHint normalizes one generic value into a float when the provider hint represents one sampling or penalty scalar.
// floatHint 用于在 provider hint 表示采样或惩罚系数时，把通用值规范化成浮点数。
func floatHint(value any) (float64, bool) {
	switch typed := value.(type) {
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// stringListHint normalizes one generic value into a non-empty string slice when the provider hint accepts either one string or multiple strings.
// stringListHint 用于在 provider hint 接受单字符串或多字符串时，把通用值规范化成非空字符串切片。
func stringListHint(value any) ([]string, bool) {
	switch typed := value.(type) {
	case string:
		if item, ok := stringHint(typed); ok {
			return []string{item}, true
		}
		return nil, false
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized, ok := stringHint(item); ok {
				out = append(out, normalized)
			}
		}
		return out, len(out) > 0
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized, ok := stringHint(item); ok {
				out = append(out, normalized)
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

// float32Ptr converts one float into the pointer shape expected by the Google GenAI SDK config structs.
// float32Ptr 用于把浮点值转换成 Google GenAI SDK 配置结构所需的指针形态。
func float32Ptr(value float64) *float32 {
	converted := float32(value)
	return &converted
}

// int32Ptr converts one integer into the pointer shape expected by the Google GenAI SDK config structs.
// int32Ptr 用于把整数转换成 Google GenAI SDK 配置结构所需的指针形态。
func int32Ptr(value int) *int32 {
	converted := int32(value)
	return &converted
}
