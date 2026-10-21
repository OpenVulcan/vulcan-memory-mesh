// Memory recall-stage diagnostics with protected query logging.
// 应用用例层记忆召回阶段诊断与受保护的查询日志。
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"strconv"
	"strings"
)

// memoryQueryLogFields converts one live search query into either a redacted diagnostic summary or a full debug payload according to the shared payload-debug logger switch.
// memoryQueryLogFields 用于根据共享 payload 调试开关，把实时检索 query 转成脱敏诊断字段或完整调试正文。
func memoryQueryLogFields(logger *logx.Logger, query string) []any {
	normalized := strings.TrimSpace(query)
	if logger != nil && logger.PayloadDebugEnabled() {
		return []any{"query", normalized}
	}
	return []any{
		"query_len", len(normalized),
		"query_sha256", shortLogDigest(normalized),
	}
}

// memoryQueryHitLogPayload stores one compact search-hit snapshot for diagnostics so operators can see the best candidate without dumping the full hit list every time.
// memoryQueryHitLogPayload 用于保存一条紧凑的检索命中快照，便于运维看到最佳候选，而不用每次都把整份命中列表全部打进日志。
type memoryQueryHitLogPayload struct {
	VectorID       string   `json:"vector_id,omitempty"`
	MemoryID       uint64   `json:"memory_id,omitempty"`
	SourceTurnID   uint64   `json:"source_turn_id,omitempty"`
	Score          float64  `json:"score"`
	RawScore       *float64 `json:"raw_score,omitempty"`
	Origin         string   `json:"origin,omitempty"`
	Abstract       string   `json:"abstract,omitempty"`
	DetailsPreview string   `json:"details_preview,omitempty"`
	TextPreview    string   `json:"text_preview,omitempty"`
}

// logMemorySearchStage records one searchable retrieval checkpoint so operators can tell whether a miss happened in vector recall, hybrid fusion, rerank, or later pruning.
// logMemorySearchStage 用于记录一条可检索的检索阶段检查点，让运维可以分辨未命中是发生在向量召回、混合融合、rerank 还是更后面的裁剪步骤。
func (u *MemoryUseCase) logMemorySearchStage(ctx context.Context, message, query string, fields []any, payload any) {
	if u == nil || u.logger == nil {
		return
	}
	baseFields := []any{
		"trace_id", trace.IDFromContext(ctx),
	}
	baseFields = append(baseFields, memoryQueryLogFields(u.logger, query)...)
	baseFields = append(baseFields, fields...)
	baseFields = u.logger.AppendPayloadFields(baseFields, "stage_payload", payload)
	u.logger.Info(message, baseFields...)
}

// firstRawMemoryHit returns the first raw vector-store hit when one exists so the caller can summarize the top ANN candidate safely.
// firstRawMemoryHit 用于在存在结果时返回第一条原始向量命中，让调用方可以安全地概括最靠前的 ANN 候选。
func firstRawMemoryHit(hits []logicdomain.MemoryHit) *logicdomain.MemoryHit {
	if len(hits) == 0 {
		return nil
	}
	return &hits[0]
}

// firstMemoryQueryHit returns the first mapped public hit when one exists so stage logs can surface the current top candidate after each ranking step.
// firstMemoryQueryHit 用于在存在结果时返回第一条公开命中，让阶段日志能在每个排序步骤后展示当前 top candidate。
func firstMemoryQueryHit(hits []MemoryQueryHit) *MemoryQueryHit {
	if len(hits) == 0 {
		return nil
	}
	return &hits[0]
}

// summarizeRawMemoryHitForLog converts one raw vector-store hit into a compact log payload that keeps only the fields needed to diagnose recall quality.
// summarizeRawMemoryHitForLog 用于把一条原始向量命中转换成紧凑日志载荷，只保留排查召回质量所需的字段。
func summarizeRawMemoryHitForLog(hit *logicdomain.MemoryHit) *memoryQueryHitLogPayload {
	if hit == nil {
		return nil
	}
	return &memoryQueryHitLogPayload{
		VectorID:    strings.TrimSpace(hit.ID),
		Score:       hit.Score,
		RawScore:    memoryHitRawScoreForLog(hit.Metadata),
		TextPreview: strings.TrimSpace(hit.Text),
	}
}

// summarizeMemoryQueryHitForLog converts one mapped hit into a compact log payload so later filters can still report “the best available memory” even when nothing survives.
// summarizeMemoryQueryHitForLog 用于把一条映射后的命中转换成紧凑日志载荷，使后续过滤即便把所有候选都裁掉，日志里仍能报告“最接近的那条记忆”。
func summarizeMemoryQueryHitForLog(hit *MemoryQueryHit) *memoryQueryHitLogPayload {
	if hit == nil {
		return nil
	}
	return &memoryQueryHitLogPayload{
		MemoryID:       hit.MemoryRef.ID,
		SourceTurnID:   hit.SourceRef.ID,
		Score:          hit.Score,
		Origin:         strings.TrimSpace(hit.Origin),
		Abstract:       strings.TrimSpace(hit.Abstract),
		DetailsPreview: strings.TrimSpace(hit.DetailsPreview),
	}
}

// memoryHitRawScoreForLog restores the original SQL-side fused score from metadata so operators can distinguish the stable caller-facing score from the raw internal RRF value.
// memoryHitRawScoreForLog 用于从 metadata 里恢复 SQL 侧原始融合分，让运维可以区分稳定的调用方分数与内部 RRF 原始值。
func memoryHitRawScoreForLog(metadata map[string]string) *float64 {
	raw := strings.TrimSpace(metadata["raw_score"])
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &value
}

// shortLogDigest produces one short stable digest for sensitive runtime strings so logs can correlate repeated failures without leaking the underlying text.
// shortLogDigest 用于为敏感运行时字符串生成短且稳定的摘要，让日志能够关联重复故障，同时不泄露底层文本。
func shortLogDigest(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}
