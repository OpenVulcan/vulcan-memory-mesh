// Native rebuild snapshot loading from durable live and restorable trash rows.
// 应用层原生重建从长期有效行与可恢复回收行加载快照。
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// nativeVectorRebuildRestorableTrashWalker is the narrow durable read contract required to avoid dropping recoverable trash vectors during native table recreation.
// nativeVectorRebuildRestorableTrashWalker 是原生表重建前必须具备的 durable 读取契约，用于避免丢失可恢复 trash 向量。
type nativeVectorRebuildRestorableTrashRowsWalker interface {
	WalkNativeMigrationRestorableTrashRows(ctx context.Context, now time.Time, visit func(vldb_sqlite.NativeRestorableTrashMemory) error) error
}

// nativeVectorRebuildMemoryWalker reads all native durable memory facts for a fixed observation time.
// nativeVectorRebuildMemoryWalker 按固定观察时刻读取 native durable 全部记忆事实。
type nativeVectorRebuildMemoryWalker interface {
	WalkNativeMigrationMemories(ctx context.Context, visit func(logicdomain.MemoryRecord) error) error
}

// nativeVectorRebuildRestorableTrashWriter rewrites one exact trash row after the new embedding is materialized.
// nativeVectorRebuildRestorableTrashWriter 在新 embedding 生成后精确重写一条 trash 行。
type nativeVectorRebuildRestorableTrashWriter interface {
	UpdateNativeRestorableTrashVector(ctx context.Context, batchID, memoryID uint64, vector []float32) error
}

// nativeVectorRebuildSnapshot keeps live rows, eligible trash rows, and unique Lance rows from one pre-reset observation.
// nativeVectorRebuildSnapshot 保存一次 reset 前观察到的 live 行、eligible trash 行和唯一 Lance 行。
type nativeVectorRebuildSnapshot struct {
	ProjectCount  int
	EligibilityAt time.Time
	Active        []logicdomain.MemoryRecord
	Trash         []vldb_sqlite.NativeRestorableTrashMemory
	Unique        []logicdomain.MemoryRecord
}

// loadNativeVectorRebuildActiveFacts loads project metadata and filters native durable facts at one captured time.
// loadNativeVectorRebuildActiveFacts 加载项目元数据，并在同一捕获时刻过滤 native durable 事实。
func loadNativeVectorRebuildActiveFacts(ctx context.Context, workspace appports.WorkspaceStore, relational any, capturedAt time.Time) ([]logicdomain.ProjectRecord, []logicdomain.MemoryRecord, error) {
	projectLister, hasProjectLister := workspace.(appports.MaintenanceProjectLister)
	var (
		projects []logicdomain.ProjectRecord
		err      error
	)
	if hasProjectLister {
		projects, err = projectLister.ListProjectsForMaintenance(ctx)
	} else {
		projects, err = workspace.ListProjects(ctx)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list projects for native vector rebuild: %w", err)
	}
	walker, ok := relational.(nativeVectorRebuildMemoryWalker)
	if !ok {
		return nil, nil, fmt.Errorf("native vector rebuild requires durable all-facts walker")
	}
	active := make([]logicdomain.MemoryRecord, 0)
	err = walker.WalkNativeMigrationMemories(ctx, func(record logicdomain.MemoryRecord) error {
		if record.Status != logicdomain.MemoryStatusActive || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(capturedAt)) {
			return nil
		}
		active = append(active, cloneVectorRebuildRecord(record))
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk native active memory facts: %w", err)
	}
	return projects, active, nil
}

// loadNativeVectorRebuildSnapshot reads live and eligible trash facts before any destructive reset.
// loadNativeVectorRebuildSnapshot 在任何破坏性 reset 前读取 live 与 eligible trash 事实。
func loadNativeVectorRebuildSnapshot(ctx context.Context, workspace appports.WorkspaceStore, relational any) (nativeVectorRebuildSnapshot, error) {
	capturedAt := time.Now().UTC()
	projects, active, err := loadNativeVectorRebuildActiveFacts(ctx, workspace, relational, capturedAt)
	if err != nil {
		return nativeVectorRebuildSnapshot{}, err
	}
	walker, ok := relational.(nativeVectorRebuildRestorableTrashRowsWalker)
	if !ok {
		return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild cannot inspect restorable trash vectors: relational store does not expose composite trash rows")
	}

	snapshot := nativeVectorRebuildSnapshot{
		ProjectCount:  len(projects),
		EligibilityAt: capturedAt,
		Active:        cloneVectorRebuildRecords(active),
		Trash:         make([]vldb_sqlite.NativeRestorableTrashMemory, 0),
		Unique:        make([]logicdomain.MemoryRecord, 0, len(active)),
	}
	payloads := make(map[string][sha256.Size]byte, len(active))
	activeIDs := make(map[string]struct{}, len(active))
	for _, record := range active {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild snapshot contains an empty active vector id")
		}
		if _, exists := activeIDs[id]; exists {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild snapshot contains duplicate active vector id %q", id)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return nativeVectorRebuildSnapshot{}, fingerprintErr
		}
		activeIDs[id] = struct{}{}
		payloads[id] = fingerprint
		snapshot.Unique = append(snapshot.Unique, cloneVectorRebuildRecord(record))
	}

	// The walker applies the same eligibility filters used by native migration at the captured observation time.
	// walker 使用与 native migration 相同的资格过滤，并固定本次快照的观察时刻。
	err = walker.WalkNativeMigrationRestorableTrashRows(ctx, snapshot.EligibilityAt, func(row vldb_sqlite.NativeRestorableTrashMemory) error {
		if row.BatchID == 0 || row.MemoryID == 0 {
			return fmt.Errorf("native vector rebuild snapshot contains invalid trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		id := strings.TrimSpace(row.Record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty trash vector id for batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(row.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if existing, exists := payloads[id]; exists {
			if existing != fingerprint {
				return fmt.Errorf("native vector rebuild found conflicting live or restorable trash payloads for vector id %q", id)
			}
		} else {
			payloads[id] = fingerprint
			snapshot.Unique = append(snapshot.Unique, cloneVectorRebuildRecord(row.Record))
		}
		snapshot.Trash = append(snapshot.Trash, vldb_sqlite.NativeRestorableTrashMemory{
			BatchID:  row.BatchID,
			MemoryID: row.MemoryID,
			Record:   cloneVectorRebuildRecord(row.Record),
		})
		return nil
	})
	if err != nil {
		return nativeVectorRebuildSnapshot{}, fmt.Errorf("inspect native restorable trash vectors: %w", err)
	}
	if len(snapshot.Trash) > 0 {
		if _, ok := relational.(nativeVectorRebuildRestorableTrashWriter); !ok {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild refuses eligible restorable trash vectors: relational store does not expose exact trash-vector updates")
		}
	}
	return snapshot, nil
}

// nativeVectorRebuildPayloadFingerprint compares source facts while ignoring the old model's vector payload.
// nativeVectorRebuildPayloadFingerprint 比较源事实并忽略旧模型向量载荷。
func nativeVectorRebuildPayloadFingerprint(record logicdomain.MemoryRecord) ([sha256.Size]byte, error) {
	withoutVector := cloneVectorRebuildRecord(record)
	withoutVector.Vector = nil
	encoded, err := json.Marshal(withoutVector)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode native vector rebuild payload %q: %w", record.ID, err)
	}
	return sha256.Sum256(encoded), nil
}
