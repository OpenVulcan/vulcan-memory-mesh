// ports_management.go defines the isolated read contract used by human-facing management use cases.
// ports_management.go 用于定义面向人工管理用例的独立读取契约。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ManagementStore exposes bounded management projections without leaking database handles to transports.
// ManagementStore 用于暴露有界管理投影，同时避免把数据库句柄泄露给传输层。
type ManagementStore interface {
	ListManagementSessions(ctx context.Context, query logicdomain.ManagementSessionQuery) (logicdomain.ManagementSessionPage, error)
	GetManagementSession(ctx context.Context, sessionID uint64) (logicdomain.ManagementSessionRecord, error)
	ListManagementTurns(ctx context.Context, query logicdomain.ManagementTurnQuery) (logicdomain.ManagementTurnPage, error)
	GetManagementTurn(ctx context.Context, turnID uint64) (logicdomain.ManagementTurnRecord, error)
	ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error)
	ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error)
}

// ManagementMutationStore exposes preview-bound, auditable management writes separately from bounded reads.
// ManagementMutationStore 用于把受预览约束、可审计的管理写入与有界读取分离暴露。
type ManagementMutationStore interface {
	InspectManagementSelection(ctx context.Context, selection logicdomain.ManagementRemovalSelection, hotWindowSize int) (logicdomain.ManagementImpact, error)
	SaveManagementPreview(ctx context.Context, preview logicdomain.ManagementPreview) error
	GetManagementPreview(ctx context.Context, token string) (logicdomain.ManagementPreview, error)
	ApplyManagementRemoval(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error)
	RestoreManagementBatch(ctx context.Context, command logicdomain.ManagementRestoreCommand) (logicdomain.ManagementOperation, error)
	PurgeManagementBatch(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error)
	GetManagementOperation(ctx context.Context, operationID string) (logicdomain.ManagementOperation, error)
	GetManagementOperationByIdempotencyKey(ctx context.Context, idempotencyKey string) (logicdomain.ManagementOperation, error)
	ListManagementRecycleBatches(ctx context.Context, cursorID uint64, limit int) (logicdomain.ManagementRecycleBatchPage, error)
	GetManagementRecycleBatch(ctx context.Context, batchID uint64) (logicdomain.ManagementRecycleBatch, error)
}

// ManagementVectorQuarantineStore exposes the sidecar vector identifiers retained by restorable manual recycle batches.
// ManagementVectorQuarantineStore 用于暴露可恢复人工回收批次暂存的旁路向量标识。
type ManagementVectorQuarantineStore interface {
	ListManagementRecycleBatchVectorIDs(ctx context.Context, batchIDs []uint64) ([]string, error)
}
