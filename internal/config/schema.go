// schema.go exposes the authoritative machine-readable configuration field inventory used by management tooling.
// schema.go 用于暴露管理器使用的权威机器可读配置字段清单。
package config

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
)

const (
	// ConfigSchemaVersion identifies the stable schema document contract consumed by external installers and managers.
	// ConfigSchemaVersion 用于标识安装器和管理器消费的稳定 schema 文档契约。
	ConfigSchemaVersion = "v1"
)

var (
	// durationSchemaType keeps the custom duration wrapper recognizable even though it is represented as a struct in Go.
	// durationSchemaType 用于识别自定义时长包装类型，避免它在 Go 反射中被误报为普通对象。
	durationSchemaType = reflect.TypeOf(Duration{})

	// configSchemaEnums lists only enum values enforced by the current configuration normalizers or validators.
	// configSchemaEnums 只列出当前配置归一化器或校验器已经明确支持的枚举值。
	configSchemaEnums = map[string][]string{
		"storage.mode":                         {"split", "controller", "combined", "native"},
		"storage.combined_provider":            {"postgres"},
		"logging.level":                        {"debug", "info", "warn", "warning", "error"},
		"logging.format":                       {"text", "json"},
		"pre_check.search_scope":               {"team", "space", "project"},
		"memory_replace_scope":                 {"session", "team", "space", "project"},
		"embedding.provider":                   {"openai", "openai_native", "openai_go", "google_ai_studio", "openrouter"},
		"vector.provider":                      {"lancedb"},
		"relational.provider":                  {"sqlite"},
		"sqlite.native.tokenizer":              {"gse", "unicode61"},
		"sqlite.tokenizer_mode":                {"jieba", "none"},
		"controller.process_mode":              {"managed", "service"},
		"postgres.flavor":                      {"paradedb", "standard"},
		"post_action.input_mode":               {"strict", "compat"},
		"retention.protect_priority_floor":     {"P0", "P1", "P2"},
		"retention.protect_memory_level_floor": {"session", "phase", "stable", "persistent"},
		"llm.routes[].provider":                {"openai", "openai_native", "openai_go", "google_ai_studio", "openrouter"},
		"llm.routes[].key_failover.policy":     {"ordered_failover", "round_robin"},
		"rerank.routes[].provider":             {"dashscope", "siliconflow", "openrouter"},
	}
)

// ConfigSchemaDocument describes the complete configuration model in a versioned, JSON-safe form.
// ConfigSchemaDocument 用于以带版本的 JSON 安全结构描述完整配置模型。
type ConfigSchemaDocument struct {
	Version    string              `json:"version"`
	ConfigType string              `json:"config_type"`
	Fields     []ConfigFieldSchema `json:"fields"`
}

// ConfigFieldSchema describes one tagged configuration field, including its shape, safe default, and sensitivity marker.
// ConfigFieldSchema 用于描述一个带 json tag 的配置字段，包括形状、安全默认值和敏感标记。
type ConfigFieldSchema struct {
	Path      string          `json:"path"`
	Type      string          `json:"type"`
	ItemType  string          `json:"item_type,omitempty"`
	KeyType   string          `json:"key_type,omitempty"`
	ValueType string          `json:"value_type,omitempty"`
	Nullable  bool            `json:"nullable,omitempty"`
	Sensitive bool            `json:"sensitive"`
	Enum      []string        `json:"enum,omitempty"`
	Default   json.RawMessage `json:"default,omitempty"`
}

// ConfigSchema builds the schema from Config and DefaultBase so the installer sees the same fields and safe defaults as the runtime.
// ConfigSchema 从 Config 与 DefaultBase 生成 schema，确保安装器看到的字段和安全默认值与运行时一致。
func ConfigSchema() ConfigSchemaDocument {
	fields := make([]ConfigFieldSchema, 0, 128)
	appendSchemaFields(reflect.TypeOf(Config{}), reflect.ValueOf(DefaultBase()), "", &fields)
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Path < fields[j].Path
	})
	return ConfigSchemaDocument{
		Version:    ConfigSchemaVersion,
		ConfigType: "Config",
		Fields:     fields,
	}
}

// WriteConfigSchema writes one deterministic schema document to the supplied writer.
// WriteConfigSchema 将确定性的 schema 文档写入指定输出流。
func WriteConfigSchema(writer io.Writer) error {
	if writer == nil {
		return errors.New("schema writer is nil")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(ConfigSchema())
}

// ConfigSchemaHasPath reports whether a concrete validation path belongs to the reflected schema field set.
// ConfigSchemaHasPath 用于判断一个具体校验路径是否属于反射生成的 schema 字段集合。
func ConfigSchemaHasPath(path string) bool {
	normalized := normalizeSchemaPath(path)
	for _, field := range ConfigSchema().Fields {
		if field.Path == normalized {
			return true
		}
		if field.Type == "array" && strings.TrimSuffix(normalized, "[]") == field.Path {
			return true
		}
	}
	return false
}

// appendSchemaFields recursively expands tagged structs and structured collection elements into stable field paths.
// appendSchemaFields 递归展开带标签的结构体和结构化集合元素，生成稳定的字段路径。
func appendSchemaFields(typ reflect.Type, defaultValue reflect.Value, prefix string, fields *[]ConfigFieldSchema) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
		if defaultValue.IsValid() && defaultValue.Kind() == reflect.Pointer && !defaultValue.IsNil() {
			defaultValue = defaultValue.Elem()
		} else {
			defaultValue = reflect.Value{}
		}
	}
	if typ == durationSchemaType || typ.Kind() != reflect.Struct {
		return
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name, _ := schemaJSONFieldName(field)
		if name == "" || name == "-" || field.PkgPath != "" {
			continue
		}
		path := joinSchemaPath(prefix, name)
		fieldDefault := reflect.Value{}
		if defaultValue.IsValid() && defaultValue.Kind() == reflect.Struct {
			fieldDefault = defaultValue.Field(index)
		}
		fieldSchema := makeConfigFieldSchema(path, field.Type, fieldDefault)
		*fields = append(*fields, fieldSchema)
		appendSchemaChildren(field.Type, fieldDefault, path, fields)
	}
}

// appendSchemaChildren expands only containers whose element or value shape is known from Go types.
// appendSchemaChildren 只展开 Go 类型明确表达了元素或值形状的容器。
func appendSchemaChildren(typ reflect.Type, defaultValue reflect.Value, path string, fields *[]ConfigFieldSchema) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
		if defaultValue.IsValid() && defaultValue.Kind() == reflect.Pointer && !defaultValue.IsNil() {
			defaultValue = defaultValue.Elem()
		} else {
			defaultValue = reflect.Value{}
		}
	}
	switch typ.Kind() {
	case reflect.Struct:
		if typ != durationSchemaType {
			appendSchemaFields(typ, defaultValue, path, fields)
		}
	case reflect.Slice, reflect.Array:
		itemType := typ.Elem()
		itemDefault := reflect.Value{}
		if defaultValue.IsValid() && (defaultValue.Kind() == reflect.Slice || defaultValue.Kind() == reflect.Array) && defaultValue.Len() > 0 {
			itemDefault = defaultValue.Index(0)
		}
		for itemType.Kind() == reflect.Pointer {
			itemType = itemType.Elem()
			if itemDefault.IsValid() && itemDefault.Kind() == reflect.Pointer && !itemDefault.IsNil() {
				itemDefault = itemDefault.Elem()
			} else {
				itemDefault = reflect.Value{}
			}
		}
		if itemType.Kind() == reflect.Struct && itemType != durationSchemaType {
			appendSchemaFields(itemType, itemDefault, path+"[]", fields)
		}
	case reflect.Map:
		// Map keys are dynamic by contract; their value type is already recorded on the map field.
		// Map 的键由配置动态决定，值类型已经记录在 map 字段自身上。
	}
}

// makeConfigFieldSchema converts one reflected Go type into the public field descriptor and attaches a safe default when available.
// makeConfigFieldSchema 将一个反射得到的 Go 类型转换为公开字段描述，并在可用时附加安全默认值。
func makeConfigFieldSchema(path string, typ reflect.Type, defaultValue reflect.Value) ConfigFieldSchema {
	fieldType, itemType, keyType, valueType, nullable := describeSchemaType(typ)
	field := ConfigFieldSchema{
		Path:      path,
		Type:      fieldType,
		ItemType:  itemType,
		KeyType:   keyType,
		ValueType: valueType,
		Nullable:  nullable,
		Sensitive: isSensitiveConfigPath(path),
		Enum:      append([]string(nil), configSchemaEnums[normalizeSchemaPath(path)]...),
	}
	if !field.Sensitive {
		if safeValue, ok := safeSchemaDefault(defaultValue, path); ok {
			if encoded, err := json.Marshal(safeValue); err == nil {
				field.Default = encoded
			}
		}
	}
	return field
}

// describeSchemaType classifies scalar, duration, pointer, collection, and arbitrary provider parameter types.
// describeSchemaType 用于分类标量、时长、指针、集合以及任意 provider 参数类型。
func describeSchemaType(typ reflect.Type) (fieldType, itemType, keyType, valueType string, nullable bool) {
	for typ.Kind() == reflect.Pointer {
		nullable = true
		typ = typ.Elem()
	}
	if typ == durationSchemaType {
		return "duration", "", "", "", nullable
	}
	switch typ.Kind() {
	case reflect.Struct:
		return "object", "", "", "", nullable
	case reflect.Slice, reflect.Array:
		itemType, _, _, _, _ = describeSchemaType(typ.Elem())
		return "array", itemType, "", "", nullable
	case reflect.Map:
		valueType, _, _, _, _ = describeSchemaType(typ.Elem())
		keyType, _, _, _, _ = describeSchemaType(typ.Key())
		return "map", "", keyType, valueType, nullable
	case reflect.String:
		return "string", "", "", "", nullable
	case reflect.Bool:
		return "boolean", "", "", "", nullable
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "integer", "", "", "", nullable
	case reflect.Float32, reflect.Float64:
		return "number", "", "", "", nullable
	case reflect.Interface:
		return "any", "", "", "", nullable
	default:
		return "unknown", "", "", "", nullable
	}
}

// safeSchemaDefault converts one default value to JSON-compatible data while removing sensitive descendants.
// safeSchemaDefault 将一个默认值转换为 JSON 兼容数据，同时移除其中的敏感后代字段。
func safeSchemaDefault(value reflect.Value, path string) (any, bool) {
	if !value.IsValid() || isSensitiveConfigPath(path) {
		return nil, false
	}
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if value.Type() == durationSchemaType {
		return value.Interface().(Duration).String(), true
	}
	switch value.Kind() {
	case reflect.Struct:
		result := make(map[string]any)
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			name, _ := schemaJSONFieldName(field)
			if name == "" || name == "-" || field.PkgPath != "" {
				continue
			}
			child, ok := safeSchemaDefault(value.Field(index), joinSchemaPath(path, name))
			if ok {
				result[name] = child
			}
		}
		return result, true
	case reflect.Slice, reflect.Array:
		result := make([]any, 0, value.Len())
		for index := 0; index < value.Len(); index++ {
			child, ok := safeSchemaDefault(value.Index(index), path+"[]")
			if ok {
				result = append(result, child)
			}
		}
		return result, true
	case reflect.Map:
		if value.IsNil() {
			return nil, false
		}
		result := make(map[string]any)
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key()
			if key.Kind() != reflect.String {
				continue
			}
			child, ok := safeSchemaDefault(iter.Value(), path+"{}")
			if ok {
				result[key.String()] = child
			}
		}
		return result, true
	default:
		if !value.CanInterface() {
			return nil, false
		}
		return value.Interface(), true
	}
}

// schemaJSONFieldName extracts the exact encoding/json name and the option suffix from a struct tag.
// schemaJSONFieldName 提取 struct tag 中与 encoding/json 完全一致的名称和选项后缀。
func schemaJSONFieldName(field reflect.StructField) (name, options string) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return field.Name, ""
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = field.Name
	}
	if len(parts) > 1 {
		options = strings.Join(parts[1:], ",")
	}
	return name, options
}

// joinSchemaPath appends a JSON field name while preserving collection markers used by the schema contract.
// joinSchemaPath 追加 JSON 字段名，同时保留 schema 契约中的集合标记。
func joinSchemaPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// isSensitiveConfigPath identifies credentials and secret-bearing key pools without treating arbitrary provider parameters as secrets.
// isSensitiveConfigPath 识别凭据和密钥池，同时不把任意 provider 参数统统当作敏感信息。
func isSensitiveConfigPath(path string) bool {
	normalized := normalizeSchemaPath(path)
	if strings.HasSuffix(normalized, ".api_keys") {
		return true
	}
	switch normalized {
	case "management.access_token", "logging.payload_encryption_key", "postgres.dsn":
		return true
	default:
		return false
	}
}

// normalizeSchemaPath replaces concrete list indexes with the schema's collection marker for enum and path lookup.
// normalizeSchemaPath 将具体列表下标替换为 schema 集合标记，用于枚举和路径查找。
func normalizeSchemaPath(path string) string {
	var builder strings.Builder
	for index := 0; index < len(path); index++ {
		if path[index] != '[' {
			builder.WriteByte(path[index])
			continue
		}
		end := strings.IndexByte(path[index:], ']')
		if end < 0 {
			builder.WriteByte(path[index])
			continue
		}
		end += index
		contents := path[index+1 : end]
		if contents == "" {
			builder.WriteString("[]")
			index = end
			continue
		}
		allDigits := true
		for _, char := range contents {
			if char < '0' || char > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			builder.WriteString("[]")
			index = end
			continue
		}
		builder.WriteString(path[index : end+1])
		index = end
	}
	return builder.String()
}
