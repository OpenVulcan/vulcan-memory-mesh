// hints.go keeps OpenRouter SDK parameter mapping helpers shared by chat and embedding adapters.
// hints.go 用于承载 OpenRouter SDK 参数映射辅助函数，供 chat 与 embedding 适配器共享。
package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/operations"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// mergeProviderHints merges adapter defaults, model-specific defaults, and request overrides without mutating configured maps.
// mergeProviderHints 用于合并适配器默认参数、模型级默认参数和请求级覆盖参数，同时避免修改配置中的原始 map。
func mergeProviderHints(base map[string]any, modelParams map[string]map[string]any, model string, request map[string]any) map[string]any {
	merged := cloneHintMap(base)
	if overrides, ok := modelParams[strings.TrimSpace(model)]; ok {
		for key, value := range overrides {
			merged[key] = value
		}
	}
	for key, value := range request {
		merged[key] = value
	}
	return merged
}

// cloneHintMap copies one flat hint map so request-scoped overrides cannot leak back into route-level defaults.
// cloneHintMap 用于复制一份扁平参数表，避免请求级覆盖反向污染路由级默认参数。
func cloneHintMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	return maps.Clone(input)
}

// cloneNestedHintMap copies model-specific provider hints and normalizes model keys to keep lookup behavior deterministic.
// cloneNestedHintMap 用于复制模型专属 provider 参数，并规范化模型键，保证运行时查找行为稳定。
func cloneNestedHintMap(input map[string]map[string]any) map[string]map[string]any {
	if len(input) == 0 {
		return map[string]map[string]any{}
	}
	cloned := make(map[string]map[string]any, len(input))
	for model, params := range input {
		cloned[strings.TrimSpace(model)] = cloneHintMap(params)
	}
	return cloned
}

// requestOptionsFromContext attaches fixed OpenRouter app attribution headers and optional trace identifiers to every SDK request.
// requestOptionsFromContext 用于为每个 SDK 请求附加固定 OpenRouter 应用归因 header，并按需透传 trace 标识。
func requestOptionsFromContext(ctx context.Context) []operations.Option {
	headers := map[string]string{
		"HTTP-Referer":            defaultAppReferer,
		"X-Title":                 defaultAppTitle,
		"X-OpenRouter-Title":      defaultAppTitle,
		"X-OpenRouter-Categories": defaultAppCategories,
	}
	traceID := strings.TrimSpace(trace.IDFromContext(ctx))
	if traceID != "" {
		headers["X-Trace-ID"] = traceID
		headers["X-Client-Request-Id"] = traceID
		headers["X-OpenRouter-VMM-Trace"] = traceID
	}
	return []operations.Option{operations.WithSetHeaders(headers)}
}

// optionalValue wraps a concrete value in the SDK's OptionalNullable representation while preserving omitted-field semantics elsewhere.
// optionalValue 用于把具体值包装成 SDK 使用的 OptionalNullable 表示，同时让其他字段继续保持省略语义。
func optionalValue[T any](value T) optionalnullable.OptionalNullable[T] {
	return optionalnullable.From(&value)
}

// floatHint extracts numeric provider hints from YAML-decoded values while rejecting non-numeric inputs.
// floatHint 用于从 YAML 解码后的值中提取数值型 provider 参数，并拒绝非数值输入。
func floatHint(value any) (float64, bool) {
	switch tv := value.(type) {
	case float64:
		return tv, true
	case float32:
		return float64(tv), true
	case int:
		return float64(tv), true
	case int64:
		return float64(tv), true
	case int32:
		return float64(tv), true
	default:
		return 0, false
	}
}

// intHint extracts integer-like provider hints and converts them into the int64 shape required by OpenRouter SDK structs.
// intHint 用于提取整数型 provider 参数，并转换成 OpenRouter SDK 结构体所需的 int64 形态。
func intHint(value any) (int64, bool) {
	switch tv := value.(type) {
	case int:
		return int64(tv), true
	case int64:
		return tv, true
	case int32:
		return int64(tv), true
	case float64:
		return int64(tv), true
	case float32:
		return int64(tv), true
	default:
		return 0, false
	}
}

// boolHint extracts boolean provider hints without accepting ambiguous string values.
// boolHint 用于提取布尔型 provider 参数，并避免接受含义不明确的字符串值。
func boolHint(value any) (bool, bool) {
	tv, ok := value.(bool)
	return tv, ok
}

// stringHint trims provider hint strings and rejects empty values so SDK requests do not carry meaningless fields.
// stringHint 用于裁剪 provider 参数字符串并拒绝空值，避免 SDK 请求携带无意义字段。
func stringHint(value any) (string, bool) {
	tv, ok := value.(string)
	if !ok {
		return "", false
	}
	tv = strings.TrimSpace(tv)
	if tv == "" {
		return "", false
	}
	return tv, true
}

// stopHint maps supported stop-sequence hint shapes onto OpenRouter's typed Stop union.
// stopHint 用于把支持的 stop 序列参数形态映射到 OpenRouter 的强类型 Stop union。
func stopHint(value any) (components.Stop, bool) {
	switch tv := value.(type) {
	case string:
		if stop := strings.TrimSpace(tv); stop != "" {
			return components.CreateStopStr(stop), true
		}
	case []string:
		stops := make([]string, 0, len(tv))
		for _, item := range tv {
			if item = strings.TrimSpace(item); item != "" {
				stops = append(stops, item)
			}
		}
		if len(stops) > 0 {
			return components.CreateStopArrayOfStr(stops), true
		}
	case []any:
		stops := make([]string, 0, len(tv))
		for _, item := range tv {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if text = strings.TrimSpace(text); text != "" {
				stops = append(stops, text)
			}
		}
		if len(stops) > 0 {
			return components.CreateStopArrayOfStr(stops), true
		}
	}
	return components.Stop{}, false
}

// metadataHint converts map-shaped hints into the string metadata table accepted by OpenRouter chat requests.
// metadataHint 用于把 map 形态的参数转换成 OpenRouter chat 请求可接受的字符串 metadata 表。
func metadataHint(value any) (map[string]string, bool) {
	switch tv := value.(type) {
	case map[string]string:
		return cloneStringMap(tv), len(tv) > 0
	case map[string]any:
		metadata := make(map[string]string, len(tv))
		for key, value := range tv {
			key = strings.TrimSpace(key)
			if key == "" || value == nil {
				continue
			}
			metadata[key] = strings.TrimSpace(fmt.Sprint(value))
		}
		return metadata, len(metadata) > 0
	default:
		return nil, false
	}
}

// cloneStringMap copies metadata hints before attaching them to SDK requests so caller-owned maps remain immutable.
// cloneStringMap 用于在挂载到 SDK 请求前复制 metadata 参数，确保调用方持有的 map 不被修改。
func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		if key = strings.TrimSpace(key); key != "" {
			cloned[key] = strings.TrimSpace(value)
		}
	}
	return cloned
}

// providerPreferencesHint decodes OpenRouter provider-routing hints through JSON so YAML maps can populate the SDK's generated preference struct.
// providerPreferencesHint 用于通过 JSON 解码 OpenRouter provider 选路参数，让 YAML map 可以填充 SDK 生成的偏好结构体。
func providerPreferencesHint(value any) (components.ProviderPreferences, bool) {
	if preferences, ok := value.(components.ProviderPreferences); ok {
		return preferences, true
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || string(body) == "null" {
		return components.ProviderPreferences{}, false
	}
	var preferences components.ProviderPreferences
	if err := json.Unmarshal(body, &preferences); err != nil {
		return components.ProviderPreferences{}, false
	}
	return preferences, true
}
