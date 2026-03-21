// postaction_test.go implements application use case tests.
// postaction_test.go 用于实现应用用例层测试。
package usecase

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
)

// TestPostActionUseCaseDefaultsScopeFields verifies the TestPostActionUseCaseDefaultsScopeFields behavior.
// TestPostActionUseCaseDefaultsScopeFields 用于验证 TestPostActionUseCaseDefaultsScopeFields 行为。
func TestPostActionUseCaseDefaultsScopeFields(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), nil, store, nil)
	result, err := usecase.Execute(context.Background(), PostActionCommand{
		SessionID: "sess-1",
		RawMessagesSnapshot: []logicdomain.RawMessage{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "收到"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	meta, ok := store.Session("sess-1")
	if !ok {
		t.Fatal("expected session metadata")
	}
	if meta.Session.UserID != "default" || meta.Session.TeamID != "default" || meta.Session.ProjectID != "default" || meta.Session.SpaceID != "default" {
		t.Fatalf("unexpected default scope session=%+v", meta.Session)
	}
}

// TestPostActionUseCaseDropsNoiseTurns verifies the TestPostActionUseCaseDropsNoiseTurns behavior.
// TestPostActionUseCaseDropsNoiseTurns 用于验证 TestPostActionUseCaseDropsNoiseTurns 行为。
func TestPostActionUseCaseDropsNoiseTurns(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	gate := mustNewNoiseGate(t, `{
  "language": "common",
  "version": "1.0.0",
  "categories": [
    {
      "name": "meta_question",
      "targets": ["user"],
      "threshold": 0.88,
      "patterns": ["你还记得"]
    }
  ]
}`)
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), gate, store, nil)
	result, err := usecase.Execute(context.Background(), PostActionCommand{
		SessionID: "sess-noise",
		RawMessagesSnapshot: []logicdomain.RawMessage{
			{Role: "user", Content: "你还记得我上次说过什么吗"},
			{Role: "assistant", Content: "我不记得"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if turns := store.Turns("sess-noise"); len(turns) != 0 {
		t.Fatalf("expected no persisted turns, got %d", len(turns))
	}
}

// mustNewNoiseGate creates a minimal noise gate for use-case integration tests.
// mustNewNoiseGate 用于给用例集成测试创建一个最小噪声门控器。
func mustNewNoiseGate(t *testing.T, commonJSON string) *processor.NoiseGate {
	t.Helper()
	root := t.TempDir()
	systemDir := filepath.Join(root, "system")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "common.json"), []byte(commonJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	gate, err := processor.NewNoiseGate(context.Background(), memory_mock.NewEmbeddingClient(64), nil, processor.NoiseGateConfig{
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
	return gate
}
