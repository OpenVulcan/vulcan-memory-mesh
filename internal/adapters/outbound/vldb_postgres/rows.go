// rows.go defines PostgreSQL scan helpers so combined-store queries can map SQL rows back into shared domain models deterministically.
// rows.go 用于定义 PostgreSQL 扫描辅助结构，让组合库查询可以稳定地把 SQL 行映射回共享领域模型。
package vldb_postgres

import (
	"database/sql"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// userScanRow stores one durable user row returned by PostgreSQL workspace lookups.
// userScanRow 用于保存 PostgreSQL workspace 查询返回的一条长期用户行。
type userScanRow struct {
	ID                uint64
	Name              string
	Profile           string
	DeleteConfirmCode string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// toDomain converts one scanned user row into the shared domain user model.
// toDomain 用于把扫描得到的用户行转换成共享领域用户模型。
func (r userScanRow) toDomain() logicdomain.UserRecord {
	return logicdomain.UserRecord{
		ID:                r.ID,
		Name:              strings.TrimSpace(r.Name),
		Profile:           r.Profile,
		DeleteConfirmCode: r.DeleteConfirmCode,
		CreatedAt:         r.CreatedAt.UTC(),
		UpdatedAt:         r.UpdatedAt.UTC(),
	}
}

// projectScanRow stores one joined project row together with its Team/Space display names.
// projectScanRow 用于保存一条连表 project 行及其 Team/Space 展示名称。
type projectScanRow struct {
	ID        uint64
	TeamID    uint64
	SpaceID   uint64
	Name      string
	Profile   string
	TeamName  string
	SpaceName string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// toDomain converts one scanned project row into the shared domain project model.
// toDomain 用于把扫描得到的项目行转换成共享领域项目模型。
func (r projectScanRow) toDomain() logicdomain.ProjectRecord {
	return logicdomain.ProjectRecord{
		ID:        r.ID,
		TeamID:    r.TeamID,
		SpaceID:   r.SpaceID,
		TeamName:  strings.TrimSpace(r.TeamName),
		SpaceName: strings.TrimSpace(r.SpaceName),
		Name:      strings.TrimSpace(r.Name),
		Profile:   r.Profile,
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// teamScanRow stores one durable team row returned by workspace administration queries.
// teamScanRow 用于保存 workspace 管理查询返回的一条长期 team 行。
type teamScanRow struct {
	ID        uint64
	Name      string
	Profile   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// toDomain converts one scanned team row into the shared domain team model.
// toDomain 用于把扫描得到的 team 行转换成共享领域 team 模型。
func (r teamScanRow) toDomain() logicdomain.TeamRecord {
	return logicdomain.TeamRecord{
		ID:        r.ID,
		Name:      strings.TrimSpace(r.Name),
		Profile:   r.Profile,
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// spaceScanRow stores one durable space row returned by workspace administration queries.
// spaceScanRow 用于保存 workspace 管理查询返回的一条长期 space 行。
type spaceScanRow struct {
	ID        uint64
	TeamID    uint64
	Name      string
	Profile   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// toDomain converts one scanned space row into the shared domain space model.
// toDomain 用于把扫描得到的 space 行转换成共享领域 space 模型。
func (r spaceScanRow) toDomain() logicdomain.SpaceRecord {
	return logicdomain.SpaceRecord{
		ID:        r.ID,
		TeamID:    r.TeamID,
		Name:      strings.TrimSpace(r.Name),
		Profile:   r.Profile,
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// sessionScanRow stores one durable session row returned by scope resolution queries.
// sessionScanRow 用于保存请求范围解析查询返回的一条长期 session 行。
type sessionScanRow struct {
	ID                     uint64
	SessionKey             string
	UserID                 uint64
	TeamID                 uint64
	SpaceID                uint64
	ProjectID              uint64
	TurnCount              int
	LastSummarizedID       uint64
	LastCompactedTurnID    uint64
	SummarizeContent       string
	SummarizeBudget        int
	LastExtractObservedAt  sql.NullTime
	LastExtractCompletedAt sql.NullTime
	LastCompactedAt        sql.NullTime
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// toDomain converts one scanned session row into the shared domain session model.
// toDomain 用于把扫描得到的 session 行转换成共享领域 session 模型。
func (r sessionScanRow) toDomain() logicdomain.SessionRecord {
	return logicdomain.SessionRecord{
		ID:                     r.ID,
		SessionKey:             strings.TrimSpace(r.SessionKey),
		UserID:                 r.UserID,
		TeamID:                 r.TeamID,
		SpaceID:                r.SpaceID,
		ProjectID:              r.ProjectID,
		TurnCount:              r.TurnCount,
		LastSummarizedID:       r.LastSummarizedID,
		LastCompactedTurnID:    r.LastCompactedTurnID,
		SummarizeContent:       r.SummarizeContent,
		SummarizeBudget:        r.SummarizeBudget,
		LastExtractObservedAt:  optionalTimeValue(r.LastExtractObservedAt),
		LastExtractCompletedAt: optionalTimeValue(r.LastExtractCompletedAt),
		LastCompactedAt:        optionalTimeValue(r.LastCompactedAt),
		CreatedAt:              r.CreatedAt.UTC(),
		UpdatedAt:              r.UpdatedAt.UTC(),
	}
}

// turnRecordScanRow stores one durable turn row loaded back from PostgreSQL for pre-check, async extraction, and turn-detail flows.
// turnRecordScanRow 用于保存从 PostgreSQL 加载的一条长期 turn 行，供 pre-check、异步提炼和 turn 详情流程复用。
type turnRecordScanRow struct {
	ID                uint64
	SessionID         uint64
	ProjectID         uint64
	DehydratedContent string
	DehydratedBudget  int
	ExtractedStatus   int
	Details           string
	DetailsBudget     int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// toDomain converts one scanned turn row into the shared domain session-turn model.
// toDomain 用于把扫描得到的 turn 行转换成共享领域 session-turn 模型。
func (r turnRecordScanRow) toDomain() logicdomain.SessionTurnRecord {
	return logicdomain.SessionTurnRecord{
		ID:                r.ID,
		SessionID:         r.SessionID,
		ProjectID:         r.ProjectID,
		DehydratedContent: r.DehydratedContent,
		DehydratedBudget:  r.DehydratedBudget,
		ExtractedStatus:   r.ExtractedStatus,
		Details:           r.Details,
		DetailsBudget:     r.DetailsBudget,
		CreatedAt:         r.CreatedAt.UTC(),
		UpdatedAt:         r.UpdatedAt.UTC(),
	}
}

// memoryNodeScanRow stores one unified durable memory row together with the pgvector textual payload.
// memoryNodeScanRow 用于保存一条统一长期记忆行及其 pgvector 文本向量载荷。
type memoryNodeScanRow struct {
	ID                       uint64
	TeamID                   uint64
	SpaceID                  uint64
	ProjectID                uint64
	UserID                   uint64
	OriginSessionID          uint64
	SourceTurnID             sql.NullInt64
	VectorID                 string
	EmbeddingText            string
	SourceKind               int
	ScopeLevel               int
	Category                 int
	Abstract                 string
	Details                  string
	MemoryStatus             int
	Priority                 int
	MemoryLevel              int
	RefreshWeight            int
	SupportCount             int
	RebuttalCount            int
	StatusReason             string
	ExpiresAt                sql.NullTime
	LastRecalledAt           sql.NullTime
	LastAdoptedAt            sql.NullTime
	LastReinforcedAt         sql.NullTime
	RecalledCount            int
	AdoptedCount             int
	ReinforcementCount       int
	CrossSessionAdoptedCount int
	DecayDisabled            bool
	DedupeHash               string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// memoryNodeScanDestinations returns the stable scan destination order matching memoryNodeSelectColumns.
// memoryNodeScanDestinations 用于返回与 memoryNodeSelectColumns 对齐的稳定扫描目标顺序。
func memoryNodeScanDestinations(row *memoryNodeScanRow) []any {
	return []any{
		&row.ID,
		&row.TeamID,
		&row.SpaceID,
		&row.ProjectID,
		&row.UserID,
		&row.OriginSessionID,
		&row.SourceTurnID,
		&row.VectorID,
		&row.EmbeddingText,
		&row.SourceKind,
		&row.ScopeLevel,
		&row.Category,
		&row.Abstract,
		&row.Details,
		&row.MemoryStatus,
		&row.Priority,
		&row.MemoryLevel,
		&row.RefreshWeight,
		&row.SupportCount,
		&row.RebuttalCount,
		&row.StatusReason,
		&row.ExpiresAt,
		&row.LastRecalledAt,
		&row.LastAdoptedAt,
		&row.LastReinforcedAt,
		&row.RecalledCount,
		&row.AdoptedCount,
		&row.ReinforcementCount,
		&row.CrossSessionAdoptedCount,
		&row.DecayDisabled,
		&row.DedupeHash,
		&row.CreatedAt,
		&row.UpdatedAt,
	}
}

// toMemoryNodeRecord converts one scanned memory row into the shared durable-memory model used by query and write flows.
// toMemoryNodeRecord 用于把扫描得到的记忆行转换成查询与写入流程使用的共享长期记忆模型。
func (r memoryNodeScanRow) toMemoryNodeRecord() logicdomain.MemoryNodeRecord {
	return logicdomain.MemoryNodeRecord{
		ID:                       r.ID,
		TeamID:                   r.TeamID,
		SpaceID:                  r.SpaceID,
		ProjectID:                r.ProjectID,
		UserID:                   r.UserID,
		OriginSessionID:          r.OriginSessionID,
		SourceTurnID:             optionalUint64Value(r.SourceTurnID),
		VectorID:                 strings.TrimSpace(r.VectorID),
		Vector:                   parsePGVectorText(r.EmbeddingText),
		SourceKind:               r.SourceKind,
		ScopeLevel:               r.ScopeLevel,
		Category:                 r.Category,
		Abstract:                 strings.TrimSpace(r.Abstract),
		Details:                  strings.TrimSpace(r.Details),
		Status:                   r.MemoryStatus,
		Priority:                 r.Priority,
		MemoryLevel:              r.MemoryLevel,
		RefreshWeight:            r.RefreshWeight,
		SupportCount:             r.SupportCount,
		RebuttalCount:            r.RebuttalCount,
		StatusReason:             r.StatusReason,
		ExpiresAt:                optionalTimeValue(r.ExpiresAt),
		LastRecalledAt:           optionalTimeValue(r.LastRecalledAt),
		LastAdoptedAt:            optionalTimeValue(r.LastAdoptedAt),
		LastReinforcedAt:         optionalTimeValue(r.LastReinforcedAt),
		RecalledCount:            r.RecalledCount,
		AdoptedCount:             r.AdoptedCount,
		ReinforcementCount:       r.ReinforcementCount,
		CrossSessionAdoptedCount: r.CrossSessionAdoptedCount,
		DecayDisabled:            r.DecayDisabled,
		DedupeHash:               strings.TrimSpace(r.DedupeHash),
		CreatedAt:                r.CreatedAt.UTC(),
		UpdatedAt:                r.UpdatedAt.UTC(),
	}
}

// toSessionMemoryNodeRecord converts one scanned unified memory row into the session-scoped memory shape consumed by turn-analysis flows.
// toSessionMemoryNodeRecord 用于把扫描得到的统一记忆行转换成 turn-analysis 流程消费的 session 级记忆结构。
func (r memoryNodeScanRow) toSessionMemoryNodeRecord() logicdomain.SessionMemoryNodeRecord {
	return logicdomain.SessionMemoryNodeRecord{
		ID:              r.ID,
		TeamID:          r.TeamID,
		SpaceID:         r.SpaceID,
		ProjectID:       r.ProjectID,
		UserID:          r.UserID,
		OriginSessionID: r.OriginSessionID,
		TurnID:          optionalUint64Value(r.SourceTurnID),
		VectorID:        strings.TrimSpace(r.VectorID),
		Category:        r.Category,
		Abstract:        strings.TrimSpace(r.Abstract),
		Details:         strings.TrimSpace(r.Details),
		SourceKind:      r.SourceKind,
		ScopeLevel:      r.ScopeLevel,
		Priority:        r.Priority,
		MemoryLevel:     r.MemoryLevel,
		RefreshWeight:   r.RefreshWeight,
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		NodeStatus:      r.MemoryStatus,
		CreatedAt:       r.CreatedAt.UTC(),
		UpdatedAt:       r.UpdatedAt.UTC(),
	}
}

// toTurnAnalysisDirectWrite converts one scanned unified memory row into the recent direct-write summary used by the turn analyzer.
// toTurnAnalysisDirectWrite 用于把扫描得到的统一记忆行转换成 turn analyzer 使用的近期主动写摘要结构。
func (r memoryNodeScanRow) toTurnAnalysisDirectWrite() logicdomain.TurnAnalysisDirectWrite {
	return logicdomain.TurnAnalysisDirectWrite{
		MemoryID:         r.ID,
		ScopeLevel:       logicdomain.MemoryScopeLevelLabel(r.ScopeLevel),
		Abstract:         strings.TrimSpace(r.Abstract),
		Details:          strings.TrimSpace(r.Details),
		CreatedTimestamp: r.CreatedAt.UTC().UnixMilli(),
	}
}

// memoryContextEdgeScanRow stores one durable memory-context edge row.
// memoryContextEdgeScanRow 用于保存一条长期记忆情境边行。
type memoryContextEdgeScanRow struct {
	MemoryID        uint64
	ContextKey      string
	ContextValue    string
	SupportCount    int
	RebuttalCount   int
	LastSupportedAt sql.NullTime
	LastRebuttedAt  sql.NullTime
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// toDomain converts one scanned contextual edge row into the shared domain evidence model.
// toDomain 用于把扫描得到的情境边行转换成共享领域证据模型。
func (r memoryContextEdgeScanRow) toDomain() logicdomain.MemoryContextEdge {
	return logicdomain.MemoryContextEdge{
		MemoryID:        r.MemoryID,
		ContextKey:      strings.TrimSpace(r.ContextKey),
		ContextValue:    strings.TrimSpace(r.ContextValue),
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		LastSupportedAt: optionalTimeValue(r.LastSupportedAt),
		LastRebuttedAt:  optionalTimeValue(r.LastRebuttedAt),
		CreatedAt:       r.CreatedAt.UTC(),
		UpdatedAt:       r.UpdatedAt.UTC(),
	}
}

// profileNodeScanRow stores one durable profile node row returned by PostgreSQL profile and lifecycle queries.
// profileNodeScanRow 用于保存 PostgreSQL 画像与生命周期查询返回的一条长期画像节点行。
type profileNodeScanRow struct {
	ID                  uint64
	TurnID              sql.NullInt64
	ProfileType         int
	BindID              uint64
	Content             string
	ProfileStatus       int
	Priority            int
	ProfileLevel        int
	LevelReason         string
	RefreshWeight       int
	SourceKind          int
	SourceID            uint64
	StatusReason        string
	ExpiresAt           sql.NullTime
	SupersededByID      uint64
	ProfileDate         string
	ProfileDateAnchorAt time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// toRecord converts one scanned profile row into the shared query/manual-write profile-node model.
// toRecord 用于把扫描得到的画像行转换成查询与手工写回共享的 profile-node 模型。
func (r profileNodeScanRow) toRecord() logicdomain.ProfileNodeRecord {
	profileDateAnchorAt := r.ProfileDateAnchorAt
	if profileDateAnchorAt.IsZero() {
		profileDateAnchorAt = r.CreatedAt
	}
	return logicdomain.ProfileNodeRecord{
		ID:                  r.ID,
		TurnID:              optionalUint64Value(r.TurnID),
		ProfileType:         r.ProfileType,
		BindID:              r.BindID,
		Content:             strings.TrimSpace(r.Content),
		Status:              r.ProfileStatus,
		Priority:            r.Priority,
		ProfileLevel:        r.ProfileLevel,
		LevelReason:         r.LevelReason,
		RefreshWeight:       r.RefreshWeight,
		ProfileDate:         strings.TrimSpace(r.ProfileDate),
		SourceKind:          r.SourceKind,
		SourceID:            r.SourceID,
		StatusReason:        r.StatusReason,
		ExpiresAt:           optionalTimeValue(r.ExpiresAt),
		SupersededByID:      r.SupersededByID,
		ProfileDateAnchorAt: profileDateAnchorAt.UTC(),
		CreatedAt:           r.CreatedAt.UTC(),
		UpdatedAt:           r.UpdatedAt.UTC(),
	}
}

// toActiveNode converts one scanned profile row into the lightweight active-node model used by reviewers and lifecycle convergence.
// toActiveNode 用于把扫描得到的画像行转换成评审器和生命周期收敛使用的轻量 active-node 模型。
func (r profileNodeScanRow) toActiveNode() logicdomain.ProfileActiveNodeRecord {
	record := r.toRecord()
	return logicdomain.ProfileActiveNodeRecord{
		ID:                  record.ID,
		TurnID:              record.TurnID,
		ProfileType:         record.ProfileType,
		BindID:              record.BindID,
		Content:             record.Content,
		Status:              record.Status,
		Priority:            record.Priority,
		ProfileLevel:        record.ProfileLevel,
		LevelReason:         record.LevelReason,
		RefreshWeight:       record.RefreshWeight,
		ProfileDate:         record.ProfileDate,
		SourceKind:          record.SourceKind,
		SourceID:            record.SourceID,
		StatusReason:        record.StatusReason,
		ExpiresAt:           record.ExpiresAt,
		SupersededByID:      record.SupersededByID,
		ProfileDateAnchorAt: record.ProfileDateAnchorAt,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}
}

// profileInstructionScanRow stores one durable manual profile-instruction row.
// profileInstructionScanRow 用于保存一条长期手工画像指令行。
type profileInstructionScanRow struct {
	ID               uint64
	ProfileType      int
	BindID           uint64
	Instruction      string
	InstructionState int
	ReviewResult     string
	FailureReason    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// toDomain converts one scanned instruction row into the shared manual profile-instruction model.
// toDomain 用于把扫描得到的指令行转换成共享手工画像指令模型。
func (r profileInstructionScanRow) toDomain() logicdomain.ProfileInstructionRecord {
	return logicdomain.ProfileInstructionRecord{
		ID:            r.ID,
		ProfileType:   r.ProfileType,
		BindID:        r.BindID,
		Instruction:   strings.TrimSpace(r.Instruction),
		Status:        r.InstructionState,
		ReviewResult:  r.ReviewResult,
		FailureReason: r.FailureReason,
		CreatedAt:     r.CreatedAt.UTC(),
		UpdatedAt:     r.UpdatedAt.UTC(),
	}
}

// optionalUint64Value dereferences nullable numeric columns while preserving zero when SQL returned NULL.
// optionalUint64Value 用于解引用可空数字列，并在 SQL 返回 NULL 时保留零值。
func optionalUint64Value(value sql.NullInt64) uint64 {
	if !value.Valid || value.Int64 <= 0 {
		return 0
	}
	return uint64(value.Int64)
}

// optionalTimeValue dereferences nullable timestamps while preserving the zero time when SQL returned NULL.
// optionalTimeValue 用于解引用可空时间戳，并在 SQL 返回 NULL 时保留零时间。
func optionalTimeValue(value sql.NullTime) time.Time {
	if !value.Valid || value.Time.IsZero() {
		return time.Time{}
	}
	return value.Time.UTC()
}
