// Pre-check recent-turn loading and prompt budgeting.
// 应用用例层 pre-check 最近轮次读取与提示词预算。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"strings"
)

// loadRecentTurnContexts loads the recent session turns, trims them by budget, and converts them into refined-or-raw turn fragments for stage-one reasoning.
// loadRecentTurnContexts 用于加载最近 session turn、按预算裁剪，并把它们转换成“提炼文或原文”的第一层推理片段。
func (u *PreCheckUseCase) loadRecentTurnContexts(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.PreCheckTurnContext, error) {
	if u.store == nil || u.config.HistoryTurns <= 0 {
		return []logicdomain.PreCheckTurnContext{}, nil
	}
	rows, err := u.store.LoadRecentSessionTurns(ctx, session, u.config.HistoryTurns)
	if err != nil {
		return nil, err
	}
	rows = trimRecentTurnsByBudget(rows, u.config.MaxInputTokens)
	contexts := make([]logicdomain.PreCheckTurnContext, 0, len(rows))
	for _, row := range rows {
		context := logicdomain.PreCheckTurnContext{TurnID: row.ID}
		if row.ExtractedStatus == logicdomain.TurnExtractedStatusDone && strings.TrimSpace(row.Details) != "" {
			context.ContentType = "DETAILS"
			context.Content = strings.TrimSpace(row.Details)
		} else {
			context.ContentType = "RAW_TURN"
			context.Content = strings.TrimSpace(row.DehydratedContent)
		}
		if context.TurnID == 0 || strings.TrimSpace(context.Content) == "" {
			continue
		}
		contexts = append(contexts, context)
	}
	return contexts, nil
}

// trimRecentTurnsByBudget keeps the newest turns under the configured token budget while preserving final chronological order.
// trimRecentTurnsByBudget 用于在配置的 token 预算内保留最新 turn，并在最终结果中维持时间顺序。
func trimRecentTurnsByBudget(rows []logicdomain.SessionTurnRecord, maxInputTokens int) []logicdomain.SessionTurnRecord {
	if len(rows) == 0 || maxInputTokens <= 0 {
		return append([]logicdomain.SessionTurnRecord(nil), rows...)
	}
	selected := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	total := 0
	for idx := len(rows) - 1; idx >= 0; idx-- {
		budget := preCheckTurnBudget(rows[idx])
		if len(selected) > 0 && total+budget > maxInputTokens {
			break
		}
		selected = append(selected, rows[idx])
		total += budget
	}
	if len(selected) == 0 {
		return []logicdomain.SessionTurnRecord{rows[len(rows)-1]}
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

// preCheckTurnBudget chooses the extracted-details budget when available and falls back to dehydrated budget for still-pending turns.
// preCheckTurnBudget 用于在存在 details 时优先使用已提炼预算，并对仍为 pending 的 turn 回退到脱水预算。
func preCheckTurnBudget(row logicdomain.SessionTurnRecord) int {
	if row.ExtractedStatus == logicdomain.TurnExtractedStatusDone && strings.TrimSpace(row.Details) != "" {
		if row.DetailsBudget > 0 {
			return row.DetailsBudget
		}
		return estimatePreCheckBudget(row.Details)
	}
	if row.DehydratedBudget > 0 {
		return row.DehydratedBudget
	}
	return estimatePreCheckBudget(row.DehydratedContent)
}

// estimatePreCheckBudget applies the local token estimator to one turn fragment so recent-turn trimming stays aligned with other text-budget decisions in the repo.
// estimatePreCheckBudget 用于对 turn 片段应用本地 token 估算器，让最近 turn 裁剪与仓库里其他文本预算逻辑保持一致。
func estimatePreCheckBudget(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return estimator.Estimate(text)
}
