// SQLite profile targets and rendered profile reads for profile workflows.
// SQLite 适配层画像工作流使用的目标定位与渲染画像读取。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"sort"
	"strings"
	"time"
)

// LoadProfileTargets loads the current durable user/project profile blobs so post-action can merge fresh profile evidence before persistence.
// LoadProfileTargets 用于加载当前长期 user/project 画像 Blob，让 post-action 在持久化前先合并新的画像证据。
func (s *Store) LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	if session.UserID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	user, err := s.loadUserByID(ctx, session.UserID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	project, err := s.loadProjectByID(ctx, session.ProjectID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	return logicdomain.ProfileTargetsSnapshot{
		UserProfile:    strings.TrimSpace(user.Profile),
		ProjectProfile: strings.TrimSpace(project.Profile),
	}, nil
}

// LoadProfileReviewTargets loads the currently active and non-expired user/project profile nodes so post-action can review new candidates against factual atomic records.
// LoadProfileReviewTargets 用于加载当前活跃且未过期的 user/project 画像节点，让 post-action 可以基于原子事实记录评审新候选。
func (s *Store) LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	if session.UserID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	userNodes, err := s.loadActiveProfileNodes(ctx, logicdomain.ProfileTypeUser, session.UserID, nowMs)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query user profile review targets: %w", err)
	}
	projectNodes, err := s.loadActiveProfileNodes(ctx, logicdomain.ProfileTypeProject, session.ProjectID, nowMs)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query project profile review targets: %w", err)
	}
	return logicdomain.ProfileReviewTargetsSnapshot{
		UserNodes:    userNodes,
		ProjectNodes: projectNodes,
	}, nil
}

// ResolveProfileTarget resolves one requested profile target into a stable bind id and hierarchy context for query or manual instruction flows.
// ResolveProfileTarget 用于把请求的画像目标解析成稳定的 bind id 和层级上下文，供查询或手工画像指令流程使用。
func (s *Store) ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		if userID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
		}
		user, err := s.loadUserByID(ctx, userID)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		return logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			BindID:      user.ID,
			UserID:      user.ID,
			UserName:    user.Name,
		}, nil
	case logicdomain.ProfileTypeProject, logicdomain.ProfileTypeTeam, logicdomain.ProfileTypeSpace:
		if projectID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
		}
		project, err := s.loadProjectByID(ctx, projectID)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		target := logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			UserID:      userID,
			TeamID:      project.TeamID,
			SpaceID:     project.SpaceID,
			ProjectID:   project.ID,
			TeamName:    project.TeamName,
			SpaceName:   project.SpaceName,
			ProjectName: project.Name,
		}
		switch profileType {
		case logicdomain.ProfileTypeProject:
			target.BindID = project.ID
		case logicdomain.ProfileTypeTeam:
			target.BindID = project.TeamID
		case logicdomain.ProfileTypeSpace:
			target.BindID = project.SpaceID
		}
		return target, nil
	default:
		return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
}

// ListActiveProfileNodes returns only the current active nodes for one resolved target in a deterministic order, while respecting the caller's explicit limit when provided.
// ListActiveProfileNodes 用于按确定性顺序返回某个已解析目标当前 active 的画像节点，并在调用方显式提供 limit 时尊重该限制。
func (s *Store) ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return nil, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return nil, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	sqlText := `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.profile_type = ? AND n.bind_id = ? AND n.profile_status = ? AND (n.expires_timestamp <= 0 OR n.expires_timestamp > ?)
ORDER BY profile_date_anchor_timestamp DESC, n.id ASC
`
	args := []any{target.ProfileType, target.BindID, logicdomain.ProfileStatusActive, nowMs}
	if limit > 0 {
		sqlText += "LIMIT ?\n"
		args = append(args, limit)
	}
	rows, err := queryRows[profileNodeRow](s, ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query active profile nodes: %w", err)
	}
	out := make([]logicdomain.ProfileNodeRecord, 0, len(rows))
	for _, row := range rows {
		node := row.toDomain()
		out = append(out, logicdomain.ProfileNodeRecord{
			ID:                  node.ID,
			TurnID:              node.TurnID,
			ProfileType:         node.ProfileType,
			BindID:              node.BindID,
			Content:             node.Content,
			Status:              node.Status,
			Priority:            node.Priority,
			ProfileLevel:        node.ProfileLevel,
			LevelReason:         node.LevelReason,
			RefreshWeight:       node.RefreshWeight,
			ProfileDate:         node.ProfileDate,
			SourceKind:          node.SourceKind,
			SourceID:            node.SourceID,
			StatusReason:        node.StatusReason,
			ExpiresAt:           node.ExpiresAt,
			SupersededByID:      node.SupersededByID,
			ProfileDateAnchorAt: node.ProfileDateAnchorAt,
			CreatedAt:           node.CreatedAt,
			UpdatedAt:           node.UpdatedAt,
		})
	}
	return out, nil
}

// LoadRenderedProfile returns the durable scope-level rendered profile body stored on the resolved target row.
// LoadRenderedProfile 用于返回已解析目标行上持久化的 scope 级渲染画像正文。
func (s *Store) LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return "", logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return "", logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	return s.loadRenderedProfileByTarget(ctx, target.ProfileType, target.BindID)
}

// loadProfileInstructionByID fetches one manual profile-instruction row by its durable numeric id so uncertain commits can be reconciled before retrying.
// loadProfileInstructionByID 用于按长期数字 id 读取一条手工画像指令，让“不确定提交”在重试前先做状态对账。
func (s *Store) loadProfileInstructionByID(ctx context.Context, instructionID uint64) (logicdomain.ProfileInstructionRecord, bool, error) {
	rows, err := queryRows[profileInstructionRow](s, ctx, `
SELECT id, profile_type, bind_id, instruction, instruction_status,
       review_result_json, failure_reason, created_timestamp, updated_timestamp
FROM vmm_profile_instructions
WHERE id = ?
LIMIT 1
`, instructionID)
	if err != nil {
		return logicdomain.ProfileInstructionRecord{}, false, err
	}
	if len(rows) == 0 {
		return logicdomain.ProfileInstructionRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// loadProfileNodeByID fetches one durable profile node by id so manual-instruction persistence can verify whether a supposedly failed insert already landed.
// loadProfileNodeByID 用于按 id 读取一条长期画像节点，让手工画像持久化能判断一个“看似失败”的插入是否其实已经落库。
func (s *Store) loadProfileNodeByID(ctx context.Context, nodeID uint64) (logicdomain.ProfileNodeRecord, bool, error) {
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.id = ?
LIMIT 1
`, nodeID)
	if err != nil {
		return logicdomain.ProfileNodeRecord{}, false, err
	}
	if len(rows) == 0 {
		return logicdomain.ProfileNodeRecord{}, false, nil
	}
	return rows[0].toRecord(), true, nil
}

// loadProfileNodeStatusesByIDs fetches lightweight lifecycle state for a small node-id set so uncertain status updates can be reconciled.
// loadProfileNodeStatusesByIDs 用于读取一小批节点的轻量生命周期状态，让“不确定”的状态更新能够做状态对账。
func (s *Store) loadProfileNodeStatusesByIDs(ctx context.Context, nodeIDs []uint64) ([]profileNodeStatusRow, error) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	nodeIDParams := sqliteUint64Params(nodeIDs)
	rows, err := queryRows[profileNodeStatusRow](s, ctx, fmt.Sprintf(`
SELECT id, profile_status, superseded_by_id, status_reason, updated_timestamp
FROM vmm_profile_nodes
WHERE id IN (%s)
ORDER BY id ASC
`, nodeIDPlaceholders), nodeIDParams...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// renderedProfileScopeTable maps one domain profile type to the concrete scope table used for durable rendered-profile blobs.
// renderedProfileScopeTable 用于把一个领域画像类型映射到持久化渲染画像 Blob 所在的具体 scope 表。
func renderedProfileScopeTable(profileType int) (string, error) {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return "vmm_users", nil
	case logicdomain.ProfileTypeTeam:
		return "vmm_teams", nil
	case logicdomain.ProfileTypeSpace:
		return "vmm_spaces", nil
	case logicdomain.ProfileTypeProject:
		return "vmm_projects", nil
	default:
		return "", logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
}

// loadRenderedProfileByTarget reads the durable scope blob after one uncertain update so callers can verify whether the final profile text already committed.
// loadRenderedProfileByTarget 用于在 scope 画像更新不确定后读取长期 Blob，让调用方验证最终画像文本是否已经提交成功。
func (s *Store) loadRenderedProfileByTarget(ctx context.Context, profileType int, bindID uint64) (string, error) {
	table, err := renderedProfileScopeTable(profileType)
	if err != nil {
		return "", err
	}
	rows, err := queryRows[profileBlobRow](s, ctx, fmt.Sprintf(`SELECT profile FROM %s WHERE id = ? LIMIT 1`, table), bindID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Profile, nil
}

// loadRenderedProfileBatch reads exact rendered-profile rows for one scope so a batch update with uncertain commit outcome can be reconciled.
// loadRenderedProfileBatch 用于读取一个 scope 的精确渲染画像行，让提交结果不确定的批量更新可以执行对账。
func (s *Store) loadRenderedProfileBatch(ctx context.Context, profileType int, ids []uint64) ([]renderedProfileBatchRow, error) {
	normalizedIDs := storageutil.NormalizeUint64List(ids)
	if len(normalizedIDs) == 0 {
		return nil, nil
	}
	table, err := renderedProfileScopeTable(profileType)
	if err != nil {
		return nil, err
	}
	rows, err := queryRows[renderedProfileBatchRow](s, ctx, fmt.Sprintf(`
SELECT id, profile, updated_at
FROM %s
WHERE id IN (%s)
ORDER BY id ASC
`, table, sqlitePlaceholders(len(normalizedIDs))), sqliteUint64Params(normalizedIDs)...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// loadActiveProfileNodes is the shared query helper used by profile review and expiry convergence to fetch only currently renderable active nodes.
// loadActiveProfileNodes 用于作为画像评审和过期收敛共用查询助手，只加载当前仍可渲染的 active 节点。
func (s *Store) loadActiveProfileNodes(ctx context.Context, profileType int, bindID uint64, nowMs int64) ([]logicdomain.ProfileActiveNodeRecord, error) {
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.profile_type = ? AND n.bind_id = ? AND n.profile_status = ? AND (n.expires_timestamp <= 0 OR n.expires_timestamp > ?)
ORDER BY profile_date_anchor_timestamp ASC, n.id ASC
`, profileType, bindID, logicdomain.ProfileStatusActive, nowMs)
	if err != nil {
		return nil, err
	}
	nodes := make([]logicdomain.ProfileActiveNodeRecord, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, row.toDomain())
	}
	return nodes, nil
}

// sortedProfileBindingIDs returns one deterministic ascending id slice from a rendered-profile update map so SQL scripts remain stable in tests and logs.
// sortedProfileBindingIDs 用于从渲染后画像更新 map 中返回确定性的升序 id 列表，确保 SQL 脚本在测试和日志里保持稳定。
func sortedProfileBindingIDs(values map[uint64]string) []uint64 {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	for id := range values {
		if id == 0 {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	return ids
}
