// hints.go provides behavior-preserving provider-hint helpers shared by outbound AI adapters.
// hints.go 用于提供出站 AI 适配器共享且保持既有语义的 provider hint 辅助能力。
package providerhint

import (
	"maps"
	"strings"
)

// CloneMap returns a writable shallow copy and deliberately preserves the adapters' non-nil empty-map contract.
// CloneMap 用于返回可写的浅拷贝，并明确保持适配器既有的非 nil 空 map 契约。
func CloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	return maps.Clone(input)
}

// CloneNestedMap trims model keys and clones every parameter map so clients cannot mutate configured defaults.
// CloneNestedMap 用于清理模型键并克隆每个参数 map，避免客户端修改已配置的默认值。
func CloneNestedMap(input map[string]map[string]any) map[string]map[string]any {
	if len(input) == 0 {
		return map[string]map[string]any{}
	}
	cloned := make(map[string]map[string]any, len(input))
	for model, params := range input {
		cloned[strings.TrimSpace(model)] = CloneMap(params)
	}
	return cloned
}

// Merge overlays model defaults and request hints onto a cloned base map so request values have final precedence.
// Merge 用于把模型默认值与请求 hint 覆盖到基础 map 的克隆上，使请求值拥有最终优先级。
func Merge(base, modelDefaults, request map[string]any) map[string]any {
	merged := CloneMap(base)
	for key, value := range modelDefaults {
		merged[key] = value
	}
	for key, value := range request {
		merged[key] = value
	}
	return merged
}

// Float64 accepts the exact numeric hint types supported by strict OpenAI-compatible adapters.
// Float64 用于接受严格 OpenAI 兼容适配器支持的精确数值 hint 类型。
func Float64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	default:
		return 0, false
	}
}

// Int64 converts the exact integer and floating-point hint types accepted by strict OpenAI-compatible adapters.
// Int64 用于转换严格 OpenAI 兼容适配器接受的精确整数与浮点 hint 类型。
func Int64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	default:
		return 0, false
	}
}

// Bool accepts only native boolean hints so configuration type mismatches stay observable.
// Bool 仅接受原生布尔 hint，使配置类型不匹配保持可观察。
func Bool(value any) (bool, bool) {
	typed, ok := value.(bool)
	return typed, ok
}

// String returns a trimmed non-empty native string and rejects every other hint representation.
// String 用于返回清理后的非空原生字符串，并拒绝其他 hint 表示。
func String(value any) (string, bool) {
	typed, ok := value.(string)
	if !ok {
		return "", false
	}
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", false
	}
	return typed, true
}
