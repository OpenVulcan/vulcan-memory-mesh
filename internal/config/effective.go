// effective.go serializes the loaded configuration for read-only manager diagnostics in the configuration layer.
// effective.go 属于配置层，为管理器只读诊断序列化已加载配置。
package config

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

// EffectiveConfigDocument carries runtime values with schema-sensitive fields omitted; Version identifies the wire contract.
// EffectiveConfigDocument 携带省略 schema 敏感字段后的运行时值；Version 标识传输契约。
type EffectiveConfigDocument struct {
	Version  string         `json:"version"`
	Redacted bool           `json:"redacted"`
	Config   map[string]any `json:"config"`
	// Sources maps JSON Pointers to leaf origins when the authoritative loader supplied a trace.
	// Sources 在权威加载器提供跟踪结果时，将 JSON Pointer 映射到叶子来源。
	Sources map[string]ConfigValueSource `json:"sources,omitempty"`
}

// WriteEffectiveConfig writes an already loaded cfg to writer, returning projection or encoding errors without raw values.
// WriteEffectiveConfig 将已加载的 cfg 写入 writer，返回不含原始值的投影或编码错误。
func WriteEffectiveConfig(writer io.Writer, cfg Config) error {
	return writeEffectiveConfig(writer, cfg, nil)
}

// WriteEffectiveConfigWithSources writes version two with authoritative origins; missing source data is rejected.
// WriteEffectiveConfigWithSources 写入带权威来源的第二版文档；缺少来源数据时返回错误。
func WriteEffectiveConfigWithSources(writer io.Writer, cfg Config, sources map[string]ConfigValueSource) error {
	if len(sources) == 0 {
		return errors.New("effective configuration sources are missing")
	}
	return writeEffectiveConfig(writer, cfg, sources)
}

// writeEffectiveConfig applies the shared secret policy and preserves null values in diagnostic output.
// writeEffectiveConfig 应用共用秘密策略并在诊断输出中保留空值，返回安全投影或编码错误。
func writeEffectiveConfig(writer io.Writer, cfg Config, sources map[string]ConfigValueSource) error {
	// Reuse the schema's recursive sensitivity policy, including key pools inside route and node arrays.
	// 复用 schema 的递归敏感策略，同时覆盖路由和节点数组中的密钥池。
	projection, ok := safeConfigProjection(reflect.ValueOf(cfg), "", true)
	fields, object := projection.(map[string]any)
	if !ok || !object {
		return errors.New("effective configuration projection failed")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	version := "v1"
	if sources != nil {
		version = "v2"
	}
	if err := encoder.Encode(EffectiveConfigDocument{Version: version, Redacted: true, Config: fields, Sources: sources}); err != nil {
		return errors.New("effective configuration output failed")
	}
	return nil
}
