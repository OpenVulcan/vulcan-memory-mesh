// realruntime_test.go verifies the real-runtime fixture helpers accept every supported AI key declaration shape without depending on live upstream access.
// realruntime_test.go 用于验证真实运行时测试基座辅助逻辑能够接受所有受支持的 AI Key 声明形态，而不依赖真实上游可用性。
package testutil

import (
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes verifies the fixture can extract one usable key from both top-level pools and node-only declarations.
// TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes 用于验证测试基座既能从顶层 Key 池提取可用 Key，也能从仅节点声明里提取可用 Key。
func TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes(t *testing.T) {
	tests := []struct {
		name    string
		apiKeys []string
		nodes   []config.AIRoutingNodeConfig
		want    string
		ok      bool
	}{
		{
			name:    "top-level pool",
			apiKeys: []string{"top-level-key"},
			want:    "top-level-key",
			ok:      true,
		},
		{
			name: "node-only pool",
			nodes: []config.AIRoutingNodeConfig{
				{Name: "primary", APIKeys: []string{"node-key-a", "node-key-b"}},
			},
			want: "node-key-a",
			ok:   true,
		},
		{
			name: "missing key",
			nodes: []config.AIRoutingNodeConfig{
				{Name: "broken"},
			},
			want: "",
			ok:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := firstRoutingAPIKey(tc.apiKeys, tc.nodes)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("firstRoutingAPIKey() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
