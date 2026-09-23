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
}

// WriteEffectiveConfig writes an already loaded cfg to writer, returning projection or encoding errors without raw values.
// WriteEffectiveConfig 将已加载的 cfg 写入 writer，返回不含原始值的投影或编码错误。
func WriteEffectiveConfig(writer io.Writer, cfg Config) error {
	// Reuse the schema's recursive sensitivity policy, including key pools inside route and node arrays.
	// 复用 schema 的递归敏感策略，同时覆盖路由和节点数组中的密钥池。
	projection, ok := safeSchemaDefault(reflect.ValueOf(cfg), "")
	fields, object := projection.(map[string]any)
	if !ok || !object {
		return errors.New("effective configuration projection failed")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(EffectiveConfigDocument{Version: "v1", Redacted: true, Config: fields}); err != nil {
		return errors.New("effective configuration output failed")
	}
	return nil
}
