package httpapi

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/services"
	"github.com/openvulcan/vmm/internal/platform/xid"
)

func newTestRouter() http.Handler {
	return newTestRouterWithLogger(nil)
}

func newTestRouterWithLogger(logger *log.Logger) http.Handler {
	ids := xid.NewGenerator()
	llm := memory_mock.NewLLMClient()
	embed := memory_mock.NewEmbeddingClient(64)
	vector := memory_mock.NewVectorStore()
	rel := memory_mock.NewRelationalStore()
	pre := services.NewPreCheckService(llm, embed, vector, memory_mock.NewPersonaProvider(), nil, 2*time.Second, 5, 0.4)
	post := services.NewPostActionService(rel, nil)
	seed := services.NewSeedMemoryService(embed, vector, ids, nil)
	return NewRouter(Dependencies{
		IDs: ids, PreCheck: pre, PostAction: post, SeedMemory: seed, Logger: logger,
		PreCheckTimeout: 3*time.Second, PostActionTimeout: 3*time.Second, SeedMemoryTimeout: 3*time.Second,
		EnableSeedRoute: true,
	})
}

func TestPreCheckRejectsNonTextHistoryContent(t *testing.T) {
	router := newTestRouter()
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","history_content":[{"role":"user","content":{"type":"text"}}],"current_content":"hi","is_first_turn":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest { t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String()) }
}

func TestSeedThenPreCheckRoundTrip(t *testing.T) {
	router := newTestRouter()

	seedBody, _ := json.Marshal(domain.SeedMemoryRequest{UserID:"u1", ProjectID:"p1", MemoryText:"要求使用 FastAPI"})
	seedReq := httptest.NewRequest(http.MethodPost, "/v1/admin/seed-memory", bytes.NewReader(seedBody))
	seedReq.Header.Set("Content-Type", "application/json")
	seedRec := httptest.NewRecorder()
	router.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK { t.Fatalf("seed expected 200, got %d body=%s", seedRec.Code, seedRec.Body.String()) }

	preBody, _ := json.Marshal(map[string]any{
		"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1",
		"history_content":[]any{},"current_content":"我们后端用什么框架？","is_first_turn":false,
	})
	preReq := httptest.NewRequest(http.MethodPost, "/v1/chat/pre-check", bytes.NewReader(preBody))
	preReq.Header.Set("Content-Type", "application/json")
	preRec := httptest.NewRecorder()
	router.ServeHTTP(preRec, preReq)
	if preRec.Code != http.StatusOK { t.Fatalf("pre-check expected 200, got %d body=%s", preRec.Code, preRec.Body.String()) }
	if !bytes.Contains(preRec.Body.Bytes(), []byte("FastAPI")) { t.Fatalf("expected FastAPI in response body=%s", preRec.Body.String()) }
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
			{"role":"assistant","content":"采用 Go 语言和 Gin 框架。"}
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
	if !strings.Contains(logOutput, `request_body={"session_id":"sess_123","user_id":"usr_8899","team_id":"team_001","space_id":"space_001","project_id":"proj_abc","is_first_turn":true,"current_content":"如果是高并发场景，它还撑得住吗？","history_content":[{"role":"user","content":"我们后端用什么框架？"},{"role":"assistant","content":"采用 Go 语言和 Gin 框架。"}]}`) {
		t.Fatalf("expected request body in logs, got %s", logOutput)
	}
}
