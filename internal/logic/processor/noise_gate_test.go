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
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
	"github.com/openvulcan/vmm/internal/testutil"
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
	testutil.RequireLiveModelAccess(t)
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
	embed := errorEmbeddingClient{err: context.DeadlineExceeded}
	gate, err := NewNoiseGate(context.Background(), embed, nil, NoiseGateConfig{
		SystemDir:         systemDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             "text-embedding-3-large",
		Dimension:         1024,
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

// TestNoiseGateUsesCachedSemanticPrototypes verifies startup can restore semantic vectors without calling live embedding.
// TestNoiseGateUsesCachedSemanticPrototypes 用于验证启动阶段可以直接恢复语义缓存，而无需调用实时 embedding。
func TestNoiseGateUsesCachedSemanticPrototypes(t *testing.T) {
	root := t.TempDir()
	systemDir := filepath.Join(root, "system")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	common := `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"threshold":0.80,"phrases":["你还记得我之前说过的内容吗"]}
  ]
}`
	if err := os.WriteFile(filepath.Join(systemDir, "common.json"), []byte(common), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rulesHash, err := loadCompiledNoiseCategories(systemDir, "", "zh-CN", 0.88)
	if err != nil {
		t.Fatal(err)
	}
	cache := &fakeNoiseEmbeddingCache{
		entries: []logicdomain.NoiseEmbeddingCacheEntry{{
			Scope:        "noise_gate",
			Language:     "zh-CN",
			CategoryName: "meta_question",
			Phrase:       "你还记得我之前说过的内容吗",
			Model:        "embed-model",
			Dimension:    1024,
			RulesHash:    rulesHash,
			Vector:       []float32{0.6, 0.8},
			UpdatedAt:    time.Now().UTC(),
		}},
	}
	embed := &countingNoiseEmbeddingClient{vector: []float32{0.6, 0.8}}
	gate, err := NewNoiseGate(context.Background(), embed, nil, NoiseGateConfig{
		SystemDir:         systemDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             "embed-model",
		Dimension:         1024,
		Cache:             cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if embed.called != 0 {
		t.Fatalf("expected cached bootstrap to skip live embedding, called=%d", embed.called)
	}
	decision := gate.AllowTurn(context.Background(), logicdomain.NormalizedTurn{UserMessage: "你还记得我之前说过的内容吗", AssistantReply: "记得"})
	if decision.Allow || decision.ReasonCode != NoiseReasonSemantic {
		t.Fatalf("expected semantic cache hit to block, got %+v", decision)
	}
}

// TestNoiseGateRecomputesSemanticPrototypesWhenModelChanges verifies model or dimension changes invalidate cache reuse.
// TestNoiseGateRecomputesSemanticPrototypesWhenModelChanges 用于验证模型或维度变化会使缓存失效并触发重算。
func TestNoiseGateRecomputesSemanticPrototypesWhenModelChanges(t *testing.T) {
	root := t.TempDir()
	systemDir := filepath.Join(root, "system")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	common := `{
  "language":"common",
  "version":"1.0.0",
  "categories":[
    {"name":"meta_question","targets":["user"],"threshold":0.80,"phrases":["你还记得我之前说过的内容吗"]}
  ]
}`
	if err := os.WriteFile(filepath.Join(systemDir, "common.json"), []byte(common), 0o644); err != nil {
		t.Fatal(err)
	}
	_, oldRulesHash, err := loadCompiledNoiseCategories(systemDir, "", "zh-CN", 0.88)
	if err != nil {
		t.Fatal(err)
	}
	cache := &fakeNoiseEmbeddingCache{
		entries: []logicdomain.NoiseEmbeddingCacheEntry{{
			Scope:        "noise_gate",
			Language:     "zh-CN",
			CategoryName: "meta_question",
			Phrase:       "你还记得我之前说过的内容吗",
			Model:        "old-model",
			Dimension:    1024,
			RulesHash:    oldRulesHash,
			Vector:       []float32{1, 0},
			UpdatedAt:    time.Now().UTC(),
		}},
	}
	embed := &countingNoiseEmbeddingClient{vector: []float32{0.6, 0.8}}
	_, err = NewNoiseGate(context.Background(), embed, nil, NoiseGateConfig{
		SystemDir:         systemDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             "new-model",
		Dimension:         1536,
		Cache:             cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if embed.called == 0 {
		t.Fatal("expected model change to trigger live embedding rebuild")
	}
	if cache.replaceCount != 1 {
		t.Fatalf("expected cache to be refreshed once, got %d", cache.replaceCount)
	}
	if cache.replaceQuery.Model != "new-model" || cache.replaceQuery.Dimension != 1536 {
		t.Fatalf("unexpected refresh query = %+v", cache.replaceQuery)
	}
}

type mustRuleBundles struct {
	common     string
	userCommon string
	lang       string
	userLang   string
}

// fakeNoiseEmbeddingCache keeps a deterministic in-memory cache for startup cache reuse tests.
// fakeNoiseEmbeddingCache 用于保存确定性的内存缓存，供启动缓存复用测试使用。
type fakeNoiseEmbeddingCache struct {
	loadQuery    logicdomain.NoiseEmbeddingCacheQuery
	replaceQuery logicdomain.NoiseEmbeddingCacheQuery
	entries      []logicdomain.NoiseEmbeddingCacheEntry
	replaceCount int
}

// LoadNoiseEmbeddingCache returns the matching cache rows for the requested fingerprint.
// LoadNoiseEmbeddingCache 用于返回请求指纹对应的缓存记录。
func (f *fakeNoiseEmbeddingCache) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	f.loadQuery = query
	loaded := make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(f.entries))
	for _, entry := range f.entries {
		if entry.Scope == query.Scope && entry.Language == query.Language && entry.Model == query.Model && entry.Dimension == query.Dimension && entry.RulesHash == query.RulesHash {
			loaded = append(loaded, entry)
		}
	}
	return loaded, nil
}

// ReplaceNoiseEmbeddingCache records the refreshed cache rows so tests can assert cache invalidation behavior.
// ReplaceNoiseEmbeddingCache 用于记录刷新后的缓存内容，便于测试断言缓存失效行为。
func (f *fakeNoiseEmbeddingCache) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	f.replaceQuery = query
	f.replaceCount++
	f.entries = append([]logicdomain.NoiseEmbeddingCacheEntry(nil), entries...)
	return nil
}

// countingNoiseEmbeddingClient returns one deterministic vector batch while recording how many live calls were made.
// countingNoiseEmbeddingClient 用于返回确定性的向量批次，并记录实时调用次数。
type countingNoiseEmbeddingClient struct {
	called int
	vector []float32
	err    error
}

// Embed returns deterministic vectors so semantic cache behavior can be asserted without network dependence.
// Embed 用于返回确定性向量，让语义缓存行为可以在无网络依赖下被断言。
func (c *countingNoiseEmbeddingClient) Embed(ctx context.Context, req logicports.EmbeddingRequest) (logicports.EmbeddingResponse, error) {
	c.called++
	if c.err != nil {
		return logicports.EmbeddingResponse{}, c.err
	}
	vectors := make([][]float32, 0, len(req.Texts))
	for range req.Texts {
		vectors = append(vectors, append([]float32(nil), c.vector...))
	}
	return logicports.EmbeddingResponse{Vectors: vectors}, nil
}

// errorEmbeddingClient forces one semantic bootstrap failure without depending on the removed in-memory embedding mock.
// errorEmbeddingClient 用于在不依赖已移除内存 embedding mock 的前提下强制制造一次语义预加载失败。
type errorEmbeddingClient struct{ err error }

// Embed returns the configured error so fallback behavior can be verified deterministically.
// Embed 用于返回预设错误，以便确定性验证回退行为。
func (c errorEmbeddingClient) Embed(ctx context.Context, req logicports.EmbeddingRequest) (logicports.EmbeddingResponse, error) {
	return logicports.EmbeddingResponse{}, c.err
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
	fixture := testutil.MustRealRuntimeFixture(t)
	gate, err := NewNoiseGate(context.Background(), fixture.Embedding, nil, NoiseGateConfig{
		SystemDir:         systemDir,
		UserDir:           userDir,
		DefaultLanguage:   "zh-CN",
		Enabled:           true,
		SemanticEnabled:   true,
		SemanticThreshold: 0.88,
		Model:             fixture.Config.Embedding.Model,
		Dimension:         fixture.Config.Embedding.Dimension,
	})
	if err != nil {
		t.Fatalf("new noise gate: %v", err)
	}
	return gate
}
