// Boundary regressions exercise typed duration parsing and complete AI route replacement through the config loader.
// 配置层边界回归通过真实加载器验证时长解析和完整 AI 路由替换。
package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDurationRejectsMillisecondOverflow preserves the destination on failure and accepts exact representable boundaries.
// TestDurationRejectsMillisecondOverflow 验证溢出时保留原值，并接受精确可表示的边界。
func TestDurationRejectsMillisecondOverflow(t *testing.T) {
	for _, millis := range []int64{math.MaxInt64/int64(time.Millisecond) + 1, math.MinInt64/int64(time.Millisecond) - 1, 18446744073710, -18446744073710} {
		duration := Duration{time.Second}
		err := json.Unmarshal([]byte(strconv.FormatInt(millis, 10)), &duration)
		if err == nil || duration.Duration != time.Second {
			t.Fatalf("overflow %d accepted or changed destination: duration=%v err=%v", millis, duration.Duration, err)
		}
	}
	for _, millis := range []int64{0, 1, -1, math.MaxInt64 / int64(time.Millisecond), math.MinInt64 / int64(time.Millisecond)} {
		var duration Duration
		if err := json.Unmarshal([]byte(strconv.FormatInt(millis, 10)), &duration); err != nil || duration.Duration != time.Duration(millis)*time.Millisecond {
			t.Fatalf("valid milliseconds %d: duration=%v err=%v", millis, duration.Duration, err)
		}
	}
}

// TestLoadRejectsWrappedDuration verifies a huge numeric YAML timeout cannot wrap into a valid short timeout.
// TestLoadRejectsWrappedDuration 验证 YAML 中的超大数值超时不会回绕成有效短超时。
func TestLoadRejectsWrappedDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(path, []byte("grpc:\n  request_timeout:\n    workspace: 18446744073710\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, newValidConfigForTest()); err == nil {
		t.Fatal("overflow timeout accepted")
	}
}

// TestLoadReplacesCompleteAIRoutes verifies new providers cannot inherit credentials, endpoints or params by array index.
// TestLoadReplacesCompleteAIRoutes 验证新供应商不会按数组下标继承旧凭据、端点或参数。
func TestLoadReplacesCompleteAIRoutes(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes[0].Endpoint = "https://old.invalid/llm"
	cfg.LLM.Routes[0].Nodes = []AIRoutingNodeConfig{{Name: "old", APIKeys: []string{"old-node-key"}}}
	cfg.Rerank.Routes[0].Endpoint = "https://old.invalid/rerank"
	cfg.Rerank.Routes[0].Nodes = []AIRoutingNodeConfig{{Name: "old", APIKeys: []string{"old-rerank-key"}}}
	cfg.Rerank.Routes[0].Params = map[string]any{"old_param": true}
	path := filepath.Join(t.TempDir(), "override.yaml")
	body := "llm:\n  routes:\n    - provider: google_ai_studio\n      model: new-model\n      api_keys: [new-key]\nrerank:\n  routes:\n    - provider: siliconflow\n      model: new-rerank\n      api_keys: [new-rerank-key]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	llm := loaded.LLM.Routes[0]
	rerank := loaded.Rerank.Routes[0]
	if llm.Endpoint != "" || len(llm.Params) != 0 || len(llm.Nodes) != 1 || strings.Join(llm.Nodes[0].APIKeys, ",") != "new-key" {
		t.Fatalf("LLM route inherited old state: endpoint=%q params=%v nodes=%v", llm.Endpoint, llm.Params, llm.Nodes)
	}
	if rerank.Endpoint != defaultSiliconFlowRerankEndpoint || len(rerank.Params) != 0 || len(rerank.Nodes) != 1 || strings.Join(rerank.Nodes[0].APIKeys, ",") != "new-rerank-key" {
		t.Fatalf("rerank route inherited old state: endpoint=%q params=%v nodes=%v", rerank.Endpoint, rerank.Params, rerank.Nodes)
	}
}

// TestLoadReplacementRouteRequiresOwnCredentials ensures an incomplete replacement is rejected even when the previous route was valid.
// TestLoadReplacementRouteRequiresOwnCredentials 验证前一条路由有效时，不完整的替换路由仍须被拒绝。
func TestLoadReplacementRouteRequiresOwnCredentials(t *testing.T) {
	for _, section := range []string{"llm", "rerank"} {
		t.Run(section, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "override.yaml")
			body := section + ":\n  routes:\n    - provider: openrouter\n      model: new-model\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, newValidConfigForTest()); err == nil || !strings.Contains(err.Error(), section+".routes[0]") {
				t.Fatalf("replacement without credentials accepted: %v", err)
			}
		})
	}
}

// TestLoadReplacesEmbeddingNodes verifies an explicit node list cannot inherit an earlier node's keys or quotas; each loaded node must validate its own credentials.
// TestLoadReplacesEmbeddingNodes 验证显式节点列表不会继承旧节点的密钥或配额，每个新节点必须校验自己的凭据。
func TestLoadReplacesEmbeddingNodes(t *testing.T) {
	for _, withKey := range []bool{false, true} {
		t.Run(strconv.FormatBool(withKey), func(t *testing.T) {
			cfg := newValidConfigForTest()
			cfg.Embedding.APIKeys = nil
			cfg.Embedding.Nodes = []AIRoutingNodeConfig{{Name: "old", APIKeys: []string{"old-secret"}, RPM: 99}}
			body := "embedding:\n  nodes:\n    - name: new-node\n"
			if withKey {
				body += "      api_keys: [new-secret]\n"
			}
			path := filepath.Join(t.TempDir(), "override.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path, cfg)
			if !withKey {
				if err == nil || !strings.Contains(err.Error(), "embedding.nodes[0].api_keys") {
					t.Fatalf("node without its own key accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := loaded.Embedding.Nodes[0]; got.Name != "new-node" || got.RPM != 0 || strings.Join(got.APIKeys, ",") != "new-secret" {
				t.Fatalf("replacement node inherited old state: %#v", got)
			}
		})
	}
}
