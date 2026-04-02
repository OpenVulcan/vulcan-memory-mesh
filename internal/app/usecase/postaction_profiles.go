// postaction_profiles.go implements the queued profile-review, lifecycle, and profile-rendering helpers used by post-action batches.
// postaction_profiles.go 用于实现 post-action 批处理中与画像评审、生命周期计算和画像渲染相关的辅助逻辑。
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// convergeExpiredProfiles runs the periodic profile-lifecycle convergence path so due active nodes are materialized as expired rows and cached profile blobs stay in sync.
// convergeExpiredProfiles 用于执行周期性的画像生命周期收敛，让到期 active 节点真实落成 expired 行，并保持缓存 profile Blob 同步。
func (u *PostActionUseCase) convergeExpiredProfiles() {
	if u == nil || u.store == nil {
		return
	}
	ctx := u.queueCtx
	if ctx == nil {
		ctx = context.Background()
	}
	targets, err := u.store.ConvergeExpiredProfileNodes(ctx, 256)
	if err != nil {
		u.markQueueMaintenanceBackoff("expired profile convergence", err)
		if u.logger != nil {
			u.logger.Error("post-action expired profile convergence failed", "err", err)
		}
		return
	}
	if len(targets) == 0 {
		return
	}

	// Render the latest active-node snapshots back into durable scope profile blobs so expired rows immediately disappear from future injections.
	// 把最新 active 节点快照重新渲染成长期 scope 画像文本，让过期行能立即从后续注入内容里消失。
	rendered := logicdomain.RenderedProfileSet{
		UserProfiles:    map[uint64]string{},
		TeamProfiles:    map[uint64]string{},
		SpaceProfiles:   map[uint64]string{},
		ProjectProfiles: map[uint64]string{},
	}
	for _, target := range targets {
		profileText := renderProfileTimeline(target.Nodes, nil, nil)
		switch target.ProfileType {
		case logicdomain.ProfileTypeUser:
			rendered.UserProfiles[target.BindID] = profileText
		case logicdomain.ProfileTypeTeam:
			rendered.TeamProfiles[target.BindID] = profileText
		case logicdomain.ProfileTypeSpace:
			rendered.SpaceProfiles[target.BindID] = profileText
		case logicdomain.ProfileTypeProject:
			rendered.ProjectProfiles[target.BindID] = profileText
		}
	}
	if err := u.store.ReplaceRenderedProfiles(ctx, rendered); err != nil {
		u.markQueueMaintenanceBackoff("expired profile render update", err)
		if u.logger != nil {
			u.logger.Error("post-action expired profile render update failed", "err", err)
		}
		return
	}
	if u.logger != nil {
		u.logger.Info(
			"post-action expired profiles converged",
			"affected_targets", len(targets),
			"user_profile_count", len(rendered.UserProfiles),
			"team_profile_count", len(rendered.TeamProfiles),
			"space_profile_count", len(rendered.SpaceProfiles),
			"project_profile_count", len(rendered.ProjectProfiles),
		)
	}
}

// reviewTurnProfiles reuses the batch profile-review pipeline for one immediate turn so the synchronous post-action path can keep profile decisions aligned with the existing reviewer contract.
// reviewTurnProfiles 用于把批量画像评审流水线复用到单条即时 turn 上，让同步 post-action 路径继续遵守现有 reviewer 契约。
func (u *PostActionUseCase) reviewTurnProfiles(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis *logicdomain.TurnAnalysis) error {
	if u == nil || analysis == nil || len(analysis.ProfileNodes) == 0 {
		return nil
	}
	batch := logicdomain.SessionBatchAnalysis{
		Turns: []logicdomain.SessionBatchTurnAnalysis{{
			TurnID:        turn.ID,
			Details:       analysis.Details,
			DetailsBudget: analysis.DetailsBudget,
			ProfileNodes:  append([]logicdomain.ProfileNodeCandidate(nil), analysis.ProfileNodes...),
		}},
	}
	err := u.reviewSessionBatchProfiles(ctx, session, []logicdomain.SessionTurnRecord{{
		ID:        turn.ID,
		SessionID: turn.SessionID,
		ProjectID: turn.ProjectID,
		CreatedAt: choosePostActionCreatedAt(turn),
		UpdatedAt: turn.UpdatedAt,
	}}, &batch)
	if err != nil {
		return err
	}
	analysis.ProfileNodes = append([]logicdomain.ProfileNodeCandidate(nil), batch.Turns[0].ProfileNodes...)
	analysis.UserProfileMerged = batch.UserProfileMerged
	analysis.MergedUserProfile = batch.MergedUserProfile
	analysis.ProjectProfileMerged = batch.ProjectProfileMerged
	analysis.MergedProjectProfile = batch.MergedProjectProfile
	return nil
}

// reviewSessionBatchProfiles reviews all fresh profile nodes across the selected batch, applies active/invalid/supersede decisions, and rebuilds the rendered user/project profiles.
// reviewSessionBatchProfiles 用于评审所选批次中的全部新画像节点，应用 active/invalid/supersede 决策，并重建 user/project 的渲染画像文本。
func (u *PostActionUseCase) reviewSessionBatchProfiles(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.SessionTurnRecord, analysis *logicdomain.SessionBatchAnalysis) error {
	if u == nil || u.store == nil || u.profiles == nil || analysis == nil {
		return nil
	}

	// Stamp every fresh node with its source turn and profile date before sending the atomic review request.
	// 在发送原子化评审请求前，先为每条新节点补齐来源 turn 和画像日期。
	turnByID := make(map[uint64]logicdomain.SessionTurnRecord, len(turns))
	nodes := make([]logicdomain.ProfileNodeCandidate, 0)
	userRefs := make([]postActionProfileNodeRef, 0)
	projectRefs := make([]postActionProfileNodeRef, 0)
	for _, turn := range turns {
		turnByID[turn.ID] = turn
	}
	for turnIdx := range analysis.Turns {
		turn := turnByID[analysis.Turns[turnIdx].TurnID]
		profileDate := profileDateFromTurn(turn)
		for nodeIdx := range analysis.Turns[turnIdx].ProfileNodes {
			node := &analysis.Turns[turnIdx].ProfileNodes[nodeIdx]
			node.Status = logicdomain.ProfileStatusPending
			node.SourceTurnID = analysis.Turns[turnIdx].TurnID
			if strings.TrimSpace(node.ProfileDate) == "" {
				node.ProfileDate = profileDate
			}
			nodes = append(nodes, *node)
			switch node.ProfileType {
			case logicdomain.ProfileTypeUser:
				userRefs = append(userRefs, postActionProfileNodeRef{TurnIndex: turnIdx, NodeIndex: nodeIdx})
			case logicdomain.ProfileTypeProject:
				projectRefs = append(projectRefs, postActionProfileNodeRef{TurnIndex: turnIdx, NodeIndex: nodeIdx})
			}
		}
	}
	if len(nodes) == 0 {
		return nil
	}

	snapshot, err := u.store.LoadProfileReviewTargets(ctx, session)
	if err != nil {
		return fmt.Errorf("load profile review targets: %w", err)
	}
	reviewed, err := u.profiles.Review(ctx, snapshot, nodes)
	if err != nil {
		return err
	}

	userRetired, userUpdated, err := applySessionBatchProfileReviewSection(analysis, turnByID, userRefs, snapshot.UserNodes, reviewed.User, "user")
	if err != nil {
		return err
	}
	projectRetired, projectUpdated, err := applySessionBatchProfileReviewSection(analysis, turnByID, projectRefs, snapshot.ProjectNodes, reviewed.Project, "project")
	if err != nil {
		return err
	}

	if userUpdated {
		analysis.UserProfileMerged = true
		analysis.MergedUserProfile = renderProfileTimeline(snapshot.UserNodes, collectReviewedProfileNodes(analysis, logicdomain.ProfileTypeUser), userRetired)
	}
	if projectUpdated {
		analysis.ProjectProfileMerged = true
		analysis.MergedProjectProfile = renderProfileTimeline(snapshot.ProjectNodes, collectReviewedProfileNodes(analysis, logicdomain.ProfileTypeProject), projectRetired)
	}
	analysis.RetiredProfileNodeIDs = normalizeProfileNodeIDs(append(userRetired, projectRetired...))
	return nil
}

// applySessionBatchProfileReviewSection maps one validated review section back onto the original turn-local profile-node coordinates and derives lifecycle fields.
// applySessionBatchProfileReviewSection 用于把已校验的评审结果映射回原始的 turn 局部画像节点坐标，并推导生命周期字段。
func applySessionBatchProfileReviewSection(analysis *logicdomain.SessionBatchAnalysis, turnByID map[uint64]logicdomain.SessionTurnRecord, refs []postActionProfileNodeRef, activeNodes []logicdomain.ProfileActiveNodeRecord, section *logicdomain.ProfileReviewSection, label string) ([]uint64, bool, error) {
	if len(refs) == 0 {
		return nil, false, nil
	}
	if section == nil {
		return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("missing %s block", label)}
	}

	activeByID := make(map[uint64]logicdomain.ProfileActiveNodeRecord, len(activeNodes))
	for _, node := range activeNodes {
		activeByID[node.ID] = node
	}
	retiredSet := map[uint64]struct{}{}
	updated := false

	// First map accepted candidates back onto the fresh nodes so they become active and carry normalized content plus lifecycle metadata.
	// 先把 accepted 候选映射回新节点，让它们变成 active，并携带规范化内容和生命周期元数据。
	for _, accepted := range section.AcceptedCandidates {
		if accepted.CandidateIndex < 0 || accepted.CandidateIndex >= len(refs) {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s accepted candidate_index %d is out of range", label, accepted.CandidateIndex)}
		}
		ref := refs[accepted.CandidateIndex]
		node := &analysis.Turns[ref.TurnIndex].ProfileNodes[ref.NodeIndex]
		if !logicdomain.ValidProfilePriority(accepted.Priority) {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s accepted candidate priority is invalid", label)}
		}
		if !logicdomain.ValidProfileLevel(accepted.ProfileLevel) {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s accepted candidate level is invalid", label)}
		}
		supersedeIDs := normalizeProfileNodeIDs(accepted.SupersedeNodeIDs)
		for _, nodeID := range supersedeIDs {
			if _, ok := activeByID[nodeID]; !ok {
				return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s supersede_node_id %d was not present in active nodes", label, nodeID)}
			}
			if _, exists := retiredSet[nodeID]; exists {
				return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s node id %d is retired by multiple actions", label, nodeID)}
			}
			retiredSet[nodeID] = struct{}{}
		}
		node.Content = strings.TrimSpace(accepted.NormalizedContent)
		node.Priority = accepted.Priority
		node.ProfileLevel = accepted.ProfileLevel
		node.LevelReason = strings.TrimSpace(accepted.LevelReason)
		node.Status = logicdomain.ProfileStatusActive
		node.SupersedeNodeIDs = supersedeIDs
		node.RefreshWeight = computeProfileRefreshWeight(activeByID, supersedeIDs)
		node.ExpiresAt = computeProfileExpiry(profileBaseTime(turnByID[analysis.Turns[ref.TurnIndex].TurnID]), node.ProfileLevel, node.RefreshWeight)
		updated = true
	}

	// Then mark rejected candidates as invalid so the batch can still persist them for later auditing without polluting the active profile set.
	// 然后把被拒绝的候选标成 invalid，便于批处理仍然落库备案，但不会污染活跃画像集合。
	for _, idx := range section.InvalidCandidateIndexes {
		if idx < 0 || idx >= len(refs) {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s invalid candidate_index %d is out of range", label, idx)}
		}
		ref := refs[idx]
		analysis.Turns[ref.TurnIndex].ProfileNodes[ref.NodeIndex].Status = logicdomain.ProfileStatusInvalid
	}

	// Finally apply retire-only directives for old nodes that should leave the rendered profile without being replaced by a fresh node.
	// 最后应用 retire-only 指令，让某些旧节点在没有新替代节点的情况下也能退出渲染画像。
	for _, nodeID := range normalizeProfileNodeIDs(section.RetireOnlyNodeIDs) {
		if _, ok := activeByID[nodeID]; !ok {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s retire_only_node_id %d was not present in active nodes", label, nodeID)}
		}
		if _, exists := retiredSet[nodeID]; exists {
			return nil, false, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s node id %d is retired multiple times", label, nodeID)}
		}
		retiredSet[nodeID] = struct{}{}
		updated = true
	}
	return profileNodeIDSet(retiredSet), updated, nil
}

// collectReviewedProfileNodes extracts the fresh active profile nodes of one target kind from the analyzed batch so the renderer can rebuild the durable profile text.
// collectReviewedProfileNodes 用于从当前分析批次中提取某一类 target 的新 active 画像节点，供渲染器重建长期画像文本。
func collectReviewedProfileNodes(analysis *logicdomain.SessionBatchAnalysis, profileType int) []logicdomain.ProfileNodeCandidate {
	if analysis == nil {
		return nil
	}
	out := make([]logicdomain.ProfileNodeCandidate, 0)
	for _, turn := range analysis.Turns {
		for _, node := range turn.ProfileNodes {
			if node.ProfileType != profileType || node.Status != logicdomain.ProfileStatusActive {
				continue
			}
			out = append(out, node)
		}
	}
	return out
}

// renderProfileTimeline rebuilds one durable profile body from remaining active historical nodes plus the fresh active nodes accepted in the current batch.
// renderProfileTimeline 用于根据仍保留的历史 active 节点和当前批次新接纳的 active 节点，重建一个仅包含正文的长期画像文本。
func renderProfileTimeline(existing []logicdomain.ProfileActiveNodeRecord, fresh []logicdomain.ProfileNodeCandidate, retiredIDs []uint64) string {
	retiredSet := map[uint64]struct{}{}
	for _, nodeID := range retiredIDs {
		retiredSet[nodeID] = struct{}{}
	}
	renderNodes := make([]profileRenderNode, 0, len(existing)+len(fresh))
	for _, node := range existing {
		if _, retired := retiredSet[node.ID]; retired {
			continue
		}
		renderNodes = append(renderNodes, profileRenderNode{
			Date:          normalizeRenderDate(node.ProfileDate, node.CreatedAt),
			Priority:      node.Priority,
			ProfileLevel:  node.ProfileLevel,
			RefreshWeight: node.RefreshWeight,
			Content:       strings.TrimSpace(node.Content),
			CreatedAt:     node.CreatedAt,
		})
	}
	for _, node := range fresh {
		if node.Status != logicdomain.ProfileStatusActive {
			continue
		}
		renderNodes = append(renderNodes, profileRenderNode{
			Date:          normalizeRenderDate(node.ProfileDate, node.ExpiresAt),
			Priority:      node.Priority,
			ProfileLevel:  node.ProfileLevel,
			RefreshWeight: node.RefreshWeight,
			Content:       strings.TrimSpace(node.Content),
			CreatedAt:     profileRenderCreatedAt(node),
		})
	}
	if len(renderNodes) == 0 {
		return ""
	}
	sort.Slice(renderNodes, func(i, j int) bool {
		if renderNodes[i].Date != renderNodes[j].Date {
			return renderNodes[i].Date < renderNodes[j].Date
		}
		if renderNodes[i].Priority != renderNodes[j].Priority {
			return renderNodes[i].Priority < renderNodes[j].Priority
		}
		if renderNodes[i].RefreshWeight != renderNodes[j].RefreshWeight {
			return renderNodes[i].RefreshWeight > renderNodes[j].RefreshWeight
		}
		if !renderNodes[i].CreatedAt.Equal(renderNodes[j].CreatedAt) {
			return renderNodes[i].CreatedAt.Before(renderNodes[j].CreatedAt)
		}
		return renderNodes[i].Content < renderNodes[j].Content
	})

	var builder strings.Builder
	currentDate := ""
	for idx, node := range renderNodes {
		if node.Date != currentDate {
			if idx > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(node.Date)
			builder.WriteString(":\n")
			currentDate = node.Date
		}
		builder.WriteString("[")
		builder.WriteString(profilePriorityLabel(node.Priority))
		builder.WriteString("][")
		builder.WriteString(profileLevelLabel(node.ProfileLevel))
		builder.WriteString("][W")
		builder.WriteString(strconv.Itoa(node.RefreshWeight))
		builder.WriteString("] ")
		builder.WriteString(node.Content)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

// computeProfileRefreshWeight derives the new refresh weight from the superseded nodes so repeated reaffirmations keep extending freshness.
// computeProfileRefreshWeight 用于根据被替代的旧节点推导新的刷新权重，让重复确认过的画像持续延长新鲜度。
func computeProfileRefreshWeight(activeByID map[uint64]logicdomain.ProfileActiveNodeRecord, supersedeIDs []uint64) int {
	maxWeight := -1
	for _, nodeID := range supersedeIDs {
		if node, ok := activeByID[nodeID]; ok && node.RefreshWeight > maxWeight {
			maxWeight = node.RefreshWeight
		}
	}
	if maxWeight < 0 {
		return 0
	}
	return maxWeight + 1
}

// computeProfileExpiry applies the current level-based decay rules and refresh-weight extension to derive the next expiration timestamp.
// computeProfileExpiry 用于应用当前基于 level 的衰减规则和 refresh_weight 续期规则，推导下一次过期时间。
func computeProfileExpiry(base time.Time, level, refreshWeight int) time.Time {
	base = base.UTC()
	if base.IsZero() {
		base = time.Now().UTC()
	}
	type profileLifetimeConfig struct {
		Base time.Duration
		Step time.Duration
		Max  time.Duration
	}
	cfg := profileLifetimeConfig{}
	switch level {
	case logicdomain.ProfileLevelTransient:
		cfg = profileLifetimeConfig{Base: 72 * time.Hour, Step: 48 * time.Hour, Max: 14 * 24 * time.Hour}
	case logicdomain.ProfileLevelSituational:
		cfg = profileLifetimeConfig{Base: 14 * 24 * time.Hour, Step: 7 * 24 * time.Hour, Max: 90 * 24 * time.Hour}
	case logicdomain.ProfileLevelStable:
		cfg = profileLifetimeConfig{Base: 90 * 24 * time.Hour, Step: 30 * 24 * time.Hour, Max: 365 * 24 * time.Hour}
	case logicdomain.ProfileLevelPersistent:
		return time.Time{}
	default:
		return base.Add(14 * 24 * time.Hour)
	}
	ttl := cfg.Base + (time.Duration(refreshWeight) * cfg.Step)
	if cfg.Max > 0 && ttl > cfg.Max {
		ttl = cfg.Max
	}
	return base.Add(ttl)
}

// profileBaseTime prefers the original turn timestamp when deriving profile dates and expirations so profile nodes stay anchored to the dialogue timeline.
// profileBaseTime 用于在推导画像日期和过期时间时优先复用原始 turn 时间戳，让画像节点继续锚定到对话时间线。
func profileBaseTime(turn logicdomain.SessionTurnRecord) time.Time {
	if !turn.CreatedAt.IsZero() {
		return turn.CreatedAt.UTC()
	}
	return time.Now().UTC()
}

// profileDateFromTurn renders the profile date string from one persisted turn so new profile nodes can be grouped on the final timeline.
// profileDateFromTurn 用于从一条已持久化 turn 生成画像日期字符串，让新画像节点能在最终时间轴中按日期分组。
func profileDateFromTurn(turn logicdomain.SessionTurnRecord) string {
	return profileBaseTime(turn).Format("2006-01-02")
}

// normalizeRenderDate falls back to one timestamp-derived YYYY-MM-DD string when the stored profile_date is absent.
// normalizeRenderDate 用于在 profile_date 缺失时回退到时间戳推导出的 YYYY-MM-DD 字符串。
func normalizeRenderDate(profileDate string, fallback time.Time) string {
	date := strings.TrimSpace(profileDate)
	if date != "" {
		return date
	}
	if fallback.IsZero() {
		return "unknown"
	}
	return fallback.UTC().Format("2006-01-02")
}

// profileRenderCreatedAt derives a stable sort timestamp for one fresh profile node when the final rendered profile is rebuilt.
// profileRenderCreatedAt 用于在重建最终画像时，为一条新的画像节点推导稳定的排序时间戳。
func profileRenderCreatedAt(node logicdomain.ProfileNodeCandidate) time.Time {
	if parsed, err := time.Parse("2006-01-02", strings.TrimSpace(node.ProfileDate)); err == nil {
		return parsed.UTC()
	}
	if !node.ExpiresAt.IsZero() {
		return node.ExpiresAt.UTC()
	}
	return time.Time{}
}

// normalizeProfileNodeIDs removes zeros, duplicates, and unstable ordering from profile-node id lists used by the reviewer and persistence layers.
// normalizeProfileNodeIDs 用于去掉评审和持久化层使用的画像节点 id 列表中的零值、重复项和不稳定顺序。
func normalizeProfileNodeIDs(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

// profileNodeIDSet converts one set-like map back into a sorted slice so later persistence and rendering can stay deterministic.
// profileNodeIDSet 用于把一个集合 map 转回排序后的切片，让后续持久化和渲染保持确定性。
func profileNodeIDSet(values map[uint64]struct{}) []uint64 {
	out := make([]uint64, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

// profileRenderNode is the compact internal rendering shape used to rebuild the final durable profile blob.
// profileRenderNode 用于表示重建最终长期画像 Blob 时使用的紧凑内部渲染结构。
type profileRenderNode struct {
	Date          string
	Priority      int
	ProfileLevel  int
	RefreshWeight int
	Content       string
	CreatedAt     time.Time
}

// profilePriorityLabel renders the compact P-label used inside rebuilt user/project profile text.
// profilePriorityLabel 用于渲染重建后的 user/project 画像文本里使用的紧凑 P 标签。
func profilePriorityLabel(priority int) string {
	switch priority {
	case logicdomain.ProfilePriorityP0:
		return "P0"
	case logicdomain.ProfilePriorityP1:
		return "P1"
	default:
		return "P2"
	}
}

// profileLevelLabel renders the compact L-label used inside rebuilt user/project profile text.
// profileLevelLabel 用于渲染重建后的 user/project 画像文本里使用的紧凑 L 标签。
func profileLevelLabel(level int) string {
	switch level {
	case logicdomain.ProfileLevelTransient:
		return "L0"
	case logicdomain.ProfileLevelSituational:
		return "L1"
	case logicdomain.ProfileLevelStable:
		return "L2"
	default:
		return "L3"
	}
}
