// schema_test.go verifies the reflected configuration schema contract and its secret-safety behavior.
// schema_test.go 用于验证反射配置 schema 契约及其敏感信息安全行为。
package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigSchemaReflectsShapesDefaultsAndSensitivity verifies nested containers, custom durations, safe defaults, and sensitive markers.
// TestConfigSchemaReflectsShapesDefaultsAndSensitivity 用于验证嵌套容器、自定义时长、安全默认值和敏感标记。
func TestConfigSchemaReflectsShapesDefaultsAndSensitivity(t *testing.T) {
	schema := ConfigSchema()
	if schema.Version != ConfigSchemaVersion {
		t.Fatalf("schema version = %q, want %q", schema.Version, ConfigSchemaVersion)
	}
	if schema.ConfigType != "Config" {
		t.Fatalf("schema config type = %q, want Config", schema.ConfigType)
	}
	seen := map[string]struct{}{}
	for _, field := range schema.Fields {
		if _, exists := seen[field.Path]; exists {
			t.Fatalf("duplicate schema path %q", field.Path)
		}
		seen[field.Path] = struct{}{}
	}

	tests := []struct {
		path      string
		typeName  string
		itemType  string
		valueType string
		nullable  bool
		sensitive bool
		enum      []string
		defaultJS string
	}{
		{path: "grpc", typeName: "object", defaultJS: `{"keepalive":{"enabled":true,"max_connection_age":"0s","max_connection_age_grace":"0s","max_connection_idle":"0s","min_ping_interval":"20s","permit_without_stream":true,"time":"30s","timeout":"10s"},"listen_addr":":8080","max_receive_message_bytes":1048576,"request_timeout":{"post_action":"8s","pre_check":"8s","workspace":"15s"},"shutdown_timeout":"10s"}`},
		{path: "grpc.keepalive.time", typeName: "duration", defaultJS: `"30s"`},
		{path: "embedding.api_keys", typeName: "array", itemType: "string", sensitive: true},
		{path: "embedding.params", typeName: "map", valueType: "any"},
		{path: "embedding.model_params", typeName: "map", valueType: "map"},
		{path: "llm.routes", typeName: "array", itemType: "object"},
		{path: "llm.routes[].provider", typeName: "string", enum: []string{"openai", "openai_native", "openai_go", "google_ai_studio", "openrouter"}},
		{path: "memory_pipeline.min_similarity_score", typeName: "number", nullable: true},
		{path: "management.access_token", typeName: "string", sensitive: true},
		{path: "storage.local_data_root", typeName: "string"},
		{path: "logging.directory", typeName: "string"},
		{path: "postgres.dsn", typeName: "string", sensitive: true},
	}
	for _, test := range tests {
		field, ok := findSchemaField(schema, test.path)
		if !ok {
			t.Fatalf("schema field %q is missing", test.path)
		}
		if field.Type != test.typeName {
			t.Fatalf("schema field %q type = %q, want %q", test.path, field.Type, test.typeName)
		}
		if field.ItemType != test.itemType {
			t.Fatalf("schema field %q item type = %q, want %q", test.path, field.ItemType, test.itemType)
		}
		if field.ValueType != test.valueType {
			t.Fatalf("schema field %q value type = %q, want %q", test.path, field.ValueType, test.valueType)
		}
		if field.Nullable != test.nullable {
			t.Fatalf("schema field %q nullable = %v, want %v", test.path, field.Nullable, test.nullable)
		}
		if field.Sensitive != test.sensitive {
			t.Fatalf("schema field %q sensitive = %v, want %v", test.path, field.Sensitive, test.sensitive)
		}
		if len(test.enum) > 0 && strings.Join(field.Enum, "\x00") != strings.Join(test.enum, "\x00") {
			t.Fatalf("schema field %q enum = %v, want %v", test.path, field.Enum, test.enum)
		}
		if test.defaultJS != "" && string(field.Default) != test.defaultJS {
			t.Fatalf("schema field %q default = %s, want %s", test.path, field.Default, test.defaultJS)
		}
		if test.sensitive && len(field.Default) != 0 {
			t.Fatalf("schema field %q unexpectedly contains a default", test.path)
		}
	}

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if strings.Contains(string(encoded), "api-key") || strings.Contains(string(encoded), "password") {
		t.Fatalf("schema unexpectedly contains a credential-shaped value: %s", encoded)
	}
	if !ConfigSchemaHasPath("llm.routes[3].provider") {
		t.Fatal("concrete route path should match the reflected route schema")
	}
	if !ConfigSchemaHasPath("embedding.api_keys[0]") {
		t.Fatal("concrete API key item path should match its array field")
	}
	if ConfigSchemaHasPath("unknown.field") {
		t.Fatal("unknown schema path unexpectedly matched")
	}
}

// TestWriteConfigSchemaIsDeterministic verifies repeated schema writes are byte-stable for installer caching and tests.
// TestWriteConfigSchemaIsDeterministic 用于验证重复写出的 schema 字节稳定，便于安装器缓存和测试。
func TestWriteConfigSchemaIsDeterministic(t *testing.T) {
	var first bytes.Buffer
	var second bytes.Buffer
	if err := WriteConfigSchema(&first); err != nil {
		t.Fatalf("write first schema: %v", err)
	}
	if err := WriteConfigSchema(&second); err != nil {
		t.Fatalf("write second schema: %v", err)
	}
	if first.String() != second.String() {
		t.Fatal("schema output changed between identical writes")
	}
}

// findSchemaField returns one descriptor by its stable schema path.
// findSchemaField 按稳定 schema 路径返回一个字段描述。
func findSchemaField(schema ConfigSchemaDocument, path string) (ConfigFieldSchema, bool) {
	for _, field := range schema.Fields {
		if field.Path == path {
			return field, true
		}
	}
	return ConfigFieldSchema{}, false
}
