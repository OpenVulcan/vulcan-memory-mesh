// management_integration_test.go verifies management projections against the real packaged SQLite FFI.
// management_integration_test.go 用于通过真实打包 SQLite FFI 验证管理投影。
package vldb_sqlite

import (
	"context"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRealSQLiteManagementSessionAndTurnPages verifies joined counts and non-overlapping cursor pages.
// TestRealSQLiteManagementSessionAndTurnPages 用于验证联表计数与不重叠游标分页。
func TestRealSQLiteManagementSessionAndTurnPages(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	createdMs := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC).UnixMilli()
	statements := []sqliteWriteStatement{
		{SQL: `INSERT INTO vmm_users (id, name, profile, delete_confirm_code, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)`, Params: []any{uint64(101), "管理用户", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_teams (id, name, profile, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, Params: []any{uint64(201), "团队", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_spaces (id, team_id, name, profile, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, Params: []any{uint64(301), uint64(201), "空间", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_projects (id, team_id, space_id, name, profile, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`, Params: []any{uint64(401), uint64(201), uint64(301), "项目", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_sessions (id, session_key, user_id, team_id, space_id, project_id, turn_count, created_timestamp, updated_timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, Params: []any{uint64(501), "session-a", uint64(101), uint64(201), uint64(301), uint64(401), 2, createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_turn_records (id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp) VALUES (?, ?, ?, ?, 10, ?, '', 0, ?, ?)`, Params: []any{uint64(601), uint64(501), uint64(401), `{"user":"首条消息","timeline":[],"assistant":"回答"}`, logicdomain.TurnExtractedStatusPending, createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_turn_records (id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp) VALUES (?, ?, ?, ?, 12, ?, 'done', 2, ?, ?)`, Params: []any{uint64(602), uint64(501), uint64(401), `{"user":"第二条","timeline":[],"assistant":"回答"}`, logicdomain.TurnExtractedStatusDone, createdMs + 1, createdMs + 1}},
	}
	if err := store.execWriteStatements(ctx, statements...); err != nil {
		t.Fatalf("seed management fixture: %v", err)
	}
	page, err := store.ListManagementSessions(ctx, logicdomain.ManagementSessionQuery{Limit: 1, Sort: "updated_desc", Status: "active"})
	if err != nil {
		t.Fatalf("ListManagementSessions() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListManagementSessions() items = %d", len(page.Items))
	}
	session := page.Items[0]
	if session.SessionID != 501 || session.UserName != "管理用户" || session.VisibleTurnCount != 2 || session.PendingTurnCount != 1 {
		t.Fatalf("session projection = %+v", session)
	}
	turns, err := store.ListManagementTurns(ctx, logicdomain.ManagementTurnQuery{SessionID: 501, Status: "all", Limit: 1})
	if err != nil {
		t.Fatalf("ListManagementTurns() error = %v", err)
	}
	if len(turns.Items) != 1 || turns.Items[0].ID != 601 || !turns.HasMore || turns.NextCursorID != 601 {
		t.Fatalf("first turn page = %+v", turns)
	}
	next, err := store.ListManagementTurns(ctx, logicdomain.ManagementTurnQuery{SessionID: 501, Status: "all", CursorID: turns.NextCursorID, Limit: 1})
	if err != nil {
		t.Fatalf("ListManagementTurns(next) error = %v", err)
	}
	if len(next.Items) != 1 || next.Items[0].ID != 602 {
		t.Fatalf("second turn page = %+v", next)
	}
}

// TestRealSQLiteManagementArchiveRecycleRestoreAndPurge verifies the complete manual lifecycle against real SQLite.
// TestRealSQLiteManagementArchiveRecycleRestoreAndPurge 用于通过真实 SQLite 验证完整人工归档、回收、恢复与永久清理生命周期。
func TestRealSQLiteManagementArchiveRecycleRestoreAndPurge(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 31, 2, 0, 0, 0, time.UTC)
	createdMs := now.UnixMilli()
	statements := []sqliteWriteStatement{
		{SQL: `INSERT INTO vmm_users (id, name, profile, delete_confirm_code, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)`, Params: []any{uint64(1101), "生命周期用户", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_teams (id, name, profile, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, Params: []any{uint64(1201), "生命周期团队", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_spaces (id, team_id, name, profile, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?)`, Params: []any{uint64(1301), uint64(1201), "生命周期空间", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_projects (id, team_id, space_id, name, profile, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`, Params: []any{uint64(1401), uint64(1201), uint64(1301), "生命周期项目", createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_sessions (id, session_key, user_id, team_id, space_id, project_id, turn_count, created_timestamp, updated_timestamp) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, Params: []any{uint64(1501), "lifecycle-session", uint64(1101), uint64(1201), uint64(1301), uint64(1401), createdMs, createdMs}},
		{SQL: `INSERT INTO vmm_turn_records (id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp) VALUES (?, ?, ?, ?, 1, ?, '', 0, ?, ?)`, Params: []any{uint64(1601), uint64(1501), uint64(1401), `{"user":"生命周期","timeline":[],"assistant":"完成"}`, logicdomain.TurnExtractedStatusDone, createdMs, createdMs}},
	}
	if err := store.execWriteStatements(ctx, statements...); err != nil {
		t.Fatalf("seed lifecycle fixture: %v", err)
	}

	archiveSelection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession, TargetIDs: []uint64{1501},
		Action: logicdomain.ManagementActionArchive, Source: "test",
	}
	archiveImpact, err := store.InspectManagementSelection(ctx, archiveSelection, 0)
	if err != nil {
		t.Fatalf("InspectManagementSelection(archive) error = %v", err)
	}
	archivePreview := logicdomain.ManagementPreview{Token: "preview-archive", Selection: archiveSelection, Impact: archiveImpact, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.SaveManagementPreview(ctx, archivePreview); err != nil {
		t.Fatalf("SaveManagementPreview(archive) error = %v", err)
	}
	archiveOperation, err := store.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID: "operation-archive", IdempotencyKey: "idempotency-archive", Preview: archivePreview, Now: now,
	})
	if err != nil || archiveOperation.Status != "succeeded" {
		t.Fatalf("ApplyManagementRemoval(archive) = %+v, %v", archiveOperation, err)
	}
	active, err := store.ListManagementSessions(ctx, logicdomain.ManagementSessionQuery{Status: "active", Limit: 10, Sort: "updated_desc"})
	if err != nil || len(active.Items) != 0 {
		t.Fatalf("active sessions after archive = %+v, %v", active, err)
	}
	archived, err := store.ListManagementSessions(ctx, logicdomain.ManagementSessionQuery{Status: "archived", Limit: 10, Sort: "updated_desc"})
	if err != nil || len(archived.Items) != 1 || archived.Items[0].Status != "archived" {
		t.Fatalf("archived sessions = %+v, %v", archived, err)
	}

	recycleSelection := archiveSelection
	recycleSelection.Action = logicdomain.ManagementActionRecycle
	recycleImpact, err := store.InspectManagementSelection(ctx, recycleSelection, 0)
	if err != nil {
		t.Fatalf("InspectManagementSelection(recycle) error = %v", err)
	}
	recyclePreview := logicdomain.ManagementPreview{Token: "preview-recycle", Selection: recycleSelection, Impact: recycleImpact, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.SaveManagementPreview(ctx, recyclePreview); err != nil {
		t.Fatalf("SaveManagementPreview(recycle) error = %v", err)
	}
	recycleOperation, err := store.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID: "operation-recycle", IdempotencyKey: "idempotency-recycle", Preview: recyclePreview,
		Now: now, ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil || recycleOperation.BatchID == 0 {
		t.Fatalf("ApplyManagementRemoval(recycle) = %+v, %v", recycleOperation, err)
	}
	recycledSession, err := store.GetManagementSession(ctx, 1501)
	if err != nil || recycledSession.Status != string(logicdomain.SessionMemoryStatusRecycled) || recycledSession.VisibleTurnCount != 0 {
		t.Fatalf("recycled session tombstone = %+v, %v", recycledSession, err)
	}
	recycledScope, err := store.ResolveRequestScope(ctx, "lifecycle-session", 1101, 1401)
	if err != nil || recycledScope.SessionID != 1501 || recycledScope.MemoryStatus != logicdomain.SessionMemoryStatusRecycled {
		t.Fatalf("ResolveRequestScope(recycled) = %+v, %v", recycledScope, err)
	}
	batch, err := store.GetManagementRecycleBatch(ctx, recycleOperation.BatchID)
	if err != nil || !batch.Restorable || batch.SessionCount != 1 || batch.TurnCount != 1 {
		t.Fatalf("recycle batch = %+v, %v", batch, err)
	}

	restoreOperation, err := store.RestoreManagementBatch(ctx, logicdomain.ManagementRestoreCommand{
		OperationID: "operation-restore", IdempotencyKey: "idempotency-restore",
		BatchID: recycleOperation.BatchID, Now: now.Add(time.Second),
	})
	if err != nil || restoreOperation.Status != "succeeded" {
		t.Fatalf("RestoreManagementBatch() = %+v, %v", restoreOperation, err)
	}
	if _, err := store.GetManagementSession(ctx, 1501); err != nil {
		t.Fatalf("GetManagementSession(restored) error = %v", err)
	}
	restoredBatch, err := store.GetManagementRecycleBatch(ctx, recycleOperation.BatchID)
	if err != nil || restoredBatch.State != "restored" || restoredBatch.Restorable == false {
		t.Fatalf("restored batch = %+v, %v", restoredBatch, err)
	}

	recycleImpact, err = store.InspectManagementSelection(ctx, recycleSelection, 0)
	if err != nil {
		t.Fatalf("InspectManagementSelection(second recycle) error = %v", err)
	}
	secondPreview := logicdomain.ManagementPreview{Token: "preview-recycle-2", Selection: recycleSelection, Impact: recycleImpact, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.SaveManagementPreview(ctx, secondPreview); err != nil {
		t.Fatalf("SaveManagementPreview(second recycle) error = %v", err)
	}
	secondRecycle, err := store.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID: "operation-recycle-2", IdempotencyKey: "idempotency-recycle-2", Preview: secondPreview,
		Now: now.Add(2 * time.Second), ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ApplyManagementRemoval(second recycle) error = %v", err)
	}
	purgeSelection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{secondRecycle.BatchID},
		Action: logicdomain.ManagementActionPurge, Source: "test",
	}
	purgeImpact, err := store.InspectManagementSelection(ctx, purgeSelection, 0)
	if err != nil {
		t.Fatalf("InspectManagementSelection(purge) error = %v", err)
	}
	purgePreview := logicdomain.ManagementPreview{Token: "preview-purge", Selection: purgeSelection, Impact: purgeImpact, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.SaveManagementPreview(ctx, purgePreview); err != nil {
		t.Fatalf("SaveManagementPreview(purge) error = %v", err)
	}
	purgeOperation, err := store.PurgeManagementBatch(ctx, logicdomain.ManagementMutationCommand{
		OperationID: "operation-purge", IdempotencyKey: "idempotency-purge", Preview: purgePreview, Now: now.Add(3 * time.Second),
	})
	if err != nil || purgeOperation.Status != "succeeded" {
		t.Fatalf("PurgeManagementBatch() = %+v, %v", purgeOperation, err)
	}
	if _, err := store.GetManagementRecycleBatch(ctx, secondRecycle.BatchID); !logicdomain.IsNotFoundError(err) {
		t.Fatalf("expected purged batch to be absent, got %v", err)
	}
	forgottenScope, err := store.ResolveRequestScope(ctx, "lifecycle-session", 1101, 1401)
	if err != nil || forgottenScope.SessionID != 1501 || forgottenScope.MemoryStatus != logicdomain.SessionMemoryStatusForgotten {
		t.Fatalf("ResolveRequestScope(forgotten) = %+v, %v", forgottenScope, err)
	}
	if _, err := store.InspectManagementSelection(ctx, logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession, TargetIDs: []uint64{1501},
		Action: logicdomain.ManagementActionUnarchive, Source: "test",
	}, 0); !logicdomain.IsConflictError(err) {
		t.Fatalf("InspectManagementSelection(unarchive forgotten) error = %v, want conflict", err)
	}
	restartSelection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession, TargetIDs: []uint64{1501},
		Action: logicdomain.ManagementActionRestart, Source: "test",
	}
	restartImpact, err := store.InspectManagementSelection(ctx, restartSelection, 0)
	if err != nil {
		t.Fatalf("InspectManagementSelection(restart) error = %v", err)
	}
	restartPreview := logicdomain.ManagementPreview{Token: "preview-restart", Selection: restartSelection, Impact: restartImpact, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := store.SaveManagementPreview(ctx, restartPreview); err != nil {
		t.Fatalf("SaveManagementPreview(restart) error = %v", err)
	}
	restartOperation, err := store.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID: "operation-restart", IdempotencyKey: "idempotency-restart", Preview: restartPreview, Now: now.Add(4 * time.Second),
	})
	if err != nil || restartOperation.Status != "succeeded" {
		t.Fatalf("ApplyManagementRemoval(restart) = %+v, %v", restartOperation, err)
	}
	restartedScope, err := store.ResolveRequestScope(ctx, "lifecycle-session", 1101, 1401)
	if err != nil || restartedScope.MemoryStatus != logicdomain.SessionMemoryStatusActive {
		t.Fatalf("ResolveRequestScope(restarted) = %+v, %v", restartedScope, err)
	}
	restartedTurns, err := store.ListManagementTurns(ctx, logicdomain.ManagementTurnQuery{SessionID: 1501, Limit: 10, Sort: "created_desc"})
	if err != nil || len(restartedTurns.Items) != 0 {
		t.Fatalf("ListManagementTurns(restarted) = %+v, %v", restartedTurns, err)
	}
}
