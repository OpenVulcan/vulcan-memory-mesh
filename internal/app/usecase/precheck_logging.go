// Pre-check diagnostic payloads and stage logging.
// 应用用例层 pre-check 诊断载荷与阶段日志。
package usecase

import (
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
)

// preCheckRawRecallGroupLogPayload stores one grouped unified-search snapshot before the pre-check similarity floor removes weak hits.
// preCheckRawRecallGroupLogPayload 用于保存 pre-check 相似度阈值裁剪前的一组 unified-search 命中快照。
type preCheckRawRecallGroupLogPayload struct {
	QueryIndex  int                       `json:"query_index"`
	Query       string                    `json:"query,omitempty"`
	RawHitCount int                       `json:"raw_hit_count"`
	TopRawHit   *memoryQueryHitLogPayload `json:"top_raw_hit,omitempty"`
}

// preCheckThresholdHitLogPayload stores the strongest available candidate around the similarity floor so operators can still inspect the nearest memory when nothing survives.
// preCheckThresholdHitLogPayload 用于保存围绕相似度阈值的最强候选，让运维即便在最终无命中时仍能看到最接近的记忆。
type preCheckThresholdHitLogPayload struct {
	MemoryID       uint64  `json:"memory_id,omitempty"`
	SourceTurnID   uint64  `json:"source_turn_id,omitempty"`
	RawScore       float64 `json:"raw_score"`
	ReviewScore    float64 `json:"review_score"`
	Origin         string  `json:"origin,omitempty"`
	Abstract       string  `json:"abstract,omitempty"`
	DetailsPreview string  `json:"details_preview,omitempty"`
}

// buildPreCheckRawRecallLogs summarizes each grouped unified-search result before threshold filtering so operators can distinguish “no recall” from “recalled then filtered”.
// buildPreCheckRawRecallLogs 用于在阈值过滤前汇总每组 unified-search 结果，让运维能区分“根本没召回”和“召回后被过滤”。
func buildPreCheckRawRecallLogs(results []MemoryQueryGroupResult) ([]preCheckRawRecallGroupLogPayload, int) {
	if len(results) == 0 {
		return nil, 0
	}
	groups := make([]preCheckRawRecallGroupLogPayload, 0, len(results))
	rawHitCount := 0
	for _, group := range results {
		rawHitCount += len(group.Hits)
		groups = append(groups, preCheckRawRecallGroupLogPayload{
			QueryIndex:  group.QueryIndex,
			Query:       group.Query,
			RawHitCount: len(group.Hits),
			TopRawHit:   summarizeMemoryQueryHitForLog(firstMemoryQueryHit(group.Hits)),
		})
	}
	return groups, rawHitCount
}

// summarizePreCheckThresholdHitForLog converts one unified-search hit plus its reviewer-facing score into a compact log payload for threshold debugging.
// summarizePreCheckThresholdHitForLog 用于把 unified-search 命中及其 reviewer 侧分数转换成紧凑日志载荷，方便排查阈值过滤。
func summarizePreCheckThresholdHitForLog(hit MemoryQueryHit, reviewScore float64) *preCheckThresholdHitLogPayload {
	return &preCheckThresholdHitLogPayload{
		MemoryID:       hit.MemoryRef.ID,
		SourceTurnID:   hit.SourceRef.ID,
		RawScore:       hit.Score,
		ReviewScore:    reviewScore,
		Origin:         strings.TrimSpace(hit.Origin),
		Abstract:       strings.TrimSpace(hit.Abstract),
		DetailsPreview: strings.TrimSpace(hit.DetailsPreview),
	}
}

// choosePreferredPreCheckThresholdHit keeps the stronger threshold-debug candidate so logs always surface the nearest available memory first.
// choosePreferredPreCheckThresholdHit 用于保留更强的阈值调试候选，保证日志总是优先展示最接近的那条记忆。
func choosePreferredPreCheckThresholdHit(current, incoming *preCheckThresholdHitLogPayload) *preCheckThresholdHitLogPayload {
	if incoming == nil {
		return current
	}
	if current == nil {
		return incoming
	}
	if incoming.ReviewScore != current.ReviewScore {
		if incoming.ReviewScore > current.ReviewScore {
			return incoming
		}
		return current
	}
	if incoming.RawScore != current.RawScore {
		if incoming.RawScore > current.RawScore {
			return incoming
		}
		return current
	}
	if len(incoming.DetailsPreview) != len(current.DetailsPreview) {
		if len(incoming.DetailsPreview) > len(current.DetailsPreview) {
			return incoming
		}
		return current
	}
	if len(incoming.Abstract) != len(current.Abstract) {
		if len(incoming.Abstract) > len(current.Abstract) {
			return incoming
		}
		return current
	}
	if incoming.MemoryID < current.MemoryID {
		return incoming
	}
	return current
}

// logPreCheckWarn keeps degraded-path logging uniform so field triage remains easy after rollout, while still allowing the shared payload-debug switch to expose the original request text during local troubleshooting.
// logPreCheckWarn 用于统一降级路径日志，方便上线后排查问题；同时在共享 payload 调试开关开启时，允许本地排障看到原始请求文本。
func (u *PreCheckUseCase) logPreCheckWarn(message, traceID string, session logicdomain.SessionRef, userContent string, err error) {
	if u.logger == nil {
		return
	}
	fields := []any{
		"trace_id", traceID,
		"session_key", session.SessionKey,
		"session_id", session.SessionID,
		"project_id", session.ProjectID,
		"user_id", session.UserID,
	}
	fields = u.appendPreCheckInvalidLLMOutputLogFields(fields, err)
	if u.logger.PayloadDebugEnabled() {
		fields = append(fields, "user_content", strings.TrimSpace(userContent))
	} else {
		fields = u.logger.AppendPayloadFields(fields, "warning_payload", map[string]any{
			"user_content": strings.TrimSpace(userContent),
		})
	}
	fields = append(fields, "err", err)
	u.logger.Warn(message, fields...)
}

// appendPreCheckInvalidLLMOutputLogFields adds scene-specific LLM diagnostics to degraded pre-check warnings without changing non-LLM warning logs.
// appendPreCheckInvalidLLMOutputLogFields 用于把按场景归属的 LLM 诊断字段追加到 pre-check 降级警告中，同时不改变非 LLM 警告日志。
func (u *PreCheckUseCase) appendPreCheckInvalidLLMOutputLogFields(fields []any, err error) []any {
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		return fields
	}
	scene := strings.TrimSpace(invalid.Scene)
	executionFound := invalid.Execution != nil
	if !executionFound && scene != "" {
		fields = append(fields, "llm_scene", scene)
	}
	if executionFound {
		fields = appendLLMExecutionLogFields(fields, *invalid.Execution)
	}
	if model := u.preCheckLLMFailureModel(scene); !executionFound && model != "" {
		fields = append(fields, "configured_model", model)
	}
	// Pre-check intentionally degrades malformed model output into an empty result, so raw provider output must remain available in server logs for prompt and parser repair.
	// pre-check 会把畸形模型输出降级为空结果，因此原始 provider 响应必须留在服务端日志中，供修复提示词和解析器使用。
	if raw := strings.TrimSpace(invalid.Raw); raw != "" {
		fields = append(fields, "llm_raw_output", invalid.Raw)
	}
	return fields
}

// preCheckLLMFailureModel resolves the configured model for the exact pre-check LLM scene that produced one InvalidLLMOutputError.
// preCheckLLMFailureModel 用于根据产生 InvalidLLMOutputError 的具体 pre-check LLM 场景解析对应的配置模型。
func (u *PreCheckUseCase) preCheckLLMFailureModel(scene string) string {
	switch scene {
	case "precheck_l1_main":
		if u == nil || u.intent == nil {
			return ""
		}
		return strings.TrimSpace(u.intent.ExtractModel())
	case "precheck_l2_main":
		if u == nil || u.reviewer == nil {
			return ""
		}
		return strings.TrimSpace(u.reviewer.ReviewModel())
	default:
		return ""
	}
}

// logPreCheckStage records one structured stage checkpoint so operators can follow the full pre-check flow, while payload details stay plaintext in debug mode or encrypted in protected mode.
// logPreCheckStage 用于记录一条结构化阶段检查点，让运维可以串起完整 pre-check 流程；其中载荷详情会在 debug 模式下明文输出，在保护模式下加密输出。
func (u *PreCheckUseCase) logPreCheckStage(message, traceID string, session logicdomain.SessionRef, fields []any, payload any) {
	if u == nil || u.logger == nil {
		return
	}
	baseFields := []any{
		"trace_id", traceID,
		"session_key", session.SessionKey,
		"session_id", session.SessionID,
		"team_id", session.TeamID,
		"space_id", session.SpaceID,
		"project_id", session.ProjectID,
		"user_id", session.UserID,
	}
	baseFields = append(baseFields, fields...)
	baseFields = u.logger.AppendPayloadFields(baseFields, "stage_payload", payload)
	u.logger.Info(message, baseFields...)
}

// logPreCheckFinal records the final execution state so callers can distinguish “skipped recall”, “selected memories”, and degraded empty results without opening the surrounding stage logs first.
// logPreCheckFinal 用于记录最终执行状态，让调用方无需先翻看上下游阶段日志，也能分辨“跳过检索”“选中了记忆”还是“降级为空结果”。
func (u *PreCheckUseCase) logPreCheckFinal(traceID string, session logicdomain.SessionRef, result PreCheckResult) {
	u.logPreCheckStage("pre-check finalized", traceID, session, []any{
		"should_inject", result.ShouldInject,
		"degraded", result.Degraded,
		"context_item_count", len(result.ContextItems),
		"context_text_present", strings.TrimSpace(result.ContextText) != "",
	}, map[string]any{
		"should_inject": result.ShouldInject,
		"degraded":      result.Degraded,
		"context_text":  result.ContextText,
		"context_items": result.ContextItems,
		"trace_id":      result.TraceID,
	})
}
