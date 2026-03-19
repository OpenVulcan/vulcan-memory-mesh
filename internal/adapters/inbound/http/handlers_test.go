package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"github.com/openvulcan/vmm/internal/platform/xid"
)

type stubPromptSource struct{}

func (stubPromptSource) GetPrompt(scene, modelName string) (string, error) {
	return scene + ":" + modelName, nil
}

func float64Ptr(v float64) *float64 { return &v }

func newTestRouter() http.Handler { return newTestRouterWithLogger(nil) }

func newTestRouterWithLogger(logger *log.Logger) http.Handler {
	ids := xid.NewGenerator()
	llm := memory_mock.NewLLMClient()
	embed := memory_mock.NewEmbeddingClient(64)
	vector := memory_mock.NewVectorStore()
	rel := memory_mock.NewRelationalStore()
	prompts := stubPromptSource{}
	pre := usecase.NewPreCheckUseCase(
		processor.NewIntentExtractor(llm, prompts, "mock-intent", 5),
		processor.NewContextAssembler(prompts, "mock-intent"),
		embed,
		vector,
		memory_mock.NewPersonaProvider(),
		logger,
		2*time.Second,
		5,
		5,
		float64Ptr(0.4),
		"mock-embedding",
		64,
	)
	post := usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), rel, logger)
	seed := usecase.NewSeedMemoryUseCase(embed, vector, ids, logger, "mock-embedding", 64)
	return NewRouter(Dependencies{
		IDs:               ids,
		PreCheck:          pre,
		PostAction:        post,
		SeedMemory:        seed,
		Logger:            logger,
		PreCheckTimeout:   3 * time.Second,
		PostActionTimeout: 3 * time.Second,
		SeedMemoryTimeout: 3 * time.Second,
		EnableSeedRoute:   true,
	})
}

func TestPreCheckRejectsNonTextHistoryContent(t *testing.T) {
	router := newTestRouter()
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","history_content":[{"role":"user","content":{"type":"text"}}],"current_content":"hi","is_first_turn":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSeedThenPreCheckRoundTrip(t *testing.T) {
	router := newTestRouter()

	seedBody, _ := json.Marshal(SeedMemoryRequestDTO{UserID: "u1", ProjectID: "p1", MemoryText: "fastapi backend framework"})
	seedReq := httptest.NewRequest(http.MethodPost, "/v1/admin/seed-memory", bytes.NewReader(seedBody))
	seedReq.Header.Set("Content-Type", "application/json")
	seedRec := httptest.NewRecorder()
	router.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed expected 200, got %d body=%s", seedRec.Code, seedRec.Body.String())
	}

	preBody, _ := json.Marshal(PreCheckRequestDTO{
		SessionID:      "s1",
		UserID:         "u1",
		TeamID:         "t1",
		ProjectID:      "p1",
		HistoryContent: []HistorySnippetDTO{},
		CurrentContent: "我们后端用什么框架？",
		IsFirstTurn:    false,
	})
	preReq := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewReader(preBody))
	preReq.Header.Set("Content-Type", "application/json")
	preRec := httptest.NewRecorder()
	router.ServeHTTP(preRec, preReq)
	if preRec.Code != http.StatusOK {
		t.Fatalf("pre-check expected 200, got %d body=%s", preRec.Code, preRec.Body.String())
	}
	if !strings.Contains(strings.ToLower(preRec.Body.String()), "fastapi") {
		t.Fatalf("expected fastapi in response body=%s", preRec.Body.String())
	}
}

func TestPostActionNormalizesRawMessages(t *testing.T) {
	ids := xid.NewGenerator()
	logger := log.New(&bytes.Buffer{}, "", 0)
	rel := memory_mock.NewRelationalStore()
	router := NewRouter(Dependencies{
		IDs:               ids,
		PreCheck:          nil,
		PostAction:        usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), rel, logger),
		SeedMemory:        nil,
		Logger:            logger,
		PostActionTimeout: 3 * time.Second,
	})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","raw_messages_snapshot":[{"role":"user","content":"你好"},{"role":"assistant","content":[{"text":"<think>hidden</think>已收到"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/post-action", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	turns := rel.Turns("s1")
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].AssistantReply != "已收到" {
		t.Fatalf("assistant reply = %q", turns[0].AssistantReply)
	}
}

func TestPostRequestLogsFullBody(t *testing.T) {
	var logBuf bytes.Buffer
	router := newTestRouterWithLogger(log.New(&logBuf, "", 0))
	body := `{
		"session_id":"sess_123",
		"user_id":"usr_8899",
		"team_id":"team_001",
		"space_id":"space_001",
		"project_id":"proj_abc",
		"is_first_turn":true,
		"current_content":"如果是高并发场景，它还撑得住吗？",
		"history_content":[
			{"role":"user","content":"我们后端用什么框架？"},
			{"role":"assistant","content":"采用 Go 语言和标准库实现。"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, `request_body={"session_id":"sess_123","user_id":"usr_8899","team_id":"team_001","space_id":"space_001","project_id":"proj_abc","is_first_turn":true,"current_content":"如果是高并发场景，它还撑得住吗？","history_content":[{"role":"user","content":"我们后端用什么框架？"},{"role":"assistant","content":"采用 Go 语言和标准库实现。"}]}`) {
		t.Fatalf("expected request body in logs, got %s", logOutput)
	}
}

func TestNotFoundCarriesTraceID(t *testing.T) {
	router := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	req.Header.Set("X-Trace-ID", "trace-fixed")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Trace-ID"); got != "trace-fixed" {
		t.Fatalf("trace header = %q", got)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.TraceID != "trace-fixed" {
		t.Fatalf("trace id = %q", env.TraceID)
	}
}

func TestWithTimeoutPreservesTraceIDInUseCaseContext(t *testing.T) {
	called := false
	pre := preCheckFunc(func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
		called = true
		if got := traceFromContext(ctx); got != "trace-a" {
			t.Fatalf("trace id in usecase ctx = %q", got)
		}
		return usecase.PreCheckResult{ShouldInject: false, ContextText: "", ContextItems: []logicdomain.ContextItem{}}, nil
	})
	router := NewRouter(Dependencies{IDs: xid.NewGenerator(), PreCheck: pre, Logger: log.New(&bytes.Buffer{}, "", 0), PreCheckTimeout: time.Second})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","history_content":[],"current_content":"hi","is_first_turn":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-ID", "trace-a")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !called {
		t.Fatal("usecase was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

type preCheckFunc func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error)

func (f preCheckFunc) Execute(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
	return f(ctx, cmd)
}

func traceFromContext(ctx context.Context) string {
	return trace.IDFromContext(ctx)
}
