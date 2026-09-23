// effective_test.go verifies the diagnostic projection without modifying the loaded runtime configuration.
// effective_test.go 验证诊断投影不会修改已加载的运行时配置。
package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestEffectiveConfigOmitsNestedSecrets checks every sensitive field family and retains ordinary values in the export.
// TestEffectiveConfigOmitsNestedSecrets 检查各类敏感字段均被省略，普通配置值仍保留在导出结果中。
func TestEffectiveConfigOmitsNestedSecrets(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"management":{"access_token":"management-secret"},"logging":{"payload_encryption_key":"log-secret"},"postgres":{"dsn":"postgres-secret"},"llm":{"routes":[{"model":"visible-model","api_keys":["route-secret"],"nodes":[{"api_keys":["node-secret"]}]}]},"embedding":{"api_keys":["embedding-secret"],"nodes":[{"api_keys":["embedding-node-secret"]}]},"rerank":{"routes":[{"api_keys":["rerank-secret"],"nodes":[{"api_keys":["rerank-node-secret"]}]}]}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the fixture actually populated typed secret fields before testing their omission.
	// 检查省略行为前，先确认夹具确实填充了各个有类型的敏感字段。
	for _, secret := range []string{"management-secret", "log-secret", "postgres-secret", "route-secret", "node-secret", "embedding-secret", "embedding-node-secret", "rerank-secret", "rerank-node-secret"} {
		if !strings.Contains(string(before), secret) {
			t.Fatalf("secret fixture did not populate %s", secret)
		}
	}
	var output bytes.Buffer
	if err := WriteEffectiveConfig(&output, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "api_keys") || !strings.Contains(output.String(), "visible-model") {
		t.Fatal("effective projection leaked sensitive values or lost ordinary model values")
	}
	var result EffectiveConfigDocument
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Version != "v1" || !result.Redacted || len(result.Config) == 0 {
		t.Fatalf("invalid export envelope: %v", err)
	}
	after, err := json.Marshal(cfg)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("diagnostic export changed the runtime configuration")
	}
}
