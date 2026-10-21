// Memory lifecycle decay and bounded score conversion.
// 应用用例层记忆生命周期衰减与有界分数转换。
package usecase

import (
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"math"
	"sort"
	"time"
)

// applyWeibullDecaySearchHits reapplies one read-time memory-lifecycle prior after fusion and rerank so stale but still unexpired rows stop dominating recall.
// applyWeibullDecaySearchHits 用于在融合和 rerank 之后重新施加一次“读时记忆生命周期先验”，让陈旧但尚未过期的记录不再长期垄断召回。
func (u *MemoryUseCase) applyWeibullDecaySearchHits(hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.weibullEnabled || len(hits) <= 1 {
		return hits
	}
	now := time.Now().UTC()
	scored := append([]MemoryQueryHit(nil), hits...)
	changed := false
	for idx := range scored {
		nextScore := clampUnitScore(scored[idx].Score * u.weibullDecayMultiplier(scored[idx], now))
		if math.Abs(nextScore-scored[idx].Score) > 1e-9 {
			changed = true
		}
		scored[idx].Score = nextScore
	}
	if !changed {
		return scored
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].MemoryRef.ID < scored[j].MemoryRef.ID
		}
		return scored[i].Score > scored[j].Score
	})
	return scored
}

// weibullDecayMultiplier converts lifecycle evidence on one memory hit into a stable read-time score multiplier without changing hard expiry semantics.
// weibullDecayMultiplier 用于把单条记忆的生命周期证据转换成稳定的读时分数乘子，同时不改变现有硬过期语义。
func (u *MemoryUseCase) weibullDecayMultiplier(hit MemoryQueryHit, now time.Time) float64 {
	if u == nil || hit.DecayDisabled {
		return 1
	}
	anchor := latestMemoryReinforcementTime(hit)
	if anchor.IsZero() || !now.After(anchor) {
		return 1
	}
	ageHours := now.Sub(anchor).Hours()
	if ageHours <= 0 {
		return 1
	}

	scaleHours := u.weibullScaleHours
	scaleHours *= memoryLevelDecayScale(hit.MemoryLevel)
	scaleHours *= memoryPriorityDecayScale(hit.Priority)
	scaleHours *= memoryScopeDecayScale(hit.ScopeLevel)
	scaleHours *= 1 + 0.08*float64(max(hit.RefreshWeight, 0))
	scaleHours *= 1 + u.weibullReinforceWeight*math.Log1p(float64(max(hit.ReinforcementCount, 0)))
	scaleHours *= 1 + u.weibullCrossSessionBoost*float64(max(hit.CrossSessionAdoptedCount, 0))
	if scaleHours <= 0 {
		return 1
	}

	survival := math.Exp(-math.Pow(ageHours/scaleHours, u.weibullShape))
	if math.IsNaN(survival) || math.IsInf(survival, 0) {
		return 1
	}
	if survival < u.weibullMinMultiplier {
		survival = u.weibullMinMultiplier
	}
	return clampUnitScore(survival)
}

// latestMemoryReinforcementTime chooses the freshest lifecycle timestamp that should anchor Weibull age calculation for one durable memory row.
// latestMemoryReinforcementTime 用于挑出一条长期记忆最“新鲜”的生命周期时间点，作为 Weibull 年龄计算的锚点。
func latestMemoryReinforcementTime(hit MemoryQueryHit) time.Time {
	latest := zeroOrUTC(hit.CreatedAt)
	candidates := []time.Time{
		hit.LastReinforcedAt,
		hit.LastAdoptedAt,
		hit.LastRecalledAt,
	}
	for _, candidate := range candidates {
		candidate = zeroOrUTC(candidate)
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

// memoryLevelDecayScale stretches the Weibull scale for higher-level memories so stable or core knowledge decays more slowly than short-term facts.
// memoryLevelDecayScale 用于为更高等级的记忆拉长 Weibull 尺度，让稳定或核心知识比短期事实衰减得更慢。
func memoryLevelDecayScale(level int) float64 {
	switch level {
	case logicdomain.MemoryLevelSession:
		return 0.8
	case logicdomain.MemoryLevelPhase:
		return 1.0
	case logicdomain.MemoryLevelStable:
		return 1.5
	case logicdomain.MemoryLevelPersistent:
		return 2.4
	default:
		return 1.0
	}
}

// memoryPriorityDecayScale slightly protects high-priority memories during read-time decay so hard rules do not disappear behind fresher trivia.
// memoryPriorityDecayScale 用于在读时衰减里轻微保护高优先级记忆，避免硬规则被更新鲜但无关紧要的细节压下去。
func memoryPriorityDecayScale(priority int) float64 {
	switch priority {
	case logicdomain.MemoryPriorityP0:
		return 1.35
	case logicdomain.MemoryPriorityP1:
		return 1.15
	default:
		return 1.0
	}
}

// memoryScopeDecayScale gives broader-scope memories a slightly longer decay horizon because they are more likely to remain useful across requests.
// memoryScopeDecayScale 用于给更大作用域的记忆略长的衰减视窗，因为它们跨请求继续有用的概率更高。
func memoryScopeDecayScale(scopeLevel int) float64 {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return 0.9
	case logicdomain.MemoryScopeLevelUser:
		return 1.25
	default:
		return 1.1
	}
}

// zeroOrUTC normalizes lifecycle timestamps used by read-time ranking so helper callers can compare zero values without extra branching.
// zeroOrUTC 用于规范化读时排序使用的生命周期时间戳，让调用方在比较零值时不必反复写分支。
func zeroOrUTC(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}

// clampUnitScore keeps ranking multipliers and final scores inside the stable 0..1 range expected by pre-check filtering, while also collapsing NaN and infinity so one unstable upstream provider value cannot poison later sorting or RPC responses.
// clampUnitScore 用于把排序乘子和最终分数都钳制在 pre-check 过滤所期望的稳定 0..1 区间，并额外吸收 NaN 与无穷值，避免上游不稳定分数污染后续排序或 RPC 响应。
func clampUnitScore(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if math.IsInf(value, 1) {
		return 1
	}
	if math.IsInf(value, -1) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
