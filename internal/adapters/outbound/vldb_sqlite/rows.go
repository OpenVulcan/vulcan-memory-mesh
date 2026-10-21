// SQLite row decoding used by relational reads, migration and export.
// SQLite 适配层关系读取、迁移和导出使用的行解码。
package vldb_sqlite

import (
	"encoding/json"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// encodeFloat32Slice returns durable vector JSON or an error for non-finite values, preserving absent vectors as an empty array.
// encodeFloat32Slice 返回持久化向量 JSON，遇到非有限值时返回错误，并将缺省向量保留为空数组。
func encodeFloat32Slice(values []float32) (string, error) {
	if len(values) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode durable vector: %w", err)
	}
	return string(data), nil
}

// decodeFloat32Slice preserves intentionally absent vectors and returns errors for corrupt JSON or nonnumeric elements.
// decodeFloat32Slice 保留明确缺失的向量，对损坏 JSON 或非数字元素返回错误。
func decodeFloat32Slice(raw string) ([]float32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &elements); err != nil {
		return nil, fmt.Errorf("invalid durable vector JSON: %w", err)
	}
	vector := make([]float32, len(elements))
	for i, element := range elements {
		if strings.TrimSpace(string(element)) == "null" {
			return nil, fmt.Errorf("durable vector element %d is null", i)
		}
		if err := json.Unmarshal(element, &vector[i]); err != nil {
			return nil, fmt.Errorf("invalid durable vector element %d: %w", i, err)
		}
	}
	return vector, nil
}

// unixMilliToTime converts one millisecond unix timestamp back into UTC time and tolerates zero values.
// unixMilliToTime 用于把毫秒 unix 时间戳转回 UTC time，并容忍零值。
func unixMilliToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

type noiseEmbeddingRow struct {
	Scope        string `json:"scope"`
	Language     string `json:"language"`
	CategoryName string `json:"category_name"`
	Phrase       string `json:"phrase"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	RulesHash    string `json:"rules_hash"`
	VectorJSON   string `json:"vector_json"`
	UpdatedAt    string `json:"updated_at"`
}

type userRow struct {
	ID                uint64 `json:"id"`
	Name              string `json:"name"`
	Profile           string `json:"profile"`
	DeleteConfirmCode string `json:"delete_confirm_code"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (r userRow) toDomain() logicdomain.UserRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.UserRecord{
		ID:                r.ID,
		Name:              r.Name,
		Profile:           r.Profile,
		DeleteConfirmCode: r.DeleteConfirmCode,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
}

type teamRow struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r teamRow) toDomain() logicdomain.TeamRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.TeamRecord{ID: r.ID, Name: r.Name, Profile: r.Profile, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type spaceRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r spaceRow) toDomain() logicdomain.SpaceRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.SpaceRecord{ID: r.ID, TeamID: r.TeamID, Name: r.Name, Profile: r.Profile, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type projectJoinRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	SpaceID   uint64 `json:"space_id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	TeamName  string `json:"team_name"`
	SpaceName string `json:"space_name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r projectJoinRow) toDomain() logicdomain.ProjectRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.ProjectRecord{
		ID:        r.ID,
		TeamID:    r.TeamID,
		SpaceID:   r.SpaceID,
		TeamName:  r.TeamName,
		SpaceName: r.SpaceName,
		Name:      r.Name,
		Profile:   r.Profile,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

type sessionRow struct {
	ID                            uint64 `json:"id"`
	SessionKey                    string `json:"session_key"`
	UserID                        uint64 `json:"user_id"`
	TeamID                        uint64 `json:"team_id"`
	SpaceID                       uint64 `json:"space_id"`
	ProjectID                     uint64 `json:"project_id"`
	TurnCount                     int    `json:"turn_count"`
	LastSummarizedID              uint64 `json:"last_summarized_id"`
	LastCompactedTurnID           uint64 `json:"last_compacted_turn_id"`
	SummarizeContent              string `json:"summarize_content"`
	SummarizeBudget               int    `json:"summarize_budget"`
	LastExtractObservedTimestamp  int64  `json:"last_extract_observed_timestamp"`
	LastExtractCompletedTimestamp int64  `json:"last_extract_completed_timestamp"`
	LastCompactedTimestamp        int64  `json:"last_compacted_timestamp"`
	CreatedTimestamp              int64  `json:"created_timestamp"`
	UpdatedTimestamp              int64  `json:"updated_timestamp"`
}

func (r sessionRow) toDomain() logicdomain.SessionRecord {
	return logicdomain.SessionRecord{
		ID:                     r.ID,
		SessionKey:             r.SessionKey,
		UserID:                 r.UserID,
		TeamID:                 r.TeamID,
		SpaceID:                r.SpaceID,
		ProjectID:              r.ProjectID,
		TurnCount:              r.TurnCount,
		LastSummarizedID:       r.LastSummarizedID,
		LastCompactedTurnID:    r.LastCompactedTurnID,
		SummarizeContent:       r.SummarizeContent,
		SummarizeBudget:        r.SummarizeBudget,
		LastExtractObservedAt:  unixMilliToTime(r.LastExtractObservedTimestamp),
		LastExtractCompletedAt: unixMilliToTime(r.LastExtractCompletedTimestamp),
		LastCompactedAt:        unixMilliToTime(r.LastCompactedTimestamp),
		CreatedAt:              unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:              unixMilliToTime(r.UpdatedTimestamp),
	}
}

type turnRecordRow struct {
	ID                uint64 `json:"id"`
	SessionID         uint64 `json:"session_id"`
	ProjectID         uint64 `json:"project_id"`
	DehydratedContent string `json:"dehydrated_content"`
	DehydratedBudget  int    `json:"dehydrated_budget"`
	ExtractedStatus   int    `json:"extracted_status"`
	Details           string `json:"details"`
	DetailsBudget     int    `json:"details_budget"`
	CreatedTimestamp  int64  `json:"created_timestamp"`
	UpdatedTimestamp  int64  `json:"updated_timestamp"`
}

// turnWindowRow stores one neighboring turn id plus its relative position around one requested anchor turn.
// turnWindowRow 用于保存某个请求锚点 turn 周围的一条相邻 turn id 及其相对位置。
type turnWindowRow struct {
	TargetID    uint64 `json:"target_id"`
	TurnID      uint64 `json:"turn_id"`
	RelativePos int    `json:"relative_pos"`
}

func (r turnRecordRow) toDomain() logicdomain.SessionTurnRecord {
	return logicdomain.SessionTurnRecord{
		ID:                r.ID,
		SessionID:         r.SessionID,
		ProjectID:         r.ProjectID,
		DehydratedContent: r.DehydratedContent,
		DehydratedBudget:  r.DehydratedBudget,
		ExtractedStatus:   r.ExtractedStatus,
		Details:           r.Details,
		DetailsBudget:     r.DetailsBudget,
		CreatedAt:         unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:         unixMilliToTime(r.UpdatedTimestamp),
	}
}

type memoryNodeRow struct {
	ID                       uint64  `json:"id"`
	TeamID                   uint64  `json:"team_id"`
	SpaceID                  uint64  `json:"space_id"`
	ProjectID                uint64  `json:"project_id"`
	UserID                   uint64  `json:"user_id"`
	OriginSessionID          uint64  `json:"origin_session_id"`
	SourceTurnID             *uint64 `json:"source_turn_id"`
	VectorID                 string  `json:"vector_id"`
	VectorJSON               string  `json:"vector_json"`
	SourceKind               int     `json:"source_kind"`
	ScopeLevel               int     `json:"scope_level"`
	Category                 int     `json:"category"`
	Abstract                 string  `json:"abstract"`
	Details                  string  `json:"details"`
	MemoryStatus             int     `json:"memory_status"`
	Priority                 int     `json:"priority"`
	MemoryLevel              int     `json:"memory_level"`
	RefreshWeight            int     `json:"refresh_weight"`
	SupportCount             int     `json:"support_count"`
	RebuttalCount            int     `json:"rebuttal_count"`
	StatusReason             string  `json:"status_reason"`
	ExpiresTimestamp         int64   `json:"expires_timestamp"`
	LastRecalledTimestamp    int64   `json:"last_recalled_timestamp"`
	LastAdoptedTimestamp     int64   `json:"last_adopted_timestamp"`
	LastReinforcedTimestamp  int64   `json:"last_reinforced_timestamp"`
	RecalledCount            int     `json:"recalled_count"`
	AdoptedCount             int     `json:"adopted_count"`
	ReinforcementCount       int     `json:"reinforcement_count"`
	CrossSessionAdoptedCount int     `json:"cross_session_adopted_count"`
	DecayDisabledValue       int     `json:"decay_disabled"`
	DedupeHash               string  `json:"dedupe_hash"`
	CreatedTimestamp         int64   `json:"created_timestamp"`
	UpdatedTimestamp         int64   `json:"updated_timestamp"`
}

// memoryLexicalRow stores one lexical recall row produced by SQLite FTS so the adapter can decode memory ids plus rank scores.
// memoryLexicalRow 用于保存 SQLite FTS 产出的 lexical 召回行，让适配器可以解码 memory id 和排序分数。
type memoryLexicalRow struct {
	MemoryID uint64  `json:"memory_id"`
	Rank     float64 `json:"rank"`
}

// memoryContextEdgeRow stores one durable contextual edge row so future filtering or debugging paths can decode the persisted support/rebuttal evidence graph.
// memoryContextEdgeRow 用于保存一条长期情境边行，便于未来过滤或调试链路解码已持久化的支持/反驳证据图。
type memoryContextEdgeRow struct {
	MemoryID               uint64 `json:"memory_id"`
	ContextKey             string `json:"context_key"`
	ContextValue           string `json:"context_value"`
	SupportCount           int    `json:"support_count"`
	RebuttalCount          int    `json:"rebuttal_count"`
	LastSupportedTimestamp int64  `json:"last_supported_timestamp"`
	LastRebuttedTimestamp  int64  `json:"last_rebutted_timestamp"`
	CreatedTimestamp       int64  `json:"created_timestamp"`
	UpdatedTimestamp       int64  `json:"updated_timestamp"`
}

// toDomain converts one SQL edge row into the public contextual-evidence record used by query-time scoring and future context-aware filters.
// toDomain 用于把一条 SQL 情境边行转换成查询期打分和未来情境过滤会复用的公开证据记录。
func (r memoryContextEdgeRow) toDomain() logicdomain.MemoryContextEdge {
	return logicdomain.MemoryContextEdge{
		MemoryID:        r.MemoryID,
		ContextKey:      strings.TrimSpace(r.ContextKey),
		ContextValue:    strings.TrimSpace(r.ContextValue),
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		LastSupportedAt: unixMilliToTime(r.LastSupportedTimestamp),
		LastRebuttedAt:  unixMilliToTime(r.LastRebuttedTimestamp),
		CreatedAt:       unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:       unixMilliToTime(r.UpdatedTimestamp),
	}
}

func (r memoryNodeRow) toDomain() logicdomain.SessionMemoryNodeRecord {
	return logicdomain.SessionMemoryNodeRecord{
		ID:              r.ID,
		TeamID:          r.TeamID,
		SpaceID:         r.SpaceID,
		ProjectID:       r.ProjectID,
		UserID:          r.UserID,
		OriginSessionID: r.OriginSessionID,
		TurnID:          optionalUint64Value(r.SourceTurnID),
		VectorID:        r.VectorID,
		Category:        r.Category,
		Abstract:        r.Abstract,
		Details:         r.Details,
		SourceKind:      r.SourceKind,
		ScopeLevel:      r.ScopeLevel,
		Priority:        r.Priority,
		MemoryLevel:     r.MemoryLevel,
		RefreshWeight:   r.RefreshWeight,
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		NodeStatus:      r.MemoryStatus,
		CreatedAt:       unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:       unixMilliToTime(r.UpdatedTimestamp),
	}
}

// toMemoryNodeRecord converts one SQL row into the public unified durable-memory record used by mixed detail lookup and direct-write dedupe flows.
// toMemoryNodeRecord 用于把一条 SQL 行转换成混合详情查询和主动写入查重流程使用的统一长期记忆记录。
func (r memoryNodeRow) toMemoryNodeRecord() (logicdomain.MemoryNodeRecord, error) {
	// Include durable identifiers so corrupt vectors can be located instead of silently disappearing.
	// 携带长期存储标识，使损坏向量可定位，避免静默消失。
	vector, err := decodeFloat32Slice(r.VectorJSON)
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("memory %d vector %q vector_json: %w", r.ID, r.VectorID, err)
	}
	return logicdomain.MemoryNodeRecord{
		ID:                       r.ID,
		TeamID:                   r.TeamID,
		SpaceID:                  r.SpaceID,
		ProjectID:                r.ProjectID,
		UserID:                   r.UserID,
		OriginSessionID:          r.OriginSessionID,
		SourceTurnID:             optionalUint64Value(r.SourceTurnID),
		VectorID:                 r.VectorID,
		Vector:                   vector,
		SourceKind:               r.SourceKind,
		ScopeLevel:               r.ScopeLevel,
		Category:                 r.Category,
		Abstract:                 r.Abstract,
		Details:                  r.Details,
		Status:                   r.MemoryStatus,
		Priority:                 r.Priority,
		MemoryLevel:              r.MemoryLevel,
		RefreshWeight:            r.RefreshWeight,
		SupportCount:             r.SupportCount,
		RebuttalCount:            r.RebuttalCount,
		StatusReason:             r.StatusReason,
		ExpiresAt:                unixMilliToTime(r.ExpiresTimestamp),
		LastRecalledAt:           unixMilliToTime(r.LastRecalledTimestamp),
		LastAdoptedAt:            unixMilliToTime(r.LastAdoptedTimestamp),
		LastReinforcedAt:         unixMilliToTime(r.LastReinforcedTimestamp),
		RecalledCount:            r.RecalledCount,
		AdoptedCount:             r.AdoptedCount,
		ReinforcementCount:       r.ReinforcementCount,
		CrossSessionAdoptedCount: r.CrossSessionAdoptedCount,
		DecayDisabled:            r.DecayDisabledValue > 0,
		DedupeHash:               r.DedupeHash,
		CreatedAt:                unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:                unixMilliToTime(r.UpdatedTimestamp),
	}, nil
}

type profileNodeRow struct {
	ID                         uint64  `json:"id"`
	TurnID                     *uint64 `json:"turn_id"`
	ProfileType                int     `json:"profile_type"`
	BindID                     uint64  `json:"bind_id"`
	Content                    string  `json:"content"`
	ProfileStatus              int     `json:"profile_status"`
	Priority                   int     `json:"priority"`
	ProfileLevel               int     `json:"profile_level"`
	LevelReason                string  `json:"level_reason"`
	RefreshWeight              int     `json:"refresh_weight"`
	SourceKind                 int     `json:"source_kind"`
	SourceID                   uint64  `json:"source_id"`
	StatusReason               string  `json:"status_reason"`
	ExpiresTimestamp           int64   `json:"expires_timestamp"`
	SupersededByID             uint64  `json:"superseded_by_id"`
	ProfileDate                string  `json:"profile_date"`
	CreatedTimestamp           int64   `json:"created_timestamp"`
	UpdatedTimestamp           int64   `json:"updated_timestamp"`
	ProfileDateAnchorTimestamp int64   `json:"profile_date_anchor_timestamp"`
}

// toRecord converts one SQL row into the public profile-node record shape returned by query RPCs and manual profile writebacks.
// toRecord 用于把一条 SQL 行转换成查询 RPC 与手工画像写回返回的公开画像节点结构。
func (r profileNodeRow) toRecord() logicdomain.ProfileNodeRecord {
	profileDateAnchorAt := unixMilliToTime(r.ProfileDateAnchorTimestamp)
	if profileDateAnchorAt.IsZero() {
		profileDateAnchorAt = unixMilliToTime(r.CreatedTimestamp)
	}
	return logicdomain.ProfileNodeRecord{
		ID:                  r.ID,
		TurnID:              optionalUint64Value(r.TurnID),
		ProfileType:         r.ProfileType,
		BindID:              r.BindID,
		Content:             r.Content,
		Status:              r.ProfileStatus,
		Priority:            r.Priority,
		ProfileLevel:        r.ProfileLevel,
		LevelReason:         r.LevelReason,
		RefreshWeight:       r.RefreshWeight,
		ProfileDate:         r.ProfileDate,
		SourceKind:          r.SourceKind,
		SourceID:            r.SourceID,
		StatusReason:        r.StatusReason,
		ExpiresAt:           unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID:      r.SupersededByID,
		ProfileDateAnchorAt: profileDateAnchorAt,
		CreatedAt:           unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:           unixMilliToTime(r.UpdatedTimestamp),
	}
}

func (r profileNodeRow) toDomain() logicdomain.ProfileActiveNodeRecord {
	profileDateAnchorAt := unixMilliToTime(r.ProfileDateAnchorTimestamp)
	if profileDateAnchorAt.IsZero() {
		profileDateAnchorAt = unixMilliToTime(r.CreatedTimestamp)
	}
	return logicdomain.ProfileActiveNodeRecord{
		ID:                  r.ID,
		TurnID:              optionalUint64Value(r.TurnID),
		ProfileType:         r.ProfileType,
		BindID:              r.BindID,
		Content:             r.Content,
		Status:              r.ProfileStatus,
		Priority:            r.Priority,
		ProfileLevel:        r.ProfileLevel,
		LevelReason:         r.LevelReason,
		RefreshWeight:       r.RefreshWeight,
		ProfileDate:         r.ProfileDate,
		SourceKind:          r.SourceKind,
		SourceID:            r.SourceID,
		StatusReason:        r.StatusReason,
		ExpiresAt:           unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID:      r.SupersededByID,
		ProfileDateAnchorAt: profileDateAnchorAt,
		CreatedAt:           unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:           unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileNodeStatusRow struct {
	ID             uint64 `json:"id"`
	ProfileStatus  int    `json:"profile_status"`
	SupersededByID uint64 `json:"superseded_by_id"`
	// StatusReason stores the lifecycle reason written by guarded status updates that need exact reconciliation.
	// StatusReason 保存受保护状态更新写入的生命周期原因，供精确对账使用。
	StatusReason string `json:"status_reason"`
	// UpdatedTimestamp stores the durable millisecond timestamp used to prove an uncertain update reached this write.
	// UpdatedTimestamp 保存长期毫秒时间戳，用于证明不确定更新已经达到本次写入。
	UpdatedTimestamp int64 `json:"updated_timestamp"`
}

type profileInstructionRow struct {
	ID                uint64 `json:"id"`
	ProfileType       int    `json:"profile_type"`
	BindID            uint64 `json:"bind_id"`
	Instruction       string `json:"instruction"`
	InstructionStatus int    `json:"instruction_status"`
	ReviewResultJSON  string `json:"review_result_json"`
	FailureReason     string `json:"failure_reason"`
	CreatedTimestamp  int64  `json:"created_timestamp"`
	UpdatedTimestamp  int64  `json:"updated_timestamp"`
}

func (r profileInstructionRow) toDomain() logicdomain.ProfileInstructionRecord {
	return logicdomain.ProfileInstructionRecord{
		ID:            r.ID,
		ProfileType:   r.ProfileType,
		BindID:        r.BindID,
		Instruction:   r.Instruction,
		Status:        r.InstructionStatus,
		ReviewResult:  r.ReviewResultJSON,
		FailureReason: r.FailureReason,
		CreatedAt:     unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:     unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileBlobRow struct {
	Profile string `json:"profile"`
}

// renderedProfileBatchRow mirrors one scope row read back after an uncertain rendered-profile batch update.
// renderedProfileBatchRow 用于映射渲染画像批量更新不确定后回读的一条 scope 行。
type renderedProfileBatchRow struct {
	// ID stores the durable scope-row id and must match the sorted batch id at the same index.
	// ID 保存长期 scope 行 id，必须与同一排序位置上的批量 id 匹配。
	ID uint64 `json:"id"`
	// Profile stores the rendered profile blob written by ReplaceRenderedProfiles.
	// Profile 保存 ReplaceRenderedProfiles 写入的渲染画像 Blob。
	Profile string `json:"profile"`
	// UpdatedAt stores the RFC3339Nano timestamp written with the profile blob, proving the row reached this batch.
	// UpdatedAt 保存随画像 Blob 一起写入的 RFC3339Nano 时间戳，用于证明该行已达到本次批量写入。
	UpdatedAt string `json:"updated_at"`
}

type obsoleteVectorRow struct {
	VectorID string `json:"vector_id"`
}

type countRow struct {
	Count int `json:"count"`
}
