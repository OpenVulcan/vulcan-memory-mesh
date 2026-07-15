// vector_schema_sync_test.go verifies startup-time vector schema coordination only rebuilds LanceDB when the tracked vector schema version actually changes.
// vector_schema_sync_test.go 用于验证启动期向量 schema 协调只会在跟踪到的向量版本变化时重建 LanceDB。
package app

import (
	"context"
	"errors"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestEnsureVectorSchemaSkipsRebuildWhenVersionAlreadyCurrent verifies SQL-only changes do not trigger an unnecessary vector-table rebuild.
// TestEnsureVectorSchemaSkipsRebuildWhenVersionAlreadyCurrent 用于验证当向量版本已经是最新时，纯 SQL 变化不会触发不必要的向量表重建。
func TestEnsureVectorSchemaSkipsRebuildWhenVersionAlreadyCurrent(t *testing.T) {
	versions := &stubSchemaVersionStore{version: vldb_lancedb.CurrentSchemaVersion}
	workspace := &stubVectorSchemaWorkspaceStore{}
	vector := &stubVectorSchemaStore{}

	if err := ensureVectorSchema(context.Background(), versions, workspace, vector, nil); err != nil {
		t.Fatalf("ensure vector schema: %v", err)
	}
	if vector.recreateCalls != 0 {
		t.Fatalf("expected no recreate call, got %d", vector.recreateCalls)
	}
	if len(vector.upserts) != 0 {
		t.Fatalf("expected no upserts, got %#v", vector.upserts)
	}
	if versions.setCalls != 0 {
		t.Fatalf("expected no version write, got %d", versions.setCalls)
	}
}

// TestEnsureVectorSchemaRebuildsAndPersistsVersion verifies a tracked vector-schema mismatch recreates the LanceDB table and repopulates it from SQLite records.
// TestEnsureVectorSchemaRebuildsAndPersistsVersion 用于验证当向量 schema 版本不匹配时，会重建 LanceDB 表并从 SQLite 记录回灌。
func TestEnsureVectorSchemaRebuildsAndPersistsVersion(t *testing.T) {
	versions := &stubSchemaVersionStore{version: 0}
	workspace := &stubVectorSchemaWorkspaceStore{
		projects: []logicdomain.ProjectRecord{
			{ID: 9},
		},
		projectMemories: map[uint64][]logicdomain.MemoryRecord{
			9: {
				{
					ID:           "vec-1",
					Text:         "phase4 current plan",
					Filter:       logicdomain.SearchFilter{ProjectID: 9, SessionID: 41},
					SourceTurnID: 88,
				},
			},
		},
	}
	vector := &stubVectorSchemaStore{}

	if err := ensureVectorSchema(context.Background(), versions, workspace, vector, nil); err != nil {
		t.Fatalf("ensure vector schema: %v", err)
	}
	if vector.recreateCalls != 1 {
		t.Fatalf("expected one recreate call, got %d", vector.recreateCalls)
	}
	if len(vector.upserts) != 1 || vector.upserts[0].SourceTurnID != 88 {
		t.Fatalf("unexpected rebuilt vector rows: %#v", vector.upserts)
	}
	if versions.lastComponent != "lancedb" || versions.lastVersion != vldb_lancedb.CurrentSchemaVersion {
		t.Fatalf("unexpected persisted version write: component=%q version=%d", versions.lastComponent, versions.lastVersion)
	}
}

// TestEnsureVectorSchemaMarksUpsertFailureAfterRecreateOutcomeUncertain verifies startup schema sync reports partial side effects once the LanceDB table has been recreated.
// TestEnsureVectorSchemaMarksUpsertFailureAfterRecreateOutcomeUncertain 用于验证启动期 schema 同步在 LanceDB 表已重建后，会把回灌失败报告为存在局部副作用。
func TestEnsureVectorSchemaMarksUpsertFailureAfterRecreateOutcomeUncertain(t *testing.T) {
	versions := &stubSchemaVersionStore{version: 0}
	workspace := &stubVectorSchemaWorkspaceStore{
		projects: []logicdomain.ProjectRecord{{ID: 10}},
		projectMemories: map[uint64][]logicdomain.MemoryRecord{
			10: {
				{ID: "vec-10-a", Text: "schema row a"},
				{ID: "vec-10-b", Text: "schema row b"},
			},
		},
	}
	vector := &stubVectorSchemaStore{upsertErrAtCall: 2, upsertErr: errors.New("lancedb upsert failed")}

	err := ensureVectorSchema(context.Background(), versions, workspace, vector, nil)
	if err == nil {
		t.Fatal("expected vector schema upsert failure")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain schema sync error, got %T %v", err, err)
	}
	if vector.recreateCalls != 1 || len(vector.upserts) != 1 {
		t.Fatalf("unexpected vector sync progress: recreate=%d upserts=%d", vector.recreateCalls, len(vector.upserts))
	}
	if versions.setCalls != 0 {
		t.Fatalf("schema version should not be persisted after row failure, got %d calls", versions.setCalls)
	}
}

// TestEnsureVectorSchemaMarksVersionPersistFailureAfterRebuildOutcomeUncertain verifies version-write failures keep the rebuilt sidecar state visible to callers.
// TestEnsureVectorSchemaMarksVersionPersistFailureAfterRebuildOutcomeUncertain 用于验证版本写入失败时，调用方仍能看到 sidecar 已完成重建这一状态。
func TestEnsureVectorSchemaMarksVersionPersistFailureAfterRebuildOutcomeUncertain(t *testing.T) {
	versions := &stubSchemaVersionStore{version: 0, setErr: errors.New("schema version write failed")}
	workspace := &stubVectorSchemaWorkspaceStore{
		projects: []logicdomain.ProjectRecord{{ID: 11}},
		projectMemories: map[uint64][]logicdomain.MemoryRecord{
			11: {
				{ID: "vec-11", Text: "schema row eleven"},
			},
		},
	}
	vector := &stubVectorSchemaStore{}

	err := ensureVectorSchema(context.Background(), versions, workspace, vector, nil)
	if err == nil {
		t.Fatal("expected schema version persistence failure")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain version persistence error, got %T %v", err, err)
	}
	if vector.recreateCalls != 1 || len(vector.upserts) != 1 {
		t.Fatalf("unexpected rebuilt vector rows: recreate=%d upserts=%d", vector.recreateCalls, len(vector.upserts))
	}
	if versions.version != 0 {
		t.Fatalf("stub version should remain old after failed persistence, got %d", versions.version)
	}
}

// stubSchemaVersionStore is the startup version-tracking double used by vector schema sync tests.
// stubSchemaVersionStore 用于作为向量 schema 协调测试里的启动期版本跟踪桩。
type stubSchemaVersionStore struct {
	version       int
	lastComponent string
	lastVersion   int
	setCalls      int
	setErr        error
}

// GetSchemaComponentVersion executes the stubbed version lookup logic.
// GetSchemaComponentVersion 用于执行桩化的版本查询逻辑。
func (s *stubSchemaVersionStore) GetSchemaComponentVersion(context.Context, string) (int, error) {
	return s.version, nil
}

// SetSchemaComponentVersion executes the stubbed version persistence logic.
// SetSchemaComponentVersion 用于执行桩化的版本持久化逻辑。
func (s *stubSchemaVersionStore) SetSchemaComponentVersion(_ context.Context, component string, version int) error {
	s.setCalls++
	if s.setErr != nil {
		return s.setErr
	}
	s.lastComponent = component
	s.lastVersion = version
	s.version = version
	return nil
}

// stubVectorSchemaWorkspaceStore is the workspace double used to provide deterministic project/memory rows for vector schema rebuild tests.
// stubVectorSchemaWorkspaceStore 用于为向量 schema 重建测试提供确定性的项目与记忆行。
type stubVectorSchemaWorkspaceStore struct {
	projects        []logicdomain.ProjectRecord
	projectMemories map[uint64][]logicdomain.MemoryRecord
}

// ListProjects executes the stubbed project listing logic.
// ListProjects 用于执行桩化的项目列表逻辑。
func (s *stubVectorSchemaWorkspaceStore) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProjectRef keeps the stub interface-complete for tests that only exercise vector schema coordination.
// ResolveProjectRef 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) ResolveProjectRef(context.Context, string) (logicdomain.ProjectRecord, error) {
	return logicdomain.ProjectRecord{}, nil
}

// ListProjectMemories executes the stubbed per-project memory listing logic.
// ListProjectMemories 用于执行桩化的按项目记忆列表逻辑。
func (s *stubVectorSchemaWorkspaceStore) ListProjectMemories(_ context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	rows := s.projectMemories[projectID]
	return append([]logicdomain.MemoryRecord(nil), rows...), nil
}

// EnsureProjectPath keeps the stub interface-complete for tests that only exercise vector schema coordination.
// EnsureProjectPath 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) EnsureProjectPath(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProjectPath keeps the stub interface-complete for tests that only exercise vector schema coordination.
// DeleteProjectPath 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) DeleteProjectPath(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	return logicdomain.ProjectDeleteResult{}, nil
}

// MigrateProjectPath keeps the stub interface-complete for tests that only exercise vector schema coordination.
// MigrateProjectPath 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) MigrateProjectPath(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, nil
}

// ResolveUserRef keeps the stub interface-complete for tests that only exercise vector schema coordination.
// ResolveUserRef 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) ResolveUserRef(context.Context, string) (logicdomain.UserRecord, error) {
	return logicdomain.UserRecord{}, nil
}

// EnsureUserName keeps the stub interface-complete for tests that only exercise vector schema coordination.
// EnsureUserName 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) EnsureUserName(context.Context, string, bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, nil
}

// ListUsers keeps the stub interface-complete for tests that only exercise vector schema coordination.
// ListUsers 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return nil, nil
}

// DeleteUserRef keeps the stub interface-complete for tests that only exercise vector schema coordination.
// DeleteUserRef 用于在仅测试向量 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaWorkspaceStore) DeleteUserRef(context.Context, string, string) (logicdomain.UserDeleteResult, error) {
	return logicdomain.UserDeleteResult{}, nil
}

// stubVectorSchemaStore is the vector-store double used by vector schema sync tests.
// stubVectorSchemaStore 用于作为向量 schema 协调测试里的向量存储桩。
type stubVectorSchemaStore struct {
	recreateCalls   int
	upsertCalls     int
	upsertErrAtCall int
	upsertErr       error
	upserts         []logicdomain.MemoryRecord
}

// Upsert records one rebuilt vector row.
// Upsert 用于记录一条被回灌的向量行。
func (s *stubVectorSchemaStore) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	s.upsertCalls++
	if s.upsertErrAtCall > 0 && s.upsertCalls == s.upsertErrAtCall {
		if s.upsertErr != nil {
			return s.upsertErr
		}
		return errors.New("stub lancedb schema upsert failed")
	}
	s.upserts = append(s.upserts, record)
	return nil
}

// Search keeps the stub interface-complete for tests that only exercise startup schema coordination.
// Search 用于在仅测试启动期 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaStore) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, nil
}

// DeleteByFilter keeps the stub interface-complete for tests that only exercise startup schema coordination.
// DeleteByFilter 用于在仅测试启动期 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaStore) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	return 0, nil
}

// DeleteByIDs keeps the stub interface-complete for tests that only exercise startup schema coordination.
// DeleteByIDs 用于在仅测试启动期 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaStore) DeleteByIDs(context.Context, []string) (uint64, error) {
	return 0, nil
}

// Shutdown keeps the stub interface-complete for tests that only exercise startup schema coordination.
// Shutdown 用于在仅测试启动期 schema 协调的场景下保持桩对象接口完整。
func (s *stubVectorSchemaStore) Shutdown(context.Context) error {
	return nil
}

// RecreateTable records one schema-recreation request.
// RecreateTable 用于记录一次 schema 重建请求。
func (s *stubVectorSchemaStore) RecreateTable(context.Context) error {
	s.recreateCalls++
	return nil
}
