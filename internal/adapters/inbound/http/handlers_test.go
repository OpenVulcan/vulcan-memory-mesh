// handlers_test.go implements the inbound HTTP adapter layer.
// handlers_test.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"github.com/openvulcan/vmm/internal/testutil"
)

// float64Ptr executes the float64Ptr logic.
// float64Ptr 用于执行 float64Ptr 逻辑。
func float64Ptr(v float64) *float64 { return &v }

// newTestRouter creates a TestRouter instance.
// newTestRouter 用于创建 TestRouter 实例。
func newTestRouter(t *testing.T) http.Handler { return newTestRouterWithLogger(t, nil) }

// newTestRouterWithLogger creates a TestRouterWithLogger instance.
// newTestRouterWithLogger 用于创建 TestRouterWithLogger 实例。
func newTestRouterWithLogger(t *testing.T, logger *logx.Logger) http.Handler {
	t.Helper()
	fixture := testutil.MustRealRuntimeFixture(t)
	ids := xid.NewGenerator()
	vector := memory_mock.NewVectorStore()
	rel := memory_mock.NewRelationalStore()
	pre := usecase.NewPreCheckUseCase(
		processor.NewIntentExtractor(fixture.LLM, fixture.Prompts, fixture.Config.LLM.Model, 5),
		processor.NewContextAssembler(fixture.Prompts, fixture.Config.LLM.Model),
		fixture.Embedding,
		vector,
		memory_mock.NewPersonaProvider(),
		logger,
		fixture.Config.PreCheck.IntentTimeout.Duration,
		5,
		5,
		float64Ptr(0.4),
		fixture.Config.Embedding.Model,
		fixture.Config.Embedding.Dimension,
	)
	post := usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), nil, rel, logger)
	seed := usecase.NewSeedMemoryUseCase(fixture.Embedding, vector, ids, logger, fixture.Config.Embedding.Model, fixture.Config.Embedding.Dimension)
	return NewRouter(Dependencies{
		IDs:                 ids,
		PreCheck:            pre,
		PostAction:          post,
		SeedMemory:          seed,
		Logger:              logger,
		PreCheckTimeout:     10 * time.Second,
		PostActionTimeout:   3 * time.Second,
		SeedMemoryTimeout:   15 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
		LogRequestBodies:    true,
		EnableSeedRoute:     true,
	})
}

// testChatUseCase adapts a plain function into the ChatExecutor interface for focused HTTP tests.
// testChatUseCase 用于把普通函数适配成 ChatExecutor 接口，便于聚焦 HTTP 测试。
type testChatUseCase func(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error)

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (f testChatUseCase) Execute(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error) {
	return f(ctx, cmd)
}

// TestPreCheckRejectsMissingUser verifies that the new pre-check transport contract requires one current user text field.
// TestPreCheckRejectsMissingUser 用于验证新的 pre-check 传输契约要求必须提供当前 user 文本字段。
func TestPreCheckRejectsMissingUser(t *testing.T) {
	router := newTestRouter(t)
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1"}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_VALIDATION_FAILED" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
}

// TestSeedThenPreCheckRoundTrip verifies that pre-check now returns an empty no-op payload even after seed-memory succeeds.
// TestSeedThenPreCheckRoundTrip 用于验证即使 seed-memory 成功，pre-check 现在也会返回空的 no-op 结果。
func TestSeedThenPreCheckRoundTrip(t *testing.T) {
	testutil.RequireLiveModelAccess(t)
	router := newTestRouter(t)

	seedBody, _ := json.Marshal(SeedMemoryRequestDTO{UserID: "u1", ProjectID: "p1", MemoryText: "fastapi backend framework decision"})
	seedReq := httptest.NewRequest(http.MethodPost, "/v1/admin/seed-memory", bytes.NewReader(seedBody))
	seedReq.Header.Set("Content-Type", "application/json")
	seedRec := httptest.NewRecorder()
	router.ServeHTTP(seedRec, seedReq)
	if seedRec.Code != http.StatusOK {
		t.Fatalf("seed expected 200, got %d body=%s", seedRec.Code, seedRec.Body.String())
	}

	preBody, _ := json.Marshal(PreCheckRequestDTO{
		SessionID: "s1",
		UserID:    "u1",
		TeamID:    "t1",
		ProjectID: "p1",
		UserText:  "请回忆 fastapi backend framework decision",
	})
	preReq := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewReader(preBody))
	preReq.Header.Set("Content-Type", "application/json")
	preRec := httptest.NewRecorder()
	router.ServeHTTP(preRec, preReq)
	if preRec.Code != http.StatusOK {
		t.Fatalf("pre-check expected 200, got %d body=%s", preRec.Code, preRec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(preRec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	dataBytes, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var resp PreCheckResponseDTO
	if err := json.Unmarshal(dataBytes, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ShouldInject {
		t.Fatalf("expected should_inject=false body=%s", preRec.Body.String())
	}
	if resp.ContextText != "" || len(resp.ContextItems) != 0 {
		t.Fatalf("expected empty pre-check payload body=%s", preRec.Body.String())
	}
}

// TestPostActionNormalizesRawMessages verifies the TestPostActionNormalizesRawMessages behavior.
// TestPostActionNormalizesRawMessages 用于验证 TestPostActionNormalizesRawMessages 行为。
func TestPostActionNormalizesRawMessages(t *testing.T) {
	ids := xid.NewGenerator()
	logger := logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"})
	rel := memory_mock.NewRelationalStore()
	router := NewRouter(Dependencies{
		IDs:                 ids,
		PreCheck:            nil,
		PostAction:          usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), nil, rel, logger),
		SeedMemory:          nil,
		Logger:              logger,
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","raw_messages_snapshot":[{"role":"user","content":"你好"},{"role":"assistant","content":[{"type":"text","text":"<think>hidden</think>已收到"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action-old", bytes.NewBufferString(body))
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

// TestPostActionCompatModeTrimsUnsupportedPayloads verifies the TestPostActionCompatModeTrimsUnsupportedPayloads behavior.
// TestPostActionCompatModeTrimsUnsupportedPayloads 用于验证 TestPostActionCompatModeTrimsUnsupportedPayloads 行为。
func TestPostActionCompatModeTrimsUnsupportedPayloads(t *testing.T) {
	ids := xid.NewGenerator()
	logger := logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"})
	rel := memory_mock.NewRelationalStore()
	router := NewRouter(Dependencies{
		IDs:                 ids,
		PostAction:          usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), nil, rel, logger),
		Logger:              logger,
		Validator:           NewRequestValidator("compat"),
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","raw_messages_snapshot":[{"role":"system","content":"drop-me"},{"role":"user","content":[{"type":"text","text":"你好"},{"type":"image_url","image_url":{"url":"https://example.com"}}]},{"role":"assistant","content":"<think>hidden</think>已收到 ![这是一只猫](https://cdn.example.com/cat.jpg) ![](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA)"},{"role":"assistant","tool_calls":[{"id":"tool-1","type":"function"}],"content":"tooling"}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action-old", bytes.NewBufferString(body))
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
	if turns[0].UserMessage != "你好" {
		t.Fatalf("user message = %q", turns[0].UserMessage)
	}
	if turns[0].AssistantReply != "已收到 [图片: 这是一只猫] [图片已过滤]" {
		t.Fatalf("assistant reply = %q", turns[0].AssistantReply)
	}
	meta, ok := rel.Session("s1")
	if !ok {
		t.Fatal("expected session metadata to be written")
	}
	if meta.Session.UserID != "default" || meta.Session.TeamID != "default" || meta.Session.ProjectID != "default" || meta.Session.SpaceID != "default" {
		t.Fatalf("unexpected default scope session=%+v", meta.Session)
	}
}

// TestPreCheckSanitizesFlattenedMediaArtifacts verifies the TestPreCheckSanitizesFlattenedMediaArtifacts behavior.
// TestPreCheckSanitizesFlattenedMediaArtifacts 用于验证 TestPreCheckSanitizesFlattenedMediaArtifacts 行为。
func TestPreCheckSanitizesFlattenedMediaArtifacts(t *testing.T) {
	called := false
	pre := preCheckFunc(func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
		called = true
		if got := cmd.CurrentContent; got != "请参考 [图片: 架构图] [图片已过滤]" {
			t.Fatalf("current content = %q", got)
		}
		if len(cmd.HistoryContent) != 0 {
			t.Fatalf("history len = %d", len(cmd.HistoryContent))
		}
		return usecase.PreCheckResult{ShouldInject: false, ContextText: "", ContextItems: []logicdomain.ContextItem{}}, nil
	})
	router := NewRouter(Dependencies{
		IDs:                 xid.NewGenerator(),
		PreCheck:            pre,
		Logger:              logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator:           NewRequestValidator("compat"),
		PreCheckTimeout:     time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","user_content":"请参考 ![架构图](https://cdn.example.com/arch.png) ![](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA)"}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !called {
		t.Fatal("usecase was not called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestPostActionStrictModeRejectsNonTextContent verifies the TestPostActionStrictModeRejectsNonTextContent behavior.
// TestPostActionStrictModeRejectsNonTextContent 用于验证 TestPostActionStrictModeRejectsNonTextContent 行为。
func TestPostActionStrictModeRejectsNonTextContent(t *testing.T) {
	ids := xid.NewGenerator()
	logger := logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"})
	rel := memory_mock.NewRelationalStore()
	router := NewRouter(Dependencies{
		IDs:                 ids,
		PostAction:          usecase.NewPostActionUseCase(processor.NewMessageNormalizer(), nil, rel, logger),
		Logger:              logger,
		Validator:           NewRequestValidator("strict"),
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","raw_messages_snapshot":[{"role":"user","content":[{"type":"text","text":"你好"},{"type":"image_url","image_url":{"url":"https://example.com"}}]},{"role":"assistant","content":"收到"}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action-old", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_VALIDATION_FAILED" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
	if !strings.Contains(env.Msg, "raw_messages_snapshot[0].content[1].type") {
		t.Fatalf("expected field path in message, got %q", env.Msg)
	}
}

// TestPostActionReturnsAcceptedImmediatelyAndProcessesInBackground verifies that the new route acknowledges first and reuses the old persistence flow asynchronously.
// TestPostActionReturnsAcceptedImmediatelyAndProcessesInBackground 用于验证新路由会先确认接收，再异步复用旧版持久化流程。
func TestPostActionReturnsAcceptedImmediatelyAndProcessesInBackground(t *testing.T) {
	started := make(chan usecase.PostActionCommand, 1)
	release := make(chan struct{})
	post := postActionFunc(func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
		started <- cmd
		<-release
		return usecase.PostActionResult{Accepted: true}, nil
	})
	router := NewRouter(Dependencies{
		IDs:                 xid.NewGenerator(),
		PostAction:          post,
		Logger:              logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator:           NewRequestValidator("compat"),
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","user_content":"第一问","assistant_content":"最终回答","timeline":[{"type":"user","content":"第一问"},{"type":"assistant","content":"中间回答"},{"type":"user","content":"补充问题"},{"type":"assistant","content":"最终回答"}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Code != http.StatusOK {
		t.Fatalf("response code = %d", env.Code)
	}
	select {
	case cmd := <-started:
		if len(cmd.RawMessagesSnapshot) != 4 {
			t.Fatalf("snapshot len = %d", len(cmd.RawMessagesSnapshot))
		}
		if cmd.RawMessagesSnapshot[0].Role != "user" || cmd.RawMessagesSnapshot[0].Content != "第一问" {
			t.Fatalf("first snapshot = %+v", cmd.RawMessagesSnapshot[0])
		}
		if cmd.RawMessagesSnapshot[3].Role != "assistant" || cmd.RawMessagesSnapshot[3].Content != "最终回答" {
			t.Fatalf("last snapshot = %+v", cmd.RawMessagesSnapshot[3])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background processing did not start")
	}
	close(release)
}

// TestPostActionRejectsTimelineBoundaryMismatch verifies that the new route requires timeline boundaries to match the top-level user and assistant texts.
// TestPostActionRejectsTimelineBoundaryMismatch 用于验证新路由要求时间线首尾必须与顶层 user 和 assistant 文本一致。
func TestPostActionRejectsTimelineBoundaryMismatch(t *testing.T) {
	router := NewRouter(Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		Logger:              logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator:           NewRequestValidator("compat"),
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","user_content":"第一问","assistant_content":"最终回答","timeline":[{"type":"user","content":"不是第一问"},{"type":"assistant","content":"最终回答"}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_VALIDATION_FAILED" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
	if !strings.Contains(env.Msg, "timeline[0].content") {
		t.Fatalf("expected timeline boundary message, got %q", env.Msg)
	}
}

// TestPostActionRejectsArrayTopLevelContent verifies that the new text-only contract rejects array values for top-level user_content.
// TestPostActionRejectsArrayTopLevelContent 用于验证新的纯文本契约会拒绝顶层 user_content 被数组替代。
func TestPostActionRejectsArrayTopLevelContent(t *testing.T) {
	router := NewRouter(Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		Logger:              logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator:           NewRequestValidator("compat"),
		PostActionTimeout:   3 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"s1","user_content":[],"assistant_content":"最终回答","timeline":[{"type":"user","content":"第一问"},{"type":"assistant","content":"最终回答"}]}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/post-action", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_INVALID_JSON" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
}

// TestPostRequestLogsFullBody verifies the TestPostRequestLogsFullBody behavior.
// TestPostRequestLogsFullBody 用于验证 TestPostRequestLogsFullBody 行为。
func TestPostRequestLogsFullBody(t *testing.T) {
	testutil.RequireLiveModelAccess(t)
	var logBuf bytes.Buffer
	router := newTestRouterWithLogger(t, logx.New(&logBuf, logx.Config{Level: "info", Format: "text"}))
	body := `{
		"session_id":"sess_123",
		"user_id":"usr_8899",
		"team_id":"team_001",
		"space_id":"space_001",
		"project_id":"proj_abc",
		"user_content":"如果是高并发场景，它还撑得住吗？"
	}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, `request_body="{\"session_id\":\"sess_123\",\"user_id\":\"usr_8899\",\"team_id\":\"team_001\",\"space_id\":\"space_001\",\"project_id\":\"proj_abc\",\"user_content\":\"如果是高并发场景，它还撑得住吗？\"}"`) {
		t.Fatalf("expected request body in logs, got %s", logOutput)
	}
}

// TestRequestTooLargeReturnsCatalogedError verifies the TestRequestTooLargeReturnsCatalogedError behavior.
// TestRequestTooLargeReturnsCatalogedError 用于验证 TestRequestTooLargeReturnsCatalogedError 行为。
func TestRequestTooLargeReturnsCatalogedError(t *testing.T) {
	router := newTestRouter(t)
	oversized := strings.Repeat("x", (1<<20)+128)
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","user_content":"` + oversized + `"}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_REQUEST_TOO_LARGE" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
}

// TestValidationErrorsReturnCatalogedError verifies that malformed pre-check bodies still map into cataloged validation errors.
// TestValidationErrorsReturnCatalogedError 用于验证格式不合法的 pre-check 请求仍会映射成标准化校验错误。
func TestValidationErrorsReturnCatalogedError(t *testing.T) {
	router := newTestRouter(t)
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","user_content":""}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ErrorID != "HTTP_VALIDATION_FAILED" {
		t.Fatalf("error id = %q", env.ErrorID)
	}
	if env.ErrorCategory != "validation" {
		t.Fatalf("error category = %q", env.ErrorCategory)
	}
}

// TestNotFoundCarriesTraceID verifies the TestNotFoundCarriesTraceID behavior.
// TestNotFoundCarriesTraceID 用于验证 TestNotFoundCarriesTraceID 行为。
func TestNotFoundCarriesTraceID(t *testing.T) {
	router := newTestRouter(t)
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

// TestWithTimeoutPreservesTraceIDInUseCaseContext verifies the TestWithTimeoutPreservesTraceIDInUseCaseContext behavior.
// TestWithTimeoutPreservesTraceIDInUseCaseContext 用于验证 TestWithTimeoutPreservesTraceIDInUseCaseContext 行为。
func TestWithTimeoutPreservesTraceIDInUseCaseContext(t *testing.T) {
	called := false
	pre := preCheckFunc(func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
		called = true
		if got := traceFromContext(ctx); got != "trace-a" {
			t.Fatalf("trace id in usecase ctx = %q", got)
		}
		return usecase.PreCheckResult{ShouldInject: false, ContextText: "", ContextItems: []logicdomain.ContextItem{}}, nil
	})
	router := NewRouter(Dependencies{IDs: xid.NewGenerator(), PreCheck: pre, Logger: logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}), PreCheckTimeout: time.Second, MaxRequestBodyBytes: 1 << 20})
	body := `{"session_id":"s1","user_id":"u1","team_id":"t1","project_id":"p1","user_content":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/vmm/pre-check", bytes.NewBufferString(body))
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

// TestChatRouteScrubsAndReturnsSanitizedText verifies the TestChatRouteScrubsAndReturnsSanitizedText behavior.
// TestChatRouteScrubsAndReturnsSanitizedText 用于验证 TestChatRouteScrubsAndReturnsSanitizedText 行为。
func TestChatRouteScrubsAndReturnsSanitizedText(t *testing.T) {
	chat := testChatUseCase(func(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error) {
		if cmd.Language != "zh-CN" {
			t.Fatalf("language = %q", cmd.Language)
		}
		return usecase.ChatResult{
			SessionID: cmd.SessionID,
			Message:   "你好，我的电话是 [MOBILE_MASKED]",
			Language:  cmd.Language,
			TraceID:   trace.IDFromContext(ctx),
		}, nil
	})
	router := NewRouter(Dependencies{
		IDs:                 xid.NewGenerator(),
		Chat:                chat,
		Logger:              logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		ChatTimeout:         time.Second,
		MaxRequestBodyBytes: 1 << 20,
	})
	body := `{"session_id":"test_001","message":"你好，我的电话是 13800138000"}`
	req := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	dataBytes, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	var resp ChatResponseDTO
	if err := json.Unmarshal(dataBytes, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Message != "你好，我的电话是 [MOBILE_MASKED]" {
		t.Fatalf("response message = %q", resp.Message)
	}
}

// preCheckFunc adapts a plain function into the PreCheckExecutor interface for focused handler tests.
// preCheckFunc 用于把普通函数适配成 PreCheckExecutor 接口，便于聚焦 handler 测试。
type preCheckFunc func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error)

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (f preCheckFunc) Execute(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
	return f(ctx, cmd)
}

// postActionFunc adapts a plain function into the PostActionExecutor interface for focused handler tests.
// postActionFunc 用于把普通函数适配成 PostActionExecutor 接口，便于聚焦 handler 测试。
type postActionFunc func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error)

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (f postActionFunc) Execute(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
	return f(ctx, cmd)
}

// traceFromContext executes the traceFromContext logic.
// traceFromContext 用于执行 traceFromContext 逻辑。
func traceFromContext(ctx context.Context) string {
	return trace.IDFromContext(ctx)
}
