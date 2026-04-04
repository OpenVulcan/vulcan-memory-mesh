// memory.go declares the unified durable-memory models shared by gRPC handlers, use cases, processors, and relational adapters.
// memory.go 用于声明 gRPC、用例层、处理器和关系适配器共享的统一长期记忆模型。
package domain

import (
	"strings"
	"time"
)

const (
	// MemoryRefTypeMemory identifies one unified memory record that should be loaded from the durable memory table.
	// MemoryRefTypeMemory 用于标识一条需要从统一记忆表读取的长期记忆记录。
	MemoryRefTypeMemory = 1

	// MemoryRefTypeTurn identifies one persisted turn row that should be loaded from the turn table.
	// MemoryRefTypeTurn 用于标识一条需要从 turn 表读取的持久化 turn 记录。
	MemoryRefTypeTurn = 2
)

const (
	// MemorySourceKindTurnExtract marks memory rows extracted from one persisted turn by the post-action analyzer.
	// MemorySourceKindTurnExtract 用于标记来源于 post-action turn 提炼流程的记忆行。
	MemorySourceKindTurnExtract = 0

	// MemorySourceKindGRPCAIWrite marks memory rows written directly by tool-facing gRPC calls.
	// MemorySourceKindGRPCAIWrite 用于标记来源于工具态 gRPC 主动写入的记忆行。
	MemorySourceKindGRPCAIWrite = 1

	// MemorySourceKindSystemSeed marks deterministic seed or future administrative import rows.
	// MemorySourceKindSystemSeed 用于标记系统种子数据或未来管理导入产生的记忆行。
	MemorySourceKindSystemSeed = 2

	// MemorySourceKindLegacyEntry marks rows migrated from the deprecated vmm_memory_entries table.
	// MemorySourceKindLegacyEntry 用于标记从已废弃 vmm_memory_entries 表迁移进来的记忆行。
	MemorySourceKindLegacyEntry = 3
)

const (
	// MemoryScopeLevelSession keeps one short-lived memory fact bound to its origin session unless it is later promoted.
	// MemoryScopeLevelSession 用于表示绑定 origin session 的短期记忆，除非后续被提升，否则不会跨 session 长留。
	MemoryScopeLevelSession = 0

	// MemoryScopeLevelProject keeps one durable fact shared by the current project scope.
	// MemoryScopeLevelProject 用于表示在当前 project 范围内共享的长期事实。
	MemoryScopeLevelProject = 1

	// MemoryScopeLevelUser keeps one user-bound durable fact that can survive across multiple projects.
	// MemoryScopeLevelUser 用于表示绑定用户且可跨多个项目继续生效的长期事实。
	MemoryScopeLevelUser = 2
)

const (
	// MemoryPriorityP0 marks a hard rule or high-risk fact that should outrank softer references during future recall.
	// MemoryPriorityP0 用于表示硬规则或高风险事实，在后续召回时应优先于较软的参考信息。
	MemoryPriorityP0 = 0

	// MemoryPriorityP1 marks one important but still updateable working memory item.
	// MemoryPriorityP1 用于表示重要但仍可在未来被更新的工作记忆。
	MemoryPriorityP1 = 1

	// MemoryPriorityP2 marks one lower-priority reference memory that is still worth retaining.
	// MemoryPriorityP2 用于表示优先级较低但仍值得保留的参考记忆。
	MemoryPriorityP2 = 2
)

const (
	// MemoryLevelSession marks a short-lived session-level memory that should normally follow the session idle-cleanup path.
	// MemoryLevelSession 用于表示短生命周期的 session 级记忆，通常应走 session idle 清理路径。
	MemoryLevelSession = 0

	// MemoryLevelPhase marks one phase-specific working memory that should outlive a few turns but still decay.
	// MemoryLevelPhase 用于表示阶段性工作记忆，寿命应长于几轮对话，但仍需衰减。
	MemoryLevelPhase = 1

	// MemoryLevelStable marks one stable project or user memory that should remain available for a long time.
	// MemoryLevelStable 用于表示稳定的项目或用户记忆，应在较长时间内保持可用。
	MemoryLevelStable = 2

	// MemoryLevelPersistent marks one very durable rule or architecture fact that should rarely expire automatically.
	// MemoryLevelPersistent 用于表示极长期的规则或架构事实，通常不应自动过期。
	MemoryLevelPersistent = 3
)

const (
	// MemoryStatusActive keeps one memory row available for recall and future lifecycle updates.
	// MemoryStatusActive 用于表示某条记忆当前仍可参与召回和后续生命周期更新。
	MemoryStatusActive = 0

	// MemoryStatusSuperseded marks one memory row that has been covered by fresher information.
	// MemoryStatusSuperseded 用于表示某条记忆已经被更新的信息覆盖。
	MemoryStatusSuperseded = 1

	// MemoryStatusDeleted marks one memory row that should no longer participate in recall.
	// MemoryStatusDeleted 用于表示某条记忆不应再参与召回。
	MemoryStatusDeleted = 2

	// MemoryStatusExpired marks one memory row that naturally aged out of the active recall window.
	// MemoryStatusExpired 用于表示某条记忆因生命周期到期而退出活跃召回窗口。
	MemoryStatusExpired = 3
)

// MemoryRef stores the `TYPE + ID` pair used by search results and detail lookup RPCs.
// MemoryRef 用于保存搜索结果和详情查询 RPC 使用的 `TYPE + ID` 组合引用。
type MemoryRef struct {
	Type int
	ID   uint64
}

// Empty reports whether the reference is missing its durable target.
// Empty 用于判断该引用是否缺少实际持久化目标。
func (r MemoryRef) Empty() bool {
	return r.Type == 0 || r.ID == 0
}

// MemoryNodeRecord stores one durable unified memory row that can be recalled, inspected, migrated, or decayed.
// MemoryNodeRecord 用于保存一条统一长期记忆行，使其可被召回、查看、迁移和衰减。
type MemoryNodeRecord struct {
	ID                       uint64
	TeamID                   uint64
	SpaceID                  uint64
	ProjectID                uint64
	UserID                   uint64
	OriginSessionID          uint64
	SourceTurnID             uint64
	VectorID                 string
	Vector                   []float32
	SourceKind               int
	ScopeLevel               int
	Category                 int
	Abstract                 string
	Details                  string
	Status                   int
	Priority                 int
	MemoryLevel              int
	RefreshWeight            int
	SupportCount             int
	RebuttalCount            int
	StatusReason             string
	ExpiresAt                time.Time
	LastRecalledAt           time.Time
	LastAdoptedAt            time.Time
	LastReinforcedAt         time.Time
	RecalledCount            int
	AdoptedCount             int
	ReinforcementCount       int
	CrossSessionAdoptedCount int
	DecayDisabled            bool
	DedupeHash               string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// MemoryContextEdge stores one durable memory-to-context relationship so later retrieval can filter or re-rank by explicit situational evidence.
// MemoryContextEdge 用于保存一条长期记忆到情境标签的关系，让后续检索可以按明确情境证据过滤或重排。
type MemoryContextEdge struct {
	MemoryID        uint64
	ContextKey      string
	ContextValue    string
	SupportCount    int
	RebuttalCount   int
	LastSupportedAt time.Time
	LastRebuttedAt  time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TurnAnalysisApplyResult stores the durable follow-up coordinates produced after one single-turn analysis has been written back.
// TurnAnalysisApplyResult 用于保存单轮分析写回后产生的长期后续坐标信息。
type TurnAnalysisApplyResult struct {
	InsertedMemoryNodes []MemoryNodeRecord
	SupersededVectorIDs []string
}

// DirectMemoryWriteApplyResult stores the durable row plus the obsolete vector ids produced by one atomic direct-memory write transaction.
// DirectMemoryWriteApplyResult 用于保存单次原子主动写记忆事务产生的持久化行，以及后续需要清理的旧向量 id。
type DirectMemoryWriteApplyResult struct {
	InsertedMemoryNode  MemoryNodeRecord
	SupersededVectorIDs []string
}

// MemorySearchRecord stores one unified search hit after vector recall has been enriched with relational memory metadata.
// MemorySearchRecord 用于保存一条经过关系层补全后的统一记忆搜索命中。
type MemorySearchRecord struct {
	MemoryRef      MemoryRef
	SourceRef      MemoryRef
	SourceKind     int
	ScopeLevel     int
	SessionID      uint64
	Category       int
	Abstract       string
	DetailsPreview string
	Score          float64
}

// MemoryLexicalHit stores one lexical-recall candidate returned by the relational search path before it is materialized back into a full durable memory row.
// MemoryLexicalHit 用于保存关系检索路径返回的一条 lexical 召回候选，在它被回表成完整长期记忆行之前使用。
type MemoryLexicalHit struct {
	MemoryID uint64
	Score    float64
}

// NormalizeMemoryContextKey converts free-form context keys into stable lower snake-style labels so processors, stores, and retrieval all index the same contextual dimension name.
// NormalizeMemoryContextKey 用于把自由形式的情境键转成稳定的小写 snake 风格标签，确保处理器、存储和检索使用同一套情境维度名称。
func NormalizeMemoryContextKey(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	replacer := strings.NewReplacer(" ", "_", "-", "_", "/", "_", "\\", "_")
	return replacer.Replace(raw)
}

// NormalizeMemoryContextValue converts one free-form contextual value into the shared lexical surface used by durable edges and query-time matching, so equivalent labels do not fork into separate evidence rows.
// NormalizeMemoryContextValue 用于把自由形式的情境值转成长期边和查询期匹配共享的词法表面，避免等价标签分裂成多条独立证据行。
func NormalizeMemoryContextValue(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	replacer := strings.NewReplacer("_", " ", "-", " ", "/", " ", "\\", " ", ".", " ")
	return strings.Join(strings.Fields(replacer.Replace(raw)), " ")
}

// ValidMemoryRefType reports whether one memory reference type belongs to the supported enum set.
// ValidMemoryRefType 用于判断某个记忆引用类型是否属于当前支持的枚举集合。
func ValidMemoryRefType(refType int) bool {
	return refType == MemoryRefTypeMemory || refType == MemoryRefTypeTurn
}

// ValidMemorySourceKind reports whether one source kind belongs to the supported unified-memory enum set.
// ValidMemorySourceKind 用于判断某个来源类型是否属于当前支持的统一记忆枚举集合。
func ValidMemorySourceKind(sourceKind int) bool {
	return sourceKind >= MemorySourceKindTurnExtract && sourceKind <= MemorySourceKindLegacyEntry
}

// ValidMemoryScopeLevel reports whether one scope level belongs to the supported unified-memory scope set.
// ValidMemoryScopeLevel 用于判断某个作用域等级是否属于当前支持的统一记忆作用域集合。
func ValidMemoryScopeLevel(scopeLevel int) bool {
	return scopeLevel >= MemoryScopeLevelSession && scopeLevel <= MemoryScopeLevelUser
}

// ValidMemoryPriority reports whether one memory priority belongs to the supported P-level enum set.
// ValidMemoryPriority 用于判断某个记忆优先级是否属于当前支持的 P 级枚举集合。
func ValidMemoryPriority(priority int) bool {
	return priority >= MemoryPriorityP0 && priority <= MemoryPriorityP2
}

// ValidMemoryLevel reports whether one memory level belongs to the supported lifecycle enum set.
// ValidMemoryLevel 用于判断某个记忆等级是否属于当前支持的生命周期枚举集合。
func ValidMemoryLevel(level int) bool {
	return level >= MemoryLevelSession && level <= MemoryLevelPersistent
}

// ValidMemoryStatus reports whether one memory status belongs to the supported durable-memory status enum set.
// ValidMemoryStatus 用于判断某个记忆状态是否属于当前支持的长期记忆状态枚举集合。
func ValidMemoryStatus(status int) bool {
	return status >= MemoryStatusActive && status <= MemoryStatusExpired
}

// MemorySourceKindLabel renders the stable source-kind text used by prompts and transport payloads.
// MemorySourceKindLabel 用于渲染提示词和传输载荷使用的稳定来源文本标签。
func MemorySourceKindLabel(sourceKind int) string {
	switch sourceKind {
	case MemorySourceKindGRPCAIWrite:
		return "GRPC_AI_WRITE"
	case MemorySourceKindSystemSeed:
		return "SYSTEM_SEED"
	case MemorySourceKindLegacyEntry:
		return "LEGACY_MEMORY_ENTRY"
	default:
		return "TURN_EXTRACT"
	}
}

// MemoryScopeLevelLabel renders the stable scope-level text used by prompts and transport payloads.
// MemoryScopeLevelLabel 用于渲染提示词和传输载荷使用的稳定作用域文本标签。
func MemoryScopeLevelLabel(scopeLevel int) string {
	switch scopeLevel {
	case MemoryScopeLevelSession:
		return "SESSION"
	case MemoryScopeLevelUser:
		return "USER"
	default:
		return "PROJECT"
	}
}
