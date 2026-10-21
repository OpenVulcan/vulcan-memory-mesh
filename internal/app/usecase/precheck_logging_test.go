// Behavioral tests grouped by precheck logging test.
// 应用用例层 pre-check 日志行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"bytes"
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"strings"
	"testing"
)

// TestPreCheckExecuteLogsIntentInvalidOutput verifies degraded pre-check intent failures keep raw first-stage model diagnostics in server logs.
// TestPreCheckExecuteLogsIntentInvalidOutput 用于验证 pre-check 第一层意图失败降级时，会把原始模型诊断留在服务端日志中。
func TestPreCheckExecuteLogsIntentInvalidOutput(t *testing.T) {
	rawOutput := strings.Join([]string{
		"```json",
		"{\"need_memory\":true,\"queries\":[\"部署策略\"]",
		"```",
	}, "\n")
	intent := &stubPreCheckIntentExtractor{
		err: logicdomain.InvalidLLMOutputError{
			Scene:   "precheck_l1_main",
			Message: "json decode failed",
			Raw:     rawOutput,
			Execution: &logicdomain.LLMExecutionMetadata{
				Purpose:         "precheck_l1_main",
				ConfiguredModel: "Qwen/Qwen3-32B",
				ResponseModel:   "provider-precheck-l1-model",
				RequestID:       "req-invalid-precheck-l1",
				Usage:           logicdomain.LLMUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CachedInputTokens: 60},
			},
		},
		model: "Qwen/Qwen3-32B",
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{},
		&stubPreCheckStore{},
		intent,
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{},
		logger,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-precheck-invalid-l1"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-invalid-l1",
			UserID:     7,
			ProjectID:  9,
		},
		UserContent: "这次部署策略怎么定？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.Degraded || result.ShouldInject {
		t.Fatalf("expected degraded empty result, got %+v", result)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "pre-check intent degraded") {
		t.Fatalf("expected intent degraded log, got %s", logs)
	}
	if !strings.Contains(logs, "llm_scene：\"precheck_l1_main\"") {
		t.Fatalf("expected l1 scene in degraded log, got %s", logs)
	}
	for _, fragment := range []string{"configured_model", "Qwen/Qwen3-32B", "response_model", "provider-precheck-l1-model", "request_id", "req-invalid-precheck-l1", "cached_input_tokens", "reasoning_tokens"} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected physical intent field %q in degraded log, got %s", fragment, logs)
		}
	}
	if !strings.Contains(logs, "TEXT(llm_raw_output)：\n"+rawOutput+"\n") {
		t.Fatalf("expected raw intent output in degraded log, got %s", logs)
	}
	if !strings.Contains(logs, "invalid llm output for precheck_l1_main: json decode failed") {
		t.Fatalf("expected invalid intent output summary in degraded log, got %s", logs)
	}
}

// TestPreCheckExecuteLogsReviewerInvalidOutput verifies degraded pre-check reviewer failures keep raw second-stage model diagnostics in server logs.
// TestPreCheckExecuteLogsReviewerInvalidOutput 用于验证 pre-check 第二层评审失败降级时，会把原始模型诊断留在服务端日志中。
func TestPreCheckExecuteLogsReviewerInvalidOutput(t *testing.T) {
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			Queries:    []string{"部署策略"},
			NeedMemory: true,
			Reason:     "用户问题需要长期部署记忆。",
		},
		model: "Qwen/Qwen3-32B",
	}
	rawOutput := strings.Join([]string{
		"```json",
		"{\"selected_candidate_numbers\":[1]",
		"```",
	}, "\n")
	reviewer := &stubPreCheckReviewer{
		err: logicdomain.InvalidLLMOutputError{
			Scene:   "precheck_l2_main",
			Message: "json decode failed",
			Raw:     rawOutput,
			Execution: &logicdomain.LLMExecutionMetadata{
				Purpose:         "precheck_l2_main",
				ConfiguredModel: "Qwen/Qwen3-235B-A22B",
				ResponseModel:   "provider-precheck-l2-model",
				RequestID:       "req-invalid-precheck-l2",
				Usage:           logicdomain.LLMUsage{PromptTokens: 300, CompletionTokens: 12, TotalTokens: 312, CachedInputTokens: 200},
			},
		},
		model: "Qwen/Qwen3-235B-A22B",
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{{
					QueryIndex: 0,
					Query:      "部署策略",
					Hits: []MemoryQueryHit{{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
						SourceKind:     logicdomain.MemorySourceKindTurnExtract,
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Abstract:       "项目默认采用本地部署。",
						DetailsPreview: "用户确认项目默认采用本地部署。",
						Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
						Score:          0.97,
						Origin:         "vector",
					}},
				}},
			},
		},
		&stubPreCheckStore{},
		intent,
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{},
		logger,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-precheck-invalid-l2"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  42,
			SessionKey: "sess-invalid-l2",
			UserID:     7,
			ProjectID:  9,
		},
		UserContent: "这次部署策略怎么定？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.Degraded || result.ShouldInject {
		t.Fatalf("expected degraded empty result, got %+v", result)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "pre-check memory adoption degraded") {
		t.Fatalf("expected memory adoption degraded log, got %s", logs)
	}
	if !strings.Contains(logs, "llm_scene：\"precheck_l2_main\"") {
		t.Fatalf("expected l2 scene in degraded log, got %s", logs)
	}
	for _, fragment := range []string{"configured_model", "Qwen/Qwen3-235B-A22B", "response_model", "provider-precheck-l2-model", "request_id", "req-invalid-precheck-l2", "cached_input_tokens", "reasoning_tokens"} {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected physical reviewer field %q in degraded log, got %s", fragment, logs)
		}
	}
	if strings.Contains(logs, "model：\"Qwen/Qwen3-32B\"") {
		t.Fatalf("expected l2 failure log to avoid intent model attribution, got %s", logs)
	}
	if !strings.Contains(logs, "TEXT(llm_raw_output)：\n"+rawOutput+"\n") {
		t.Fatalf("expected raw reviewer output in degraded log, got %s", logs)
	}
	if !strings.Contains(logs, "invalid llm output for precheck_l2_main: json decode failed") {
		t.Fatalf("expected invalid reviewer output summary in degraded log, got %s", logs)
	}
}

// TestPreCheckExecuteLogsRawUserContentWhenPayloadDebugEnabled verifies degraded pre-check warnings can include the original request text when the shared payload-debug logger switch is explicitly enabled for local troubleshooting.
// TestPreCheckExecuteLogsRawUserContentWhenPayloadDebugEnabled 用于验证当共享 payload 调试开关显式开启时，降级 pre-check 警告日志可以带出原始请求文本，方便本地排障。
func TestPreCheckExecuteLogsRawUserContentWhenPayloadDebugEnabled(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "warn", Format: "text", DebugPayloads: true})
	store := &stubPreCheckStore{recentTurnsErr: context.DeadlineExceeded}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{},
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				NeedMemory: false,
				Reason:     "self contained",
			},
		},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{},
		logger,
	)

	userContent := "这个改动会不会影响 SQLite schema 13 的兼容性？"
	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-debug-log"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: userContent,
	}); err != nil {
		t.Fatalf("execute pre-check with payload-debug logger: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "pre-check recent turns degraded") || !strings.Contains(logs, "TEXT(user_content)：") || !strings.Contains(logs, userContent) {
		t.Fatalf("expected payload-debug pre-check warning to include raw user content, got %s", logs)
	}
}

// TestPreCheckExecuteLogsFullStagePayloadsWhenPayloadDebugEnabled verifies the live pre-check flow emits readable stage-by-stage payload logs when the shared debug switch is explicitly enabled.
// TestPreCheckExecuteLogsFullStagePayloadsWhenPayloadDebugEnabled 用于验证当共享调试开关显式开启时，实时 pre-check 流程会输出可读的逐阶段载荷日志。
func TestPreCheckExecuteLogsFullStagePayloadsWhenPayloadDebugEnabled(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
	store := &stubPreCheckStore{
		recentTurns: []logicdomain.SessionTurnRecord{
			{ID: 7, SessionID: 41, Details: "上一轮确认并发控制改成 channel。", DetailsBudget: 20, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
		},
	}
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "为什么改成 channel",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "项目已经决定用 channel 替代 mutex。",
							DetailsPreview: "这是当前项目的并发实现决策。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.93,
						},
					},
				},
			},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"为什么改成 channel"},
				NeedMemory: true,
				Reason:     "needs architecture memory",
			},
		},
		&stubPreCheckReviewer{
			result: logicdomain.PreCheckMemoryReviewResult{SelectedCandidateNumbers: []int{1}},
		},
		&stubPreCheckAssembler{
			text: "assembled adopted context",
			items: []logicdomain.ContextItem{
				{Kind: "memory", Title: "混合召回记忆", Text: "项目已经决定用 channel 替代 mutex。", Source: "memory", Score: 0.93, TurnID: 8},
			},
		},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		logger,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-stage-debug"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "为什么这里要换成 channel？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}

	logs := logBuf.String()
	expectedMessages := []string{
		`MSG："pre-check recent turns prepared"`,
		`MSG："pre-check intent analyzed"`,
		`MSG："pre-check memory query prepared"`,
		`MSG："pre-check memory candidates recalled"`,
		`MSG："pre-check memory candidates reviewed"`,
		`MSG："pre-check lifecycle write-back completed"`,
		`MSG："pre-check finalized"`,
	}
	for _, message := range expectedMessages {
		if !strings.Contains(logs, message) {
			t.Fatalf("expected stage log %s, got %s", message, logs)
		}
	}
	if !strings.Contains(logs, "为什么这里要换成 channel？") || !strings.Contains(logs, "项目已经决定用 channel 替代 mutex。") {
		t.Fatalf("expected debug stage logs to include plaintext payloads, got %s", logs)
	}
}

// TestPreCheckExecuteLogsRawRecallAndBestRejectedHit verifies pre-check now emits the grouped raw recall count plus the strongest filtered-out candidate so zero-candidate outcomes remain explainable.
// TestPreCheckExecuteLogsRawRecallAndBestRejectedHit 用于验证 pre-check 现在会记录分组原始召回数量以及最强的被过滤候选，让零候选结果仍然可解释。
func TestPreCheckExecuteLogsRawRecallAndBestRejectedHit(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "用户购车计划的历史记录和相关偏好",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 601},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 77},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "用户购车预算曾在 100-150 万之间。",
							DetailsPreview: "用户考虑过保时捷卡宴并关注性能与空间。",
							Category:       logicdomain.MemoryNodeCategoryGeneral,
							Score:          0.61,
							Origin:         "hybrid_rrf",
						},
					},
				},
			},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"用户购车计划的历史记录和相关偏好"},
				NeedMemory: true,
				Reason:     "需要长期记忆",
			},
		},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 5, MinSimilarityScore: 0.8, HistoryTurns: 3, MaxInputTokens: 200},
		logger,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-threshold-log"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  7,
			SessionKey: "sess-car",
			UserID:     1,
			TeamID:     1,
			SpaceID:    1,
			ProjectID:  1,
		},
		UserContent: "之前我询问了你买车的事情，你还记得么",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if result.ShouldInject {
		t.Fatalf("expected zero adopted candidates when all raw hits fall below threshold, got %#v", result)
	}

	logs := logBuf.String()
	expectedMessages := []string{
		`MSG："pre-check raw memory hits returned"`,
		`MSG："pre-check memory candidate filtering applied"`,
		`MSG："pre-check memory candidates recalled"`,
	}
	for _, message := range expectedMessages {
		if !strings.Contains(logs, message) {
			t.Fatalf("expected stage log %s, got %s", message, logs)
		}
	}
	expectedFragments := []string{
		`raw_hit_count：1`,
		`below_threshold_count：1`,
		`review_candidate_count：0`,
		`"best_raw_hit":`,
		`"best_below_threshold_hit":`,
		`"memory_id": 601`,
		`"review_score": 0.61`,
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected logs to contain %s, got %s", fragment, logs)
		}
	}
}

// TestPreCheckExecuteEncryptsStagePayloadsWhenPayloadProtectionEnabled verifies the live pre-check flow keeps its stage payloads out of plaintext logs while still leaving encrypted audit envelopes for later forensic inspection.
// TestPreCheckExecuteEncryptsStagePayloadsWhenPayloadProtectionEnabled 用于验证实时 pre-check 流程在保护模式下不会把阶段载荷写成明文，但仍会留下可供后续取证的加密信封。
func TestPreCheckExecuteEncryptsStagePayloadsWhenPayloadProtectionEnabled(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{
		Level:                "info",
		Format:               "text",
		ProtectPayloads:      true,
		PayloadEncryptionKey: "0123456789abcdef0123456789abcdef",
	})
	store := &stubPreCheckStore{
		recentTurns: []logicdomain.SessionTurnRecord{
			{ID: 7, SessionID: 41, Details: "上一轮确认并发控制改成 channel。", DetailsBudget: 20, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{},
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				NeedMemory: false,
				Reason:     "question is self-contained",
			},
		},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{HistoryTurns: 4, MaxInputTokens: 200},
		logger,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-stage-protected"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "为什么这里要换成 channel？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, `MSG："pre-check intent analyzed"`) || !strings.Contains(logs, `MSG："pre-check finalized"`) {
		t.Fatalf("expected protected stage logs, got %s", logs)
	}
	if strings.Contains(logs, "为什么这里要换成 channel？") || strings.Contains(logs, "上一轮确认并发控制改成 channel。") {
		t.Fatalf("expected protected stage logs to hide plaintext payloads, got %s", logs)
	}
	if !strings.Contains(logs, "JSON(stage_payload_protected)：") || !strings.Contains(logs, `"algorithm": "AES-256-GCM"`) {
		t.Fatalf("expected protected payload envelopes in stage logs, got %s", logs)
	}
}
