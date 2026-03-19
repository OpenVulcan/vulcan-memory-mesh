// precheck_test.go implements application use cases.
// precheck_test.go 用于实现应用用例层。
package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// precheckPromptSource is a deterministic prompt source used to keep pre-check tests focused on orchestration.
// precheckPromptSource 用于作为确定性的提示词来源，让 pre-check 测试聚焦编排逻辑。
type precheckPromptSource struct{}

// GetPrompt executes the GetPrompt logic.
// GetPrompt 用于执行 GetPrompt 逻辑。
func (precheckPromptSource) GetPrompt(scene, modelName string) (string, error) {
	return scene + ":" + modelName, nil
}

// fakeLLM is a controllable LLM test double used to inject intent results and failures.
// fakeLLM 用于作为可控的 LLM 测试替身，注入意图结果和失败场景。
type fakeLLM struct {
	content string
	err     error
}

// Generate executes the Generate logic.
// Generate 用于执行 Generate 逻辑。
func (f fakeLLM) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if f.err != nil {
		return appports.LLMResponse{}, f.err
	}
	return appports.LLMResponse{Content: f.content}, nil
}

// fakeEmbedding is a controllable embedding test double used to observe recall inputs.
// fakeEmbedding 用于作为可控的 embedding 测试替身，观察召回输入。
type fakeEmbedding struct {
	called   int
	lastReq  appports.EmbeddingRequest
	response appports.EmbeddingResponse
	err      error
}

// Embed executes the Embed logic.
// Embed 用于执行 Embed 逻辑。
func (f *fakeEmbedding) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	f.called++
	f.lastReq = req
	if f.err != nil {
		return appports.EmbeddingResponse{}, f.err
	}
	if len(f.response.Vectors) > 0 {
		return f.response, nil
	}
	vectors := make([][]float32, 0, len(req.Texts))
	for range req.Texts {
		vectors = append(vectors, []float32{1, 0, 0})
	}
	return appports.EmbeddingResponse{Vectors: vectors}, nil
}

// fakeVector is a controllable vector store test double used to shape recall outputs.
// fakeVector 用于作为可控的向量库测试替身，塑造召回输出。
type fakeVector struct {
	called int
	hits   []logicdomain.MemoryHit
	err    error
}

// Upsert executes the Upsert logic.
// Upsert 用于执行 Upsert 逻辑。
func (f *fakeVector) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error { return nil }

// Search executes the Search logic.
// Search 用于执行 Search 逻辑。
func (f *fakeVector) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	f.called++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]logicdomain.MemoryHit, len(f.hits))
	copy(out, f.hits)
	return out, nil
}

// Shutdown executes the Shutdown logic.
// Shutdown 用于执行 Shutdown 逻辑。
func (f *fakeVector) Shutdown(ctx context.Context) error { return nil }

// fakePersona is a controllable persona provider test double used to verify first-turn orchestration.
// fakePersona 用于作为可控的画像提供器测试替身，验证首轮编排逻辑。
type fakePersona struct {
	called int
	result logicdomain.PersonaContext
	err    error
}

// Load loads related data.
// Load 用于加载相关数据。
func (f *fakePersona) Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error) {
	f.called++
	if f.err != nil {
		return logicdomain.PersonaContext{}, f.err
	}
	return f.result, nil
}

// float64Ptr executes the float64Ptr logic.
// float64Ptr 用于执行 float64Ptr 逻辑。
func float64Ptr(v float64) *float64 { return &v }

// newUseCaseForTest creates a UseCaseForTest instance.
// newUseCaseForTest 用于创建 UseCaseForTest 实例。
func newUseCaseForTest(t *testing.T, llm appports.LLMClient, embedding appports.EmbeddingClient, vector appports.VectorStore, persona appports.ContextPersonaProvider, maxKeywords int, minSimilarity *float64) *PreCheckUseCase {
	t.Helper()
	prompts := precheckPromptSource{}
	return NewPreCheckUseCase(
		processor.NewIntentExtractor(llm, prompts, "mock-intent", 10),
		processor.NewContextAssembler(prompts, "mock-intent"),
		embedding,
		vector,
		persona,
		nil,
		time.Second,
		5,
		maxKeywords,
		minSimilarity,
		"mock-embedding",
		3,
	)
}

// TestPreCheckSkipsMemoryWhenNeedMemoryFalse verifies the TestPreCheckSkipsMemoryWhenNeedMemoryFalse behavior.
// TestPreCheckSkipsMemoryWhenNeedMemoryFalse 用于验证 TestPreCheckSkipsMemoryWhenNeedMemoryFalse 行为。
func TestPreCheckSkipsMemoryWhenNeedMemoryFalse(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{}
	persona := &fakePersona{}
	uc := newUseCaseForTest(t, fakeLLM{content: `{"keywords":["go"],"need_memory":false,"reason":"not needed"}`}, embedding, vector, persona, 5, float64Ptr(0.75))
	res, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-1"), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "hello", IsFirstTurn: false})
	if err != nil {
		t.Fatal(err)
	}
	if res.Degraded {
		t.Fatal("unexpected degraded")
	}
	if embedding.called != 0 {
		t.Fatalf("embedding called %d times", embedding.called)
	}
	if vector.called != 0 {
		t.Fatalf("vector called %d times", vector.called)
	}
}

// TestPreCheckFallsBackToRawQuestionWhenIntentInvalid verifies the TestPreCheckFallsBackToRawQuestionWhenIntentInvalid behavior.
// TestPreCheckFallsBackToRawQuestionWhenIntentInvalid 用于验证 TestPreCheckFallsBackToRawQuestionWhenIntentInvalid 行为。
func TestPreCheckFallsBackToRawQuestionWhenIntentInvalid(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{hits: []logicdomain.MemoryHit{{ID: "m1", Text: "remember raw question", Score: 0.9}}}
	uc := newUseCaseForTest(t, fakeLLM{content: "not-json"}, embedding, vector, &fakePersona{}, 5, float64Ptr(0.75))
	res, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-2"), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "原始问题", IsFirstTurn: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded {
		t.Fatal("expected degraded=true")
	}
	if embedding.called != 1 {
		t.Fatalf("embedding called %d times", embedding.called)
	}
	if len(embedding.lastReq.Texts) != 1 || embedding.lastReq.Texts[0] != "原始问题" {
		t.Fatalf("embedding req texts = %#v", embedding.lastReq.Texts)
	}
	if !res.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
}

// TestPreCheckTruncatesKeywordsAndFiltersLowSimilarity verifies the TestPreCheckTruncatesKeywordsAndFiltersLowSimilarity behavior.
// TestPreCheckTruncatesKeywordsAndFiltersLowSimilarity 用于验证 TestPreCheckTruncatesKeywordsAndFiltersLowSimilarity 行为。
func TestPreCheckTruncatesKeywordsAndFiltersLowSimilarity(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{hits: []logicdomain.MemoryHit{{ID: "hi", Text: "high score memory", Score: 0.91}, {ID: "lo", Text: "low score memory", Score: 0.40}}}
	uc := newUseCaseForTest(t, fakeLLM{content: `{"keywords":["k1","k2","k3","k4","k5","k6","k7"],"need_memory":true,"reason":"test"}`}, embedding, vector, &fakePersona{}, 5, float64Ptr(0.75))
	res, err := uc.Execute(context.Background(), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "question", IsFirstTurn: false})
	if err != nil {
		t.Fatal(err)
	}
	if embedding.called != 1 {
		t.Fatalf("embedding called %d times", embedding.called)
	}
	if got := len(embedding.lastReq.Texts); got != 5 {
		t.Fatalf("keyword count = %d, want 5", got)
	}
	if got := len(res.ContextItems); got != 1 {
		t.Fatalf("context items len = %d, want 1", got)
	}
	if res.ContextItems[0].Text != "high score memory" {
		t.Fatalf("unexpected context item: %#v", res.ContextItems[0])
	}
}

// TestPreCheckLoadsPersonaOnFirstTurn verifies the TestPreCheckLoadsPersonaOnFirstTurn behavior.
// TestPreCheckLoadsPersonaOnFirstTurn 用于验证 TestPreCheckLoadsPersonaOnFirstTurn 行为。
func TestPreCheckLoadsPersonaOnFirstTurn(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{}
	persona := &fakePersona{result: logicdomain.PersonaContext{Profile: []string{"backend engineer"}}}
	uc := newUseCaseForTest(t, fakeLLM{content: `{"keywords":[],"need_memory":false,"reason":"persona only"}`}, embedding, vector, persona, 5, float64Ptr(0.75))
	res, err := uc.Execute(context.Background(), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "hello", IsFirstTurn: true})
	if err != nil {
		t.Fatal(err)
	}
	if persona.called != 1 {
		t.Fatalf("persona called %d times", persona.called)
	}
	if !res.ShouldInject {
		t.Fatal("expected should_inject=true because persona exists")
	}
}

// TestPreCheckPropagatesVectorError verifies the TestPreCheckPropagatesVectorError behavior.
// TestPreCheckPropagatesVectorError 用于验证 TestPreCheckPropagatesVectorError 行为。
func TestPreCheckPropagatesVectorError(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{err: errors.New("vector boom")}
	uc := newUseCaseForTest(t, fakeLLM{content: `{"keywords":["go"],"need_memory":true,"reason":"test"}`}, embedding, vector, &fakePersona{}, 5, float64Ptr(0.75))
	_, err := uc.Execute(context.Background(), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "hello", IsFirstTurn: false})
	if err == nil || err.Error() != "vector search: vector boom" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPreCheckPreservesExplicitZeroMinSimilarity verifies the TestPreCheckPreservesExplicitZeroMinSimilarity behavior.
// TestPreCheckPreservesExplicitZeroMinSimilarity 用于验证 TestPreCheckPreservesExplicitZeroMinSimilarity 行为。
func TestPreCheckPreservesExplicitZeroMinSimilarity(t *testing.T) {
	embedding := &fakeEmbedding{}
	vector := &fakeVector{hits: []logicdomain.MemoryHit{{ID: "lo", Text: "low score memory", Score: 0.10}}}
	uc := newUseCaseForTest(t, fakeLLM{content: `{"keywords":["go"],"need_memory":true,"reason":"test"}`}, embedding, vector, &fakePersona{}, 5, float64Ptr(0))
	res, err := uc.Execute(context.Background(), PreCheckCommand{SessionID: "s1", UserID: "u1", TeamID: "t1", ProjectID: "p1", CurrentContent: "hello", IsFirstTurn: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.ContextItems); got != 1 {
		t.Fatalf("context items len = %d, want 1", got)
	}
	if res.ContextItems[0].Text != "low score memory" {
		t.Fatalf("unexpected context item: %#v", res.ContextItems[0])
	}
}
