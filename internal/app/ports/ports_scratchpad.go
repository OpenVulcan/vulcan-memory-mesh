// ports_scratchpad.go keeps scratchpad persistence and scratchpad maintenance contracts used by deterministic working-memory flows.
// ports_scratchpad.go 用于承载确定性工作记忆链路依赖的 scratchpad 持久化与维护契约。
package ports

import (
	"context"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ScratchpadStore is the isolated relational port used by the deterministic working-memory chain to validate scope coordinates, guard plan locks, and persist scratchpad key/value nodes without touching the main memory/session flow.
// ScratchpadStore 用于给确定性工作记忆链路提供隔离的关系端口，让它在不触碰主记忆/主 session 流程的前提下校验范围坐标、守护计划锁并持久化 scratchpad key/value 节点。
type ScratchpadStore interface {
	EnsureScratchpadScope(ctx context.Context, scope logicdomain.ScratchpadScope) error
	LoadScratchpadPlan(ctx context.Context, scope logicdomain.ScratchpadScope) (logicdomain.ScratchpadPlanRecord, bool, error)
	CreateScratchpadPlan(ctx context.Context, scope logicdomain.ScratchpadScope, planName string, createdAt time.Time) (logicdomain.ScratchpadPlanRecord, error)
	UpsertScratchpadItems(ctx context.Context, planID uint64, items []logicdomain.ScratchpadItem, updatedAt time.Time) (logicdomain.ScratchpadUpsertPersistResult, error)
	DeleteScratchpadItems(ctx context.Context, planID uint64, keys []string, updatedAt time.Time) (logicdomain.ScratchpadDeletePersistResult, error)
	ListScratchpadItems(ctx context.Context, planID uint64, keys []string) ([]logicdomain.ScratchpadItem, error)
	ListScratchpadKeys(ctx context.Context, planID uint64) ([]string, error)
	CleanScratchpad(ctx context.Context, scope logicdomain.ScratchpadScope) (logicdomain.ScratchpadCleanPersistResult, error)
}

// ScratchpadMaintenanceStore is the narrow background-maintenance port used to hard-delete expired scratchpad plans without reusing retention trash semantics.
// ScratchpadMaintenanceStore 用于给后台维护流程提供一个狭窄端口，让其能在不复用 retention 回收站语义的前提下硬删除过期 scratchpad 计划。
type ScratchpadMaintenanceStore interface {
	DeleteExpiredScratchpadSessions(ctx context.Context, before time.Time, limit int) (logicdomain.ScratchpadGCResult, error)
}
