// noise_gate_test.go implements tests for the hybrid regex-and-semantic memory admission gate.
// noise_gate_test.go 用于实现结合正则和语义的记忆准入门控器测试。
package processor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNoiseGateRegexBlocksAssistantDenial verifies the TestNoiseGateRegexBlocksAssistantDenial behavior.
// TestNoiseGateRegexBlocksAssistantDenial 用于验证 TestNoiseGateRegexBlocksAssistantDenial 行为。
func TestNoiseGateRegexBlocksAssistantDenial(t *testing.T) {
	gate := mustBuildNoiseGate(t, mustRuleBundles{
		common: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"denial_response","targets":["assistant"],"patterns":["我不记得"]}
  ]
}`,
	})
	decision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "部署方案是什么",
		AssistantReply: "我不记得这件事",
	})
	if decision.Allow {
		t.Fatal("expected denial response to be rejected")
	}
	if decision.Category != "denial_response" || decision.ReasonCode != NoiseReasonRegex {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

// TestNoiseGateSemanticBlocksMetaQuestion verifies the TestNoiseGateSemanticBlocksMetaQuestion behavior.
// TestNoiseGateSemanticBlocksMetaQuestion 用于验证 TestNoiseGateSemanticBlocksMetaQuestion 行为。
func TestNoiseGateSemanticBlocksMetaQuestion(t *testing.T) {
	gate := mustBuildNoiseGate(t, mustRuleBundles{
		common: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {
      "name":"meta_question",
      "targets":["user"],
      "threshold":0.80,
      "phrases":["你还记得我之前说过的内容吗"]
    }
  ]
}`,
	})
	decision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "你还记得我之前说过的内容吗",
		AssistantReply: "记得。",
	})
	if decision.Allow {
		t.Fatal("expected semantic match to be rejected")
	}
	if decision.Category != "meta_question" || decision.ReasonCode != NoiseReasonSemantic {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

// TestNoiseGateUserCommonOverridesSystemCommon verifies the TestNoiseGateUserCommonOverridesSystemCommon behavior.
// TestNoiseGateUserCommonOverridesSystemCommon 用于验证 TestNoiseGateUserCommonOverridesSystemCommon 行为。
func TestNoiseGateUserCommonOverridesSystemCommon(t *testing.T) {
	gate := mustBuildNoiseGate(t, mustRuleBundles{
		common: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"denial_response","targets":["assistant"],"patterns":["system denial"]}
  ]
}`,
		userCommon: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"denial_response","targets":["assistant"],"patterns":["user denial"]}
  ]
}`,
	})
	systemDecision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "test",
		AssistantReply: "system denial",
	})
	if !systemDecision.Allow {
		t.Fatalf("expected system common rule to be replaced, got %+v", systemDecision)
	}
	userDecision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "test",
		AssistantReply: "user denial",
	})
	if userDecision.Allow {
		t.Fatalf("expected user common rule to be active, got %+v", userDecision)
	}
}

// TestNoiseGateLanguageOverridesCommonCategory verifies the TestNoiseGateLanguageOverridesCommonCategory behavior.
// TestNoiseGateLanguageOverridesCommonCategory 用于验证 TestNoiseGateLanguageOverridesCommonCategory 行为。
func TestNoiseGateLanguageOverridesCommonCategory(t *testing.T) {
	gate := mustBuildNoiseGate(t, mustRuleBundles{
		common: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"patterns":["hello"]}
  ]
}`,
		lang: `{
  "language":"zh-CN",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"patterns":["你还记得"]}
  ]
}`,
	})
	englishDecision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "hello",
		AssistantReply: "ok",
	})
	if !englishDecision.Allow {
		t.Fatalf("expected language category to replace common category, got %+v", englishDecision)
	}
	chineseDecision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "你还记得吗",
		AssistantReply: "ok",
	})
	if chineseDecision.Allow {
		t.Fatalf("expected language category to block chinese meta question, got %+v", chineseDecision)
	}
}

// TestNoiseGateSemanticFallbackAllowsWhenEmbeddingFails verifies the TestNoiseGateSemanticFallbackAllowsWhenEmbeddingFails behavior.
// TestNoiseGateSemanticFallbackAllowsWhenEmbeddingFails 用于验证 TestNoiseGateSemanticFallbackAllowsWhenEmbeddingFails 行为。
func TestNoiseGateSemanticFallbackAllowsWhenEmbeddingFails(t *testing.T) {
	root := t.TempDir()
	systemDir := filepath.Join(root, "system")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "common.json"), []byte(`{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"threshold":0.8,"phrases":["你还记得我之前说过的内容吗"]}
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	embed := memory_mock.NewEmbeddingClient(64)
	embed.ForceError = context.DeadlineExceeded
	gate, err := NewNoiseGate(context.Background(), embed, nil, NoiseGateConfig{
		SystemDir:         systemDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             "mock",
		Dimension:         64,
	})
	if err != nil {
		t.Fatalf("new noise gate: %v", err)
	}
	decision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
		UserMessage:    "你还记得我之前说过的内容吗",
		AssistantReply: "记得。",
	})
	if !decision.Allow || decision.ReasonCode != NoiseReasonAllow {
		t.Fatalf("expected regex-only fallback to allow when semantic preload fails, got %+v", decision)
	}
}

// TestNoiseGateConcurrentAllowTurn verifies the TestNoiseGateConcurrentAllowTurn behavior.
// TestNoiseGateConcurrentAllowTurn 用于验证 TestNoiseGateConcurrentAllowTurn 行为。
func TestNoiseGateConcurrentAllowTurn(t *testing.T) {
	gate := mustBuildNoiseGate(t, mustRuleBundles{
		common: `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"patterns":["你还记得"]}
  ]
}`,
	})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				decision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{
					UserMessage:    "你还记得我上次说过的内容吗",
					AssistantReply: "我不记得",
				})
				if decision.Allow {
					t.Errorf("expected concurrent decision to stay blocked")
					return
				}
			}
		}()
	}
	wg.Wait()
}

type mustRuleBundles struct {
	common     string
	userCommon string
	lang       string
	userLang   string
}

// mustBuildNoiseGate creates one gate with temp rule bundles for processor tests.
// mustBuildNoiseGate 用于为处理器测试创建带临时规则包的门控器。
func mustBuildNoiseGate(t *testing.T, bundles mustRuleBundles) *NoiseGate {
	t.Helper()
	root := t.TempDir()
	systemDir := filepath.Join(root, "system")
	userDir := filepath.Join(root, "user")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(bundles.common) != "" {
		if err := os.WriteFile(filepath.Join(systemDir, "common.json"), []byte(bundles.common), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.TrimSpace(bundles.userCommon) != "" {
		if err := os.WriteFile(filepath.Join(userDir, "common.json"), []byte(bundles.userCommon), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.TrimSpace(bundles.lang) != "" {
		if err := os.WriteFile(filepath.Join(systemDir, "zh-CN.json"), []byte(bundles.lang), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.TrimSpace(bundles.userLang) != "" {
		if err := os.WriteFile(filepath.Join(userDir, "zh-CN.json"), []byte(bundles.userLang), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gate, err := NewNoiseGate(context.Background(), memory_mock.NewEmbeddingClient(64), nil, NoiseGateConfig{
		SystemDir:         systemDir,
		UserDir:           userDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             "mock",
		Dimension:         64,
	})
	if err != nil {
		t.Fatalf("new noise gate: %v", err)
	}
	return gate
}
