// maintenance_test.go verifies that one-shot maintenance preserves the runtime's exact inference authority.
// maintenance_test.go 用于验证一次性维护会保留运行时的准确推理权威。
package app

import (
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vulcan_inference"
	"github.com/openvulcan/vmm/internal/config"
)

// TestBuildMaintenanceEmbeddingUsesManagedInference verifies managed rebuild never consults standalone provider key pools.
// TestBuildMaintenanceEmbeddingUsesManagedInference 用于验证托管重建绝不会读取独立供应商密钥池。
func TestBuildMaintenanceEmbeddingUsesManagedInference(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.APIKeys = nil
	managed := &config.ManagedConfig{
		Parent: config.ManagedParent{
			ProcessID:       1,
			StartedAtUnixMS: 1,
		},
		Runtime: config.ManagedRuntime{
			Inference: config.ManagedInferenceConfig{
				DiscoveryFile:      "managed-inference.json",
				ExpectedCallerID:   "vmm-managed",
				ConsumerProfileID:  "default",
				StartupTimeout:     config.Duration{Duration: time.Second},
				EmbeddingDimension: cfg.Embedding.Dimension,
			},
		},
	}

	embedding, err := buildMaintenanceEmbedding(cfg, managed)
	if err != nil {
		t.Fatalf("buildMaintenanceEmbedding returned error: %v", err)
	}
	if _, ok := embedding.(*vulcan_inference.Client); !ok {
		t.Fatalf("managed embedding type = %T, want *vulcan_inference.Client", embedding)
	}
}
