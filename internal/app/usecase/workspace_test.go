// workspace_test.go verifies the admin workspace use case keeps returning stable errors for direct-call and partial-construction scenarios.
// workspace_test.go 用于验证管理类 workspace 用例在直接调用和部分装配场景下仍会返回稳定错误。
package usecase

import (
	"context"
	"errors"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestWorkspaceUseCaseRejectsNilReceiver verifies the exported workspace use case returns one stable error instead of panicking when direct tests or manual integrations accidentally invoke it on a nil receiver.
// TestWorkspaceUseCaseRejectsNilReceiver 用于验证导出的 workspace 用例在直接测试或手工集成误把它调用到 nil 接收者上时，会返回稳定错误，而不是直接 panic。
func TestWorkspaceUseCaseRejectsNilReceiver(t *testing.T) {
	var uc *WorkspaceUseCase

	_, err := uc.ListProjects(context.Background())
	if err == nil || err.Error() != "workspace store is nil" {
		t.Fatalf("unexpected nil receiver error: %v", err)
	}
}

// TestWorkspaceUseCaseRejectsNilStore verifies the exported workspace use case fails fast with one stable error when partial construction omits the relational workspace store required by admin operations.
// TestWorkspaceUseCaseRejectsNilStore 用于验证当部分装配遗漏管理操作所需的关系型 workspace store 时，导出的 workspace 用例会快速返回稳定错误。
func TestWorkspaceUseCaseRejectsNilStore(t *testing.T) {
	uc := &WorkspaceUseCase{}

	_, err := uc.ResolveUser(context.Background(), "alice", false)
	if err == nil || err.Error() != "workspace store is nil" {
		t.Fatalf("unexpected nil store error: %v", err)
	}
}

// TestWorkspaceUseCaseListProjectsUsesStore verifies the guard helper does not change the normal admin path when one valid workspace store is present.
// TestWorkspaceUseCaseListProjectsUsesStore 用于验证当存在有效 workspace store 时，新的 guard helper 不会改变正常的管理查询路径。
func TestWorkspaceUseCaseListProjectsUsesStore(t *testing.T) {
	store := &stubWorkspaceStore{
		projects: []logicdomain.ProjectRecord{{ID: 9, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA"}},
	}
	uc := NewWorkspaceUseCase(store, nil)

	projects, err := uc.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != 9 {
		t.Fatalf("unexpected projects: %+v", projects)
	}
}

// stubWorkspaceStore supplies deterministic hierarchy and user records for workspace use-case tests.
// stubWorkspaceStore 用于为 workspace 用例测试提供确定性的层级和用户记录。
type stubWorkspaceStore struct {
	projects             []logicdomain.ProjectRecord
	resolvedProject      logicdomain.ProjectRecord
	resolvedProjectErr   error
	projectMemories      []logicdomain.MemoryRecord
	projectMemoriesErr   error
	resolvedUser         logicdomain.UserRecord
	resolvedUserErr      error
	deleteProjectResult  logicdomain.ProjectDeleteResult
	deleteProjectErr     error
	deleteProjectCalls   int
	migrateProjectResult logicdomain.ProjectMigrationResult
	migrateProjectErr    error
	migrateProjectCalls  int
	deleteUserResult     logicdomain.UserDeleteResult
	deleteUserErr        error
	deleteUserCalls      int
}

// stubWorkspaceVectorStore supplies one narrow vector-store double for workspace orchestration tests so local failure-path assertions do not affect broader shared vector test fixtures.
// stubWorkspaceVectorStore 用于给 workspace 编排测试提供一个狭窄的向量库替身，避免本地失败路径断言影响更广泛复用的共享向量测试夹具。
type stubWorkspaceVectorStore struct {
	upserts       []logicdomain.MemoryRecord
	deleteFilters []logicdomain.SearchFilter
	deleteIDs     [][]string
	upsertErr     error
	deleteErr     error
	deletedRows   uint64
}

// Upsert records one vector rebuild write and optionally returns the configured failure.
// Upsert 用于记录一次向量重建写入，并按需返回预设失败。
func (s *stubWorkspaceVectorStore) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	s.upserts = append(s.upserts, record)
	return s.upsertErr
}

// Search keeps the vector port interface-complete for workspace tests that never exercise recall behavior.
// Search 用于补齐 workspace 测试所需的向量端口接口，而这些测试本身不覆盖召回行为。
func (*stubWorkspaceVectorStore) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, nil
}

// DeleteByFilter records one destructive cleanup request and returns the configured row count or failure.
// DeleteByFilter 用于记录一次破坏性清理请求，并返回预设删除行数或失败。
func (s *stubWorkspaceVectorStore) DeleteByFilter(_ context.Context, filter logicdomain.SearchFilter) (uint64, error) {
	s.deleteFilters = append(s.deleteFilters, filter)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return s.deletedRows, nil
}

// DeleteByIDs records one explicit id-based cleanup request so the port stays interface-complete.
// DeleteByIDs 用于记录一次按 id 清理请求，保证该端口在测试中保持接口完整。
func (s *stubWorkspaceVectorStore) DeleteByIDs(_ context.Context, ids []string) (uint64, error) {
	s.deleteIDs = append(s.deleteIDs, append([]string(nil), ids...))
	return 0, nil
}

// Shutdown keeps the vector port interface-complete for direct workspace tests that never own background resources.
// Shutdown 用于补齐 workspace 测试中的向量端口关闭接口，而这些测试本身不持有后台资源。
func (*stubWorkspaceVectorStore) Shutdown(context.Context) error { return nil }

// ListProjects returns canned project records for deterministic admin assertions.
// ListProjects 用于返回预设项目记录，保证管理类断言稳定。
func (s *stubWorkspaceStore) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProjectRef keeps the test double interface-complete while current focused tests exercise nil-guard behavior and list reads.
// ResolveProjectRef 用于补齐测试替身接口，而当前聚焦测试只覆盖 nil 保护和列表读取。
func (s *stubWorkspaceStore) ResolveProjectRef(context.Context, string) (logicdomain.ProjectRecord, error) {
	return s.resolvedProject, s.resolvedProjectErr
}

// ListProjectMemories keeps the test double interface-complete while current focused tests do not rebuild vectors.
// ListProjectMemories 用于补齐测试替身接口，而当前聚焦测试不覆盖向量重建。
func (s *stubWorkspaceStore) ListProjectMemories(context.Context, uint64) ([]logicdomain.MemoryRecord, error) {
	return append([]logicdomain.MemoryRecord(nil), s.projectMemories...), s.projectMemoriesErr
}

// EnsureProjectPath keeps the test double interface-complete while current focused tests do not cover project creation.
// EnsureProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目创建流程。
func (s *stubWorkspaceStore) EnsureProjectPath(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProjectPath keeps the test double interface-complete while current focused tests do not cover project deletion.
// DeleteProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目删除流程。
func (s *stubWorkspaceStore) DeleteProjectPath(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	s.deleteProjectCalls++
	return s.deleteProjectResult, s.deleteProjectErr
}

// MigrateProjectPath keeps the test double interface-complete while current focused tests do not cover project migration.
// MigrateProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目迁移流程。
func (s *stubWorkspaceStore) MigrateProjectPath(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	s.migrateProjectCalls++
	return s.migrateProjectResult, s.migrateProjectErr
}

// ResolveUserRef keeps the test double interface-complete while current focused tests only verify the nil-store guard.
// ResolveUserRef 用于补齐测试替身接口，而当前聚焦测试只验证 nil store guard。
func (s *stubWorkspaceStore) ResolveUserRef(context.Context, string) (logicdomain.UserRecord, error) {
	return s.resolvedUser, s.resolvedUserErr
}

// EnsureUserName keeps the test double interface-complete while current focused tests do not cover user creation.
// EnsureUserName 用于补齐测试替身接口，而当前聚焦测试不覆盖用户创建流程。
func (s *stubWorkspaceStore) EnsureUserName(context.Context, string, bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, nil
}

// ListUsers keeps the test double interface-complete while current focused tests do not cover user listing.
// ListUsers 用于补齐测试替身接口，而当前聚焦测试不覆盖用户列表流程。
func (s *stubWorkspaceStore) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return nil, nil
}

// DeleteUserRef keeps the test double interface-complete while current focused tests do not cover user deletion.
// DeleteUserRef 用于补齐测试替身接口，而当前聚焦测试不覆盖用户删除流程。
func (s *stubWorkspaceStore) DeleteUserRef(context.Context, string, string) (logicdomain.UserDeleteResult, error) {
	s.deleteUserCalls++
	return s.deleteUserResult, s.deleteUserErr
}

// TestWorkspaceUseCaseDeleteProjectRejectsProtectedDefaultProject verifies the admin delete entry blocks the bootstrap default project before confirmation or vector cleanup can start.
// TestWorkspaceUseCaseDeleteProjectRejectsProtectedDefaultProject 用于验证管理删除入口会在确认和向量清理开始前拦截启动默认项目。
func TestWorkspaceUseCaseDeleteProjectRejectsProtectedDefaultProject(t *testing.T) {
	store := &stubWorkspaceStore{
		resolvedProject: logicdomain.ProjectRecord{
			ID:        1,
			TeamID:    1,
			SpaceID:   1,
			TeamName:  logicdomain.DefaultWorkspaceResourceName,
			SpaceName: logicdomain.DefaultWorkspaceResourceName,
			Name:      logicdomain.DefaultWorkspaceResourceName,
		},
	}
	vector := &stubWorkspaceVectorStore{}
	uc := NewWorkspaceUseCase(store, vector)

	_, err := uc.DeleteProject(context.Background(), logicdomain.DefaultProjectPath(), false)
	if !logicdomain.IsProtectedResourceError(err) {
		t.Fatalf("expected protected resource error, got %v", err)
	}
	if err == nil || err.Error() != "project: default project default/default/default cannot be deleted" {
		t.Fatalf("unexpected protected project error: %v", err)
	}
	if store.deleteProjectCalls != 0 {
		t.Fatalf("delete project should not reach store delete, got %d calls", store.deleteProjectCalls)
	}
	if len(vector.deleteFilters) != 0 {
		t.Fatalf("delete project should not touch vector store before rejection, got %+v", vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseDeleteProjectKeepsConfirmationNonDestructive verifies the first confirmation phase still returns the store response without deleting vectors early.
// TestWorkspaceUseCaseDeleteProjectKeepsConfirmationNonDestructive 用于验证第一次确认阶段会直接返回 store 响应，而不会提前删除向量。
func TestWorkspaceUseCaseDeleteProjectKeepsConfirmationNonDestructive(t *testing.T) {
	project := logicdomain.ProjectRecord{ID: 9, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA"}
	store := &stubWorkspaceStore{
		resolvedProject:     project,
		deleteProjectResult: logicdomain.ProjectDeleteResult{Project: project, Message: "confirm delete project TeamA/SpaceA/ProjectA", NeedsConfirm: true},
	}
	vector := &stubWorkspaceVectorStore{}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.DeleteProject(context.Background(), project.Path(), false)
	if err != nil {
		t.Fatalf("delete project confirmation phase: %v", err)
	}
	if !result.NeedsConfirm {
		t.Fatalf("expected confirmation response, got %+v", result)
	}
	if store.deleteProjectCalls != 1 {
		t.Fatalf("delete project should delegate one confirmation call, got %d", store.deleteProjectCalls)
	}
	if len(vector.deleteFilters) != 0 {
		t.Fatalf("delete project confirmation phase should not touch vectors, got %+v", vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseDeleteUserRejectsProtectedDefaultUser verifies the admin delete entry blocks the bootstrap default user before confirmation-code issuance or vector cleanup can start.
// TestWorkspaceUseCaseDeleteUserRejectsProtectedDefaultUser 用于验证管理删除入口会在确认码签发和向量清理开始前拦截启动默认用户。
func TestWorkspaceUseCaseDeleteUserRejectsProtectedDefaultUser(t *testing.T) {
	store := &stubWorkspaceStore{
		resolvedUser: logicdomain.UserRecord{
			ID:   1,
			Name: logicdomain.DefaultWorkspaceResourceName,
		},
	}
	vector := &stubWorkspaceVectorStore{}
	uc := NewWorkspaceUseCase(store, vector)

	_, err := uc.DeleteUser(context.Background(), logicdomain.DefaultWorkspaceResourceName, "")
	if !logicdomain.IsProtectedResourceError(err) {
		t.Fatalf("expected protected resource error, got %v", err)
	}
	if err == nil || err.Error() != "user: default user default cannot be deleted" {
		t.Fatalf("unexpected protected user error: %v", err)
	}
	if store.deleteUserCalls != 0 {
		t.Fatalf("delete user should not reach store delete, got %d calls", store.deleteUserCalls)
	}
	if len(vector.deleteFilters) != 0 {
		t.Fatalf("delete user should not touch vector store before rejection, got %+v", vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseDeleteUserKeepsConfirmationNonDestructive verifies the confirmation-code handshake still returns the store response without deleting vectors early.
// TestWorkspaceUseCaseDeleteUserKeepsConfirmationNonDestructive 用于验证确认码握手阶段会直接返回 store 响应，而不会提前删除向量。
func TestWorkspaceUseCaseDeleteUserKeepsConfirmationNonDestructive(t *testing.T) {
	user := logicdomain.UserRecord{ID: 7, Name: "alice"}
	store := &stubWorkspaceStore{
		resolvedUser:     user,
		deleteUserResult: logicdomain.UserDeleteResult{User: user, Message: "confirm deletion of user alice with the provided confirmation code", RequiresConfirmation: true, ConfirmationCode: "confirm-me"},
	}
	vector := &stubWorkspaceVectorStore{}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.DeleteUser(context.Background(), user.Name, "")
	if err != nil {
		t.Fatalf("delete user confirmation phase: %v", err)
	}
	if !result.RequiresConfirmation || result.ConfirmationCode != "confirm-me" {
		t.Fatalf("expected confirmation response, got %+v", result)
	}
	if store.deleteUserCalls != 1 {
		t.Fatalf("delete user should delegate one confirmation call, got %d", store.deleteUserCalls)
	}
	if len(vector.deleteFilters) != 0 {
		t.Fatalf("delete user confirmation phase should not touch vectors, got %+v", vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseDeleteProjectReturnsOutcomeUncertainAfterVectorCleanup verifies the delete flow surfaces one outcome-uncertain error when vectors are already gone but the relational delete fails afterwards.
// TestWorkspaceUseCaseDeleteProjectReturnsOutcomeUncertainAfterVectorCleanup 用于验证当向量已删除、但关系删除随后失败时，项目删除流程会返回结果不确定错误。
func TestWorkspaceUseCaseDeleteProjectReturnsOutcomeUncertainAfterVectorCleanup(t *testing.T) {
	project := logicdomain.ProjectRecord{ID: 9, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA"}
	store := &stubWorkspaceStore{
		resolvedProject:  project,
		deleteProjectErr: errors.New("sql timeout"),
	}
	vector := &stubWorkspaceVectorStore{deletedRows: 4}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.DeleteProject(context.Background(), project.Path(), true)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome uncertain error, got %v", err)
	}
	if result.DeletedVectorRows != 4 {
		t.Fatalf("expected deleted vector rows to be preserved, got %+v", result)
	}
	if store.deleteProjectCalls != 1 {
		t.Fatalf("expected one relational delete call, got %d", store.deleteProjectCalls)
	}
	if len(vector.deleteFilters) != 1 || vector.deleteFilters[0].ProjectID != project.ID {
		t.Fatalf("expected one vector delete filter for project %d, got %+v", project.ID, vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseDeleteUserReturnsOutcomeUncertainAfterVectorCleanup verifies the delete flow surfaces one outcome-uncertain error when user vectors are already gone but the relational delete fails afterwards.
// TestWorkspaceUseCaseDeleteUserReturnsOutcomeUncertainAfterVectorCleanup 用于验证当用户向量已删除、但关系删除随后失败时，用户删除流程会返回结果不确定错误。
func TestWorkspaceUseCaseDeleteUserReturnsOutcomeUncertainAfterVectorCleanup(t *testing.T) {
	user := logicdomain.UserRecord{ID: 7, Name: "alice", DeleteConfirmCode: "confirm-me"}
	store := &stubWorkspaceStore{
		resolvedUser:  user,
		deleteUserErr: errors.New("commit timeout"),
	}
	vector := &stubWorkspaceVectorStore{deletedRows: 2}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.DeleteUser(context.Background(), user.Name, user.DeleteConfirmCode)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome uncertain error, got %v", err)
	}
	if result.DeletedVectorRows != 2 {
		t.Fatalf("expected deleted vector rows to be preserved, got %+v", result)
	}
	if store.deleteUserCalls != 1 {
		t.Fatalf("expected one relational delete call, got %d", store.deleteUserCalls)
	}
	if len(vector.deleteFilters) != 1 || vector.deleteFilters[0].UserID != user.ID {
		t.Fatalf("expected one vector delete filter for user %d, got %+v", user.ID, vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenTargetVectorRebuildFails verifies the migration flow reports one outcome-uncertain error once SQL migration has committed but target vector rebuild cannot finish.
// TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenTargetVectorRebuildFails 用于验证当 SQL 迁移已提交、但目标向量重建无法完成时，项目迁移流程会返回结果不确定错误。
func TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenTargetVectorRebuildFails(t *testing.T) {
	source := logicdomain.ProjectRecord{ID: 9, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA"}
	target := logicdomain.ProjectRecord{ID: 10, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectB"}
	store := &stubWorkspaceStore{
		migrateProjectResult: logicdomain.ProjectMigrationResult{Source: source, Target: target, Message: "project migrated"},
		projectMemories: []logicdomain.MemoryRecord{{
			ID:     "vec-1",
			Text:   "hello",
			Vector: []float32{0.1, 0.2},
			Filter: logicdomain.SearchFilter{ProjectID: target.ID},
		}},
	}
	vector := &stubWorkspaceVectorStore{upsertErr: errors.New("lancedb down")}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.MigrateProject(context.Background(), source.Path(), target.Path(), true)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome uncertain error, got %v", err)
	}
	if store.migrateProjectCalls != 1 {
		t.Fatalf("expected one relational migration call, got %d", store.migrateProjectCalls)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one attempted vector rebuild write, got %+v", vector.upserts)
	}
	if result.RebuiltVectorRows != 0 {
		t.Fatalf("expected no completed vector rebuild rows, got %+v", result)
	}
	if len(vector.deleteFilters) != 0 {
		t.Fatalf("source vector cleanup should not run after rebuild failure, got %+v", vector.deleteFilters)
	}
}

// TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenSourceVectorCleanupFails verifies the migration flow preserves the committed SQL result and rebuilt row count when only the final source-vector cleanup fails.
// TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenSourceVectorCleanupFails 用于验证当只有最后的源向量清理失败时，项目迁移流程仍会保留已提交 SQL 结果和已重建行数，并返回结果不确定错误。
func TestWorkspaceUseCaseMigrateProjectReturnsOutcomeUncertainWhenSourceVectorCleanupFails(t *testing.T) {
	source := logicdomain.ProjectRecord{ID: 9, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectA"}
	target := logicdomain.ProjectRecord{ID: 10, TeamID: 3, SpaceID: 5, TeamName: "TeamA", SpaceName: "SpaceA", Name: "ProjectB"}
	store := &stubWorkspaceStore{
		migrateProjectResult: logicdomain.ProjectMigrationResult{Source: source, Target: target, Message: "project migrated"},
		projectMemories: []logicdomain.MemoryRecord{{
			ID:     "vec-1",
			Text:   "hello",
			Vector: []float32{0.1, 0.2},
			Filter: logicdomain.SearchFilter{ProjectID: target.ID},
		}},
	}
	vector := &stubWorkspaceVectorStore{deleteErr: errors.New("delete source vectors failed")}
	uc := NewWorkspaceUseCase(store, vector)

	result, err := uc.MigrateProject(context.Background(), source.Path(), target.Path(), true)
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome uncertain error, got %v", err)
	}
	if result.RebuiltVectorRows != 1 {
		t.Fatalf("expected one completed vector rebuild row before cleanup failure, got %+v", result)
	}
	if len(vector.deleteFilters) != 1 || vector.deleteFilters[0].ProjectID != source.ID {
		t.Fatalf("expected one source vector cleanup filter for project %d, got %+v", source.ID, vector.deleteFilters)
	}
}
