// workspace_test.go verifies the admin workspace use case keeps returning stable errors for direct-call and partial-construction scenarios.
// workspace_test.go 用于验证管理类 workspace 用例在直接调用和部分装配场景下仍会返回稳定错误。
package usecase

import (
	"context"
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
	projects []logicdomain.ProjectRecord
}

// ListProjects returns canned project records for deterministic admin assertions.
// ListProjects 用于返回预设项目记录，保证管理类断言稳定。
func (s *stubWorkspaceStore) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProjectRef keeps the test double interface-complete while current focused tests exercise nil-guard behavior and list reads.
// ResolveProjectRef 用于补齐测试替身接口，而当前聚焦测试只覆盖 nil 保护和列表读取。
func (s *stubWorkspaceStore) ResolveProjectRef(context.Context, string) (logicdomain.ProjectRecord, error) {
	return logicdomain.ProjectRecord{}, nil
}

// ListProjectMemories keeps the test double interface-complete while current focused tests do not rebuild vectors.
// ListProjectMemories 用于补齐测试替身接口，而当前聚焦测试不覆盖向量重建。
func (s *stubWorkspaceStore) ListProjectMemories(context.Context, uint64) ([]logicdomain.MemoryRecord, error) {
	return nil, nil
}

// EnsureProjectPath keeps the test double interface-complete while current focused tests do not cover project creation.
// EnsureProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目创建流程。
func (s *stubWorkspaceStore) EnsureProjectPath(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProjectPath keeps the test double interface-complete while current focused tests do not cover project deletion.
// DeleteProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目删除流程。
func (s *stubWorkspaceStore) DeleteProjectPath(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	return logicdomain.ProjectDeleteResult{}, nil
}

// MigrateProjectPath keeps the test double interface-complete while current focused tests do not cover project migration.
// MigrateProjectPath 用于补齐测试替身接口，而当前聚焦测试不覆盖项目迁移流程。
func (s *stubWorkspaceStore) MigrateProjectPath(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, nil
}

// ResolveUserRef keeps the test double interface-complete while current focused tests only verify the nil-store guard.
// ResolveUserRef 用于补齐测试替身接口，而当前聚焦测试只验证 nil store guard。
func (s *stubWorkspaceStore) ResolveUserRef(context.Context, string) (logicdomain.UserRecord, error) {
	return logicdomain.UserRecord{}, nil
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
	return logicdomain.UserDeleteResult{}, nil
}
