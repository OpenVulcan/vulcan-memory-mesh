// effective_sources.go observes the real loading stages for redacted, read-only configuration diagnostics.
// effective_sources.go 属于配置层，观察真实加载阶段，为脱敏只读配置诊断记录来源。
package config

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ConfigValueSource identifies the last contributing stage; Environment contains names only, never values.
// ConfigValueSource 标识最后参与赋值的阶段；Environment 仅含变量名，Normalized 表示随后被归一化调整。
type ConfigValueSource struct {
	Kind        string   `json:"kind"`
	File        string   `json:"file,omitempty"`
	Environment []string `json:"environment,omitempty"`
	Normalized  bool     `json:"normalized,omitempty"`
}

// configSourceTrace retains only detached, redacted leaf values while the authoritative loader is running.
// configSourceTrace 仅在权威加载器执行期间保留已分离、已脱敏的叶子值及其来源，不写入磁盘。
type configSourceTrace struct {
	values  map[string]any
	sources map[string]ConfigValueSource
}

// LoadPathsWithSources returns the same validated configuration as LoadPaths plus JSON-Pointer-keyed leaf sources.
// LoadPathsWithSources 返回与 LoadPaths 相同的已校验配置，以及按 JSON Pointer 定位的叶子来源；失败不返回诊断残留。
func LoadPathsWithSources(paths []string, fallback Config) (Config, map[string]ConfigValueSource, error) {
	trace := &configSourceTrace{}
	cfg, err := loadConfigPaths(paths, fallback, trace)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, trace.sources, nil
}

// sourcePointer appends one escaped JSON object key or array index without ambiguity from dots or slashes in map keys.
// sourcePointer 追加经转义的 JSON 对象键或数组下标，避免动态键中的点和斜线造成歧义。
func sourcePointer(parent, key string) string {
	return parent + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// effectiveLeaves flattens the already redacted projection, retaining nulls and empty collections as leaves.
// effectiveLeaves 展平已经脱敏的投影，将空值和空集合保留为叶子，写入调用方提供的结果表。
func effectiveLeaves(value any, path string, result map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 0 {
			for key, child := range typed {
				effectiveLeaves(child, sourcePointer(path, key), result)
			}
			return
		}
	case []any:
		if len(typed) > 0 {
			for index, child := range typed {
				effectiveLeaves(child, sourcePointer(path, strconv.Itoa(index)), result)
			}
			return
		}
	}
	result[path] = value
}

// observe attributes real changes and explicit writes, retaining original inputs when normalization changes their values.
// observe 根据实际变化和显式赋值记录来源；归一化改变值时保留输入来源，并剔除已删除数组元素的记录。
func (t *configSourceTrace) observe(cfg Config, stage ConfigValueSource, explicit map[string]ConfigValueSource) {
	projection, _ := safeConfigProjection(reflect.ValueOf(cfg), "", true)
	current := map[string]any{}
	effectiveLeaves(projection, "", current)
	sources := make(map[string]ConfigValueSource, len(current))
	for path, value := range current {
		source, assigned := explicit[path]
		// Object-valued map assignments also replace all children; find their closest explicit owner.
		// 对象形式的 map 项赋值同时替换子项，因此寻找最近的显式赋值祖先。
		for parent := path; !assigned && strings.Contains(parent, "/"); {
			parent = parent[:strings.LastIndex(parent, "/")]
			source, assigned = explicit[parent]
		}
		previous, exists := t.values[path]
		if !assigned {
			source = t.sources[path]
			if !exists || !reflect.DeepEqual(previous, value) {
				if stage.Kind == "normalization" && exists && source.Kind != "initial" {
					source.Normalized = true
				} else {
					source = stage
				}
			}
		}
		sources[path] = source
	}
	t.values, t.sources = current, sources
}

// file observes the already decoded layer after the actual strict merge and embedding reset have completed.
// file 在真实严格合并和 embedding 重置完成后观察已解码层；同值覆盖也属于新文件，标量 null 不伪造赋值。
func (t *configSourceTrace) file(path string, body, rawBody []byte, cfg Config) error {
	var layer any
	// The authoritative decoder already accepted these bytes, so this decode cannot introduce alternate semantics.
	// 权威解码器已经接受这些字节，此处仅读取字段存在性，不引入另一套合并逻辑。
	if err := json.Unmarshal(body, &layer); err != nil {
		return err
	}
	raw, err := decodeRawConfigLayer(path, rawBody)
	if err != nil {
		return err
	}
	references := map[string][]string{}
	collectSourceEnvironment(raw, reflect.TypeOf(cfg), "", references)
	explicit := map[string]ConfigValueSource{}
	stage := ConfigValueSource{Kind: "file", File: path}
	declaredSourceWrites(layer, reflect.TypeOf(cfg), "", stage, references, explicit)
	t.observe(cfg, stage, explicit)
	return nil
}

// declaredSourceWrites follows encoding/json field names and nullable types to identify explicit assignments without merging values.
// declaredSourceWrites 按 encoding/json 字段名与可空类型识别显式赋值，不执行值合并；参数携带类型、指针及原始变量引用。
func declaredSourceWrites(value any, typ reflect.Type, pointer string, source ConfigValueSource, references map[string][]string, result map[string]ConfigValueSource) {
	if typ == durationSchemaType {
		result[pointer] = sourceWithReferences(source, pointer, references)
		return
	}
	if value == nil {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
			result[pointer] = sourceWithReferences(source, pointer, references)
		}
		return
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for key, child := range object {
			// Exact names win; case-insensitive matching mirrors encoding/json's existing typed field contract.
			// 精确名称优先，再按 encoding/json 的既有类型字段契约执行不区分大小写匹配。
			index := sourceFieldIndex(typ, key)
			if index >= 0 {
				field := typ.Field(index)
				name, _ := schemaJSONFieldName(field)
				declaredSourceWrites(child, field.Type, sourcePointer(pointer, name), source, references, result)
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return
		}
		if len(array) == 0 {
			result[pointer] = source
			return
		}
		for index, child := range array {
			declaredSourceWrites(child, typ.Elem(), sourcePointer(pointer, strconv.Itoa(index)), source, references, result)
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for key := range object {
			result[sourcePointer(pointer, key)] = sourceWithReferences(source, sourcePointer(pointer, key), references)
		}
	default:
		result[pointer] = sourceWithReferences(source, pointer, references)
	}
}

// sourceWithReferences records only names participating in the selected file value, including nested map assignments.
// sourceWithReferences 仅记录参与所选文件值的变量名，同时覆盖嵌套 map 赋值，不记录变量内容。
func sourceWithReferences(source ConfigValueSource, pointer string, references map[string][]string) ConfigValueSource {
	names := map[string]bool{}
	for path, keys := range references {
		if path == pointer || strings.HasPrefix(path, pointer+"/") || strings.HasPrefix(pointer, path+"/") {
			for _, key := range keys {
				names[key] = true
			}
		}
	}
	for name := range names {
		source.Environment = append(source.Environment, name)
	}
	sort.Strings(source.Environment)
	return source
}

// collectSourceEnvironment follows the raw YAML/JSON tree with canonical field names before expansion, avoiding ambiguous dotted map paths.
// collectSourceEnvironment 在展开前按规范字段名遍历原始 YAML/JSON 树，以 JSON Pointer 避免动态 map 点号键歧义。
func collectSourceEnvironment(value any, typ reflect.Type, pointer string, result map[string][]string) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typed := value.(type) {
	case string:
		result[pointer] = collectEnvReferencesInString(typed, map[string]struct{}{})
	case []any:
		childType := typ
		if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			childType = typ.Elem()
		}
		for index, child := range typed {
			collectSourceEnvironment(child, childType, sourcePointer(pointer, strconv.Itoa(index)), result)
		}
	case map[string]any:
		for key, child := range typed {
			name, childType := key, typ
			if typ.Kind() == reflect.Struct {
				index := sourceFieldIndex(typ, key)
				if index < 0 {
					continue
				}
				field := typ.Field(index)
				name, _ = schemaJSONFieldName(field)
				childType = field.Type
			} else if typ.Kind() == reflect.Map {
				childType = typ.Elem()
			}
			collectSourceEnvironment(child, childType, sourcePointer(pointer, name), result)
		}
	}
}

// sourceFieldIndex resolves the exact or folded encoding/json field name and returns -1 for non-config members.
// sourceFieldIndex 解析 encoding/json 的精确或大小写折叠字段名，非配置成员返回 -1。
func sourceFieldIndex(typ reflect.Type, key string) int {
	index := -1
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _ := schemaJSONFieldName(field)
		if field.PkgPath != "" || name == "-" {
			continue
		}
		if name == key {
			return i
		}
		if index < 0 && strings.EqualFold(name, key) {
			index = i
		}
	}
	return index
}

// environment uses the loader's authoritative override allowlist after successful parsing, including same-value assignments.
// environment 在成功解析后使用加载器的权威覆盖白名单，记录同值环境赋值，并观察附带的字段重置。
func (t *configSourceTrace) environment(cfg Config, allowed map[string]struct{}) {
	explicit := map[string]ConfigValueSource{}
	for key := range allowed {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			continue
		}
		for _, field := range supportedEnvOverrideValuePaths[key] {
			pointer := ""
			for _, part := range strings.Split(field, ".") {
				pointer = sourcePointer(pointer, part)
			}
			explicit[pointer] = ConfigValueSource{Kind: "environment", Environment: []string{key}}
		}
	}
	t.observe(cfg, ConfigValueSource{Kind: "environment"}, explicit)
}
