// native_storage_integration_test.go verifies the native storage pair and the offline migration against temporary artifacts.
// native_storage_integration_test.go 使用临时产物验证原生存储组合与离线迁移，不触碰仓库或用户数据。
package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	controlleradapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_controller"
	lancedbadapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	sqliteadapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	_ "modernc.org/sqlite"
)

// nativeIntegrationBusinessTables is the audited durable business table set copied by migration, excluding version metadata that native startup extends.
// nativeIntegrationBusinessTables 是迁移实现审计过的长期业务表清单，排除原生启动会追加的版本元数据表。
var nativeIntegrationBusinessTables = []string{
	"vmm_noise_embeddings",
	"vmm_users",
	"vmm_teams",
	"vmm_spaces",
	"vmm_projects",
	"vmm_sessions",
	"vmm_turn_records",
	"vmm_turn_analysis_failures",
	"vmm_memory_nodes",
	"vmm_memory_context_edges",
	"vmm_recycle_batches",
	"vmm_recycle_jobs",
	"vmm_memory_nodes_trash",
	"vmm_memory_context_edges_trash",
	"vmm_turn_records_trash",
	"vmm_vector_gc_jobs",
	"vmm_profile_nodes",
	"vmm_profile_instructions",
	"vmm_management_session_states",
	"vmm_management_previews",
	"vmm_management_operations",
	"vmm_management_recycle_batches",
	"vmm_sessions_trash",
	"vmm_profile_nodes_trash",
}

// TestNativeStorageBuildHealthOwnershipAndRestart verifies the real native pair, health gate, exclusive ownership, and reopen behavior.
// TestNativeStorageBuildHealthOwnershipAndRestart 验证真实原生数据库组合、健康门禁、独占所有权和重启复用行为。
func TestNativeStorageBuildHealthOwnershipAndRestart(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	root := t.TempDir()
	promptLayout := testPackagedPromptLayout(root)
	cfg := testNativeConfig(root, nativeLibrary)

	deps, err := buildNativeStorageDependencies(cfg, promptLayout, true)
	if err != nil {
		t.Fatalf("build native storage dependencies: %v", err)
	}
	t.Cleanup(func() { _ = deps.Lifecycle.Shutdown(context.Background()) })
	if deps.ManageVectorSchema {
		t.Fatal("native storage must not delegate destructive vector schema management to the legacy path")
	}
	if deps.Relational == nil || deps.Vector == nil || deps.Health == nil || deps.Lifecycle == nil {
		t.Fatalf("native storage dependencies are incomplete: %+v", deps)
	}
	if err := deps.Health.CheckHealth(context.Background()); err != nil {
		t.Fatalf("native storage health check: %v", err)
	}
	deferredRoot := t.TempDir()
	deferredLayout := testPackagedPromptLayout(deferredRoot)
	deferredCfg := testNativeConfig(deferredRoot, nativeLibrary)
	deferred, err := buildNativeStorageDependencies(deferredCfg, deferredLayout, false)
	if err != nil {
		t.Fatalf("build native storage without vector schema: %v", err)
	}
	t.Cleanup(func() { _ = deferred.Lifecycle.Shutdown(context.Background()) })
	if deferred.ManageVectorSchema {
		_ = deferred.Lifecycle.Shutdown(context.Background())
		t.Fatal("native maintenance build unexpectedly owns vector schema management")
	}
	if err := deferred.Health.CheckHealth(context.Background()); err == nil {
		_ = deferred.Lifecycle.Shutdown(context.Background())
		t.Fatal("native build without vector schema unexpectedly passed the full health gate")
	}
	if err := deferred.Lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown native build without vector schema: %v", err)
	}
	relationalBeforeClean, ok := deps.Relational.(*sqliteadapter.Store)
	if !ok {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("native relational dependency type before clean = %T", deps.Relational)
	}
	cleanSession, err := relationalBeforeClean.ResolveRequestScope(context.Background(), "native-debug-clean-before", 1, 1)
	if err != nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("resolve pre-clean native session: %v", err)
	}
	cleanTurn, err := relationalBeforeClean.AppendTurnRecord(context.Background(), cleanSession, logicdomain.TurnRecord{
		UserContent:      "清理前旧关键词",
		AssistantContent: "保留旧 FTS 文档用于清理验证",
		CreatedAt:        time.Date(2026, 9, 20, 1, 50, 0, 0, time.UTC),
	})
	if err != nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("append pre-clean native turn: %v", err)
	}
	oldMemory, err := relationalBeforeClean.CreateDirectMemoryNode(context.Background(), cleanSession, logicdomain.MemoryNodeRecord{
		SourceTurnID: cleanTurn.ID,
		VectorID:     "native-debug-clean-old-vector",
		Vector:       []float32{0.1, 0.1, 0.1},
		SourceKind:   logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:   logicdomain.MemoryScopeLevelProject,
		Category:     logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:     "清理前旧关键词",
		Details:      "old native fts marker",
		Status:       logicdomain.MemoryStatusActive,
		Priority:     logicdomain.MemoryPriorityP2,
		MemoryLevel:  logicdomain.MemoryLevelStable,
		CreatedAt:    time.Date(2026, 9, 20, 1, 51, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 9, 20, 1, 51, 0, 0, time.UTC),
	})
	if err != nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("create pre-clean native memory: %v", err)
	}
	oldHits, err := relationalBeforeClean.SearchLexicalMemory(context.Background(), "旧关键词", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || !containsNativeMemoryID(oldHits, oldMemory.ID) {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("pre-clean old FTS recall = %+v, %v", oldHits, err)
	}
	if contender, err := buildNativeStorageDependencies(cfg, promptLayout, true); err == nil {
		_ = contender.Lifecycle.Shutdown(context.Background())
		t.Fatal("second native storage owner acquired the same database pair")
	}

	// A cancelled shutdown context must still release both OS locks and backend handles.
	// 已取消的关闭上下文仍必须释放两把系统锁和底层数据库句柄。
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := deps.Lifecycle.Shutdown(cancelled); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown native storage with cancelled context: %v", err)
	}

	reopened, err := buildNativeStorageDependencies(cfg, promptLayout, true)
	if err != nil {
		t.Fatalf("reopen native storage after cancelled shutdown: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Lifecycle.Shutdown(context.Background()) })
	if err := reopened.Health.CheckHealth(context.Background()); err != nil {
		t.Fatalf("reopened native storage health check: %v", err)
	}
	restartedRelational, ok := reopened.Relational.(*sqliteadapter.Store)
	if !ok {
		t.Fatalf("reopened native relational dependency type = %T", reopened.Relational)
	}
	if err := restartedRelational.DebugCleanManagedSchema(context.Background()); err != nil {
		t.Fatalf("debug clean native relational schema: %v", err)
	}
	if err := reopened.Lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown reopened native storage: %v", err)
	}

	// Reopening after the destructive clean must recreate schema, FTS metadata, and memory triggers before serving writes.
	// 破坏性清理后的重启必须在提供写入前重新创建 schema、FTS 元数据和记忆触发器。
	rebuilt, err := buildNativeStorageDependencies(cfg, promptLayout, true)
	if err != nil {
		t.Fatalf("rebuild native storage after debug clean: %v", err)
	}
	t.Cleanup(func() { _ = rebuilt.Lifecycle.Shutdown(context.Background()) })
	rebuiltRelational, ok := rebuilt.Relational.(*sqliteadapter.Store)
	if !ok {
		t.Fatalf("rebuilt native relational dependency type = %T", rebuilt.Relational)
	}
	if err := rebuilt.Health.CheckHealth(context.Background()); err != nil {
		t.Fatalf("rebuilt native storage health check: %v", err)
	}
	staleHits, err := rebuiltRelational.SearchLexicalMemory(context.Background(), "旧关键词", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("search old keyword after native clean: %v", err)
	}
	if len(staleHits) != 0 {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("old FTS keyword survived native clean: %+v", staleHits)
	}
	rebuiltSession, err := rebuiltRelational.ResolveRequestScope(context.Background(), "native-debug-clean-restart", 1, 1)
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("resolve rebuilt native session: %v", err)
	}
	rebuiltTurn, err := rebuiltRelational.AppendTurnRecord(context.Background(), rebuiltSession, logicdomain.TurnRecord{
		UserContent:      "原生清理后重建中文检索",
		AssistantContent: "验证 FTS 触发器与 marker",
		CreatedAt:        time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("append rebuilt native turn: %v", err)
	}
	rebuiltMemory, err := rebuiltRelational.CreateDirectMemoryNode(context.Background(), rebuiltSession, logicdomain.MemoryNodeRecord{
		SourceTurnID: rebuiltTurn.ID,
		VectorID:     "native-debug-clean-vector",
		Vector:       []float32{0.2, 0.3, 0.4},
		SourceKind:   logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:   logicdomain.MemoryScopeLevelProject,
		Category:     logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:     "原生清理后重建中文检索",
		Details:      "FTS marker trigger recovery",
		Status:       logicdomain.MemoryStatusActive,
		Priority:     logicdomain.MemoryPriorityP2,
		MemoryLevel:  logicdomain.MemoryLevelStable,
		CreatedAt:    time.Date(2026, 9, 20, 2, 1, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 9, 20, 2, 1, 0, 0, time.UTC),
	})
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("create rebuilt native memory: %v", err)
	}
	if rebuiltMemory.ID != oldMemory.ID {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("rebuilt native memory ID = %d, want old numeric ID %d for clean reuse", rebuiltMemory.ID, oldMemory.ID)
	}
	rebuiltHits, err := rebuiltRelational.SearchLexicalMemory(context.Background(), "清理后重建 中文", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("search rebuilt native Chinese FTS: %v", err)
	}
	if !containsNativeMemoryID(rebuiltHits, rebuiltMemory.ID) {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("rebuilt native FTS did not recall memory %d: %+v", rebuiltMemory.ID, rebuiltHits)
	}
	oldHitsAfterReuse, err := rebuiltRelational.SearchLexicalMemory(context.Background(), "旧关键词", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("search old keyword after numeric ID reuse: %v", err)
	}
	if len(oldHitsAfterReuse) != 0 {
		_ = rebuilt.Lifecycle.Shutdown(context.Background())
		t.Fatalf("old FTS keyword leaked into reused numeric ID: %+v", oldHitsAfterReuse)
	}
	if err := rebuilt.Lifecycle.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown rebuilt native storage: %v", err)
	}
}

// TestNativeVectorFilterAndOrderingWithFixedFixture verifies deterministic scope filtering and nearest-first ordering on the native LanceDB table.
// TestNativeVectorFilterAndOrderingWithFixedFixture 验证原生 LanceDB 向量表的固定范围过滤与近邻优先排序。
func TestNativeVectorFilterAndOrderingWithFixedFixture(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	root := t.TempDir()
	promptLayout := testPackagedPromptLayout(root)
	cfg := testNativeConfig(root, nativeLibrary)
	deps, err := buildNativeStorageDependencies(cfg, promptLayout, true)
	if err != nil {
		t.Fatalf("build native storage for vector contract: %v", err)
	}
	vector, ok := deps.Vector.(*lancedbadapter.Store)
	if !ok {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("native vector dependency type = %T", deps.Vector)
	}
	ctx := context.Background()
	defer func() { _ = deps.Lifecycle.Shutdown(context.Background()) }()
	createdAt := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	matchingFilter := logicdomain.SearchFilter{UserID: 7, TeamID: 8, SpaceID: 9, ProjectID: 10, SessionID: 9007199254740993}
	for _, record := range []logicdomain.MemoryRecord{
		{ID: "native-vector-exact", Text: "exact fixed vector", Vector: []float32{1, 0, 0}, Filter: matchingFilter, SourceTurnID: 1, CreatedAt: createdAt},
		{ID: "native-vector-close", Text: "close fixed vector", Vector: []float32{0.9, 0.1, 0}, Filter: matchingFilter, SourceTurnID: 2, CreatedAt: createdAt.Add(time.Second)},
		{ID: "native-vector-other-scope", Text: "excluded fixed vector", Vector: []float32{1, 0, 0}, Filter: logicdomain.SearchFilter{UserID: 99, TeamID: 8, SpaceID: 9, ProjectID: 10}, SourceTurnID: 3, CreatedAt: createdAt.Add(2 * time.Second)},
	} {
		if err := vector.Upsert(ctx, record); err != nil {
			t.Fatalf("upsert fixed vector %q: %v", record.ID, err)
		}
	}
	hits, err := vector.Search(ctx, []float32{1, 0, 0}, 3, matchingFilter)
	if err != nil {
		t.Fatalf("search fixed vector scope: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("fixed vector filtered hit count = %d, want 2: %+v", len(hits), hits)
	}
	if hits[0].ID != "native-vector-exact" || hits[1].ID != "native-vector-close" {
		t.Fatalf("fixed vector nearest-first order = [%s %s], want [native-vector-exact native-vector-close]", hits[0].ID, hits[1].ID)
	}
	if hits[0].Score < hits[1].Score {
		t.Fatalf("fixed vector scores are not descending: %.9f, %.9f", hits[0].Score, hits[1].Score)
	}
	if hits[0].Filter.SessionID != matchingFilter.SessionID || hits[1].Filter.SessionID != matchingFilter.SessionID {
		t.Fatalf("fixed vector full-width session IDs changed: [%d %d], want %d", hits[0].Filter.SessionID, hits[1].Filter.SessionID, matchingFilter.SessionID)
	}
	limited, err := vector.Search(ctx, []float32{1, 0, 0}, 1, matchingFilter)
	if err != nil {
		t.Fatalf("search fixed vector top one: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "native-vector-exact" {
		t.Fatalf("fixed vector top one = %+v, want exact vector", limited)
	}
}

// TestNativeVectorRebuildLiveAndRestorableTrashRoundTrip verifies a real native rebuild, restart, and management restore with a deterministic offline embedding model.
// TestNativeVectorRebuildLiveAndRestorableTrashRoundTrip 验证真实原生重建、重启和管理恢复，并使用确定性的离线 embedding 模型。
func TestNativeVectorRebuildLiveAndRestorableTrashRoundTrip(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	ctx := context.Background()

	t.Run("rebuilds_live_and_eligible_trash", func(t *testing.T) {
		root := t.TempDir()
		promptLayout := testPackagedPromptLayout(root)
		oldCfg := testNativeConfig(root, nativeLibrary)
		oldCfg.Embedding.Provider = "fixture"
		oldCfg.Embedding.Model = "fixture-embedding-v1"

		storage, err := buildNativeStorageDependencies(oldCfg, promptLayout, true)
		if err != nil {
			t.Fatalf("build native storage for vector rebuild: %v", err)
		}
		deps := maintenanceDependenciesFromStorage(storage)
		closed := false
		defer func() {
			if !closed {
				_ = deps.Shutdown(ctx)
			}
		}()

		relational, ok := deps.Relational.(*sqliteadapter.Store)
		if !ok {
			t.Fatalf("native rebuild relational dependency type = %T", deps.Relational)
		}
		vector, ok := deps.Vector.(*lancedbadapter.Store)
		if !ok {
			t.Fatalf("native rebuild vector dependency type = %T", deps.Vector)
		}
		fixture := seedNativeVectorRebuildFixture(t, ctx, relational, vector)
		beforeCount, err := vector.Count(ctx)
		if err != nil {
			t.Fatalf("count native vectors before rebuild: %v", err)
		}
		if beforeCount != 2 {
			t.Fatalf("native vector count before rebuild = %d, want 2", beforeCount)
		}

		newActiveVector := []float32{0.91, 0.07, 0.02}
		newTrashVector := []float32{0.03, 0.94, 0.05}
		newCfg := oldCfg
		newCfg.Embedding.Model = "fixture-embedding-v2"
		deps.Embedding = &nativeDeterministicEmbeddingClient{
			vectors: map[string][]float32{
				fixture.ActiveRecord.Text: newActiveVector,
				fixture.TrashRecord.Text:  newTrashVector,
			},
		}
		report, err := RunVectorRebuild(ctx, newCfg, deps, nil)
		if err != nil {
			t.Fatalf("run native vector rebuild: %v", err)
		}
		if report.Mode != "native" || report.MemoryCount != 2 || report.DurableRowsUpdated != 2 || report.VectorRowsRebuilt != 2 {
			t.Fatalf("native vector rebuild report = %+v, want native/2/2/2", report)
		}
		markerPath := nativeVectorRebuildIncompleteMarkerPath(newCfg.SQLite.Native.Path)
		if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
			t.Fatalf("successful native rebuild marker stat error = %v, want absent", statErr)
		}
		identity, err := readNativeEmbeddingIdentity(nativeEmbeddingIdentityPath(newCfg.SQLite.Native.Path))
		if err != nil {
			t.Fatalf("read native embedding identity after rebuild: %v", err)
		}
		wantIdentity, err := nativeEmbeddingIdentityForConfig(newCfg)
		if err != nil {
			t.Fatalf("derive native embedding identity after rebuild: %v", err)
		}
		if identity != wantIdentity {
			t.Fatalf("native embedding identity after rebuild = %+v, want %+v", identity, wantIdentity)
		}

		activeRows, err := relational.LoadMemoryNodesByIDs(ctx, []uint64{fixture.ActiveMemoryID})
		if err != nil || len(activeRows) != 1 {
			t.Fatalf("load rebuilt active memory = %+v, %v", activeRows, err)
		}
		assertNativeRebuildVector(t, activeRows[0].Vector, newActiveVector)
		if activeRows[0].OriginSessionID != nativeVectorRebuildActiveSessionID {
			t.Fatalf("rebuilt active OriginSessionID = %d, want %d", activeRows[0].OriginSessionID, nativeVectorRebuildActiveSessionID)
		}

		var trashRows []sqliteadapter.NativeRestorableTrashMemory
		if err := relational.WalkNativeMigrationRestorableTrashRows(ctx, time.Now().UTC(), func(row sqliteadapter.NativeRestorableTrashMemory) error {
			trashRows = append(trashRows, row)
			return nil
		}); err != nil {
			t.Fatalf("walk rebuilt restorable trash: %v", err)
		}
		if len(trashRows) != 1 || trashRows[0].BatchID != fixture.ManagementBatchID || trashRows[0].MemoryID != fixture.TrashMemoryID {
			t.Fatalf("rebuilt restorable trash identities = %+v, want one fixture row", trashRows)
		}
		assertNativeRebuildVector(t, trashRows[0].Record.Vector, newTrashVector)
		if trashRows[0].Record.Filter.SessionID != nativeVectorRebuildTrashSessionID {
			t.Fatalf("rebuilt trash SessionID = %d, want %d", trashRows[0].Record.Filter.SessionID, nativeVectorRebuildTrashSessionID)
		}

		physicalCount, err := vector.Count(ctx)
		if err != nil || physicalCount != 2 {
			t.Fatalf("native vector count after rebuild = %d, %v, want 2", physicalCount, err)
		}
		for _, query := range []struct {
			name        string
			vector      []float32
			filter      logicdomain.SearchFilter
			wantID      string
			wantSession uint64
		}{
			{name: "live", vector: newActiveVector, filter: fixture.ActiveRecord.Filter, wantID: fixture.ActiveRecord.ID, wantSession: nativeVectorRebuildActiveSessionID},
			{name: "trash", vector: newTrashVector, filter: fixture.TrashRecord.Filter, wantID: fixture.TrashRecord.ID, wantSession: nativeVectorRebuildTrashSessionID},
		} {
			hits, searchErr := vector.Search(ctx, query.vector, 1, query.filter)
			if searchErr != nil || len(hits) != 1 || hits[0].ID != query.wantID {
				t.Fatalf("%s vector search = %+v, %v, want id %q", query.name, hits, searchErr, query.wantID)
			}
			if hits[0].Filter.SessionID != query.wantSession {
				t.Fatalf("%s vector search SessionID = %d, want %d", query.name, hits[0].Filter.SessionID, query.wantSession)
			}
		}

		databasePath := newCfg.SQLite.Native.Path
		if err := deps.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown native storage after rebuild: %v", err)
		}
		closed = true
		assertNativeRebuildSQLiteVector(t, databasePath, "vmm_memory_nodes", fixture.ActiveVectorID, 0, 0, newActiveVector)
		assertNativeRebuildSQLiteVector(t, databasePath, "vmm_memory_nodes_trash", fixture.TrashVectorID, fixture.ManagementBatchID, fixture.TrashMemoryID, newTrashVector)

		restarted, err := buildNativeStorageDependencies(newCfg, promptLayout, true)
		if err != nil {
			t.Fatalf("reopen native storage after vector rebuild: %v", err)
		}
		restartedClosed := false
		defer func() {
			if !restartedClosed {
				_ = restarted.Lifecycle.Shutdown(ctx)
			}
		}()
		if err := restarted.Health.CheckHealth(ctx); err != nil {
			t.Fatalf("health after native vector rebuild restart: %v", err)
		}
		restartedRelational, ok := restarted.Relational.(*sqliteadapter.Store)
		if !ok {
			t.Fatalf("restarted native relational dependency type = %T", restarted.Relational)
		}
		restartedVector, ok := restarted.Vector.(*lancedbadapter.Store)
		if !ok {
			t.Fatalf("restarted native vector dependency type = %T", restarted.Vector)
		}
		restartedCount, err := restartedVector.Count(ctx)
		if err != nil || restartedCount != 2 {
			t.Fatalf("restarted native vector count = %d, %v, want 2", restartedCount, err)
		}
		restoreOperation, err := restartedRelational.RestoreManagementBatch(ctx, logicdomain.ManagementRestoreCommand{
			OperationID:    "native-vector-rebuild-restore-operation",
			IdempotencyKey: "native-vector-rebuild-restore-idempotency",
			BatchID:        fixture.ManagementBatchID,
			Now:            time.Now().UTC(),
		})
		if err != nil || restoreOperation.Status != "succeeded" {
			t.Fatalf("restore rebuilt management batch = %+v, %v", restoreOperation, err)
		}
		restoredRows, err := restartedRelational.LoadMemoryNodesByIDs(ctx, []uint64{fixture.TrashMemoryID})
		if err != nil || len(restoredRows) != 1 {
			t.Fatalf("load restored rebuilt memory = %+v, %v", restoredRows, err)
		}
		assertNativeRebuildVector(t, restoredRows[0].Vector, newTrashVector)
		if restoredRows[0].OriginSessionID != nativeVectorRebuildTrashSessionID {
			t.Fatalf("restored memory OriginSessionID = %d, want %d", restoredRows[0].OriginSessionID, nativeVectorRebuildTrashSessionID)
		}
		restoredHits, err := restartedVector.Search(ctx, newTrashVector, 1, fixture.TrashRecord.Filter)
		if err != nil || len(restoredHits) != 1 || restoredHits[0].ID != fixture.TrashVectorID || restoredHits[0].Filter.SessionID != nativeVectorRebuildTrashSessionID {
			t.Fatalf("restored rebuilt vector search = %+v, %v, want %q and exact large SessionID", restoredHits, err, fixture.TrashVectorID)
		}
		restoredCount, err := restartedVector.Count(ctx)
		if err != nil || restoredCount != 2 {
			t.Fatalf("native vector count after restore = %d, %v, want 2", restoredCount, err)
		}
		if err := restarted.Lifecycle.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown restarted native storage: %v", err)
		}
		restartedClosed = true
	})

	t.Run("retains_marker_on_embedding_failure", func(t *testing.T) {
		root := t.TempDir()
		promptLayout := testPackagedPromptLayout(root)
		cfg := testNativeConfig(root, nativeLibrary)
		cfg.Embedding.Provider = "fixture"
		cfg.Embedding.Model = "fixture-embedding-failure-v1"
		storage, err := buildNativeStorageDependencies(cfg, promptLayout, true)
		if err != nil {
			t.Fatalf("build native storage for failed vector rebuild: %v", err)
		}
		deps := maintenanceDependenciesFromStorage(storage)
		closed := false
		defer func() {
			if !closed {
				_ = deps.Shutdown(ctx)
			}
		}()
		relational, ok := deps.Relational.(*sqliteadapter.Store)
		if !ok {
			t.Fatalf("failed rebuild relational dependency type = %T", deps.Relational)
		}
		vector, ok := deps.Vector.(*lancedbadapter.Store)
		if !ok {
			t.Fatalf("failed rebuild vector dependency type = %T", deps.Vector)
		}
		seedNativeVectorRebuildFixture(t, ctx, relational, vector)
		failedCfg := cfg
		failedCfg.Embedding.Model = "fixture-embedding-failure-v2"
		deps.Embedding = &nativeDeterministicEmbeddingClient{err: errors.New("deterministic embedding failure")}
		if _, err := RunVectorRebuild(ctx, failedCfg, deps, nil); err == nil {
			t.Fatal("expected native vector rebuild embedding failure")
		}
		markerPath := nativeVectorRebuildIncompleteMarkerPath(failedCfg.SQLite.Native.Path)
		if _, statErr := os.Stat(markerPath); statErr != nil {
			t.Fatalf("failed native vector rebuild marker stat error = %v, want retained marker", statErr)
		}
		if err := deps.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown failed native rebuild dependencies: %v", err)
		}
		closed = true
		if _, err := buildNativeStorageDependencies(failedCfg, promptLayout, true); err == nil || !strings.Contains(err.Error(), "native vector rebuild is incomplete") {
			t.Fatalf("ordinary native startup after failed vector rebuild = %v, want marker refusal", err)
		}
	})
}

// Native vector rebuild fixtures use values above JavaScript's exact integer range to prove JSON/FFI transport remains lossless.
// 原生向量重建 fixture 使用超过 JavaScript 精确整数范围的数值，验证 JSON/FFI 传输保持无损。
const (
	nativeVectorRebuildActiveSessionID = uint64(9_007_199_254_740_993)
	nativeVectorRebuildTrashSessionID  = uint64(9_007_199_254_740_995)
)

// nativeVectorRebuildFixture records the live row, restorable trash row, and management batch used by the native rebuild test.
// nativeVectorRebuildFixture 保存原生重建测试使用的 active 行、可恢复 trash 行及管理批次。
type nativeVectorRebuildFixture struct {
	ActiveMemoryID    uint64
	ActiveVectorID    string
	ActiveRecord      logicdomain.MemoryRecord
	TrashMemoryID     uint64
	TrashVectorID     string
	TrashRecord       logicdomain.MemoryRecord
	ManagementBatchID uint64
}

// nativeDeterministicEmbeddingClient provides local text-to-vector results and can deliberately fail before destructive rebuild work starts.
// nativeDeterministicEmbeddingClient 提供本地确定性的文本到向量结果，也可以在破坏性重建开始前故意失败。
type nativeDeterministicEmbeddingClient struct {
	vectors map[string][]float32
	err     error
}

// Embed returns one configured vector per input text without contacting a network provider.
// Embed 为每条输入文本返回预配置向量，不访问任何网络 provider。
func (c *nativeDeterministicEmbeddingClient) Embed(ctx context.Context, request appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	if err := ctx.Err(); err != nil {
		return appports.EmbeddingResponse{}, err
	}
	if c.err != nil {
		return appports.EmbeddingResponse{}, c.err
	}
	response := appports.EmbeddingResponse{Vectors: make([][]float32, len(request.Texts))}
	for index, text := range request.Texts {
		vector, ok := c.vectors[text]
		if !ok {
			return appports.EmbeddingResponse{}, fmt.Errorf("deterministic embedding fixture has no vector for text %q", text)
		}
		response.Vectors[index] = append([]float32(nil), vector...)
	}
	return response, nil
}

// seedNativeVectorRebuildFixture creates real active and manual-recycle rows, then seeds their old vectors into native LanceDB.
// seedNativeVectorRebuildFixture 创建真实 active 与人工回收行，再把旧向量写入原生 LanceDB。
func seedNativeVectorRebuildFixture(t *testing.T, ctx context.Context, relational *sqliteadapter.Store, vector *lancedbadapter.Store) nativeVectorRebuildFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	expiresAt := now.Add(48 * time.Hour)
	activeSession, err := relational.ResolveRequestScope(ctx, "native-vector-rebuild-active", 1, 1)
	if err != nil {
		t.Fatalf("resolve native rebuild active session: %v", err)
	}
	activeTurn, err := relational.AppendTurnRecord(ctx, activeSession, logicdomain.TurnRecord{
		UserContent:      "原生向量重建活动记忆",
		AssistantContent: "验证大整数会话标识与新向量",
		CreatedAt:        now,
	})
	if err != nil {
		t.Fatalf("append native rebuild active turn: %v", err)
	}
	activeMemory, err := relational.CreateDirectMemoryNode(ctx, activeSession, logicdomain.MemoryNodeRecord{
		OriginSessionID: nativeVectorRebuildActiveSessionID,
		SourceTurnID:    activeTurn.ID,
		VectorID:        "native-rebuild-live-vector",
		Vector:          []float32{0.1, 0.2, 0.3},
		SourceKind:      logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel:      logicdomain.MemoryScopeLevelSession,
		Category:        logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:        "原生重建活动中文记忆",
		Details:         "活动向量在模型切换后必须保留精确会话标识",
		Status:          logicdomain.MemoryStatusActive,
		Priority:        logicdomain.MemoryPriorityP2,
		MemoryLevel:     logicdomain.MemoryLevelSession,
		ExpiresAt:       expiresAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatalf("create native rebuild active memory: %v", err)
	}

	trashSession, err := relational.ResolveRequestScope(ctx, "native-vector-rebuild-trash", 1, 1)
	if err != nil {
		t.Fatalf("resolve native rebuild trash session: %v", err)
	}
	trashTurn, err := relational.AppendTurnRecord(ctx, trashSession, logicdomain.TurnRecord{
		UserContent:      "原生向量重建回收记忆",
		AssistantContent: "验证恢复后仍能按大整数会话召回",
		CreatedAt:        now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("append native rebuild trash turn: %v", err)
	}
	if _, err := relational.ApplyTurnAnalysis(ctx, trashSession, trashTurn, logicdomain.TurnAnalysis{Details: "native rebuild fixture analysis", DetailsBudget: 1}); err != nil {
		t.Fatalf("complete native rebuild trash turn: %v", err)
	}
	trashMemory, err := relational.CreateDirectMemoryNode(ctx, trashSession, logicdomain.MemoryNodeRecord{
		OriginSessionID: nativeVectorRebuildTrashSessionID,
		SourceTurnID:    trashTurn.ID,
		VectorID:        "native-rebuild-trash-vector",
		Vector:          []float32{0.4, 0.5, 0.6},
		SourceKind:      logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel:      logicdomain.MemoryScopeLevelSession,
		Category:        logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:        "原生重建回收中文记忆",
		Details:         "可恢复回收行的向量只更新关系载荷并保持 LanceDB 行",
		Status:          logicdomain.MemoryStatusActive,
		Priority:        logicdomain.MemoryPriorityP2,
		MemoryLevel:     logicdomain.MemoryLevelSession,
		ExpiresAt:       expiresAt,
		CreatedAt:       now.Add(time.Second),
		UpdatedAt:       now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("create native rebuild trash memory: %v", err)
	}
	selection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession,
		TargetIDs:  []uint64{trashSession.SessionID},
		Action:     logicdomain.ManagementActionRecycle,
		Source:     "native-vector-rebuild-integration-test",
	}
	impact, err := relational.InspectManagementSelection(ctx, selection, 0)
	if err != nil {
		t.Fatalf("inspect native rebuild recycle selection: %v", err)
	}
	preview := logicdomain.ManagementPreview{
		Token:     "native-vector-rebuild-preview",
		Selection: selection,
		Impact:    impact,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := relational.SaveManagementPreview(ctx, preview); err != nil {
		t.Fatalf("save native rebuild recycle preview: %v", err)
	}
	operation, err := relational.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID:    "native-vector-rebuild-recycle-operation",
		IdempotencyKey: "native-vector-rebuild-recycle-idempotency",
		Preview:        preview,
		Now:            now,
		ExpiresAt:      now.Add(time.Hour),
	})
	if err != nil || operation.Status != "succeeded" || operation.BatchID == 0 {
		t.Fatalf("apply native rebuild recycle = %+v, %v", operation, err)
	}
	var activeRecord logicdomain.MemoryRecord
	activeRecords, err := relational.ListProjectMemoriesForMaintenance(ctx, activeMemory.ProjectID)
	if err != nil {
		t.Fatalf("list native rebuild active memories: %v", err)
	}
	for _, record := range activeRecords {
		if record.ID == activeMemory.VectorID {
			activeRecord = record
			break
		}
	}
	if activeRecord.ID == "" {
		t.Fatalf("native rebuild active vector %q was not listed", activeMemory.VectorID)
	}
	var trashRecord logicdomain.MemoryRecord
	if err := relational.WalkNativeMigrationRestorableTrashRows(ctx, now, func(row sqliteadapter.NativeRestorableTrashMemory) error {
		if row.BatchID == operation.BatchID && row.MemoryID == trashMemory.ID {
			trashRecord = row.Record
		}
		return nil
	}); err != nil {
		t.Fatalf("walk native rebuild fixture trash: %v", err)
	}
	if trashRecord.ID == "" {
		t.Fatalf("native rebuild trash vector %q was not listed", trashMemory.VectorID)
	}
	for _, record := range []logicdomain.MemoryRecord{activeRecord, trashRecord} {
		if err := vector.Upsert(ctx, record); err != nil {
			t.Fatalf("seed native rebuild vector %q: %v", record.ID, err)
		}
	}
	return nativeVectorRebuildFixture{
		ActiveMemoryID:    activeMemory.ID,
		ActiveVectorID:    activeMemory.VectorID,
		ActiveRecord:      activeRecord,
		TrashMemoryID:     trashMemory.ID,
		TrashVectorID:     trashMemory.VectorID,
		TrashRecord:       trashRecord,
		ManagementBatchID: operation.BatchID,
	}
}

// assertNativeRebuildVector compares a durable vector payload without introducing a tolerance that could hide model-output drift.
// assertNativeRebuildVector 比较持久化向量载荷，不引入可能掩盖模型输出漂移的误差容忍度。
func assertNativeRebuildVector(t *testing.T, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("native rebuild vector dimension = %d, want %d: got %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("native rebuild vector[%d] = %v, want %v: got %v", index, got[index], want[index], got)
		}
	}
}

// assertNativeRebuildSQLiteVector reads the committed durable vector JSON from the isolated native SQLite file.
// assertNativeRebuildSQLiteVector 从隔离的原生 SQLite 文件读取已提交的 durable vector JSON。
func assertNativeRebuildSQLiteVector(t *testing.T, databasePath, table, vectorID string, batchID, memoryID uint64, want []float32) {
	t.Helper()
	db, err := sql.Open("sqlite", readOnlySQLiteTestDSN(databasePath))
	if err != nil {
		t.Fatalf("open native rebuild SQLite for %s: %v", table, err)
	}
	defer db.Close()
	var vectorJSON string
	var query string
	var args []any
	switch table {
	case "vmm_memory_nodes":
		query = `SELECT vector_json FROM vmm_memory_nodes WHERE vector_id = ?`
		args = []any{vectorID}
	case "vmm_memory_nodes_trash":
		query = `SELECT vector_json FROM vmm_memory_nodes_trash WHERE vector_id = ? AND batch_id = ? AND id = ?`
		args = []any{vectorID, batchID, memoryID}
	default:
		t.Fatalf("unsupported native rebuild assertion table %q", table)
	}
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&vectorJSON); err != nil {
		t.Fatalf("read native rebuild %s vector JSON: %v", table, err)
	}
	var vector []float32
	if err := json.Unmarshal([]byte(vectorJSON), &vector); err != nil {
		t.Fatalf("decode native rebuild %s vector JSON %q: %v", table, vectorJSON, err)
	}
	assertNativeRebuildVector(t, vector, want)
}

// TestNativeMigrationSplitToNativePreservesFactsAndRecall verifies a real legacy SQLite fixture, full relational migration, FTS rebuild, vector replay, and restart.
// TestNativeMigrationSplitToNativePreservesFactsAndRecall 验证真实旧 SQLite 夹具、完整关系迁移、FTS 重建、向量回放和重启后的查询。
func TestNativeMigrationSplitToNativePreservesFactsAndRecall(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	legacyLibrary := locateLegacySQLiteLibrary(t)
	root, child := nativeMigrationProcessRoot(t)
	if !child {
		return
	}
	promptLayout := testPackagedPromptLayout(root)
	legacyLibraryPath := filepath.Join(root, "output", "libs", legacySQLiteLibraryFileName())
	copyTestArtifact(t, legacyLibrary, legacyLibraryPath)
	sourceDatabase := filepath.Join(root, "output", "database", "sqlite.db")
	if err := os.MkdirAll(filepath.Dir(sourceDatabase), 0o700); err != nil {
		t.Fatalf("create legacy database directory: %v", err)
	}

	cfg := config.DefaultBase()
	cfg.Storage.Mode = "split"
	cfg.SQLite.Timeout.Duration = 5 * time.Second
	cfg.LanceDB.Timeout.Duration = 5 * time.Second
	cfg.LanceDB.TableName = "migration_vectors"
	cfg.LanceDB.VectorColumn = "vector"
	cfg.Embedding.Dimension = 3
	cfg.LanceDB.Native.LibraryPath = nativeLibrary

	ctx := context.Background()
	legacy, err := sqliteadapter.NewStore(legacyLibraryPath, sourceDatabase, cfg.SQLite.Timeout.Duration, sqliteadapter.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		t.Fatalf("open legacy fixture store: %v", err)
	}
	seed := seedNativeMigrationLegacyFixture(t, ctx, legacy)
	sourceRows, err := legacy.LoadMemoryNodesByIDs(ctx, []uint64{seed.MemoryID})
	if err != nil {
		t.Fatalf("load legacy fixture memory by exact ID: %v", err)
	}
	if len(sourceRows) != 1 || sourceRows[0].ID != seed.MemoryID || sourceRows[0].VectorID != seed.VectorID || len(sourceRows[0].Vector) != 3 {
		t.Fatalf("legacy memory identity/vector = %+v", sourceRows)
	}
	sourceLexicalHits, err := legacy.SearchLexicalMemory(ctx, "原生 SQLite 中文 迁移", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		t.Fatalf("legacy Chinese FTS recall: %v", err)
	}
	if !containsNativeMemoryID(sourceLexicalHits, seed.MemoryID) {
		t.Fatalf("legacy Chinese FTS did not recall memory %d: %+v", seed.MemoryID, sourceLexicalHits)
	}
	if err := legacy.Shutdown(ctx); err != nil {
		t.Fatalf("close legacy fixture store: %v", err)
	}
	assertNativeMigrationTrashVector(t, sourceDatabase, seed)

	sourceDigest, sourceCounts, err := hashNativeMigrationSQLiteFacts(sourceDatabase)
	if err != nil {
		t.Fatalf("hash legacy fixture facts: %v", err)
	}

	nativeOutput := filepath.Join(root, "native-output")
	if err := MigrateLegacyStorageToNative(ctx, cfg, promptLayout, nativeOutput); err != nil {
		t.Fatalf("migrate split storage to native: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nativeOutput, "sqlite.db.migration-incomplete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration quarantine marker remains after success: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nativeOutput, "source-snapshot.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration snapshot remains after success: %v", err)
	}

	nativeCfg := testNativeConfig(nativeOutput, nativeLibrary)
	deps, err := buildNativeStorageDependencies(nativeCfg, promptLayout, true)
	if err != nil {
		t.Fatalf("open migrated native storage through composition root: %v", err)
	}
	t.Cleanup(func() { _ = deps.Lifecycle.Shutdown(context.Background()) })
	relational, ok := deps.Relational.(*sqliteadapter.Store)
	if !ok {
		t.Fatalf("native relational dependency type = %T", deps.Relational)
	}
	vector, ok := deps.Vector.(*lancedbadapter.Store)
	if !ok {
		t.Fatalf("native vector dependency type = %T", deps.Vector)
	}
	if err := deps.Health.CheckHealth(ctx); err != nil {
		t.Fatalf("migrated native storage health check: %v", err)
	}
	management, err := relational.GetManagementSession(ctx, seed.ManagementSessionID)
	if err != nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("load migrated management session state: %v", err)
	}
	if management.Status != string(logicdomain.SessionMemoryStatusRecycled) {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("migrated management session status = %q, want recycled", management.Status)
	}

	rows, err := relational.LoadMemoryNodesByIDs(ctx, []uint64{seed.MemoryID})
	if err != nil {
		t.Fatalf("load migrated memory by exact ID: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != seed.MemoryID || rows[0].VectorID != seed.VectorID || len(rows[0].Vector) != 3 {
		t.Fatalf("migrated memory identity/vector changed: %+v", rows)
	}
	lexicalHits, err := relational.SearchLexicalMemory(ctx, "原生 SQLite 中文 迁移", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		t.Fatalf("native Chinese FTS recall: %v", err)
	}
	if !containsNativeMemoryID(lexicalHits, seed.MemoryID) {
		t.Fatalf("native Chinese FTS did not recall memory %d: %+v", seed.MemoryID, lexicalHits)
	}
	count, err := vector.Count(ctx)
	if err != nil {
		t.Fatalf("count migrated vectors: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated vector count = %d, want active plus restorable trash vector", count)
	}
	vectorHits, err := vector.Search(ctx, []float32{0.1, 0.2, 0.3}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		t.Fatalf("native vector recall: %v", err)
	}
	if len(vectorHits) != 1 || vectorHits[0].ID != seed.VectorID {
		t.Fatalf("native vector identity changed: %+v", vectorHits)
	}
	trashVectorHits, err := vector.Search(ctx, []float32{0.4, 0.5, 0.6}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || len(trashVectorHits) != 1 || trashVectorHits[0].ID != seed.RestorableVectorID {
		t.Fatalf("migrated restorable trash vector search = %+v, %v", trashVectorHits, err)
	}
	if err := deps.Lifecycle.Shutdown(ctx); err != nil {
		t.Fatalf("close migrated native storage before restart: %v", err)
	}
	assertNativeMigrationDurableQueues(t, filepath.Join(nativeOutput, "sqlite.db"), seed)
	targetDigest, targetCounts, err := hashNativeMigrationSQLiteFacts(filepath.Join(nativeOutput, "sqlite.db"))
	if err != nil {
		t.Fatalf("hash migrated native facts: %v", err)
	}
	if targetDigest != sourceDigest {
		t.Fatalf("migrated relational digest changed: source=%s target=%s", sourceDigest, targetDigest)
	}
	if len(targetCounts) != len(sourceCounts) {
		t.Fatalf("migrated table count map changed: source=%d target=%d", len(sourceCounts), len(targetCounts))
	}
	for table, want := range sourceCounts {
		if got := targetCounts[table]; got != want {
			t.Fatalf("migrated table %s row count = %d, want %d", table, got, want)
		}
	}

	restarted, err := buildNativeStorageDependencies(nativeCfg, promptLayout, true)
	if err != nil {
		t.Fatalf("restart migrated native storage: %v", err)
	}
	restartedRelational := restarted.Relational.(*sqliteadapter.Store)
	restartedVector := restarted.Vector.(*lancedbadapter.Store)
	if err := restarted.Health.CheckHealth(ctx); err != nil {
		t.Fatalf("restarted native health check: %v", err)
	}
	if rows, err := restartedRelational.LoadMemoryNodesByIDs(ctx, []uint64{seed.MemoryID}); err != nil || len(rows) != 1 {
		t.Fatalf("restarted exact memory lookup = %+v, %v", rows, err)
	}
	if count, err := restartedVector.Count(ctx); err != nil || count != 2 {
		t.Fatalf("restarted vector count = %d, %v", count, err)
	}
	restoreOperation, err := restartedRelational.RestoreManagementBatch(ctx, logicdomain.ManagementRestoreCommand{
		OperationID:    "native-migration-restore-operation",
		IdempotencyKey: "native-migration-restore-idempotency",
		BatchID:        seed.ManagementBatchID,
		Now:            time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC),
	})
	if err != nil || restoreOperation.Status != "succeeded" {
		t.Fatalf("restore migrated management batch = %+v, %v", restoreOperation, err)
	}
	restoredRows, err := restartedRelational.LoadMemoryNodesByIDs(ctx, []uint64{seed.RestorableMemoryID})
	if err != nil || len(restoredRows) != 1 || restoredRows[0].ID != seed.RestorableMemoryID || restoredRows[0].VectorID != seed.RestorableVectorID || len(restoredRows[0].Vector) != 3 {
		t.Fatalf("restored migrated trash memory = %+v, %v", restoredRows, err)
	}
	restoredVectorHits, err := restartedVector.Search(ctx, []float32{0.4, 0.5, 0.6}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || len(restoredVectorHits) != 1 || restoredVectorHits[0].ID != seed.RestorableVectorID {
		t.Fatalf("restored migrated trash vector search = %+v, %v", restoredVectorHits, err)
	}
	restoredBatch, err := restartedRelational.GetManagementRecycleBatch(ctx, seed.ManagementBatchID)
	if err != nil || restoredBatch.State != "restored" {
		t.Fatalf("restored migrated recycle batch = %+v, %v", restoredBatch, err)
	}
	if err := restarted.Lifecycle.Shutdown(ctx); err != nil {
		t.Fatalf("close restarted native storage: %v", err)
	}

	reportBytes, err := os.ReadFile(filepath.Join(nativeOutput, "migration-report.json"))
	if err != nil {
		t.Fatalf("read migration report: %v", err)
	}
	var report struct {
		SourceMode      string          `json:"source_mode"`
		Vectors         int64           `json:"vectors"`
		RestorableTrash int64           `json:"restorable_trash_rows"`
		Duplicates      int64           `json:"duplicate_vector_rows"`
		SQLite          json.RawMessage `json:"sqlite"`
	}
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatalf("decode migration report: %v", err)
	}
	if report.SourceMode != "split" || report.Vectors != 2 || report.RestorableTrash != 1 || report.Duplicates != 0 {
		t.Fatalf("migration report summary = %+v", report)
	}
	var sqliteReport struct {
		SourceSchemaVersion int `json:"source_schema_version"`
		Tables              []struct {
			Name string `json:"name"`
			Rows int64  `json:"rows"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(report.SQLite, &sqliteReport); err != nil {
		t.Fatalf("decode SQLite migration report: %v", err)
	}
	if sqliteReport.SourceSchemaVersion != 22 || len(sqliteReport.Tables) != len(sourceCounts)+1 {
		t.Fatalf("SQLite migration report = %+v", sqliteReport)
	}
	for _, table := range sqliteReport.Tables {
		if table.Name == "vmm_schema_versions" {
			if table.Rows < 1 {
				t.Fatalf("SQLite report schema-version rows = %d, want at least one", table.Rows)
			}
			continue
		}
		want, ok := sourceCounts[table.Name]
		if !ok || table.Rows != want {
			t.Fatalf("SQLite report table %s rows = %d, want %d", table.Name, table.Rows, want)
		}
	}
}

// TestNativeMigrationMissingActiveVectorQuarantinesTarget verifies a real migration failure keeps its quarantine marker and leaves the source unchanged.
// TestNativeMigrationMissingActiveVectorQuarantinesTarget 验证真实迁移遇到 active 缺失向量时保留隔离标记且源库不变。
func TestNativeMigrationMissingActiveVectorQuarantinesTarget(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	legacyLibrary := locateLegacySQLiteLibrary(t)
	root, child := nativeMigrationProcessRoot(t)
	if !child {
		return
	}
	promptLayout := testPackagedPromptLayout(root)
	legacyLibraryPath := filepath.Join(root, "output", "libs", legacySQLiteLibraryFileName())
	copyTestArtifact(t, legacyLibrary, legacyLibraryPath)
	sourceDatabase := filepath.Join(root, "output", "database", "sqlite.db")
	if err := os.MkdirAll(filepath.Dir(sourceDatabase), 0o700); err != nil {
		t.Fatalf("create missing-vector source directory: %v", err)
	}
	cfg := config.DefaultBase()
	cfg.Storage.Mode = "split"
	cfg.SQLite.Timeout.Duration = 5 * time.Second
	cfg.LanceDB.Timeout.Duration = 5 * time.Second
	cfg.LanceDB.TableName = "migration_vectors"
	cfg.LanceDB.VectorColumn = "vector"
	cfg.Embedding.Dimension = 3
	cfg.LanceDB.Native.LibraryPath = nativeLibrary
	ctx := context.Background()
	legacy, err := sqliteadapter.NewStore(legacyLibraryPath, sourceDatabase, cfg.SQLite.Timeout.Duration, sqliteadapter.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		t.Fatalf("open missing-vector legacy fixture: %v", err)
	}
	session, err := legacy.ResolveRequestScope(ctx, "native-migration-missing-vector", 1, 1)
	if err != nil {
		_ = legacy.Shutdown(context.Background())
		t.Fatalf("resolve missing-vector session: %v", err)
	}
	turn, err := legacy.AppendTurnRecord(ctx, session, logicdomain.TurnRecord{UserContent: "缺失 active 向量", AssistantContent: "迁移必须拒绝", CreatedAt: time.Date(2026, 9, 20, 4, 0, 0, 0, time.UTC)})
	if err != nil {
		_ = legacy.Shutdown(context.Background())
		t.Fatalf("append missing-vector turn: %v", err)
	}
	if _, err := legacy.CreateDirectMemoryNode(ctx, session, logicdomain.MemoryNodeRecord{
		SourceTurnID: turn.ID,
		VectorID:     "missing-active-vector",
		Vector:       nil,
		SourceKind:   logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:   logicdomain.MemoryScopeLevelProject,
		Category:     logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:     "缺失 active 向量迁移标记",
		Details:      "the active row has no durable embedding",
		Status:       logicdomain.MemoryStatusActive,
		Priority:     logicdomain.MemoryPriorityP2,
		MemoryLevel:  logicdomain.MemoryLevelStable,
		CreatedAt:    time.Date(2026, 9, 20, 4, 1, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 9, 20, 4, 1, 0, 0, time.UTC),
	}); err != nil {
		_ = legacy.Shutdown(context.Background())
		t.Fatalf("seed missing-vector memory: %v", err)
	}
	if err := legacy.Shutdown(ctx); err != nil {
		t.Fatalf("close missing-vector legacy fixture: %v", err)
	}
	sourceDigest, sourceCounts, err := hashNativeMigrationSQLiteFacts(sourceDatabase)
	if err != nil {
		t.Fatalf("hash missing-vector source facts: %v", err)
	}
	nativeOutput := filepath.Join(root, "native-output")
	err = MigrateLegacyStorageToNative(ctx, cfg, promptLayout, nativeOutput)
	if err == nil {
		t.Fatalf("missing active vector migration unexpectedly succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(nativeOutput, "sqlite.db.migration-incomplete")); statErr != nil {
		t.Fatalf("missing-vector migration quarantine marker = %v, want retained marker", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(nativeOutput, "source-snapshot.db")); statErr != nil {
		t.Fatalf("missing-vector migration snapshot = %v, want retained snapshot", statErr)
	}
	afterDigest, afterCounts, err := hashNativeMigrationSQLiteFacts(sourceDatabase)
	if err != nil {
		t.Fatalf("hash source after rejected migration: %v", err)
	}
	if afterDigest != sourceDigest {
		t.Fatalf("rejected migration changed source digest: before=%s after=%s", sourceDigest, afterDigest)
	}
	for table, want := range sourceCounts {
		if got := afterCounts[table]; got != want {
			t.Fatalf("rejected migration changed source table %s rows: got %d want %d", table, got, want)
		}
	}
	nativeCfg := testNativeConfig(nativeOutput, nativeLibrary)
	if deps, openErr := buildNativeStorageDependencies(nativeCfg, promptLayout, true); openErr == nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatal("ordinary native startup accepted an incomplete migration target")
	}
}

// TestNativeMigrationControllerToNativePreservesFactsAndRecall verifies offline migration through an isolated local controller process.
// TestNativeMigrationControllerToNativePreservesFactsAndRecall 验证通过隔离本地 controller 进程执行离线迁移。
func TestNativeMigrationControllerToNativePreservesFactsAndRecall(t *testing.T) {
	nativeLibrary := locateNativeLanceLibrary(t)
	controllerBinary := locateNativeMigrationControllerBinary(t)
	root := t.TempDir()
	promptLayout := testPackagedPromptLayout(root)
	endpoint := reserveNativeMigrationControllerEndpoint(t)
	cfg := testControllerMigrationConfig(root, endpoint, controllerBinary, nativeLibrary)
	ctx := context.Background()

	controllerRuntime, err := controlleradapter.New(ctx, testControllerRuntimeConfig(endpoint, controllerBinary, filepath.Join(root, "output", "database"), false))
	if err != nil {
		t.Fatalf("start isolated controller fixture runtime: %v", err)
	}
	t.Cleanup(func() { _ = controllerRuntime.Shutdown(context.Background()) })
	legacy, err := sqliteadapter.NewControllerStore(controllerRuntime, cfg.SQLite.Timeout.Duration, sqliteadapter.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("open controller fixture store: %v", err)
	}
	t.Cleanup(func() { _ = legacy.Shutdown(context.Background()) })
	seed := seedNativeMigrationLegacyFixture(t, ctx, legacy)
	sourceRows, err := legacy.LoadMemoryNodesByIDs(ctx, []uint64{seed.MemoryID})
	if err != nil {
		_ = legacy.Shutdown(context.Background())
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("load controller fixture memory: %v", err)
	}
	if len(sourceRows) != 1 || sourceRows[0].ID != seed.MemoryID || sourceRows[0].VectorID != seed.VectorID || len(sourceRows[0].Vector) != 3 {
		_ = legacy.Shutdown(context.Background())
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("controller fixture memory identity/vector = %+v", sourceRows)
	}
	sourceLexicalHits, err := legacy.SearchLexicalMemory(ctx, "原生 SQLite 中文 迁移", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil {
		_ = legacy.Shutdown(context.Background())
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("controller fixture Chinese FTS recall: %v", err)
	}
	if !containsNativeMemoryID(sourceLexicalHits, seed.MemoryID) {
		_ = legacy.Shutdown(context.Background())
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("controller fixture Chinese FTS did not recall memory %d: %+v", seed.MemoryID, sourceLexicalHits)
	}
	if err := legacy.Shutdown(ctx); err != nil {
		_ = controllerRuntime.Shutdown(context.Background())
		t.Fatalf("close controller fixture store: %v", err)
	}
	if err := controllerRuntime.Shutdown(ctx); err != nil {
		t.Fatalf("close controller fixture runtime: %v", err)
	}

	sourceDatabase := filepath.Join(root, "output", "database", "sqlite.db")
	assertNativeMigrationTrashVector(t, sourceDatabase, seed)
	sourceDigest, sourceCounts, err := hashNativeMigrationSQLiteFacts(sourceDatabase)
	if err != nil {
		t.Fatalf("hash controller fixture facts: %v", err)
	}
	nativeOutput := filepath.Join(root, "native-output")
	if err := MigrateLegacyStorageToNative(ctx, cfg, promptLayout, nativeOutput); err != nil {
		t.Fatalf("migrate controller storage to native: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nativeOutput, "sqlite.db.migration-incomplete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controller migration quarantine marker remains: %v", err)
	}

	nativeCfg := testNativeConfig(nativeOutput, nativeLibrary)
	deps, err := buildNativeStorageDependencies(nativeCfg, promptLayout, true)
	if err != nil {
		t.Fatalf("open controller-migrated native storage: %v", err)
	}
	t.Cleanup(func() { _ = deps.Lifecycle.Shutdown(context.Background()) })
	relational, ok := deps.Relational.(*sqliteadapter.Store)
	if !ok {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated relational dependency type = %T", deps.Relational)
	}
	vector, ok := deps.Vector.(*lancedbadapter.Store)
	if !ok {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated vector dependency type = %T", deps.Vector)
	}
	if err := deps.Health.CheckHealth(ctx); err != nil {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated native health check: %v", err)
	}
	rows, err := relational.LoadMemoryNodesByIDs(ctx, []uint64{seed.MemoryID})
	if err != nil || len(rows) != 1 || rows[0].ID != seed.MemoryID || rows[0].VectorID != seed.VectorID || len(rows[0].Vector) != 3 {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated exact memory = %+v, %v", rows, err)
	}
	management, err := relational.GetManagementSession(ctx, seed.ManagementSessionID)
	if err != nil || management.Status != string(logicdomain.SessionMemoryStatusRecycled) {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated management state = %+v, %v", management, err)
	}
	hits, err := relational.SearchLexicalMemory(ctx, "原生 SQLite 中文 迁移", 8, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || !containsNativeMemoryID(hits, seed.MemoryID) {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated Chinese FTS = %+v, %v", hits, err)
	}
	count, err := vector.Count(ctx)
	if err != nil || count != 2 {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated vector count = %d, want active plus restorable trash vector; %v", count, err)
	}
	vectorHits, err := vector.Search(ctx, []float32{0.1, 0.2, 0.3}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || len(vectorHits) != 1 || vectorHits[0].ID != seed.VectorID {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated vector search = %+v, %v", vectorHits, err)
	}
	trashVectorHits, err := vector.Search(ctx, []float32{0.4, 0.5, 0.6}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || len(trashVectorHits) != 1 || trashVectorHits[0].ID != seed.RestorableVectorID {
		_ = deps.Lifecycle.Shutdown(context.Background())
		t.Fatalf("controller-migrated restorable trash vector search = %+v, %v", trashVectorHits, err)
	}
	if err := deps.Lifecycle.Shutdown(ctx); err != nil {
		t.Fatalf("close controller-migrated native storage: %v", err)
	}
	assertNativeMigrationDurableQueues(t, filepath.Join(nativeOutput, "sqlite.db"), seed)
	targetDigest, targetCounts, err := hashNativeMigrationSQLiteFacts(filepath.Join(nativeOutput, "sqlite.db"))
	if err != nil {
		t.Fatalf("hash controller-migrated native facts: %v", err)
	}
	if targetDigest != sourceDigest {
		t.Fatalf("controller migration relational digest changed: source=%s target=%s", sourceDigest, targetDigest)
	}
	for table, want := range sourceCounts {
		if got := targetCounts[table]; got != want {
			t.Fatalf("controller migration table %s row count = %d, want %d", table, got, want)
		}
	}
	reportBytes, err := os.ReadFile(filepath.Join(nativeOutput, "migration-report.json"))
	if err != nil {
		t.Fatalf("read controller migration report: %v", err)
	}
	var report struct {
		SourceMode      string `json:"source_mode"`
		Vectors         int64  `json:"vectors"`
		RestorableTrash int64  `json:"restorable_trash_rows"`
		Duplicates      int64  `json:"duplicate_vector_rows"`
	}
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatalf("decode controller migration report: %v", err)
	}
	if report.SourceMode != "controller" || report.Vectors != 2 || report.RestorableTrash != 1 || report.Duplicates != 0 {
		t.Fatalf("controller migration report = %+v", report)
	}
	restarted, err := buildNativeStorageDependencies(nativeCfg, promptLayout, true)
	if err != nil {
		t.Fatalf("restart controller-migrated native storage for restore: %v", err)
	}
	restartedRelational := restarted.Relational.(*sqliteadapter.Store)
	restartedVector := restarted.Vector.(*lancedbadapter.Store)
	if err := restarted.Health.CheckHealth(ctx); err != nil {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restart controller-migrated native health check: %v", err)
	}
	if count, err := restartedVector.Count(ctx); err != nil || count != 2 {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restart controller-migrated vector count = %d, %v", count, err)
	}
	restoreOperation, err := restartedRelational.RestoreManagementBatch(ctx, logicdomain.ManagementRestoreCommand{
		OperationID:    "controller-migration-restore-operation",
		IdempotencyKey: "controller-migration-restore-idempotency",
		BatchID:        seed.ManagementBatchID,
		Now:            time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC),
	})
	if err != nil || restoreOperation.Status != "succeeded" {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restore controller-migrated management batch = %+v, %v", restoreOperation, err)
	}
	restoredRows, err := restartedRelational.LoadMemoryNodesByIDs(ctx, []uint64{seed.RestorableMemoryID})
	if err != nil || len(restoredRows) != 1 || restoredRows[0].ID != seed.RestorableMemoryID || restoredRows[0].VectorID != seed.RestorableVectorID || len(restoredRows[0].Vector) != 3 {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restored controller-migrated trash memory = %+v, %v", restoredRows, err)
	}
	restoredVectorHits, err := restartedVector.Search(ctx, []float32{0.4, 0.5, 0.6}, 1, logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1})
	if err != nil || len(restoredVectorHits) != 1 || restoredVectorHits[0].ID != seed.RestorableVectorID {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restored controller-migrated trash vector search = %+v, %v", restoredVectorHits, err)
	}
	restoredBatch, err := restartedRelational.GetManagementRecycleBatch(ctx, seed.ManagementBatchID)
	if err != nil || restoredBatch.State != "restored" {
		_ = restarted.Lifecycle.Shutdown(context.Background())
		t.Fatalf("restored controller-migrated recycle batch = %+v, %v", restoredBatch, err)
	}
	if err := restarted.Lifecycle.Shutdown(ctx); err != nil {
		t.Fatalf("close restarted controller-migrated native storage: %v", err)
	}
}

// assertNativeMigrationTrashVector proves that a restorable manual batch keeps a valid vector payload in the legacy trash table before migration.
// assertNativeMigrationTrashVector 用于证明可恢复人工批次在迁移前仍在旧 trash 表保留有效向量载荷。
func assertNativeMigrationTrashVector(t *testing.T, databasePath string, fixture nativeMigrationFixture) {
	t.Helper()
	db, err := sql.Open("sqlite", readOnlySQLiteTestDSN(databasePath))
	if err != nil {
		t.Fatalf("open source SQLite for trash vector assertion: %v", err)
	}
	defer db.Close()
	var vectorID string
	var vectorJSON string
	if err := db.QueryRowContext(context.Background(), `
SELECT vector_id, vector_json
FROM vmm_memory_nodes_trash
WHERE id = ? AND batch_id = ?`, fixture.RestorableMemoryID, fixture.ManagementBatchID).Scan(&vectorID, &vectorJSON); err != nil {
		t.Fatalf("query source restorable trash vector: %v", err)
	}
	if vectorID != fixture.RestorableVectorID || strings.TrimSpace(vectorJSON) == "" {
		t.Fatalf("source restorable trash vector identity/payload = id %q json %q, want id %q and non-empty JSON", vectorID, vectorJSON, fixture.RestorableVectorID)
	}
	var vector []float32
	if err := json.Unmarshal([]byte(vectorJSON), &vector); err != nil {
		t.Fatalf("decode source restorable trash vector JSON: %v", err)
	}
	if len(vector) != 3 || vector[0] != 0.4 || vector[1] != 0.5 || vector[2] != 0.6 {
		t.Fatalf("source restorable trash vector = %v, want [0.4 0.5 0.6]", vector)
	}
}

// nativeMigrationFixture records stable identifiers used to assert exact relational and vector preservation.
// nativeMigrationFixture 保存用于断言关系数据和向量数据精确保留的稳定标识。
type nativeMigrationFixture struct {
	MemoryID            uint64
	VectorID            string
	RestorableMemoryID  uint64
	RestorableVectorID  string
	FailureTurnID       uint64
	ManagementSessionID uint64
	ManagementBatchID   uint64
}

// seedNativeMigrationLegacyFixture creates active memory, retry, recycle, and management facts through the real legacy adapter APIs.
// seedNativeMigrationLegacyFixture 通过真实旧适配器 API 创建活跃记忆、失败重试、回收和人工管理状态。
func seedNativeMigrationLegacyFixture(t *testing.T, ctx context.Context, store *sqliteadapter.Store) nativeMigrationFixture {
	t.Helper()
	activeSession, err := store.ResolveRequestScope(ctx, "native-migration-active", 1, 1)
	if err != nil {
		t.Fatalf("resolve active migration session: %v", err)
	}
	activeTurn, err := store.AppendTurnRecord(ctx, activeSession, logicdomain.TurnRecord{
		UserContent:      "原生 SQLite 中文迁移验收",
		AssistantContent: "保留关系数据和向量",
		CreatedAt:        time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("append active migration turn: %v", err)
	}
	if _, err := store.RecordTurnAnalysisFailure(ctx, activeSession, activeTurn.ID, "extract", "fixture retry", 5, false); err != nil {
		t.Fatalf("record migration failure queue row: %v", err)
	}
	if err := store.EnqueueVectorGCJobs(ctx, logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   42,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"orphan-vector"},
		NextRunAt: time.Date(2026, 9, 20, 1, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("enqueue migration vector GC row: %v", err)
	}
	createdMemory, err := store.CreateDirectMemoryNode(ctx, activeSession, logicdomain.MemoryNodeRecord{
		SourceTurnID: activeTurn.ID,
		VectorID:     "native-migration-vector",
		Vector:       []float32{0.1, 0.2, 0.3},
		SourceKind:   logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:   logicdomain.MemoryScopeLevelProject,
		Category:     logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:     "原生 SQLite 中文 迁移",
		Details:      "原生 SQLite 中文 分词 验收",
		Status:       logicdomain.MemoryStatusActive,
		Priority:     logicdomain.MemoryPriorityP2,
		MemoryLevel:  logicdomain.MemoryLevelStable,
		CreatedAt:    time.Date(2026, 9, 20, 1, 2, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 9, 20, 1, 2, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create active migration memory: %v", err)
	}

	managementSession, err := store.ResolveRequestScope(ctx, "native-migration-management", 1, 1)
	if err != nil {
		t.Fatalf("resolve management migration session: %v", err)
	}
	managementTurn, err := store.AppendTurnRecord(ctx, managementSession, logicdomain.TurnRecord{
		UserContent:      "需要人工回收的旧会话",
		AssistantContent: "已完成分析",
		CreatedAt:        time.Date(2026, 9, 20, 1, 3, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("append management migration turn: %v", err)
	}
	if _, err := store.ApplyTurnAnalysis(ctx, managementSession, managementTurn, logicdomain.TurnAnalysis{Details: "fixture analysis completed", DetailsBudget: 1}); err != nil {
		t.Fatalf("complete management migration turn: %v", err)
	}
	restorableMemory, err := store.CreateDirectMemoryNode(ctx, managementSession, logicdomain.MemoryNodeRecord{
		SourceTurnID: managementTurn.ID,
		VectorID:     "native-migration-restorable-vector",
		Vector:       []float32{0.4, 0.5, 0.6},
		SourceKind:   logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:   logicdomain.MemoryScopeLevelProject,
		Category:     logicdomain.MemoryNodeCategoryProjectContext,
		Abstract:     "可恢复人工批次中文向量",
		Details:      "回收站向量必须随迁移保留，恢复仅修改关系表",
		Status:       logicdomain.MemoryStatusActive,
		Priority:     logicdomain.MemoryPriorityP2,
		MemoryLevel:  logicdomain.MemoryLevelStable,
		CreatedAt:    time.Date(2026, 9, 20, 1, 3, 30, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 9, 20, 1, 3, 30, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create restorable migration memory: %v", err)
	}
	selection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession,
		TargetIDs:  []uint64{managementSession.SessionID},
		Action:     logicdomain.ManagementActionRecycle,
		Source:     "native-integration-test",
	}
	impact, err := store.InspectManagementSelection(ctx, selection, 0)
	if err != nil {
		t.Fatalf("inspect management recycle fixture: %v", err)
	}
	if impact.MemoryCount < 1 {
		t.Fatalf("management recycle fixture memory count = %d, want at least one restorable memory", impact.MemoryCount)
	}
	now := time.Date(2026, 9, 20, 1, 4, 0, 0, time.UTC)
	preview := logicdomain.ManagementPreview{Token: "native-migration-preview", Selection: selection, Impact: impact, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.SaveManagementPreview(ctx, preview); err != nil {
		t.Fatalf("save management recycle fixture: %v", err)
	}
	operation, err := store.ApplyManagementRemoval(ctx, logicdomain.ManagementMutationCommand{
		OperationID:    "native-migration-operation",
		IdempotencyKey: "native-migration-idempotency",
		Preview:        preview,
		Now:            now,
		ExpiresAt:      time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || operation.Status != "succeeded" || operation.BatchID == 0 {
		t.Fatalf("apply management recycle fixture = %+v, %v", operation, err)
	}
	managed, err := store.GetManagementSession(ctx, managementSession.SessionID)
	if err != nil || managed.Status != string(logicdomain.SessionMemoryStatusRecycled) {
		t.Fatalf("management recycle state = %+v, %v", managed, err)
	}

	return nativeMigrationFixture{
		MemoryID:            createdMemory.ID,
		VectorID:            createdMemory.VectorID,
		RestorableMemoryID:  restorableMemory.ID,
		RestorableVectorID:  restorableMemory.VectorID,
		FailureTurnID:       activeTurn.ID,
		ManagementSessionID: managementSession.SessionID,
		ManagementBatchID:   operation.BatchID,
	}
}

// assertNativeMigrationDurableQueues checks the durable retry, vector-GC, and management records after native shutdown.
// assertNativeMigrationDurableQueues 在原生存储关闭后校验持久化失败队列、向量回收队列和管理操作记录。
func assertNativeMigrationDurableQueues(t *testing.T, databasePath string, fixture nativeMigrationFixture) {
	t.Helper()
	db, err := sql.Open("sqlite", readOnlySQLiteTestDSN(databasePath))
	if err != nil {
		t.Fatalf("open migrated SQLite for queue assertions: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	var failureCount int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM vmm_turn_analysis_failures
WHERE turn_id = ? AND stage = 'extract' AND attempt_count = 1 AND terminal_status = 'retrying'`, fixture.FailureTurnID).Scan(&failureCount); err != nil {
		t.Fatalf("query migrated failure queue: %v", err)
	}
	if failureCount != 1 {
		t.Fatalf("migrated failure queue rows = %d, want 1", failureCount)
	}
	var gcCount int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM vmm_vector_gc_jobs
WHERE vector_id = 'orphan-vector' AND job_type = ? AND batch_id = 42 AND completed_timestamp = 0`, string(logicdomain.VectorGCJobTypeRetentionRecycle)).Scan(&gcCount); err != nil {
		t.Fatalf("query migrated vector GC queue: %v", err)
	}
	if gcCount != 1 {
		t.Fatalf("migrated vector GC queue rows = %d, want 1", gcCount)
	}
	var operationStatus string
	var operationBatchID int64
	if err := db.QueryRowContext(ctx, `
SELECT status, batch_id FROM vmm_management_operations WHERE operation_id = 'native-migration-operation'`).Scan(&operationStatus, &operationBatchID); err != nil {
		t.Fatalf("query migrated management operation: %v", err)
	}
	if operationStatus != "succeeded" || uint64(operationBatchID) != fixture.ManagementBatchID {
		t.Fatalf("migrated management operation = status %q batch %d, want succeeded batch %d", operationStatus, operationBatchID, fixture.ManagementBatchID)
	}
	var recycleState string
	var restorable int64
	if err := db.QueryRowContext(ctx, `
SELECT state, restorable FROM vmm_management_recycle_batches WHERE batch_id = ?`, fixture.ManagementBatchID).Scan(&recycleState, &restorable); err != nil {
		t.Fatalf("query migrated recycle batch: %v", err)
	}
	if recycleState != "recycled" || restorable != 1 {
		t.Fatalf("migrated recycle batch = state %q restorable %d, want recycled/1", recycleState, restorable)
	}
}

// testNativeConfig creates an isolated native configuration whose paths never resolve into repository storage.
// testNativeConfig 创建完全隔离的原生配置，保证测试路径不会解析到仓库存储。
func testNativeConfig(root, library string) config.Config {
	cfg := config.DefaultBase()
	cfg.Storage.Mode = "native"
	cfg.SQLite.Timeout.Duration = 5 * time.Second
	cfg.SQLite.Native.Path = filepath.Join(root, "sqlite.db")
	cfg.SQLite.Native.Tokenizer = "gse"
	cfg.LanceDB.Timeout.Duration = 5 * time.Second
	cfg.LanceDB.TableName = "migration_vectors"
	cfg.LanceDB.VectorColumn = "vector"
	cfg.LanceDB.Native.Path = filepath.Join(root, "lancedb")
	cfg.LanceDB.Native.LibraryPath = library
	cfg.Embedding.Dimension = 3
	return cfg
}

// testControllerMigrationConfig creates an isolated controller-source configuration for offline migration.
// testControllerMigrationConfig 创建用于离线迁移的隔离 controller 源配置。
func testControllerMigrationConfig(root, endpoint, executable, nativeLibrary string) config.Config {
	cfg := config.DefaultBase()
	cfg.Storage.Mode = "controller"
	cfg.SQLite.Timeout.Duration = 10 * time.Second
	cfg.LanceDB.Timeout.Duration = 10 * time.Second
	cfg.LanceDB.TableName = "migration_vectors"
	cfg.LanceDB.VectorColumn = "vector"
	cfg.Embedding.Dimension = 3
	cfg.LanceDB.Native.LibraryPath = nativeLibrary
	cfg.Controller.Endpoint = endpoint
	cfg.Controller.AutoSpawn = true
	cfg.Controller.Executable = executable
	cfg.Controller.ProcessMode = "managed"
	cfg.Controller.MinimumUptime.Duration = time.Second
	cfg.Controller.IdleTimeout.Duration = 2 * time.Second
	cfg.Controller.LeaseTTL.Duration = 10 * time.Second
	cfg.Controller.ConnectTimeout.Duration = 2 * time.Second
	cfg.Controller.StartupTimeout.Duration = 20 * time.Second
	cfg.Controller.StartupRetryInterval.Duration = 100 * time.Millisecond
	cfg.Controller.LeaseRenewInterval.Duration = time.Second
	cfg.Controller.RequestTimeout.Duration = 10 * time.Second
	cfg.Controller.SpaceID = "vmm-native-migration-controller"
	cfg.Controller.SpaceLabel = "VMM native migration integration"
	return cfg
}

// testControllerRuntimeConfig maps one isolated database root to the controller SDK contract used by fixture creation.
// testControllerRuntimeConfig 将隔离数据库根目录映射为创建 fixture 所需的 controller SDK 配置。
func testControllerRuntimeConfig(endpoint, executable, databaseRoot string, requireExclusive bool) controlleradapter.Config {
	return controlleradapter.Config{
		Endpoint:              endpoint,
		AutoSpawn:             true,
		Executable:            executable,
		ProcessMode:           "managed",
		MinimumUptime:         time.Second,
		IdleTimeout:           2 * time.Second,
		LeaseTTL:              10 * time.Second,
		ConnectTimeout:        2 * time.Second,
		StartupTimeout:        20 * time.Second,
		StartupRetryInterval:  100 * time.Millisecond,
		LeaseRenewInterval:    time.Second,
		RequestTimeout:        10 * time.Second,
		RequireExclusiveSpace: requireExclusive,
		SpaceID:               "vmm-native-migration-controller",
		SpaceLabel:            "VMM native migration integration",
		SpaceRoot:             databaseRoot,
		SQLiteDatabase:        filepath.Join(databaseRoot, "sqlite.db"),
		LanceDBDirectory:      filepath.Join(databaseRoot, "lancedb"),
	}
}

// locateNativeMigrationControllerBinary resolves an explicit controller binary or the pinned repository dependency.
// locateNativeMigrationControllerBinary 解析显式 controller 可执行文件或仓库中固定的依赖产物。
func locateNativeMigrationControllerBinary(t *testing.T) string {
	t.Helper()
	if explicit := strings.TrimSpace(os.Getenv("VMM_CONTROLLER_EXECUTABLE")); explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			path = filepath.Join(testRepositoryRoot(), path)
		}
		if isRegularFile(path) {
			return path
		}
		t.Skipf("VMM_CONTROLLER_EXECUTABLE=%s does not name a regular controller binary", explicit)
	}
	binaryName := "vldb-controller"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	root := testRepositoryRoot()
	checked := []string{
		filepath.Join(root, "third_party", "deps", binaryName),
		filepath.Join(root, "output", "bin", binaryName),
	}
	for _, path := range checked {
		if isRegularFile(path) {
			return path
		}
	}
	t.Skipf("controller migration dependency unavailable; set VMM_CONTROLLER_EXECUTABLE or install the pinned binary; checked %s", strings.Join(checked, ", "))
	return ""
}

// reserveNativeMigrationControllerEndpoint reserves an unused loopback endpoint for one isolated managed controller process.
// reserveNativeMigrationControllerEndpoint 为单个隔离 managed controller 进程预留未使用的 loopback 端点。
func reserveNativeMigrationControllerEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve native migration controller endpoint: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release native migration controller endpoint: %v", err)
	}
	return "http://" + address
}

// testPackagedPromptLayout models the shipped output/configs directory while keeping all files under t.TempDir.
// testPackagedPromptLayout 模拟正式 output/configs 布局，同时把所有文件限制在 t.TempDir 内。
func testPackagedPromptLayout(root string) config.PromptLayout {
	configs := filepath.Join(root, "output", "configs")
	if err := os.MkdirAll(configs, 0o700); err != nil {
		panic(fmt.Sprintf("create test config layout: %v", err))
	}
	return config.PromptLayout{SystemDir: configs}
}

// locateNativeLanceLibrary resolves an explicit test artifact, the repository target artifact, or a verified packaging manifest.
// locateNativeLanceLibrary 按显式环境变量、仓库 target 产物和已存在的打包 manifest 顺序解析测试库。
func locateNativeLanceLibrary(t *testing.T) string {
	t.Helper()
	name := nativeLanceLibraryFileName()
	checked := make([]string, 0, 8)
	if explicit := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY")); explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			path = filepath.Join(testRepositoryRoot(), path)
		}
		checked = append(checked, path)
		if isRegularFile(path) {
			return path
		}
		t.Skipf("VMM_NATIVE_LANCEDB_LIBRARY=%s does not name a regular native LanceDB library; checked %s", explicit, strings.Join(checked, ", "))
	}
	root := testRepositoryRoot()
	for _, path := range []string{
		filepath.Join(root, "native", "lancedb", "target", "release", name),
		filepath.Join(root, "output", "libs", name),
	} {
		checked = append(checked, path)
		if isRegularFile(path) {
			return path
		}
	}
	if path := locateNativeManifestLibrary(root, name); path != "" {
		return path
	}
	t.Skipf("native LanceDB library unavailable; set VMM_NATIVE_LANCEDB_LIBRARY or build the exact target artifact; checked %s", strings.Join(checked, ", "))
	return ""
}

// locateNativeManifestLibrary reads only native packaging manifests and never guesses a library filename from a directory listing.
// locateNativeManifestLibrary 只读取原生打包 manifest，不根据目录内容猜测动态库文件名。
func locateNativeManifestLibrary(root, expectedName string) string {
	manifestRoot := filepath.Join(root, "third_party", "deps", "native_lancedb")
	targets, err := os.ReadDir(manifestRoot)
	if err != nil {
		return ""
	}
	for _, target := range targets {
		if !target.IsDir() {
			continue
		}
		manifestPath := filepath.Join(manifestRoot, target.Name(), "current.json")
		encoded, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest struct {
			LibraryFile string `json:"library_file"`
		}
		if json.Unmarshal(encoded, &manifest) != nil || filepath.Base(manifest.LibraryFile) != expectedName {
			continue
		}
		candidate := filepath.Join(manifestRoot, target.Name(), manifest.LibraryFile)
		if isRegularFile(candidate) {
			return candidate
		}
	}
	return ""
}

// locateLegacySQLiteLibrary resolves the packaged legacy SQLite DLL used only to create the temporary migration source.
// locateLegacySQLiteLibrary 解析仅用于构造临时迁移源库的旧版 SQLite 动态库。
func locateLegacySQLiteLibrary(t *testing.T) string {
	t.Helper()
	name := legacySQLiteLibraryFileName()
	root := testRepositoryRoot()
	candidates := []string{
		filepath.Join(root, "third_party", "deps", name),
		filepath.Join(root, "output", "libs", name),
	}
	for _, path := range candidates {
		if isRegularFile(path) {
			return path
		}
	}
	t.Skipf("legacy SQLite library unavailable; migration integration test not executed; checked %s", strings.Join(candidates, ", "))
	return ""
}

// nativeMigrationProcessRoot gives a child process a parent-owned fixture directory so resident legacy DLLs unload before directory cleanup.
// nativeMigrationProcessRoot 让子进程使用父测试持有的夹具目录，确保驻留旧 DLL 随子进程退出后再清理目录；返回目录和当前是否为执行子进程。
func nativeMigrationProcessRoot(t *testing.T) (string, bool) {
	t.Helper()
	const testKey = "VMM_MIGRATION_TEST_CHILD"
	const rootKey = "VMM_MIGRATION_TEST_ROOT"
	childName, childRoot := os.Getenv(testKey), os.Getenv(rootKey)
	if childName != "" || childRoot != "" {
		if childName != t.Name() || !filepath.IsAbs(childRoot) {
			t.Fatal("invalid migration subprocess fixture identity")
		}
		return childRoot, true
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), testKey+"="+t.Name(), rootKey+"="+root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("migration subprocess failed: %v\n%s", err, output)
	}
	t.Logf("migration subprocess completed:\n%s", output)
	return "", false
}

// copyTestArtifact copies a known library into an isolated temporary packaged layout.
// copyTestArtifact 将已确认的动态库复制到隔离的临时打包目录。
func copyTestArtifact(t *testing.T, source, destination string) {
	t.Helper()
	encoded, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read test artifact %s: %v", source, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatalf("create test artifact directory: %v", err)
	}
	if err := os.WriteFile(destination, encoded, 0o700); err != nil {
		t.Fatalf("copy test artifact to %s: %v", destination, err)
	}
}

// hashNativeMigrationSQLiteFacts computes deterministic type-preserving hashes for every audited migrated table.
// hashNativeMigrationSQLiteFacts 对每张审计迁移表计算保留 SQLite 类型的确定性摘要。
func hashNativeMigrationSQLiteFacts(path string) (string, map[string]int64, error) {
	db, err := sql.Open("sqlite", readOnlySQLiteTestDSN(path))
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	ctx := context.Background()
	digest := sha256.New()
	counts := make(map[string]int64, len(nativeIntegrationBusinessTables))
	for _, table := range nativeIntegrationBusinessTables {
		columns, primaryKeys, err := sqliteTestTableColumns(ctx, db, table)
		if err != nil {
			return "", nil, fmt.Errorf("read %s schema: %w", table, err)
		}
		if len(primaryKeys) == 0 {
			return "", nil, fmt.Errorf("table %s has no primary key", table)
		}
		writeTestHashString(digest, table)
		writeTestHashString(digest, strings.Join(columns, "\x00"))
		query := "SELECT " + quoteTestIdentifiers(columns) + " FROM " + quoteTestIdentifier(table) + " ORDER BY " + quoteTestOrder(primaryKeys)
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return "", nil, fmt.Errorf("query %s: %w", table, err)
		}
		var count int64
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(values))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return "", nil, fmt.Errorf("scan %s: %w", table, err)
			}
			writeTestHashUint64(digest, uint64(len(values)))
			for _, value := range values {
				if err := writeTestHashValue(digest, value); err != nil {
					_ = rows.Close()
					return "", nil, fmt.Errorf("hash %s: %w", table, err)
				}
			}
			count++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return "", nil, fmt.Errorf("iterate %s: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return "", nil, fmt.Errorf("close %s: %w", table, err)
		}
		counts[table] = count
		writeTestHashUint64(digest, uint64(count))
	}
	return hex.EncodeToString(digest.Sum(nil)), counts, nil
}

// sqliteTestTableColumns reads declared column and primary-key order for one audited table.
// sqliteTestTableColumns 读取一张审计表的声明列顺序和主键顺序。
func sqliteTestTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, []string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteTestIdentifier(table)+")")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	type column struct {
		name string
		pk   int
	}
	columns := make([]column, 0)
	for rows.Next() {
		var cid, notNull, pk int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk); err != nil {
			return nil, nil, err
		}
		columns = append(columns, column{name: name, pk: pk})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(columns) == 0 {
		return nil, nil, fmt.Errorf("table is empty or missing")
	}
	names := make([]string, len(columns))
	primaryKeys := make([]column, 0)
	for index, item := range columns {
		names[index] = item.name
		if item.pk > 0 {
			primaryKeys = append(primaryKeys, item)
		}
	}
	sort.Slice(primaryKeys, func(left, right int) bool { return primaryKeys[left].pk < primaryKeys[right].pk })
	orderedKeys := make([]string, len(primaryKeys))
	for index, item := range primaryKeys {
		orderedKeys[index] = item.name
	}
	return names, orderedKeys, nil
}

// writeTestHashValue writes an unambiguous type tag and length-delimited value into a migration fact digest.
// writeTestHashValue 将带类型标签且带长度的值写入迁移事实摘要，避免不同 SQLite 类型发生碰撞。
func writeTestHashValue(digest hash.Hash, value any) error {
	switch typed := value.(type) {
	case nil:
		_, _ = digest.Write([]byte{0})
	case int64:
		_, _ = digest.Write([]byte{1})
		writeTestHashUint64(digest, uint64(typed))
	case float64:
		_, _ = digest.Write([]byte{2})
		writeTestHashUint64(digest, math.Float64bits(typed))
	case string:
		_, _ = digest.Write([]byte{3})
		writeTestHashBytes(digest, []byte(typed))
	case []byte:
		_, _ = digest.Write([]byte{4})
		writeTestHashBytes(digest, typed)
	case bool:
		_, _ = digest.Write([]byte{5})
		if typed {
			_, _ = digest.Write([]byte{1})
		} else {
			_, _ = digest.Write([]byte{0})
		}
	default:
		return fmt.Errorf("unsupported SQLite value type %T", value)
	}
	return nil
}

// writeTestHashString appends a length-delimited string to the digest.
// writeTestHashString 将带长度的字符串追加到摘要。
func writeTestHashString(digest hash.Hash, value string) {
	writeTestHashBytes(digest, []byte(value))
}

// writeTestHashBytes appends a length-delimited byte sequence to the digest.
// writeTestHashBytes 将带长度的字节序列追加到摘要。
func writeTestHashBytes(digest hash.Hash, value []byte) {
	writeTestHashUint64(digest, uint64(len(value)))
	_, _ = digest.Write(value)
}

// writeTestHashUint64 appends one fixed-width big-endian integer to the digest.
// writeTestHashUint64 将固定宽度的大端整数追加到摘要。
func writeTestHashUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

// readOnlySQLiteTestDSN opens a SQLite file read-only while still observing a committed WAL during verification.
// readOnlySQLiteTestDSN 以只读方式打开 SQLite 文件，同时允许校验过程观察已提交的 WAL 内容。
func readOnlySQLiteTestDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro"
}

// quoteTestIdentifier quotes an identifier obtained from a test-controlled audited table list.
// quoteTestIdentifier 为来自测试审计清单的标识符添加 SQLite 引号。
func quoteTestIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// quoteTestIdentifiers renders a comma-separated list of quoted identifiers.
// quoteTestIdentifiers 渲染逗号分隔的带引号标识符列表。
func quoteTestIdentifiers(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteTestIdentifier(value)
	}
	return strings.Join(quoted, ", ")
}

// quoteTestOrder renders deterministic ascending primary-key ordering.
// quoteTestOrder 渲染确定性的主键升序排序片段。
func quoteTestOrder(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteTestIdentifier(value) + " ASC"
	}
	return strings.Join(quoted, ", ")
}

// containsNativeMemoryID reports whether one lexical result page includes the exact durable memory ID.
// containsNativeMemoryID 判断 lexical 结果页是否包含指定的长期记忆 ID。
func containsNativeMemoryID(hits []logicdomain.MemoryLexicalHit, id uint64) bool {
	for _, hit := range hits {
		if hit.MemoryID == id {
			return true
		}
	}
	return false
}

// testRepositoryRoot resolves the repository root from this test file rather than from the process working directory.
// testRepositoryRoot 根据测试文件位置解析仓库根目录，而不依赖进程当前目录。
func testRepositoryRoot() string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

// isRegularFile reports whether a path names a usable regular file artifact.
// isRegularFile 判断路径是否指向可用的普通文件产物。
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// nativeLanceLibraryFileName returns the exact platform filename expected from the native packaging contract.
// nativeLanceLibraryFileName 返回原生打包契约要求的精确平台动态库文件名。
func nativeLanceLibraryFileName() string {
	switch runtime.GOOS {
	case "windows":
		return "vmm_lancedb_native.dll"
	case "darwin":
		return "libvmm_lancedb_native.dylib"
	default:
		return "libvmm_lancedb_native.so"
	}
}

// legacySQLiteLibraryFileName returns the existing legacy SQLite artifact filename for the current host.
// legacySQLiteLibraryFileName 返回当前主机旧版 SQLite 产物文件名。
func legacySQLiteLibraryFileName() string {
	switch runtime.GOOS {
	case "windows":
		return "vldb_sqlite.dll"
	case "darwin":
		return "libvldb_sqlite.dylib"
	default:
		return "libvldb_sqlite.so"
	}
}
