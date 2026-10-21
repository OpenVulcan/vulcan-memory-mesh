// SQLite adapter tests for project mutations test.
// SQLite 适配层项目变更测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// projectDeleteQueryResponder returns a QueryJSON fake for project resolution, delete planning counts, and profile-count rows used by DeleteProjectPath tests.
// projectDeleteQueryResponder 用于返回 DeleteProjectPath 测试所需的项目解析、删除规划计数和画像计数 QueryJSON 替身。
func projectDeleteQueryResponder(t *testing.T, sessions, messages, memories, remainingProjects, remainingSpaces, profiles int) func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
	t.Helper()
	return func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[0].Kind != sqliteffi.SQLValueString || params[0].String != "TeamA" ||
				params[1].Kind != sqliteffi.SQLValueString || params[1].String != "SpaceA" ||
				params[2].Kind != sqliteffi.SQLValueString || params[2].String != "ProjectA" {
				t.Fatalf("expected typed project path params, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":101,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-05T00:00:00Z",
				"updated_at":"2026-07-05T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, sessions)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, messages)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes WHERE project_id = ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, memories)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_projects WHERE space_id = ? AND id <> ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, remainingProjects)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_spaces WHERE team_id = ? AND id <> ?"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, remainingSpaces)}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes WHERE"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"count":%d}]`, profiles)}, nil
		default:
			t.Fatalf("unexpected project delete query: %q", sql)
			return nil, nil
		}
	}
}

// TestStoreDeleteProjectPathChecksPlannedRowsChanged verifies every destructive project-delete statement must match the row counts computed by the delete plan.
// TestStoreDeleteProjectPathChecksPlannedRowsChanged 用于验证项目删除的每条破坏性语句都必须匹配删除计划计算出的行数。
func TestStoreDeleteProjectPathChecksPlannedRowsChanged(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 7)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 4}, nil
		case strings.Contains(sql, "DELETE FROM vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "DELETE FROM vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "DELETE FROM vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "DELETE FROM vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "DELETE FROM vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected project delete statement: %q", sql)
			return nil, nil
		}
	}

	result, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("DeleteProjectPath returned error: %v", err)
	}
	if result.DeletedProjects != 1 || result.DeletedSpaces != 1 || result.DeletedTeams != 1 ||
		result.DeletedSessions != 2 || result.DeletedMessages != 3 || result.DeletedMemories != 4 || result.DeletedProfiles != 5 {
		t.Fatalf("unexpected project delete result: %+v", result)
	}
	if len(executed) != 7 {
		t.Fatalf("expected seven checked delete statements, got %d", len(executed))
	}
}

// TestStoreDeleteProjectPathRejectsRowsChangedDrift verifies row-count drift stops the project delete before later relational rows are removed.
// TestStoreDeleteProjectPathRejectsRowsChangedDrift 用于验证行数漂移会中止项目删除，避免继续删除后续关系行。
func TestStoreDeleteProjectPathRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		default:
			t.Fatalf("unexpected project delete statement after row-count drift: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project delete drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project memory nodes affected 3 rows, want 4") {
		t.Fatalf("expected memory row-count drift detail, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after memory row drift, got %d statements", len(executed))
	}
}

// TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain verifies the first destructive delete step preserves SQLite commit-boundary ambiguity.
// TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain 用于验证首个破坏性删除步骤会保留 SQLite 提交边界不确定语义。
func TestStoreDeleteProjectPathMarksInitialCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if !strings.Contains(req.GetSql(), "DELETE FROM vmm_profile_nodes") {
			t.Fatalf("expected first project delete step to delete profiles, got %q", req.GetSql())
		}
		return nil, errors.New("failed to commit project profile delete")
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected initial project delete commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project profile nodes") || !strings.Contains(err.Error(), "failed to commit project profile delete") {
		t.Fatalf("unexpected initial project delete commit error: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected delete to stop after first commit-unknown step, got %d statements", len(executed))
	}
}

// TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain verifies ordinary later failures are unsafe after earlier destructive deletes committed.
// TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain 用于验证前序破坏性删除已提交后，后续普通失败也不能被当作干净失败。
func TestStoreDeleteProjectPathMarksLaterErrorAfterMutationUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = projectDeleteQueryResponder(t, 2, 3, 4, 0, 0, 5)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "DELETE FROM vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "DELETE FROM vmm_memory_nodes"):
			return nil, errors.New("project memory delete unavailable")
		default:
			t.Fatalf("unexpected project delete statement after prior mutation: %q", sql)
			return nil, nil
		}
	}

	_, err := store.DeleteProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected later project delete error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete project memory nodes") || !strings.Contains(err.Error(), "project memory delete unavailable") {
		t.Fatalf("unexpected later project delete error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected delete to stop after second statement failed, got %d statements", len(executed))
	}
}

// TestStoreMigrateProjectPathUsesTypedSQLiteParams verifies project migration writes bind hierarchy ids instead of rendering them into SQL text.
// TestStoreMigrateProjectPathUsesTypedSQLiteParams 用于验证项目迁移写入会绑定层级 id，而不是把它们渲染进 SQL 文本。
func TestStoreMigrateProjectPathUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "UPDATE vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "UPDATE vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 3}, nil
		case strings.Contains(sql, "UPDATE vmm_profile_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected project migration statement: %q", sql)
			return nil, nil
		}
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			switch params[0].Int64 {
			case 101:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case 202:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":5}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":1}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err != nil {
		t.Fatalf("MigrateProjectPath returned error: %v", err)
	}
	if result.MigratedSessions != 2 || result.MigratedMessages != 5 || result.MigratedMemories != 3 {
		t.Fatalf("unexpected migration counts: %+v", result)
	}
	if len(executed) != 4 {
		t.Fatalf("expected four migration updates, got %d", len(executed))
	}
	for _, req := range executed {
		for _, leaked := range []string{"101", "202", "301", "302", "401", "402"} {
			if strings.Contains(req.GetSql(), leaked) {
				t.Fatalf("expected migration id %q to stay out of SQL text, got %q", leaked, req.GetSql())
			}
		}
	}

	sessionParams := executed[0].GetParams()
	if len(sessionParams) != 5 {
		t.Fatalf("expected five session migration params, got %d: %#v", len(sessionParams), sessionParams)
	}
	requireSQLiteInt64Params(t, sessionParams[:3], 302, 402, 202)
	if sessionParams[3].Kind != sqliteffi.SQLValueInt64 || sessionParams[3].Int64 <= 0 {
		t.Fatalf("expected session migration timestamp param, got %#v", sessionParams[3])
	}
	requireSQLiteInt64Params(t, sessionParams[4:], 101)

	turnParams := executed[1].GetParams()
	if len(turnParams) != 3 {
		t.Fatalf("expected three turn migration params, got %d: %#v", len(turnParams), turnParams)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{turnParams[0]}, 202)
	if turnParams[1].Kind != sqliteffi.SQLValueInt64 || turnParams[1].Int64 <= 0 {
		t.Fatalf("expected turn migration timestamp param, got %#v", turnParams[1])
	}
	requireSQLiteInt64Params(t, turnParams[2:], 101)

	memoryParams := executed[2].GetParams()
	if len(memoryParams) != 5 {
		t.Fatalf("expected five memory migration params, got %d: %#v", len(memoryParams), memoryParams)
	}
	requireSQLiteInt64Params(t, memoryParams[:3], 302, 402, 202)
	if memoryParams[3].Kind != sqliteffi.SQLValueInt64 || memoryParams[3].Int64 <= 0 {
		t.Fatalf("expected memory migration timestamp param, got %#v", memoryParams[3])
	}
	requireSQLiteInt64Params(t, memoryParams[4:], 101)

	profileParams := executed[3].GetParams()
	if len(profileParams) != 3 {
		t.Fatalf("expected three profile migration params, got %d: %#v", len(profileParams), profileParams)
	}
	requireSQLiteInt64Params(t, profileParams, 202, int64(logicdomain.ProfileTypeProject), 101)
}

// TestStoreMigrateProjectPathRejectsRowsChangedDrift verifies migration stops before later writes when SQLite reports fewer moved rows than the migration plan counted.
// TestStoreMigrateProjectPathRejectsRowsChangedDrift 用于验证当 SQLite 报告的迁移行数少于迁移计划时，迁移会在后续写入前停止。
func TestStoreMigrateProjectPathRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "UPDATE vmm_sessions"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		case strings.Contains(sql, "UPDATE vmm_turn_records"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 5}, nil
		case strings.Contains(sql, "UPDATE vmm_memory_nodes"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		default:
			t.Fatalf("unexpected project migration statement after row-count drift: %q", sql)
			return nil, nil
		}
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			switch params[0].Int64 {
			case 101:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case 202:
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":5}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "COUNT(*) AS count FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":1}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project migration drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "migrate project memory nodes affected 2 rows, want 3") {
		t.Fatalf("expected memory migration row-count drift detail, got %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("expected migration to stop before profile update, got %d statements", len(executed))
	}
}

// TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock verifies stale source resolution is rechecked under the write lock before migration updates run.
// TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock 用于验证陈旧源项目解析会在写锁下重新校验，并在迁移更新执行前被拒绝。
func TestStoreMigrateProjectPathRejectsStaleEndpointAfterWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		t.Fatalf("expected stale migration endpoint to stop before writes, got %q", req.GetSql())
		return nil, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			params := req.GetParams()
			if len(params) != 3 || params[2].Kind != sqliteffi.SQLValueString {
				t.Fatalf("expected project path params, got %#v", params)
			}
			switch params[2].String {
			case "ProjectA":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":101,
					"team_id":301,
					"space_id":401,
					"name":"ProjectA",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamA",
					"space_name":"SpaceA"
				}]`}, nil
			case "ProjectB":
				return &fakeQueryJSONResponse{JsonData: `[{
					"id":202,
					"team_id":302,
					"space_id":402,
					"name":"ProjectB",
					"profile":"",
					"created_at":"2026-07-05T00:00:00Z",
					"updated_at":"2026-07-05T00:00:00Z",
					"team_name":"TeamB",
					"space_name":"SpaceB"
				}]`}, nil
			default:
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.id = ?"):
			params := req.GetParams()
			if len(params) != 1 || params[0].Kind != sqliteffi.SQLValueInt64 {
				t.Fatalf("expected project id param, got %#v", params)
			}
			if params[0].Int64 != 101 {
				t.Fatalf("expected source project to be reloaded first, got %#v", params)
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":101,
				"team_id":301,
				"space_id":401,
				"name":"ProjectARecreated",
				"profile":"",
				"created_at":"2026-07-05T00:00:00Z",
				"updated_at":"2026-07-05T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected stale endpoint query: %q", sql)
			return nil, nil
		}
	}

	_, err := store.MigrateProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", "TeamB/SpaceB/ProjectB", true)
	if err == nil || !logicdomain.IsConflictError(err) {
		t.Fatalf("expected stale migration endpoint to be a conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "source project changed before migration") {
		t.Fatalf("expected stale source detail, got %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("expected stale endpoint to stop before writes, got %d statements", len(executed))
	}
}

// TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock verifies a project created after the first lookup is returned as existing instead of inserting duplicates.
// TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock 用于验证第一次查询后被创建的项目会在写锁内作为已存在项目返回，而不是继续插入重复层级。
func TestStoreEnsureProjectPathRechecksHierarchyUnderWriteLock(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	teamLookups := 0
	spaceLookups := 0
	projectLookups := 0
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			teamLookups++
			return &fakeQueryJSONResponse{JsonData: `[{"id":301,"name":"TeamA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			spaceLookups++
			return &fakeQueryJSONResponse{JsonData: `[{"id":401,"team_id":301,"name":"SpaceA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			projectLookups++
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":501,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected ensure project query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		t.Fatalf("expected locked project recheck to stop before inserts, got %q", req.GetSql())
		return nil, nil
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath returned error: %v", err)
	}
	if !result.Exists || result.Project.ID != 501 || result.CreatedTeam || result.CreatedSpace || result.CreatedProject {
		t.Fatalf("expected existing project result after locked recheck, got %+v", result)
	}
	if teamLookups != 2 || spaceLookups != 2 || projectLookups != 1 {
		t.Fatalf("unexpected locked lookup counts: teams=%d spaces=%d projects=%d", teamLookups, spaceLookups, projectLookups)
	}
}

// TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift verifies hierarchy creation stops with an uncertain result when the final project insert does not report one inserted row.
// TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift 用于验证当最终 project 插入没有报告一条写入时，层级创建会以结果不确定状态停止。
func TestStoreEnsureProjectPathRejectsInsertRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			t.Fatalf("unexpected ensure project drift query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		default:
			t.Fatalf("unexpected ensure project drift write: %q", sql)
			return nil, nil
		}
	}

	_, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected project insert drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "insert project") || !strings.Contains(err.Error(), "affected 0 rows, want 1") {
		t.Fatalf("expected project insert row-count detail, got %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("expected team, space, and project insert attempts, got %d", len(executed))
	}
}

// TestStoreEnsureProjectPathReconcilesParentCommitUnknown verifies ambiguous Team/Space inserts are recovered by durable unique-key readback before project creation continues.
// TestStoreEnsureProjectPathReconcilesParentCommitUnknown 用于验证 Team/Space 插入提交结果不明时，会通过长期唯一键回读恢复并继续创建 project。
func TestStoreEnsureProjectPathReconcilesParentCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	teamLookups := 0
	spaceLookups := 0
	projectLookups := 0
	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			teamLookups++
			if teamLookups < 3 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":301,"name":"TeamA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			spaceLookups++
			if spaceLookups < 2 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{"id":401,"team_id":301,"name":"SpaceA","profile":"","created_at":"2026-07-06T00:00:00Z","updated_at":"2026-07-06T00:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			projectLookups++
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			t.Fatalf("unexpected ensure project parent reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return nil, errors.New("failed to commit team insert")
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return nil, errors.New("failed to commit space insert")
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		default:
			t.Fatalf("unexpected ensure project parent reconcile write: %q", sql)
			return nil, nil
		}
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath should recover parent commit-unknown inserts: %v", err)
	}
	if result.Project.ID != 501 || !result.CreatedTeam || !result.CreatedSpace || !result.CreatedProject {
		t.Fatalf("expected recovered parent hierarchy and created project, got %+v", result)
	}
	if teamLookups != 3 || spaceLookups != 2 || projectLookups != 1 {
		t.Fatalf("unexpected reconcile lookup counts: teams=%d spaces=%d projects=%d", teamLookups, spaceLookups, projectLookups)
	}
	if len(executed) != 3 {
		t.Fatalf("expected three hierarchy insert attempts, got %d", len(executed))
	}
}

// TestStoreEnsureProjectPathReconcilesProjectCommitUnknown verifies the final project insert can recover success by reading back the scoped project row.
// TestStoreEnsureProjectPathReconcilesProjectCommitUnknown 用于验证最终 project 插入提交结果不明时，可以通过范围内 project 行回读恢复成功。
func TestStoreEnsureProjectPathReconcilesProjectCommitUnknown(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	scopedProjectLookups := 0
	executed := make([]*fakeExecuteRequest, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_teams"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_spaces"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":401}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":501}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE t.name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_teams") && strings.Contains(sql, "WHERE name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_spaces") && strings.Contains(sql, "WHERE team_id = ? AND name = ?"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p") && strings.Contains(sql, "WHERE p.space_id = ? AND p.name = ?"):
			scopedProjectLookups++
			if scopedProjectLookups == 1 {
				return &fakeQueryJSONResponse{JsonData: `[]`}, nil
			}
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":501,
				"team_id":301,
				"space_id":401,
				"name":"ProjectA",
				"profile":"",
				"created_at":"2026-07-06T00:00:00Z",
				"updated_at":"2026-07-06T00:00:00Z",
				"team_name":"TeamA",
				"space_name":"SpaceA"
			}]`}, nil
		default:
			t.Fatalf("unexpected ensure project reconcile query: %q", sql)
			return nil, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "INSERT INTO vmm_teams"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_spaces"):
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		case strings.Contains(sql, "INSERT INTO vmm_projects"):
			return nil, errors.New("failed to commit project insert")
		default:
			t.Fatalf("unexpected ensure project reconcile write: %q", sql)
			return nil, nil
		}
	}

	result, err := store.EnsureProjectPath(context.Background(), "TeamA/SpaceA/ProjectA", true)
	if err != nil {
		t.Fatalf("EnsureProjectPath should recover project commit-unknown insert: %v", err)
	}
	if result.Project.ID != 501 || !result.CreatedTeam || !result.CreatedSpace || !result.CreatedProject {
		t.Fatalf("expected recovered project creation result, got %+v", result)
	}
	if scopedProjectLookups != 2 {
		t.Fatalf("expected pre-insert project lookup plus reconcile readback, got %d", scopedProjectLookups)
	}
	if len(executed) != 3 {
		t.Fatalf("expected three hierarchy insert attempts, got %d", len(executed))
	}
}

// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams verifies delete/count helpers emit placeholder SQL plus stable param ordering.
// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams 用于验证删除/统计辅助逻辑会输出占位符 SQL 与稳定参数顺序。
func TestBuildProjectProfileNodesDeleteSQLUsesTypedParams(t *testing.T) {
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(9, 5, 3, true, true)
	if strings.Contains(deleteSQL, "bind_id = 9") || strings.Count(deleteSQL, "?") != 7 {
		t.Fatalf("unexpected delete sql: %q", deleteSQL)
	}
	expected := []any{
		logicdomain.ProfileTypeProject, uint64(9), uint64(9),
		logicdomain.ProfileTypeSpace, uint64(5),
		logicdomain.ProfileTypeTeam, uint64(3),
	}
	if fmt.Sprint(deleteParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected delete params: got=%v want=%v", deleteParams, expected)
	}

	countSQL, countParams := buildProjectProfileNodesCountSQL(9, 5, 3, true, true)
	if strings.Contains(countSQL, "bind_id = 9") || strings.Count(countSQL, "?") != 7 {
		t.Fatalf("unexpected count sql: %q", countSQL)
	}
	if fmt.Sprint(countParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected count params: got=%v want=%v", countParams, expected)
	}
}
