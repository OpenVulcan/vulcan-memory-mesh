// Native rebuild verification before publishing storage identity.
// 应用层原生重建在发布存储身份前的完整性核验。
package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"strings"
)

// nativeVectorRebuildCounter is the optional health surface used to verify the physical native vector row count after a rebuild.
// nativeVectorRebuildCounter 是重建后核对原生物理向量行数所需的可选健康能力。
type nativeVectorRebuildCounter interface {
	Count(context.Context) (int64, error)
}

// verifyNativeVectorRebuildCompletion proves that relationship facts, vector dimensions, and physical vector rows converged to one snapshot.
// verifyNativeVectorRebuildCompletion 证明关系事实、向量维度和物理向量行数已经收敛到同一份快照。
func verifyNativeVectorRebuildCompletion(ctx context.Context, workspace appports.WorkspaceStore, relational any, vector appports.VectorStore, expected nativeVectorRebuildSnapshot, expectedDimension int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if workspace == nil {
		return fmt.Errorf("native vector rebuild verification requires workspace store")
	}
	if relational == nil {
		return fmt.Errorf("native vector rebuild verification requires relational store")
	}
	if vector == nil {
		return fmt.Errorf("native vector rebuild verification requires vector store")
	}
	if expected.EligibilityAt.IsZero() {
		return fmt.Errorf("native vector rebuild verification requires the snapshot eligibility time")
	}
	eligibilityAt := expected.EligibilityAt
	_, actual, err := loadNativeVectorRebuildActiveFacts(ctx, workspace, relational, eligibilityAt)
	if err != nil {
		return fmt.Errorf("reload relationship facts after native vector rebuild: %w", err)
	}
	expectedActiveIDs := make(map[string]struct{}, len(expected.Active))
	expectedPayloads := make(map[string][sha256.Size]byte, len(expected.Unique))
	for _, record := range expected.Active {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty active vector id")
		}
		if _, exists := expectedActiveIDs[id]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate active vector id %q", id)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		expectedActiveIDs[id] = struct{}{}
		expectedPayloads[id] = fingerprint
	}
	for _, record := range expected.Unique {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty unique vector id")
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if existing, exists := expectedPayloads[id]; exists && existing != fingerprint {
			return fmt.Errorf("native vector rebuild snapshot contains conflicting unique payloads for vector id %q", id)
		}
		expectedPayloads[id] = fingerprint
	}
	if len(actual) != len(expected.Active) {
		return fmt.Errorf("relationship fact count changed during native vector rebuild: got %d, want %d", len(actual), len(expected.Active))
	}
	actualIDs := make(map[string]struct{}, len(actual))
	for _, record := range actual {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("relationship facts contain an empty vector id after native vector rebuild")
		}
		if _, exists := actualIDs[id]; exists {
			return fmt.Errorf("relationship facts contain duplicate vector id %q after native vector rebuild", id)
		}
		actualIDs[id] = struct{}{}
		expectedPayload, exists := expectedPayloads[id]
		if !exists {
			return fmt.Errorf("native vector rebuild produced unexpected relationship vector id %q", id)
		}
		actualPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if actualPayload != expectedPayload {
			return fmt.Errorf("relationship payload hash changed for vector id %q after native vector rebuild", id)
		}
		if expectedDimension <= 0 || len(record.Vector) != expectedDimension {
			return fmt.Errorf("relationship vector %q has dimension %d after native vector rebuild, want %d", id, len(record.Vector), expectedDimension)
		}
	}
	trashWalker, ok := relational.(nativeVectorRebuildRestorableTrashRowsWalker)
	if !ok {
		return fmt.Errorf("native vector rebuild verification requires composite restorable-trash rows")
	}
	type trashKey struct {
		batchID  uint64
		memoryID uint64
	}
	expectedTrash := make(map[trashKey]vldb_sqlite.NativeRestorableTrashMemory, len(expected.Trash))
	for _, row := range expected.Trash {
		key := trashKey{batchID: row.BatchID, memoryID: row.MemoryID}
		if row.BatchID == 0 || row.MemoryID == 0 {
			return fmt.Errorf("native vector rebuild snapshot contains invalid trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		if _, exists := expectedTrash[key]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedTrash[key] = row
	}
	actualTrash := make(map[trashKey]vldb_sqlite.NativeRestorableTrashMemory, len(expected.Trash))
	err = trashWalker.WalkNativeMigrationRestorableTrashRows(ctx, eligibilityAt, func(row vldb_sqlite.NativeRestorableTrashMemory) error {
		key := trashKey{batchID: row.BatchID, memoryID: row.MemoryID}
		if _, exists := actualTrash[key]; exists {
			return fmt.Errorf("native vector rebuild produced duplicate trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedRow, exists := expectedTrash[key]
		if !exists {
			return fmt.Errorf("native vector rebuild produced unexpected trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		id := strings.TrimSpace(row.Record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild produced empty trash vector id batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(expectedRow.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		actualPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(row.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if actualPayload != expectedPayload {
			return fmt.Errorf("trash payload hash changed for batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		uniquePayload, exists := expectedPayloads[id]
		if !exists || uniquePayload != actualPayload {
			return fmt.Errorf("trash payload hash does not match unique vector id %q", id)
		}
		if expectedDimension <= 0 || len(row.Record.Vector) != expectedDimension {
			return fmt.Errorf("trash vector %q has dimension %d after native vector rebuild, want %d", id, len(row.Record.Vector), expectedDimension)
		}
		actualTrash[key] = row
		return nil
	})
	if err != nil {
		return fmt.Errorf("reload restorable trash facts after native vector rebuild: %w", err)
	}
	if len(actualTrash) != len(expectedTrash) {
		return fmt.Errorf("restorable trash fact count changed during native vector rebuild: got %d, want %d", len(actualTrash), len(expectedTrash))
	}
	uniqueIDs := make(map[string]struct{}, len(expected.Unique))
	for _, record := range expected.Unique {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty unique vector id")
		}
		if _, exists := uniqueIDs[id]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate unique vector id %q", id)
		}
		uniqueIDs[id] = struct{}{}
	}
	for _, row := range actualTrash {
		id := strings.TrimSpace(row.Record.ID)
		if _, exists := uniqueIDs[id]; !exists {
			return fmt.Errorf("native vector rebuild produced unexpected unique trash vector id %q", id)
		}
	}
	counter, ok := vector.(nativeVectorRebuildCounter)
	if !ok {
		return fmt.Errorf("native vector store does not expose physical row counting")
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("count native vector rows after rebuild: %w", err)
	}
	if count != int64(len(uniqueIDs)) {
		return fmt.Errorf("native vector row count after rebuild: got %d, want %d", count, len(uniqueIDs))
	}
	return nil
}
