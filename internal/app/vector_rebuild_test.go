// vector_rebuild_test.go verifies one-shot vector rebuild orchestration so split and combined modes keep the correct rebuild order when models or dimensions change.
// vector_rebuild_test.go 用于验证一次性向量重建编排，确保分离模式与合并模式在模型或维度变化时都遵循正确的重建顺序。
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRunVectorRebuildWithPortsSplitRebuildsDurableAndSidecar verifies split mode first clears durable SQLite vectors, recreates the detached vector table, and then refills both stores from rebuilt embeddings.
// TestRunVectorRebuildWithPortsSplitRebuildsDurableAndSidecar 用于验证分离模式会先清空 durable SQLite 向量、重建旁路向量表，再把新 embedding 同步回填到两个存储。
func TestRunVectorRebuildWithPortsSplitRebuildsDurableAndSidecar(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 2
	initialRecords := []logicdomain.MemoryRecord{
		{ID: "vec-1", Text: "memory one", Vector: []float32{10, 10}},
		{ID: "vec-2", Text: "memory two", Vector: []float32{20, 20}},
	}
	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 7}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			7: initialRecords,
		},
	}
	durable := newStubVectorDurableStore(initialRecords)
	embedding := &stubVectorEmbeddingClient{
		responses: []appports.EmbeddingResponse{{
			Vectors: [][]float32{{1, 2}, {3, 4}},
		}},
	}
	vector := newStubMaintenanceVectorStore(initialRecords)

	report, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err != nil {
		t.Fatalf("runVectorRebuildWithPorts returned error: %v", err)
	}
	if report.Mode != "split-lancedb" {
		t.Fatalf("mode = %q", report.Mode)
	}
	if report.ProjectCount != 1 || report.MemoryCount != 2 || report.DurableRowsUpdated != 2 || report.VectorRowsRebuilt != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if durable.clearCalls != 1 || len(durable.clearedVectorIDs) != 2 {
		t.Fatalf("durable clear calls = %d ids = %+v", durable.clearCalls, durable.clearedVectorIDs)
	}
	if durable.replaceCalls != 1 || len(durable.replaced) != 2 {
		t.Fatalf("durable replace calls = %d records = %+v", durable.replaceCalls, durable.replaced)
	}
	if vector.recreateCalls != 1 {
		t.Fatal("expected split vector table recreation")
	}
	if len(vector.upserts) != 2 || len(vector.upserts[0].Vector) != 2 || vector.upserts[0].Vector[0] != 1 {
		t.Fatalf("unexpected vector upserts: %+v", vector.upserts)
	}
	if got := durable.current["vec-1"].Vector; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected durable vec-1 to be rebuilt, got %+v", got)
	}
	if got := vector.current["vec-2"].Vector; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("expected sidecar vec-2 to be rebuilt, got %+v", got)
	}
}

// TestRunVectorRebuildWithPortsSplitStopsBeforeResetOnEmbeddingFailure verifies split mode now materializes rebuilt vectors before the destructive reset starts, so embedding failures leave the original durable and sidecar snapshots untouched.
// TestRunVectorRebuildWithPortsSplitStopsBeforeResetOnEmbeddingFailure 用于验证 split 模式现在会在破坏性 reset 前先准备好新向量，因此 embedding 失败不会动到原始 durable 与 sidecar 快照。
func TestRunVectorRebuildWithPortsSplitStopsBeforeResetOnEmbeddingFailure(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 2
	initialRecords := []logicdomain.MemoryRecord{
		{ID: "vec-15", Text: "memory fifteen", Vector: []float32{15, 15}},
	}

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 15}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			15: initialRecords,
		},
	}
	durable := newStubVectorDurableStore(initialRecords)
	embedding := &stubVectorEmbeddingClient{err: errors.New("embedding backend unavailable")}
	vector := newStubMaintenanceVectorStore(initialRecords)

	_, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err == nil {
		t.Fatal("expected embedding failure")
	}
	if durable.clearCalls != 0 || len(durable.clearedVectorIDs) != 0 {
		t.Fatalf("expected no durable clear before reset, got calls=%d ids=%+v", durable.clearCalls, durable.clearedVectorIDs)
	}
	if durable.replaceCalls != 0 {
		t.Fatalf("expected no durable replacement writes before embedding failure, got %d", durable.replaceCalls)
	}
	if vector.recreateCalls != 0 {
		t.Fatalf("expected no sidecar table recreation before embedding failure, got %d", vector.recreateCalls)
	}
	if len(vector.upserts) != 0 {
		t.Fatalf("expected no sidecar writes before embedding failure, got %d", len(vector.upserts))
	}
	if got := durable.current["vec-15"].Vector; len(got) != 2 || got[0] != 15 || got[1] != 15 {
		t.Fatalf("expected durable vec-15 to stay untouched, got %+v", got)
	}
	if got := vector.current["vec-15"].Vector; len(got) != 2 || got[0] != 15 || got[1] != 15 {
		t.Fatalf("expected sidecar vec-15 to stay untouched, got %+v", got)
	}
}

// TestRunVectorRebuildWithPortsSplitDimensionChangeRepairsTargetState verifies split mode repairs back to the new current-dimension target state after one intermediate sidecar failure, instead of trying to restore incompatible historical vectors into the new table.
// TestRunVectorRebuildWithPortsSplitDimensionChangeRepairsTargetState 用于验证当 split 模式切到新 embedding 维度后，只要中途 sidecar 写入失败一次，系统会修复回新的当前维度目标状态，而不是再尝试把不兼容的历史向量塞回新表。
func TestRunVectorRebuildWithPortsSplitDimensionChangeRepairsTargetState(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 2
	initialRecords := []logicdomain.MemoryRecord{
		{ID: "vec-16", Text: "memory sixteen", Vector: []float32{16, 16, 16}},
		{ID: "vec-17", Text: "memory seventeen", Vector: []float32{17, 17, 17}},
	}

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 16}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			16: initialRecords,
		},
	}
	durable := newStubVectorDurableStore(initialRecords)
	embedding := &stubVectorEmbeddingClient{
		responses: []appports.EmbeddingResponse{{
			Vectors: [][]float32{{1, 6}, {1, 7}},
		}},
	}
	vector := newStubMaintenanceVectorStore(initialRecords)
	vector.expectedVectorDimension = 2
	vector.upsertErrAtCall = 2
	vector.upsertErr = errors.New("lancedb unavailable")

	report, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err != nil {
		t.Fatalf("runVectorRebuildWithPorts returned error: %v", err)
	}
	if report.DurableRowsUpdated != 2 || report.VectorRowsRebuilt != 2 {
		t.Fatalf("unexpected report after automatic repair: %+v", report)
	}
	if durable.clearCalls != 2 {
		t.Fatalf("expected reset plus repair to clear durable vectors twice, got %d", durable.clearCalls)
	}
	if vector.recreateCalls != 2 {
		t.Fatalf("expected reset plus automatic repair sidecar recreation, got %d", vector.recreateCalls)
	}
	if durable.replaceCalls != 2 {
		t.Fatalf("expected initial fill plus automatic repair fill, got %d replace calls", durable.replaceCalls)
	}
	if got := durable.current["vec-16"].Vector; len(got) != 2 || got[0] != 1 || got[1] != 6 {
		t.Fatalf("expected durable vec-16 to converge to rebuilt target vector, got %+v", got)
	}
	if got := vector.current["vec-17"].Vector; len(got) != 2 || got[0] != 1 || got[1] != 7 {
		t.Fatalf("expected sidecar vec-17 to converge to rebuilt target vector, got %+v", got)
	}
}

// TestRunVectorRebuildWithPortsCombinedRebuildsDimensionsBeforeRewrite verifies combined mode rebuilds the PostgreSQL vector-bearing columns before it rewrites active embeddings, and never touches the detached vector-store path.
// TestRunVectorRebuildWithPortsCombinedRebuildsDimensionsBeforeRewrite 用于验证合并模式会先重建 PostgreSQL 向量列，再回填 active embedding，并且不会误触旁路向量库路径。
func TestRunVectorRebuildWithPortsCombinedRebuildsDimensionsBeforeRewrite(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 3
	cfg.Storage.Mode = "combined"
	cfg.Storage.CombinedProvider = "postgres"

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 9}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			9: {
				{ID: "vec-9", Text: "memory nine"},
				{ID: "vec-10", Text: "memory ten"},
			},
		},
	}
	durable := &stubVectorDurableStore{}
	embedding := &stubVectorEmbeddingClient{
		responses: []appports.EmbeddingResponse{{
			Vectors: [][]float32{{9, 9, 9}, {10, 10, 10}},
		}},
	}
	vector := &stubMaintenanceVectorStore{}

	report, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err != nil {
		t.Fatalf("runVectorRebuildWithPorts returned error: %v", err)
	}
	if report.Mode != "combined-postgres" {
		t.Fatalf("mode = %q", report.Mode)
	}
	if durable.dimensionRebuildCalls != 1 {
		t.Fatalf("dimension rebuild calls = %d", durable.dimensionRebuildCalls)
	}
	if durable.replaceCalls != 0 || len(durable.replaced) != 0 {
		t.Fatalf("combined mode should not use standalone durable replacement writes: calls=%d records=%+v", durable.replaceCalls, durable.replaced)
	}
	if len(durable.dimensionRebuildRecords) != 2 {
		t.Fatalf("dimension rebuild records = %+v", durable.dimensionRebuildRecords)
	}
	if vector.recreated || len(vector.upserts) > 0 {
		t.Fatalf("combined mode should not touch detached vector store: %+v", vector)
	}
	if report.VectorRowsRebuilt != 2 {
		t.Fatalf("vector rows rebuilt = %d", report.VectorRowsRebuilt)
	}
}

// TestRunVectorRebuildWithPortsCombinedStillMigratesDimensionsWithoutActiveRows verifies combined mode still migrates the vector schema even when there are no active durable memories to re-embed.
// TestRunVectorRebuildWithPortsCombinedStillMigratesDimensionsWithoutActiveRows 用于验证合并模式即使没有 active durable 记忆需要重建，也仍然会迁移向量 schema。
func TestRunVectorRebuildWithPortsCombinedStillMigratesDimensionsWithoutActiveRows(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 4
	cfg.Storage.Mode = "combined"
	cfg.Storage.CombinedProvider = "postgres"

	workspace := &stubMaintenanceWorkspace{
		projects:          []logicdomain.ProjectRecord{{ID: 12}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{12: nil},
	}
	durable := &stubVectorDurableStore{}
	embedding := &stubVectorEmbeddingClient{}
	vector := &stubMaintenanceVectorStore{}

	report, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err != nil {
		t.Fatalf("runVectorRebuildWithPorts returned error: %v", err)
	}
	if durable.dimensionRebuildCalls != 1 {
		t.Fatalf("dimension rebuild calls = %d", durable.dimensionRebuildCalls)
	}
	if len(durable.dimensionRebuildRecords) != 0 {
		t.Fatalf("expected no active records to rebuild, got %+v", durable.dimensionRebuildRecords)
	}
	if embedding.calls != 0 {
		t.Fatalf("expected no embedding calls on empty active dataset, got %d", embedding.calls)
	}
	if report.DurableRowsUpdated != 0 || report.VectorRowsRebuilt != 0 {
		t.Fatalf("unexpected empty combined report: %+v", report)
	}
}

// TestRunVectorRebuildWithPortsRejectsUnexpectedEmbeddingDimensionsBeforeReset verifies split-mode rebuilds validate prepared embeddings before any destructive reset starts.
// TestRunVectorRebuildWithPortsRejectsUnexpectedEmbeddingDimensionsBeforeReset 用于验证 split 模式会在任何破坏性 reset 开始前校验准备好的 embedding 维度。
func TestRunVectorRebuildWithPortsRejectsUnexpectedEmbeddingDimensionsBeforeReset(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 3
	initialRecords := []logicdomain.MemoryRecord{
		{ID: "vec-11", Text: "memory eleven", Vector: []float32{11, 11, 11}},
	}

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 11}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			11: initialRecords,
		},
	}
	durable := newStubVectorDurableStore(initialRecords)
	embedding := &stubVectorEmbeddingClient{
		responses: []appports.EmbeddingResponse{{
			Vectors: [][]float32{{1, 2}},
		}},
	}
	vector := newStubMaintenanceVectorStore(initialRecords)

	_, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err == nil {
		t.Fatal("expected dimension validation error")
	}
	if durable.clearCalls != 0 {
		t.Fatalf("expected no durable clear before validation failure, got %d", durable.clearCalls)
	}
	if durable.replaceCalls != 0 {
		t.Fatalf("expected no durable replacement writes before validation failure, got %d", durable.replaceCalls)
	}
	if vector.recreateCalls != 0 || len(vector.upserts) != 0 {
		t.Fatalf("expected no sidecar reset or writes before validation failure, got recreate_calls=%d upserts=%d", vector.recreateCalls, len(vector.upserts))
	}
	if got := durable.current["vec-11"].Vector; len(got) != 3 || got[0] != 11 || got[1] != 11 || got[2] != 11 {
		t.Fatalf("expected durable vec-11 to stay untouched, got %+v", got)
	}
	if got := vector.current["vec-11"].Vector; len(got) != 3 || got[0] != 11 || got[1] != 11 || got[2] != 11 {
		t.Fatalf("expected sidecar vec-11 to stay untouched, got %+v", got)
	}
}

// TestRunVectorRebuildWithPortsSplitRecoversAfterSidecarUpsertFailure verifies split mode automatically replays the already-prepared target vectors after one intermediate LanceDB upsert failure.
// TestRunVectorRebuildWithPortsSplitRecoversAfterSidecarUpsertFailure 用于验证当 LanceDB 在中途 upsert 失败一次后，split 模式会自动重放已准备好的目标向量并完成修复。
func TestRunVectorRebuildWithPortsSplitRecoversAfterSidecarUpsertFailure(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 2
	initialRecords := []logicdomain.MemoryRecord{
		{ID: "vec-21", Text: "memory twenty one", Vector: []float32{21, 21}},
		{ID: "vec-22", Text: "memory twenty two", Vector: []float32{22, 22}},
	}

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 21}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			21: initialRecords,
		},
	}
	durable := newStubVectorDurableStore(initialRecords)
	embedding := &stubVectorEmbeddingClient{
		responses: []appports.EmbeddingResponse{{
			Vectors: [][]float32{{1, 2}, {3, 4}},
		}},
	}
	vector := newStubMaintenanceVectorStore(initialRecords)
	vector.upsertErrAtCall = 2
	vector.upsertErr = errors.New("lancedb unavailable")

	report, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err != nil {
		t.Fatalf("runVectorRebuildWithPorts returned error: %v", err)
	}
	if report.DurableRowsUpdated != 2 || report.VectorRowsRebuilt != 2 {
		t.Fatalf("unexpected report after automatic repair: %+v", report)
	}
	if durable.replaceCalls != 2 {
		t.Fatalf("expected initial fill plus automatic repair fill, got %d replace calls", durable.replaceCalls)
	}
	if durable.clearCalls != 2 {
		t.Fatalf("expected reset plus automatic repair clear, got %d clear calls", durable.clearCalls)
	}
	if vector.recreateCalls != 2 {
		t.Fatalf("expected reset plus automatic repair sidecar recreation, got %d", vector.recreateCalls)
	}
	if got := durable.current["vec-21"].Vector; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected durable vec-21 to converge to rebuilt vector, got %+v", got)
	}
	if got := durable.current["vec-22"].Vector; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("expected durable vec-22 to converge to rebuilt vector, got %+v", got)
	}
	if got := vector.current["vec-21"].Vector; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected sidecar vec-21 to converge to rebuilt vector, got %+v", got)
	}
	if got := vector.current["vec-22"].Vector; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("expected sidecar vec-22 to converge to rebuilt vector, got %+v", got)
	}
}

// TestRunVectorRebuildWithPortsCombinedDelaysMigrationUntilEmbeddingsReady verifies combined mode does not touch PostgreSQL vector columns until every refreshed embedding has been prepared successfully.
// TestRunVectorRebuildWithPortsCombinedDelaysMigrationUntilEmbeddingsReady 用于验证合并模式会等全部新 embedding 准备成功后才触碰 PostgreSQL 向量列。
func TestRunVectorRebuildWithPortsCombinedDelaysMigrationUntilEmbeddingsReady(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Embedding.Dimension = 3
	cfg.Storage.Mode = "combined"
	cfg.Storage.CombinedProvider = "postgres"

	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 13}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			13: {
				{ID: "vec-13", Text: "memory thirteen"},
			},
		},
	}
	durable := &stubVectorDurableStore{}
	embedding := &stubVectorEmbeddingClient{err: errors.New("embedding backend unavailable")}
	vector := &stubMaintenanceVectorStore{}

	_, err := runVectorRebuildWithPorts(context.Background(), cfg, embedding, workspace, durable, vector, nil)
	if err == nil {
		t.Fatal("expected embedding failure")
	}
	if durable.dimensionRebuildCalls != 0 {
		t.Fatalf("expected no dimension rebuild before embeddings are ready, got %d", durable.dimensionRebuildCalls)
	}
	if durable.replaceCalls != 0 {
		t.Fatalf("expected no durable replacement writes, got %d", durable.replaceCalls)
	}
	if vector.recreated || len(vector.upserts) != 0 {
		t.Fatalf("expected no detached vector-store activity, got recreated=%v upserts=%d", vector.recreated, len(vector.upserts))
	}
}

// TestWaitVectorRebuildBudgetWindowHonorsContextCancellation verifies budget-window waiting exits promptly when callers abort the maintenance command.
// TestWaitVectorRebuildBudgetWindowHonorsContextCancellation 用于验证预算窗口等待在调用方取消维护命令时会及时退出。
func TestWaitVectorRebuildBudgetWindowHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitVectorRebuildBudgetWindow(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

// TestLoadVectorRebuildRecordsPrefersMaintenanceProjectAndMemoryListers verifies one-shot rebuild enumeration prefers the maintenance-only project and memory listing paths when the workspace store exposes them.
// TestLoadVectorRebuildRecordsPrefersMaintenanceProjectAndMemoryListers 用于验证当 workspace store 暴露维护专用的项目与记忆枚举能力时，一次性重建会优先走维护路径。
func TestLoadVectorRebuildRecordsPrefersMaintenanceProjectAndMemoryListers(t *testing.T) {
	workspace := &stubMaintenanceWorkspace{
		projects: []logicdomain.ProjectRecord{{ID: 88}},
		maintenanceProjects: []logicdomain.ProjectRecord{{ID: 99}},
		memoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			88: {{ID: "online-record"}},
		},
		maintenanceMemoriesByProject: map[uint64][]logicdomain.MemoryRecord{
			99: {{ID: "maintenance-record"}},
		},
	}

	projects, records, err := loadVectorRebuildRecords(context.Background(), workspace)
	if err != nil {
		t.Fatalf("loadVectorRebuildRecords returned error: %v", err)
	}
	if workspace.listProjectsCalls != 0 {
		t.Fatalf("expected normal ListProjects to stay unused, got %d calls", workspace.listProjectsCalls)
	}
	if workspace.listProjectsForMaintenanceCalls != 1 {
		t.Fatalf("expected maintenance project listing to be used once, got %d calls", workspace.listProjectsForMaintenanceCalls)
	}
	if workspace.listProjectMemoriesCalls != 0 {
		t.Fatalf("expected normal ListProjectMemories to stay unused, got %d calls", workspace.listProjectMemoriesCalls)
	}
	if workspace.listProjectMemoriesForMaintenanceCalls != 1 {
		t.Fatalf("expected maintenance listing to be used once, got %d calls", workspace.listProjectMemoriesForMaintenanceCalls)
	}
	if len(projects) != 1 || projects[0].ID != 99 {
		t.Fatalf("expected maintenance projects to be loaded, got %+v", projects)
	}
	if len(records) != 1 || records[0].ID != "maintenance-record" {
		t.Fatalf("expected maintenance records to be loaded, got %+v", records)
	}
}

// stubMaintenanceWorkspace provides deterministic project/memory listings while keeping the full WorkspaceStore interface satisfied for orchestration tests.
// stubMaintenanceWorkspace 用于为编排测试提供确定性的项目和记忆列表，同时保持完整的 WorkspaceStore 接口实现。
type stubMaintenanceWorkspace struct {
	projects                               []logicdomain.ProjectRecord
	maintenanceProjects                    []logicdomain.ProjectRecord
	memoriesByProject                      map[uint64][]logicdomain.MemoryRecord
	maintenanceMemoriesByProject           map[uint64][]logicdomain.MemoryRecord
	listProjectsCalls                      int
	listProjectsForMaintenanceCalls        int
	listProjectMemoriesCalls               int
	listProjectMemoriesForMaintenanceCalls int
}

// ListProjects returns the preloaded project list.
// ListProjects 用于返回预置的项目列表。
func (s *stubMaintenanceWorkspace) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	s.listProjectsCalls++
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ListProjectsForMaintenance returns the dedicated maintenance project list when provided so orchestration tests can distinguish the maintenance project enumeration path from the normal online listing path.
// ListProjectsForMaintenance 用于在存在独立维护项目列表时返回专用维护结果，让编排测试区分维护项目枚举路径与普通在线枚举路径。
func (s *stubMaintenanceWorkspace) ListProjectsForMaintenance(context.Context) ([]logicdomain.ProjectRecord, error) {
	s.listProjectsForMaintenanceCalls++
	if s.maintenanceProjects != nil {
		return append([]logicdomain.ProjectRecord(nil), s.maintenanceProjects...), nil
	}
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProjectRef stays unused in these rebuild orchestration tests.
// ResolveProjectRef 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) ResolveProjectRef(context.Context, string) (logicdomain.ProjectRecord, error) {
	return logicdomain.ProjectRecord{}, nil
}

// ListProjectMemories returns the deterministic memory batch for one project.
// ListProjectMemories 用于返回某个项目对应的确定性记忆批次。
func (s *stubMaintenanceWorkspace) ListProjectMemories(_ context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	s.listProjectMemoriesCalls++
	return cloneVectorRebuildRecords(s.memoriesByProject[projectID]), nil
}

// ListProjectMemoriesForMaintenance returns the dedicated maintenance batch when provided so orchestration tests can distinguish the maintenance read path from the normal online listing path.
// ListProjectMemoriesForMaintenance 用于在存在独立维护批次时返回专用维护结果，让编排测试区分维护读路径与普通在线枚举路径。
func (s *stubMaintenanceWorkspace) ListProjectMemoriesForMaintenance(_ context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	s.listProjectMemoriesForMaintenanceCalls++
	if s.maintenanceMemoriesByProject != nil {
		return cloneVectorRebuildRecords(s.maintenanceMemoriesByProject[projectID]), nil
	}
	return cloneVectorRebuildRecords(s.memoriesByProject[projectID]), nil
}

// EnsureProjectPath stays unused in these rebuild orchestration tests.
// EnsureProjectPath 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) EnsureProjectPath(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProjectPath stays unused in these rebuild orchestration tests.
// DeleteProjectPath 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) DeleteProjectPath(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	return logicdomain.ProjectDeleteResult{}, nil
}

// MigrateProjectPath stays unused in these rebuild orchestration tests.
// MigrateProjectPath 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) MigrateProjectPath(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, nil
}

// ResolveUserRef stays unused in these rebuild orchestration tests.
// ResolveUserRef 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) ResolveUserRef(context.Context, string) (logicdomain.UserRecord, error) {
	return logicdomain.UserRecord{}, nil
}

// EnsureUserName stays unused in these rebuild orchestration tests.
// EnsureUserName 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) EnsureUserName(context.Context, string, bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, nil
}

// ListUsers stays unused in these rebuild orchestration tests.
// ListUsers 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return nil, nil
}

// DeleteUserRef stays unused in these rebuild orchestration tests.
// DeleteUserRef 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceWorkspace) DeleteUserRef(context.Context, string, string) (logicdomain.UserDeleteResult, error) {
	return logicdomain.UserDeleteResult{}, nil
}

// stubVectorDurableStore records durable vector rewrites and combined-mode dimension rebuild requests.
// stubVectorDurableStore 用于记录 durable 向量重写和合并模式维度重建请求。
type stubVectorDurableStore struct {
	clearCalls              int
	replaceCalls            int
	dimensionRebuildCalls   int
	clearedVectorIDs        []string
	dimensionRebuildRecords []logicdomain.MemoryRecord
	replaced                []logicdomain.MemoryRecord
	current                 map[string]logicdomain.MemoryRecord
}

// newStubVectorDurableStore seeds one durable-store stub with the caller's initial records so rebuild assertions can verify the final durable snapshot after reset, retry, or repair flows.
// newStubVectorDurableStore 用于用调用方给定的初始记录预热 durable 存根，方便重建断言校验 reset、重试或自动修复后的最终 durable 快照。
func newStubVectorDurableStore(initial []logicdomain.MemoryRecord) *stubVectorDurableStore {
	store := &stubVectorDurableStore{
		current: make(map[string]logicdomain.MemoryRecord, len(initial)),
	}
	for _, record := range cloneVectorRebuildRecords(initial) {
		store.current[record.ID] = record
	}
	return store
}

// ClearMemoryVectors records the durable vector ids that were cleared before split-mode refill starts.
// ClearMemoryVectors 用于记录 split 模式回填开始前被清空的 durable 向量 id。
func (s *stubVectorDurableStore) ClearMemoryVectors(_ context.Context, vectorIDs []string) error {
	s.clearCalls++
	s.clearedVectorIDs = append(s.clearedVectorIDs, vectorIDs...)
	if s.current == nil {
		s.current = make(map[string]logicdomain.MemoryRecord)
	}
	for _, vectorID := range vectorIDs {
		record := cloneVectorRebuildRecord(s.current[vectorID])
		record.ID = vectorID
		record.Vector = nil
		s.current[vectorID] = record
	}
	return nil
}

// ReplaceMemoryVectors records the incoming durable vector payload.
// ReplaceMemoryVectors 用于记录传入的 durable 向量载荷。
func (s *stubVectorDurableStore) ReplaceMemoryVectors(_ context.Context, records []logicdomain.MemoryRecord) error {
	s.replaceCalls++
	if s.current == nil {
		s.current = make(map[string]logicdomain.MemoryRecord, len(records))
	}
	cloned := cloneVectorRebuildRecords(records)
	s.replaced = append(s.replaced, cloned...)
	for _, record := range cloned {
		s.current[record.ID] = record
	}
	return nil
}

// RebuildMemoryVectorDimensions records one combined-mode dimension migration request together with the active records prepared for atomic backfill.
// RebuildMemoryVectorDimensions 用于记录一次合并模式维度迁移请求，以及为原子回填准备好的 active 记录。
func (s *stubVectorDurableStore) RebuildMemoryVectorDimensions(_ context.Context, records []logicdomain.MemoryRecord) error {
	s.dimensionRebuildCalls++
	s.dimensionRebuildRecords = append(s.dimensionRebuildRecords, cloneVectorRebuildRecords(records)...)
	return nil
}

// stubVectorEmbeddingClient replays deterministic embedding responses batch by batch.
// stubVectorEmbeddingClient 用于按批次回放确定性的 embedding 响应。
type stubVectorEmbeddingClient struct {
	responses []appports.EmbeddingResponse
	calls     int
	err       error
}

// Embed returns the next queued response.
// Embed 用于返回下一个预置响应。
func (s *stubVectorEmbeddingClient) Embed(_ context.Context, _ appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	s.calls++
	if s.err != nil {
		return appports.EmbeddingResponse{}, s.err
	}
	if s.calls > len(s.responses) {
		return appports.EmbeddingResponse{}, nil
	}
	resp := s.responses[s.calls-1]
	return resp, nil
}

// stubMaintenanceVectorStore records split-mode sidecar rebuild operations.
// stubMaintenanceVectorStore 用于记录分离模式旁路向量库重建操作。
type stubMaintenanceVectorStore struct {
	recreated               bool
	recreateCalls           int
	upserts                 []logicdomain.MemoryRecord
	upsertCalls             int
	upsertErrAtCall         int
	upsertErr               error
	current                 map[string]logicdomain.MemoryRecord
	expectedVectorDimension int
}

// newStubMaintenanceVectorStore seeds one sidecar-store stub with the caller's initial rows so rebuild assertions can compare the repaired live recall surface against the expected target snapshot.
// newStubMaintenanceVectorStore 用于用调用方给定的初始行预热 sidecar 存根，方便重建断言把修复后的实际召回面与期望目标快照进行比对。
func newStubMaintenanceVectorStore(initial []logicdomain.MemoryRecord) *stubMaintenanceVectorStore {
	store := &stubMaintenanceVectorStore{
		current: make(map[string]logicdomain.MemoryRecord, len(initial)),
	}
	for _, record := range cloneVectorRebuildRecords(initial) {
		store.current[record.ID] = record
	}
	return store
}

// Upsert records one rebuilt vector row.
// Upsert 用于记录一条重建后的向量行。
func (s *stubMaintenanceVectorStore) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	s.upsertCalls++
	if s.upsertErrAtCall > 0 && s.upsertCalls == s.upsertErrAtCall {
		if s.upsertErr != nil {
			return s.upsertErr
		}
		return errors.New("stub sidecar upsert failed")
	}
	if s.expectedVectorDimension > 0 && len(record.Vector) != s.expectedVectorDimension {
		return errors.New("stub sidecar vector dimension mismatch")
	}
	if s.current == nil {
		s.current = make(map[string]logicdomain.MemoryRecord)
	}
	cloned := cloneVectorRebuildRecord(record)
	s.upserts = append(s.upserts, cloned)
	s.current[record.ID] = cloned
	return nil
}

// Search stays unused in these rebuild orchestration tests.
// Search 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceVectorStore) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, nil
}

// DeleteByFilter stays unused in these rebuild orchestration tests.
// DeleteByFilter 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceVectorStore) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	return 0, nil
}

// DeleteByIDs stays unused in these rebuild orchestration tests.
// DeleteByIDs 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceVectorStore) DeleteByIDs(context.Context, []string) (uint64, error) {
	return 0, nil
}

// Shutdown stays unused in these rebuild orchestration tests.
// Shutdown 在这些重建编排测试中保持未使用状态。
func (*stubMaintenanceVectorStore) Shutdown(context.Context) error {
	return nil
}

// RecreateTable records one split-mode sidecar table recreation.
// RecreateTable 用于记录一次分离模式旁路表重建。
func (s *stubMaintenanceVectorStore) RecreateTable(context.Context) error {
	s.recreated = true
	s.recreateCalls++
	s.current = make(map[string]logicdomain.MemoryRecord)
	return nil
}
