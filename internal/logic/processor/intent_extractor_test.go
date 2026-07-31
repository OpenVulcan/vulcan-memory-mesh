// intent_extractor_test.go keeps parser-level assertions deterministic while real-model coverage lives in higher-level integration tests.
// intent_extractor_test.go 用于保持解析器级断言的确定性，而真实模型覆盖交由更高层集成测试承担。
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestIntentExtractorSendsExactStructuredOutput verifies the intent scene cannot regress to schema-free JSON mode.
// TestIntentExtractorSendsExactStructuredOutput 用于验证意图场景不会退回无 Schema 的 JSON 模式。
func TestIntentExtractorSendsExactStructuredOutput(t *testing.T) {
	llm := &stubProfileMergerLLM{response: logicports.LLMResponse{Content: `{"reason":"no lookup needed","need_memory":false,"queries":[]}`}}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	extractor := NewIntentExtractor(llm, prompts, "test-model", 5)

	if _, err := extractor.Extract(context.Background(), nil, "hello"); err != nil {
		t.Fatalf("extract intent: %v", err)
	}
	assertStructuredOutputRequest(t, llm.request, "vmm_precheck_intent")
}

// TestParseIntentResponseParsesMarkdownJSON verifies that fenced JSON model output is accepted and de-duplicated correctly.
// TestParseIntentResponseParsesMarkdownJSON 用于验证带 fenced code 的 JSON 模型输出能被正确解析并去重。
func TestParseIntentResponseParsesMarkdownJSON(t *testing.T) {
	intent, err := parseIntentResponse("analysis...\n```json\n{\n  \"queries\": [\"go memory 设计\", \"go memory 设计\"],\n  \"need_memory\": true,\n  \"reason\": \"match user question\"\n}\n```\nextra")
	if err != nil {
		t.Fatal(err)
	}
	if !intent.NeedMemory {
		t.Fatal("expected need_memory=true")
	}
	if len(intent.Queries) != 1 {
		t.Fatalf("queries len = %d", len(intent.Queries))
	}
	if intent.Queries[0] != "go memory 设计" {
		t.Fatalf("queries = %#v", intent.Queries)
	}
}

// TestParseIntentResponseAcceptsNoMemoryWithEmptyQueries verifies the no-recall branch remains valid when queries is explicitly empty.
// TestParseIntentResponseAcceptsNoMemoryWithEmptyQueries 用于验证无需召回分支在显式返回空 queries 时仍然合法。
func TestParseIntentResponseAcceptsNoMemoryWithEmptyQueries(t *testing.T) {
	intent, err := parseIntentResponse(`{"reason":"recent context answers it","need_memory":false,"queries":[]}`)
	if err != nil {
		t.Fatalf("parse no-memory intent: %v", err)
	}
	if intent.NeedMemory || len(intent.Queries) != 0 || intent.Reason != "recent context answers it" {
		t.Fatalf("unexpected no-memory intent: %#v", intent)
	}
}

// TestParseIntentResponseRejectsInvalidJSON verifies that malformed payloads are surfaced as InvalidLLMOutputError values.
// TestParseIntentResponseRejectsInvalidJSON 用于验证畸形载荷会被识别为 InvalidLLMOutputError。
func TestParseIntentResponseRejectsInvalidJSON(t *testing.T) {
	_, err := parseIntentResponse("```json\n[1,2,3]\n```")
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidLLMOutputError, got %v", err)
	}
}

// TestParseIntentResponseRejectsLegacyKeywords verifies the parser no longer accepts the removed keywords fallback field.
// TestParseIntentResponseRejectsLegacyKeywords 用于验证解析器不再接受已经移除的 keywords 回退字段。
func TestParseIntentResponseRejectsLegacyKeywords(t *testing.T) {
	_, err := parseIntentResponse(`{"reason":"needs memory","need_memory":true,"keywords":["fastapi"]}`)
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidLLMOutputError, got %v", err)
	}
}

// TestParseIntentResponseRejectsMissingRequiredFields verifies the first-stage parser requires every field in the current prompt contract.
// TestParseIntentResponseRejectsMissingRequiredFields 用于验证第一层解析器会要求当前提示词契约中的所有字段都显式存在。
func TestParseIntentResponseRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "missing-reason", raw: `{"need_memory":true,"queries":["历史决策"]}`},
		{name: "missing-need-memory", raw: `{"reason":"needs memory","queries":["历史决策"]}`},
		{name: "missing-queries", raw: `{"reason":"needs memory","need_memory":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseIntentResponse(tc.raw)
			var invalid logicdomain.InvalidLLMOutputError
			if !errors.As(err, &invalid) {
				t.Fatalf("expected InvalidLLMOutputError, got %v", err)
			}
		})
	}
}

// TestParseIntentResponseRejectsNeedMemoryQueryMismatch verifies need_memory and queries stay consistent at the parser boundary.
// TestParseIntentResponseRejectsNeedMemoryQueryMismatch 用于验证 need_memory 与 queries 在解析器边界保持一致。
func TestParseIntentResponseRejectsNeedMemoryQueryMismatch(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "need-memory-empty-queries", raw: `{"reason":"needs memory","need_memory":true,"queries":[]}`},
		{name: "need-memory-blank-queries", raw: `{"reason":"needs memory","need_memory":true,"queries":["  "]}`},
		{name: "no-memory-with-query", raw: `{"reason":"recent answer exists","need_memory":false,"queries":["历史决策"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseIntentResponse(tc.raw)
			var invalid logicdomain.InvalidLLMOutputError
			if !errors.As(err, &invalid) {
				t.Fatalf("expected InvalidLLMOutputError, got %v", err)
			}
		})
	}
}

// TestStripMarkdownFences verifies that helper-level fence stripping keeps the bare JSON body.
// TestStripMarkdownFences 用于验证围栏剥离辅助函数会保留纯净 JSON 内容。
func TestStripMarkdownFences(t *testing.T) {
	got := stripMarkdownFences("```JSON\n{\"k\":1}\n```")
	if got != "{\"k\":1}" {
		t.Fatalf("got %q", got)
	}
}

// TestRenderIntentUserPromptIncludesContextHints verifies that the first-stage prompt body now carries deterministic current/recent context hints alongside the raw turn window.
// TestRenderIntentUserPromptIncludesContextHints 用于验证第一层提示词请求体现在会携带确定性的当前/最近情境 hints，以及原始 turn 窗口。
func TestRenderIntentUserPromptIncludesContextHints(t *testing.T) {
	rendered := renderIntentUserPrompt([]logicdomain.PreCheckTurnContext{
		{TurnID: 11, ContentType: "DETAILS", Content: "上一轮已经确认 SQLite schema 13 需要保持兼容。"},
		{TurnID: 12, ContentType: "RAW_TURN", Content: `{"user":"这里为什么删除 DuckDB 适配层","assistant":"因为当前主线只保留 SQLite"}`},
	}, "这个改动会不会影响 SQLite schema 13 的兼容性？", 4)

	var payload struct {
		CurrentUserInput    string   `json:"current_user_input"`
		CurrentContextHints []string `json:"current_context_hints"`
		RecentContextHints  []string `json:"recent_context_hints"`
		MaxSearchQueries    int      `json:"max_search_queries"`
	}
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered prompt: %v", err)
	}
	assertCompactJSONPrompt(t, rendered)
	if payload.CurrentUserInput != "这个改动会不会影响 SQLite schema 13 的兼容性？" {
		t.Fatalf("unexpected current input: %q", payload.CurrentUserInput)
	}
	if payload.MaxSearchQueries != 4 {
		t.Fatalf("unexpected max_search_queries: %d", payload.MaxSearchQueries)
	}
	if len(payload.CurrentContextHints) == 0 || payload.CurrentContextHints[0] != payload.CurrentUserInput {
		t.Fatalf("unexpected current_context_hints: %#v", payload.CurrentContextHints)
	}
	if len(payload.RecentContextHints) == 0 {
		t.Fatalf("expected recent_context_hints, got %#v", payload.RecentContextHints)
	}
	foundDuckDB := false
	foundSQLite := false
	for _, hint := range payload.RecentContextHints {
		if hint == "duckdb" || hint == "DuckDB" || hint == "这里为什么删除 DuckDB 适配层 因为当前主线只保留 SQLite" {
			foundDuckDB = true
		}
		if hint == "sqlite" || hint == "SQLite" || hint == "上一轮已经确认 SQLite schema 13 需要保持兼容" {
			foundSQLite = true
		}
	}
	if !foundDuckDB || !foundSQLite {
		t.Fatalf("recent_context_hints did not preserve expected anchors: %#v", payload.RecentContextHints)
	}
}
