// openapi_test.go verifies the published management contract remains parseable and covers every live route.
// openapi_test.go 用于验证发布的管理契约始终可解析并覆盖全部实时路由。
package managementapi

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestManagementOpenAPIParsesAndCoversLiveRoutes protects consumers from documentation drift.
// TestManagementOpenAPIParsesAndCoversLiveRoutes 用于保护消费者免受文档漂移影响。
func TestManagementOpenAPIParsesAndCoversLiveRoutes(t *testing.T) {
	contractPath := filepath.Join("..", "..", "..", "..", "docs", "openapi", "vmm-management-v1.yaml")
	payload, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read management OpenAPI: %v", err)
	}
	var contract map[string]any
	if err := yaml.Unmarshal(payload, &contract); err != nil {
		t.Fatalf("parse management OpenAPI: %v", err)
	}
	paths, ok := contract["paths"].(map[string]any)
	if !ok {
		t.Fatalf("management OpenAPI paths have unexpected type %T", contract["paths"])
	}
	for _, path := range []string{
		"/capabilities",
		"/users",
		"/projects",
		"/sessions",
		"/sessions/{session_id}",
		"/sessions/{session_id}/turns",
		"/turns",
		"/turns/{turn_id}",
		"/turns/{turn_id}/content",
		"/removal-previews",
		"/removals",
		"/restores",
		"/purges",
		"/operations/{operation_id}",
		"/recycle-batches",
		"/recycle-batches/{batch_id}",
	} {
		if _, exists := paths[path]; !exists {
			t.Errorf("management OpenAPI is missing live path %s", path)
		}
	}
}
