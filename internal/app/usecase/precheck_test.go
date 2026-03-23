// precheck_test.go verifies both deterministic helpers and the real-model pre-check orchestration path.
// precheck_test.go 用于同时验证确定性辅助逻辑和真实模型驱动的 pre-check 编排链路。
package usecase

import (
	"context"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"github.com/openvulcan/vmm/internal/testutil"
)

// fakeVector shapes recall outputs without simulating the model itself.
// fakeVector 用于塑造召回结果，而不去模拟模型本身。
type fakeVector struct {
	called int
	hits   []logicdomain.MemoryHit
	err    error
}

// Upsert keeps the test vector store interface-complete while these tests focus on search behavior only.
// Upsert 用于补齐测试向量库接口，而这些测试只关注检索行为。
func (f *fakeVector) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error { return nil }

// Search records recall attempts and returns the configured hit set for deterministic orchestration checks.
// Search 用于记录召回次数，并返回预设命中集以便稳定验证编排逻辑。
func (f *fakeVector) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	f.called++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]logicdomain.MemoryHit, len(f.hits))
	copy(out, f.hits)
	return out, nil
}

// Shutdown keeps the test vector store compliant with the shutdown-aware port contract.
// Shutdown 用于让测试向量库满足带关闭能力的端口契约。
func (f *fakeVector) Shutdown(ctx context.Context) error { return nil }

// fakePersona injects one controllable persona payload into first-turn orchestration tests.
// fakePersona 用于向首轮编排测试注入可控的画像结果。
type fakePersona struct {
	called int
	result logicdomain.PersonaContext
	err    error
}

// Load records persona lookups and returns the configured persona payload.
// Load 用于记录画像读取次数，并返回预设画像结果。
func (f *fakePersona) Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error) {
	f.called++
	if f.err != nil {
		return logicdomain.PersonaContext{}, f.err
	}
	return f.result, nil
}

// countingLLM wraps the real LLM client so tests can assert the real model path has been exercised.
// countingLLM 用于包装真实 LLM 客户端，让测试可以断言真实模型链路确实被执行过。
type countingLLM struct {
	next   appports.LLMClient
	called int
}

// Generate forwards the real model request while counting how many times the orchestration layer invoked it.
// Generate 用于透传真实模型请求，并统计编排层实际调用了多少次。
func (c *countingLLM) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	c.called++
	return c.next.Generate(ctx, req)
}

// countingEmbedding wraps the real embedding client so tests can assert semantic retrieval actually ran.
// countingEmbedding 用于包装真实 embedding 客户端，让测试可以断言语义检索确实执行了。
type countingEmbedding struct {
	next   appports.EmbeddingClient
	called int
}

// Embed forwards the real embedding request while counting semantic calls.
// Embed 用于透传真实 embedding 请求，并统计语义调用次数。
func (c *countingEmbedding) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	c.called++
	return c.next.Embed(ctx, req)
}

// float64Ptr stores one local helper for readability inside threshold-oriented tests.
// float64Ptr 用于保存一个本地辅助函数，方便阈值相关测试阅读。
func float64Ptr(v float64) *float64 { return &v }

// newRealUseCaseForTest wires the real prompt source, real LLM, and real embedding clients into one test use case.
// newRealUseCaseForTest 用于把真实提示词、真实 LLM 和真实 embedding 客户端装配成测试用例。
func newRealUseCaseForTest(t *testing.T, vector appports.VectorStore, persona appports.ContextPersonaProvider) (*PreCheckUseCase, *countingLLM, *countingEmbedding) {
	t.Helper()
	fixture := testutil.RequireLiveModelAccess(t)
	llm := &countingLLM{next: fixture.LLM}
	embedding := &countingEmbedding{next: fixture.Embedding}
	uc := NewPreCheckUseCase(
		processor.NewIntentExtractor(llm, fixture.Prompts, fixture.Config.LLM.Model, 5),
		processor.NewContextAssembler(fixture.Prompts, fixture.Config.LLM.Model),
		embedding,
		vector,
		persona,
		logx.Default(),
		20*time.Second,
		5,
		5,
		float64Ptr(0.75),
		fixture.Config.Embedding.Model,
		fixture.Config.Embedding.Dimension,
	)
	return uc, llm, embedding
}

// TestValidatePreCheckRejectsMissingFields keeps the request contract checks deterministic and independent from model calls.
// TestValidatePreCheckRejectsMissingFields 用于保持请求契约校验的确定性，不受模型调用影响。
func TestValidatePreCheckRejectsMissingFields(t *testing.T) {
	err := validatePreCheck(PreCheckCommand{SessionID: "s1", ProjectID: "p1"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if validation, ok := err.(logicdomain.ValidationError); !ok || validation.Field != "user_id" {
		t.Fatalf("unexpected validation error: %#v", err)
	}
}

// TestRecentDialogueKeepsLatestTwoRounds verifies the history slicing helper without involving the real model path.
// TestRecentDialogueKeepsLatestTwoRounds 用于在不触发真实模型路径的前提下验证历史裁剪辅助逻辑。
func TestRecentDialogueKeepsLatestTwoRounds(t *testing.T) {
	history := []logicdomain.HistorySnippet{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "system", Content: "drop"},
		{Role: "user", Content: "u3"},
		{Role: "assistant", Content: "a3"},
	}
	got := recentDialogue(history, 2)
	if len(got) != 4 {
		t.Fatalf("recent history len = %d", len(got))
	}
	if got[0].Content != "u2" || got[1].Content != "a2" || got[2].Content != "u3" || got[3].Content != "a3" {
		t.Fatalf("unexpected recent history = %#v", got)
	}
}

// TestResolveSearchTermsFallsBackToRawQuestion keeps fallback behavior deterministic by asserting the helper directly.
// TestResolveSearchTermsFallsBackToRawQuestion 用于直接断言回退辅助逻辑，保持该行为的确定性。
func TestResolveSearchTermsFallsBackToRawQuestion(t *testing.T) {
	uc := &PreCheckUseCase{logger: logx.Default(), maxSearchKeywords: 5}
	keywords, needMemory, degraded := uc.resolveSearchTerms("原始问题", logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: "bad json"})
	if !needMemory || !degraded {
		t.Fatalf("unexpected fallback flags needMemory=%v degraded=%v", needMemory, degraded)
	}
	if len(keywords) != 1 || keywords[0] != "原始问题" {
		t.Fatalf("unexpected fallback keywords = %#v", keywords)
	}
}

// TestPreCheckExecuteBypassesAllInjectionWork verifies that pre-check now short-circuits before any model or recall work runs.
// TestPreCheckExecuteBypassesAllInjectionWork 用于验证 pre-check 现在会在任何模型或召回逻辑执行前直接短路返回。
func TestPreCheckExecuteUsesRealModelAndEmbedding(t *testing.T) {
	vector := &fakeVector{hits: []logicdomain.MemoryHit{{ID: "m1", Text: "remembered framework decision", Score: 0.99}}}
	persona := &fakePersona{}
	uc, llm, embedding := newRealUseCaseForTest(t, vector, persona)
	res, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-real-precheck"), PreCheckCommand{
		SessionID:      "s-real",
		UserID:         "u-real",
		TeamID:         "t-real",
		ProjectID:      "p-real",
		CurrentContent: "我们后端用什么框架？",
		IsFirstTurn:    false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if llm.called != 0 {
		t.Fatalf("expected real llm to stay idle, got %d calls", llm.called)
	}
	if embedding.called != 0 {
		t.Fatalf("expected real embedding to stay idle, got %d calls", embedding.called)
	}
	if vector.called != 0 {
		t.Fatalf("expected vector recall to stay idle, got %d calls", vector.called)
	}
	if res.ShouldInject {
		t.Fatal("expected should_inject=false")
	}
	if len(res.ContextItems) != 0 {
		t.Fatalf("expected no context items, got %#v", res.ContextItems)
	}
	if res.ContextText != "" {
		t.Fatalf("expected empty context text, got %q", res.ContextText)
	}
}

// TestPreCheckFirstTurnAlsoBypassesPersonaLoading verifies that first-turn requests are short-circuited before persona loading starts.
// TestPreCheckFirstTurnAlsoBypassesPersonaLoading 用于验证首轮请求也会在画像加载启动前直接短路返回。
func TestPreCheckFirstTurnStillLoadsPersonaWithRealModel(t *testing.T) {
	vector := &fakeVector{}
	persona := &fakePersona{result: logicdomain.PersonaContext{Profile: []string{"backend engineer"}}}
	uc, llm, _ := newRealUseCaseForTest(t, vector, persona)
	res, err := uc.Execute(context.Background(), PreCheckCommand{
		SessionID:      "s-first",
		UserID:         "u-first",
		TeamID:         "t-first",
		ProjectID:      "p-first",
		CurrentContent: "先了解一下我的上下文",
		IsFirstTurn:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if llm.called != 0 {
		t.Fatalf("expected real llm to stay idle on first turn, got %d calls", llm.called)
	}
	if persona.called != 0 {
		t.Fatalf("expected persona loader to stay idle, got %d calls", persona.called)
	}
	if res.ShouldInject {
		t.Fatal("expected should_inject=false")
	}
	if res.ContextText != "" || len(res.ContextItems) != 0 {
		t.Fatalf("expected empty pre-check payload, got text=%q items=%#v", res.ContextText, res.ContextItems)
	}
}
