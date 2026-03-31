// store_test.go exercises the DuckDB-gateway adapter against the current hierarchy/session/turn schema.
// store_test.go 用于围绕当前层级、session 和 turn 表结构验证 DuckDB 网关适配器。
package vldb_duckdb

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_duckdb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// TestInitBootstrapsCurrentSchemaOnFreshInstall verifies fresh installs reset disposable debug tables, create the current baseline schema, and record its version.
// TestInitBootstrapsCurrentSchemaOnFreshInstall 用于验证首次安装会重置可丢弃的调试表、创建当前基线结构，并写入对应版本号。
func TestInitBootstrapsCurrentSchemaOnFreshInstall(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"WHERE singleton_id = 1": `[]`,
		},
	}
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}

	execs := server.execRequests()
	if len(execs) != 6 {
		t.Fatalf("expected 6 execute calls, got %d", len(execs))
	}
	if !strings.Contains(execs[0].Sql, "CREATE TABLE IF NOT EXISTS vmm_version") {
		t.Fatalf("missing version bootstrap sql: %s", execs[0].Sql)
	}
	if !strings.Contains(execs[1].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing managed schema reset sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_users") {
		t.Fatalf("missing current schema user table sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_turn_records") {
		t.Fatalf("missing current schema turn table sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_memory_nodes") {
		t.Fatalf("missing current schema memory node table sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_profile_nodes") {
		t.Fatalf("missing current schema profile node table sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "profile TEXT NOT NULL DEFAULT ''") {
		t.Fatalf("missing hierarchy profile columns in current schema sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "details TEXT NOT NULL DEFAULT ''") {
		t.Fatalf("missing turn details column in current schema sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "turn_count INTEGER NOT NULL DEFAULT 0") {
		t.Fatalf("missing current schema session turn counter sql: %s", execs[2].Sql)
	}
	if strings.Contains(execs[2].Sql, "FOREIGN KEY(session_id) REFERENCES vmm_sessions(id)") {
		t.Fatalf("unexpected inbound session foreign key in current schema sql: %s", execs[2].Sql)
	}
	if strings.Contains(execs[2].Sql, "vmm_memories") {
		t.Fatalf("unexpected legacy compatibility table in current schema sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[3].Sql, "INSERT INTO vmm_users") {
		t.Fatalf("missing default debug seed sql: %s", execs[3].Sql)
	}
	if !strings.Contains(execs[3].Sql, "INSERT INTO vmm_projects") {
		t.Fatalf("missing default debug project seed sql: %s", execs[3].Sql)
	}
	if !strings.Contains(execs[3].Sql, "VALUES (1, 'default'") {
		t.Fatalf("expected deterministic default seed ids and names, got %s", execs[3].Sql)
	}
	if !strings.Contains(execs[4].Sql, "DELETE FROM vmm_version") {
		t.Fatalf("missing version cleanup sql: %s", execs[4].Sql)
	}
	if !strings.Contains(execs[5].Sql, "INSERT INTO vmm_version") {
		t.Fatalf("missing version insert sql: %s", execs[5].Sql)
	}
	var params []any
	if err := json.Unmarshal([]byte(execs[5].ParamsJson), &params); err != nil {
		t.Fatalf("decode version insert params: %v", err)
	}
	if len(params) != 3 || params[0] != float64(versionSingletonID) || params[1] != float64(currentSchemaVersion) {
		t.Fatalf("unexpected version params: %#v", params)
	}
}

// TestInitResetsManagedSchemaOnVersionMismatch verifies old debug data is discarded and the current baseline schema is recreated.
// TestInitResetsManagedSchemaOnVersionMismatch 用于验证遇到旧版本调试数据时会直接丢弃并重建当前基线 schema。
func TestInitResetsManagedSchemaOnVersionMismatch(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"WHERE singleton_id = 1": `[{"schema_version":1}]`,
		},
	}
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store with version mismatch: %v", err)
	}
	execs := server.execRequests()
	if len(execs) != 6 {
		t.Fatalf("expected version bootstrap plus reset path, got %d calls", len(execs))
	}
	if !strings.Contains(execs[1].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing reset sql after version mismatch: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_turn_records") {
		t.Fatalf("missing recreated turn schema after version mismatch: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[3].Sql, "INSERT INTO vmm_users") {
		t.Fatalf("missing default debug seed sql after version mismatch: %s", execs[3].Sql)
	}
}

// TestResolveRequestScopeCreatesSession verifies business interceptors can resolve numeric user/project ids and auto-create one session row.
// TestResolveRequestScopeCreatesSession 用于验证业务拦截器可以解析数字 user/project id，并自动创建 session 行。
func TestResolveRequestScopeCreatesSession(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":8}]`,
			"FROM vmm_users":      `[{"id":7,"name":"alice","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_sessions":   `[]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_sessions": `[{"next_id":41}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	session, err := store.ResolveRequestScope(context.Background(), "sess-key-1", 7, 9)
	if err != nil {
		t.Fatalf("resolve request scope: %v", err)
	}
	if session.SessionID != 41 || session.SessionKey != "sess-key-1" {
		t.Fatalf("unexpected session ref: %+v", session)
	}
	if session.TeamID != 3 || session.SpaceID != 5 || session.ProjectID != 9 || session.UserID != 7 {
		t.Fatalf("unexpected scope ids: %+v", session)
	}
	if session.TeamName != "team-a" || session.SpaceName != "space-a" || session.ProjectName != "proj-a" || session.UserName != "alice" {
		t.Fatalf("unexpected scope names: %+v", session)
	}
	if session.TurnCount != 0 || session.SummarizeBudget != 0 || !session.CreatedAt.Equal(session.UpdatedAt) {
		t.Fatalf("unexpected new session analysis counters: %+v", session)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1]
	if !strings.Contains(last.Sql, "INSERT INTO vmm_sessions") {
		t.Fatalf("expected session insert sql, got %s", last.Sql)
	}
}

// TestResolveRequestScopeReusesExistingSessionAfterUserSwitch verifies debug-stage session reuse tolerates manual user-id switches under the same project/session key.
// TestResolveRequestScopeReusesExistingSessionAfterUserSwitch 用于验证调试阶段在同一 project/session_key 下即使手动切换 user_id，也仍然会复用已有 session。
func TestResolveRequestScopeReusesExistingSessionAfterUserSwitch(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":8}]`,
			"FROM vmm_users":      `[{"id":8,"name":"bob","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_sessions":   `[{"id":41,"session_key":"sess-key-1","user_id":7,"team_id":3,"space_id":5,"project_id":9,"turn_count":2,"last_summarized_id":0,"summarize_content":"","summarize_budget":0,"created_timestamp":1710000000000,"updated_timestamp":1710000001000}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	session, err := store.ResolveRequestScope(context.Background(), "sess-key-1", 8, 9)
	if err != nil {
		t.Fatalf("resolve request scope with switched user id: %v", err)
	}
	if session.SessionID != 41 || session.SessionKey != "sess-key-1" {
		t.Fatalf("unexpected reused session ref: %+v", session)
	}
	if session.UserID != 8 || session.ProjectID != 9 || session.TeamID != 3 || session.SpaceID != 5 {
		t.Fatalf("unexpected resolved scope after user switch: %+v", session)
	}
	if session.TurnCount != 2 || session.SummarizeBudget != 0 || session.UpdatedAt.IsZero() {
		t.Fatalf("unexpected reused session counters: %+v", session)
	}

	execs := server.execRequests()
	for _, req := range execs {
		if strings.Contains(req.Sql, "INSERT INTO vmm_sessions") {
			t.Fatalf("did not expect a new session row when reusing existing session: %s", req.Sql)
		}
	}
}

// TestAppendTurnRecordPersistsDehydratedPayload verifies post-action persistence writes one dehydrated turn row and then increments the session turn counter.
// TestAppendTurnRecordPersistsDehydratedPayload 用于验证 post-action 持久化会写入一条脱水 turn 行，并随后递增 session turn 计数。
func TestAppendTurnRecordPersistsDehydratedPayload(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_turn_records": `[{"next_id":100}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	persistedTurn, err := store.AppendTurnRecord(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-key-1",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.TurnRecord{
		UserContent:      "第一问",
		AssistantContent: "最终回答",
		Timeline: []logicdomain.TurnTimelineItem{
			{Type: "assistant", Content: "中间回答"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatalf("append turn record: %v", err)
	}
	if persistedTurn.ID != 100 || persistedTurn.SessionID != 41 || persistedTurn.ProjectID != 9 {
		t.Fatalf("unexpected persisted turn metadata: %+v", persistedTurn)
	}

	execs := server.execRequests()
	if len(execs) < 3 {
		t.Fatalf("expected version bootstrap + 2 persistence writes, got %d", len(execs))
	}
	insertSQL := execs[len(execs)-2].Sql
	if !strings.Contains(insertSQL, "INSERT INTO vmm_turn_records") {
		t.Fatalf("expected turn insert sql, got %s", insertSQL)
	}
	if !strings.Contains(insertSQL, "\"assistant\":\"最终回答\"") {
		t.Fatalf("expected final assistant in dehydrated payload, got %s", insertSQL)
	}
	if !strings.Contains(insertSQL, "\"content\":\"中间回答\"") {
		t.Fatalf("expected cleaned assistant timeline content to stay in dehydrated payload, got %s", insertSQL)
	}
	updateSQL := execs[len(execs)-1].Sql
	if !strings.Contains(updateSQL, "SET turn_count = turn_count + 1") {
		t.Fatalf("expected session turn counter update sql, got %s", updateSQL)
	}
	if !strings.Contains(updateSQL, "summarize_budget = summarize_budget + ") {
		t.Fatalf("expected session summarize budget update sql, got %s", updateSQL)
	}
}

// TestApplyTurnAnalysisWritesTurnSummaryAndDerivedNodes verifies one successful turn analysis updates the turn row and inserts both memory/profile node rows.
// TestApplyTurnAnalysisWritesTurnSummaryAndDerivedNodes 用于验证一次成功的 turn 分析会更新 turn 行，并插入 memory/profile 节点行。
func TestApplyTurnAnalysisWritesTurnSummaryAndDerivedNodes(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_memory_nodes":  `[{"next_id":201}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_profile_nodes": `[{"next_id":301}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	err := store.ApplyTurnAnalysis(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-key-1",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:               100,
		SessionID:        41,
		ProjectID:        9,
		DehydratedBudget: 123,
	}, logicdomain.TurnAnalysis{
		Details:              "这轮对话明确需要先给出 AI 记忆子项目建议。",
		DetailsBudget:        15,
		UserProfileMerged:    true,
		MergedUserProfile:    "合并后的用户画像",
		ProjectProfileMerged: true,
		MergedProjectProfile: "合并后的项目画像",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryRequirementTODO, VectorID: "11111111-1111-4111-8111-111111111111", Abstract: "当前对话需要先形成 AI 记忆子项目建议。", Details: "用户当前诉求是获得该子项目的设计建议。"},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{ProfileType: logicdomain.ProfileTypeProject, Content: "当前项目聚焦 AI 记忆能力设计。", Status: logicdomain.ProfileStatusMerged},
		},
	})
	if err != nil {
		t.Fatalf("apply turn analysis: %v", err)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1].Sql
	if !strings.Contains(last, "UPDATE vmm_turn_records") {
		t.Fatalf("expected turn analysis update sql, got %s", last)
	}
	if !strings.Contains(last, "details_budget = 15") {
		t.Fatalf("expected details budget update, got %s", last)
	}
	if !strings.Contains(last, "UPDATE vmm_users") || !strings.Contains(last, "合并后的用户画像") {
		t.Fatalf("expected user profile update sql, got %s", last)
	}
	if !strings.Contains(last, "UPDATE vmm_projects") || !strings.Contains(last, "合并后的项目画像") {
		t.Fatalf("expected project profile update sql, got %s", last)
	}
	if !strings.Contains(last, "WHERE id = 100;") {
		t.Fatalf("expected turn analysis update statement terminator, got %s", last)
	}
	if !strings.Contains(last, ");\n\nINSERT INTO vmm_profile_nodes") && !strings.Contains(last, ");\r\n\r\nINSERT INTO vmm_profile_nodes") {
		t.Fatalf("expected memory/profile inserts to be separated by statement terminators, got %s", last)
	}
	if !strings.Contains(last, "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("expected memory node insert sql, got %s", last)
	}
	if !strings.Contains(last, "INSERT INTO vmm_profile_nodes") {
		t.Fatalf("expected profile node insert sql, got %s", last)
	}
	if !strings.Contains(last, ", 2, ") {
		t.Fatalf("expected merged profile status in profile node insert, got %s", last)
	}
	if !strings.Contains(last, "CAST('11111111-1111-4111-8111-111111111111' AS UUID)") {
		t.Fatalf("expected supplied vector uuid cast in memory node insert, got %s", last)
	}
}

// TestLoadProfileTargetsReadsCurrentUserAndProjectProfiles verifies post-action profile merging can load the current durable user/project profile blobs from DuckDB.
// TestLoadProfileTargetsReadsCurrentUserAndProjectProfiles 用于验证 post-action 画像合并可以从 DuckDB 读取当前长期 user/project 画像 Blob。
func TestLoadProfileTargetsReadsCurrentUserAndProjectProfiles(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":8}]`,
			"FROM vmm_users":      `[{"id":7,"name":"alice","profile":"用户画像","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","profile":"项目画像","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	snapshot, err := store.LoadProfileTargets(context.Background(), logicdomain.SessionRef{
		UserID:    7,
		ProjectID: 9,
	})
	if err != nil {
		t.Fatalf("load profile targets: %v", err)
	}
	if snapshot.UserProfile != "用户画像" || snapshot.ProjectProfile != "项目画像" {
		t.Fatalf("unexpected profile snapshot: %+v", snapshot)
	}
}

// TestApplyManualProfileInstructionLeavesTurnIDNull verifies explicit manual profile instructions persist profile nodes without fabricating a turn binding.
// TestApplyManualProfileInstructionLeavesTurnIDNull 用于验证显式手工画像指令落库时不会伪造 turn 绑定，而是把 profile node 的 turn_id 留空。
func TestApplyManualProfileInstructionLeavesTurnIDNull(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_profile_nodes": `[{"next_id":501}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	_, err := store.ApplyManualProfileInstruction(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		ProjectID:   9,
	}, logicdomain.ProfileInstructionRecord{
		ID:          77,
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		Status:      logicdomain.ProfileInstructionStatusPending,
	}, []logicdomain.ProfileNodeCandidate{
		{
			ProfileType:      logicdomain.ProfileTypeProject,
			Content:          "当前项目统一使用 Go 语言编写。",
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP0,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			LevelReason:      "显式项目指令。",
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         77,
			ProfileDate:      "2026-03-30",
			RefreshWeight:    0,
			StatusReason:     "",
			SupersedeNodeIDs: nil,
		},
	}, nil, "[Profile Legend]\n项目画像", `{"reason":"manual instruction"}`)
	if err != nil {
		t.Fatalf("apply manual profile instruction: %v", err)
	}

	execs := server.execRequests()
	joined := ""
	for _, req := range execs {
		joined += req.Sql + "\n"
	}
	if !strings.Contains(joined, "INSERT INTO vmm_profile_nodes") {
		t.Fatalf("expected profile node insert sql, got %s", joined)
	}
	if !strings.Contains(joined, "VALUES (501, NULL, 1, 9,") {
		t.Fatalf("expected manual profile node turn_id to stay NULL, got %s", joined)
	}
}

// TestApplyManualProfileInstructionExecutesSeparateStatements verifies manual profile persistence no longer
// batches profile-node inserts and follow-up retire/applied updates into one large script, which would trigger
// DuckDB shared-connection deadlocks on the hot vmm_profile_nodes table.
// TestApplyManualProfileInstructionExecutesSeparateStatements 用于验证手工画像持久化不再把 profile-node 插入和后续退役、
// applied 更新揉成一大段脚本，以避免 DuckDB 在热点 vmm_profile_nodes 表上触发共享连接死锁。
func TestApplyManualProfileInstructionExecutesSeparateStatements(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_profile_nodes": `[{"next_id":601}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	_, err := store.ApplyManualProfileInstruction(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		ProjectID:   9,
	}, logicdomain.ProfileInstructionRecord{
		ID:          88,
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		Status:      logicdomain.ProfileInstructionStatusPending,
	}, []logicdomain.ProfileNodeCandidate{
		{
			ProfileType:      logicdomain.ProfileTypeProject,
			Content:          "项目统一使用 Go 语言。",
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP0,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			LevelReason:      "显式项目指令。",
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         88,
			ProfileDate:      "2026-03-31",
			SupersedeNodeIDs: []uint64{41},
		},
	}, []logicdomain.ProfileRetireDecision{
		{NodeID: 42, Reason: "旧约束已被新指令替代。"},
	}, "[Profile Legend]\n项目画像", `{"reason":"manual instruction"}`)
	if err != nil {
		t.Fatalf("apply manual profile instruction with split statements: %v", err)
	}

	execs := server.execRequests()
	joined := ""
	for _, req := range execs {
		joined += req.Sql + "\n"
	}
	if !strings.Contains(joined, "INSERT INTO vmm_profile_nodes") {
		t.Fatalf("expected manual profile node insert sql, got %s", joined)
	}
	if !strings.Contains(joined, "UPDATE vmm_profile_nodes") {
		t.Fatalf("expected follow-up profile retire/supersede sql, got %s", joined)
	}
	if !strings.Contains(joined, "UPDATE vmm_profile_instructions") {
		t.Fatalf("expected applied instruction update sql, got %s", joined)
	}
	for _, req := range execs {
		if strings.Contains(req.Sql, "INSERT INTO vmm_profile_nodes") && strings.Contains(req.Sql, "UPDATE vmm_profile_instructions") {
			t.Fatalf("did not expect one execute script to batch profile insert and instruction update together: %s", req.Sql)
		}
	}
}

// TestCreateProfileInstructionReconcilesCommitUncertainInsert verifies one deadlock-style gateway error
// is treated as success when the instruction row can already be read back by its deterministic id.
// TestCreateProfileInstructionReconcilesCommitUncertainInsert 用于验证当网关返回类似 deadlock 的不确定提交错误时，
// 如果 instruction 行已经能按确定性 id 读回，就应把这次创建收敛成成功。
func TestCreateProfileInstructionReconcilesCommitUncertainInsert(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_profile_instructions": `[{"next_id":901}]`,
			"FROM vmm_profile_instructions\nWHERE id = 901":                            `[{"id":901,"profile_type":1,"bind_id":9,"instruction":"项目统一使用 Go。","instruction_status":0,"review_result_json":"","failure_reason":"","created_timestamp":1774962000000,"updated_timestamp":1774962000000}]`,
		},
		execErrors: map[string]string{
			"INSERT INTO vmm_profile_instructions": "duckdb execute_batch failed: TransactionContext Error: Failed to commit: resource deadlock would occur: resource deadlock would occur",
		},
	}
	store := newDuckDBTestStore(t, server)

	record, err := store.CreateProfileInstruction(context.Background(), logicdomain.ProfileInstructionRecord{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		Instruction: "项目统一使用 Go。",
		Status:      logicdomain.ProfileInstructionStatusPending,
	})
	if err != nil {
		t.Fatalf("create profile instruction with uncertain commit: %v", err)
	}
	if record.ID != 901 || record.ProfileType != logicdomain.ProfileTypeProject || record.BindID != 9 {
		t.Fatalf("unexpected reconciled instruction record: %+v", record)
	}
}

// TestApplyManualProfileInstructionReconcilesCommitUncertainWrites verifies manual profile persistence
// can recover from "reported failed but already committed" insert/applied-update errors without duplicating nodes.
// TestApplyManualProfileInstructionReconcilesCommitUncertainWrites 用于验证手工画像持久化在遇到“报错但已提交”的
// 节点插入和 instruction applied 更新时，能够做状态对账并成功收敛，而不会重复制造节点。
func TestApplyManualProfileInstructionReconcilesCommitUncertainWrites(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_profile_nodes": `[{"next_id":701}]`,
			"FROM vmm_profile_nodes\nWHERE id = 701":                            `[{"id":701,"turn_id":null,"profile_type":1,"bind_id":9,"content":"项目全称为 VulcanMemoryMesh，简称 VMM。","profile_status":2,"priority":1,"profile_level":2,"level_reason":"显式项目指令。","refresh_weight":0,"source_kind":1,"source_id":88,"status_reason":"","expires_timestamp":0,"superseded_by_id":0,"profile_date":"2026-03-31","created_timestamp":1774962100000,"updated_timestamp":1774962100000}]`,
			"FROM vmm_profile_instructions\nWHERE id = 88":                      `[{"id":88,"profile_type":1,"bind_id":9,"instruction":"项目名称改为 VulcanMemoryMesh，简称 VMM。","instruction_status":1,"review_result_json":"{\"reason\":\"manual instruction\"}","failure_reason":"","created_timestamp":1774962090000,"updated_timestamp":1774962100000}]`,
		},
		execErrors: map[string]string{
			"INSERT INTO vmm_profile_nodes":       "duckdb execute_batch failed: TransactionContext Error: Failed to commit: resource deadlock would occur: resource deadlock would occur",
			"UPDATE vmm_profile_instructions SET": "duckdb execute_batch failed: Invalid Error: resource deadlock would occur: resource deadlock would occur",
		},
	}
	store := newDuckDBTestStore(t, server)

	result, err := store.ApplyManualProfileInstruction(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		ProjectID:   9,
	}, logicdomain.ProfileInstructionRecord{
		ID:          88,
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
		Status:      logicdomain.ProfileInstructionStatusPending,
	}, []logicdomain.ProfileNodeCandidate{
		{
			ProfileType:   logicdomain.ProfileTypeProject,
			Content:       "项目全称为 VulcanMemoryMesh，简称 VMM。",
			Status:        logicdomain.ProfileStatusActive,
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			LevelReason:   "显式项目指令。",
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      88,
			ProfileDate:   "2026-03-31",
			ExpiresAt:     time.Time{},
			StatusReason:  "",
			RefreshWeight: 0,
		},
	}, nil, "[Profile Legend]\n项目画像", `{"reason":"manual instruction"}`)
	if err != nil {
		t.Fatalf("apply manual profile instruction with uncertain writes: %v", err)
	}
	if result.InstructionID != 88 || len(result.AcceptedNodes) != 1 || result.AcceptedNodes[0].ID != 701 {
		t.Fatalf("unexpected reconciled manual instruction result: %+v", result)
	}
}

// TestDeleteUserRefReusesExistingConfirmationCode verifies duplicate first-step delete requests do not rewrite the same user row once one confirmation code already exists.
// TestDeleteUserRefReusesExistingConfirmationCode 用于验证删除第一步在确认码已存在时会直接复用，而不会重复改写同一用户行。
func TestDeleteUserRefReusesExistingConfirmationCode(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"FROM vmm_users":   `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"keep-code","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	result, err := store.DeleteUserRef(context.Background(), "7", "")
	if err != nil {
		t.Fatalf("delete user first phase with existing code: %v", err)
	}
	if !result.RequiresConfirmation {
		t.Fatalf("expected confirmation requirement, got %+v", result)
	}
	if result.ConfirmationCode != "keep-code" {
		t.Fatalf("expected existing confirmation code, got %+v", result)
	}

	for _, req := range server.execRequests() {
		if strings.Contains(req.Sql, "UPDATE vmm_users") && strings.Contains(req.Sql, "delete_confirm_code") {
			t.Fatalf("did not expect confirmation code rewrite, got %s", req.Sql)
		}
	}
}

// TestDeleteUserRefWrongConfirmationCodeReturnsExistingCode verifies stale or wrong confirmation codes do not rotate the durable token.
// TestDeleteUserRefWrongConfirmationCodeReturnsExistingCode 用于验证陈旧或错误的确认码不会导致长期令牌被重新生成。
func TestDeleteUserRefWrongConfirmationCodeReturnsExistingCode(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"FROM vmm_users":   `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"keep-code","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	result, err := store.DeleteUserRef(context.Background(), "7", "wrong-code")
	if err != nil {
		t.Fatalf("delete user with wrong confirmation code: %v", err)
	}
	if !result.RequiresConfirmation {
		t.Fatalf("expected confirmation requirement, got %+v", result)
	}
	if result.ConfirmationCode != "keep-code" {
		t.Fatalf("expected existing confirmation code to be reused, got %+v", result)
	}

	for _, req := range server.execRequests() {
		if strings.Contains(req.Sql, "UPDATE vmm_users") && strings.Contains(req.Sql, "delete_confirm_code") {
			t.Fatalf("did not expect confirmation code rewrite, got %s", req.Sql)
		}
	}
}

// TestDeleteUserRefKeepsSharedScopeProfileNodes verifies user deletion only removes user-owned profile facts and
// leaves shared project/team/space profile nodes untouched even when they were extracted from this user's turns.
// TestDeleteUserRefKeepsSharedScopeProfileNodes 用于验证删除用户时只清理用户拥有的画像事实；即使 project/team/space 的共享画像
// 曾经来自该用户 turn，也不能被连带删除。
func TestDeleteUserRefKeepsSharedScopeProfileNodes(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":                          `[{"schema_version":8}]`,
			"FROM vmm_users":                            `[{"id":7,"name":"alice","profile":"","delete_confirm_code":"keep-code","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"COUNT(*) AS count FROM vmm_sessions":       `[{"count":1}]`,
			"COUNT(*) AS count FROM vmm_turn_records":   `[{"count":2}]`,
			"COUNT(*) AS count FROM vmm_memory_entries": `[{"count":3}]`,
			"COUNT(*) AS count FROM vmm_memory_nodes":   `[{"count":4}]`,
			"COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?": `[{"count":5}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	result, err := store.DeleteUserRef(context.Background(), "7", "keep-code")
	if err != nil {
		t.Fatalf("delete user with valid confirmation code: %v", err)
	}
	if result.RequiresConfirmation {
		t.Fatalf("did not expect confirmation requirement after supplying the durable code, got %+v", result)
	}
	if result.DeletedUsers != 1 || result.DeletedProfiles != 5 {
		t.Fatalf("expected user/profile delete counts to be surfaced, got %+v", result)
	}

	joined := ""
	for _, req := range server.execRequests() {
		joined += req.Sql + "\n"
	}
	if !strings.Contains(joined, "DELETE FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?") {
		t.Fatalf("expected user-bound profile node delete sql, got %s", joined)
	}
	if !strings.Contains(joined, "UPDATE vmm_profile_nodes") || !strings.Contains(joined, "SET turn_id = NULL") {
		t.Fatalf("expected surviving shared profile nodes to be detached from deleted user turns, got %s", joined)
	}
	if !strings.Contains(joined, "source_kind = ?") || !strings.Contains(joined, "profile_type <> ? AND turn_id IN") {
		t.Fatalf("expected shared-scope detachment sql to target non-user profile nodes, got %s", joined)
	}
	if strings.Contains(joined, "DELETE FROM vmm_profile_nodes WHERE turn_id IN") {
		t.Fatalf("did not expect turn-derived shared profile delete sql during user removal, got %s", joined)
	}
}

// TestDeleteProjectPathCountsProfilesAndCascadesEmptyParents verifies project deletion now reports
// the actual deleted project/profile counts and removes empty parent space/team rows when the project was their last child.
// TestDeleteProjectPathCountsProfilesAndCascadesEmptyParents 用于验证项目删除现在会返回真实的项目/画像删除统计，
// 并在该项目是父级最后子项时级联删除变空的 space 和 team 行。
func TestDeleteProjectPathCountsProfilesAndCascadesEmptyParents(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":8}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","profile":"","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?":                                 `[{"count":2}]`,
			"COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?":                             `[{"count":4}]`,
			"COUNT(*) AS count FROM vmm_memory_entries WHERE project_id = ?":                           `[{"count":3}]`,
			"COUNT(*) AS count FROM vmm_memory_nodes WHERE project_id = ?":                             `[{"count":5}]`,
			"COUNT(*) AS count FROM vmm_projects WHERE space_id = ? AND id <> ?":                       `[{"count":0}]`,
			"COUNT(*) AS count FROM vmm_spaces WHERE team_id = ? AND id <> ?":                          `[{"count":0}]`,
			"SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE (profile_type = 1 AND bind_id = 9)": `[{"count":6}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	result, err := store.DeleteProjectPath(context.Background(), "team-a/space-a/proj-a", true)
	if err != nil {
		t.Fatalf("delete project path: %v", err)
	}
	if result.DeletedProjects != 1 || result.DeletedSpaces != 1 || result.DeletedTeams != 1 {
		t.Fatalf("expected project delete cascade counts, got %+v", result)
	}
	if result.DeletedSessions != 2 || result.DeletedMessages != 4 || result.DeletedMemories != 8 || result.DeletedProfiles != 6 {
		t.Fatalf("expected project delete row counts, got %+v", result)
	}

	joined := ""
	for _, req := range server.execRequests() {
		joined += req.Sql + "\n"
	}
	if !strings.Contains(joined, "DELETE FROM vmm_profile_nodes WHERE (profile_type = 1 AND bind_id = 9)") {
		t.Fatalf("expected project profile delete sql, got %s", joined)
	}
	if !strings.Contains(joined, "(profile_type = 3 AND bind_id = 5)") {
		t.Fatalf("expected empty-space profile delete sql, got %s", joined)
	}
	if !strings.Contains(joined, "(profile_type = 2 AND bind_id = 3)") {
		t.Fatalf("expected empty-team profile delete sql, got %s", joined)
	}
	if !strings.Contains(joined, "DELETE FROM vmm_spaces WHERE id = ?") {
		t.Fatalf("expected empty space delete sql, got %s", joined)
	}
	if !strings.Contains(joined, "DELETE FROM vmm_teams WHERE id = ?") {
		t.Fatalf("expected empty team delete sql, got %s", joined)
	}
}

// TestConvergeExpiredProfileNodesMarksDueRowsAndReturnsAffectedTargets verifies periodic expiry convergence flips due active nodes to expired and returns the remaining renderable nodes for the affected target.
// TestConvergeExpiredProfileNodesMarksDueRowsAndReturnsAffectedTargets 用于验证周期性过期收敛会把到期 active 节点改为 expired，并返回受影响目标剩余可渲染节点。
func TestConvergeExpiredProfileNodesMarksDueRowsAndReturnsAffectedTargets(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
			"WHERE profile_status = ? AND expires_timestamp > 0 AND expires_timestamp <= ?":                                       `[{"id":41,"turn_id":501,"profile_type":0,"bind_id":7,"content":"旧的临时偏好","profile_status":2,"priority":2,"profile_level":0,"level_reason":"一次性上下文","refresh_weight":0,"expires_timestamp":1711785600000,"superseded_by_id":0,"profile_date":"2026-03-29","created_timestamp":1711700000000,"updated_timestamp":1711700000000}]`,
			"WHERE profile_type = ? AND bind_id = ? AND profile_status = ? AND (expires_timestamp <= 0 OR expires_timestamp > ?)": `[{"id":42,"turn_id":502,"profile_type":0,"bind_id":7,"content":"用户偏好使用 Rust。","profile_status":2,"priority":1,"profile_level":2,"level_reason":"稳定偏好","refresh_weight":2,"expires_timestamp":0,"superseded_by_id":0,"profile_date":"2026-03-30","created_timestamp":1711800000000,"updated_timestamp":1711800000000}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	targets, err := store.ConvergeExpiredProfileNodes(context.Background(), 16)
	if err != nil {
		t.Fatalf("converge expired profile nodes: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 affected target, got %+v", targets)
	}
	if targets[0].ProfileType != logicdomain.ProfileTypeUser || targets[0].BindID != 7 {
		t.Fatalf("unexpected affected target: %+v", targets[0])
	}
	if len(targets[0].Nodes) != 1 || targets[0].Nodes[0].Content != "用户偏好使用 Rust。" {
		t.Fatalf("unexpected remaining active nodes: %+v", targets[0].Nodes)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1].Sql
	if !strings.Contains(last, "UPDATE vmm_profile_nodes") || !strings.Contains(last, "SET profile_status = 4") {
		t.Fatalf("expected expired profile-node update sql, got %s", last)
	}
}

// TestReplaceRenderedProfilesUpdatesUserAndProjectBlobs verifies the post-expiry render writeback path can update both durable user and project profile blobs in one SQL script.
// TestReplaceRenderedProfilesUpdatesUserAndProjectBlobs 用于验证过期收敛后的渲染回写路径可以在一段 SQL 脚本里同时更新长期 user/project 画像 Blob。
func TestReplaceRenderedProfilesUpdatesUserAndProjectBlobs(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":8}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{
			7: "[Profile Legend]\n用户画像",
		},
		ProjectProfiles: map[uint64]string{
			9: "[Profile Legend]\n项目画像",
		},
	})
	if err != nil {
		t.Fatalf("replace rendered profiles: %v", err)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1].Sql
	if !strings.Contains(last, "UPDATE vmm_users") || !strings.Contains(last, "用户画像") {
		t.Fatalf("expected user profile update sql, got %s", last)
	}
	if !strings.Contains(last, "UPDATE vmm_projects") || !strings.Contains(last, "项目画像") {
		t.Fatalf("expected project profile update sql, got %s", last)
	}
}

// TestDebugCleanManagedSchemaExecutesDropScript verifies the debug-clean helper wipes the managed DuckDB schema through one execute call.
// TestDebugCleanManagedSchemaExecutesDropScript 用于验证调试清理辅助逻辑会通过一次执行调用清空受管 DuckDB schema。
func TestDebugCleanManagedSchemaExecutesDropScript(t *testing.T) {
	server := &fakeDuckDBServer{}
	store := newDuckDBTestStoreWithoutInit(t, server)

	if err := debugCleanWithClient(context.Background(), store.client, time.Second); err != nil {
		t.Fatalf("debug clean managed schema: %v", err)
	}

	execs := server.execRequests()
	if len(execs) != 1 {
		t.Fatalf("expected 1 debug-clean execute call, got %d", len(execs))
	}
	if !strings.Contains(execs[0].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing managed table cleanup in debug-clean sql: %s", execs[0].Sql)
	}
	if !strings.Contains(execs[0].Sql, "DROP TABLE IF EXISTS vmm_version") {
		t.Fatalf("missing version-table cleanup in debug-clean sql: %s", execs[0].Sql)
	}
}

// newDuckDBTestStore creates one initialized adapter backed by a bufconn gRPC server.
// newDuckDBTestStore 用于创建一个已经初始化完成、并由 bufconn gRPC 服务支撑的适配器实例。
func newDuckDBTestStore(t *testing.T, server *fakeDuckDBServer) *Store {
	t.Helper()
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
}

// newDuckDBTestStoreWithoutInit creates one adapter test instance and lets the caller control schema bootstrap timing.
// newDuckDBTestStoreWithoutInit 用于创建一个适配器测试实例，并把 schema 初始化时机交给调用方控制。
func newDuckDBTestStoreWithoutInit(t *testing.T, server *fakeDuckDBServer) *Store {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	duckdbv1.RegisterDuckDbServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return &Store{
		conn:    conn,
		client:  duckdbv1.NewDuckDbServiceClient(conn),
		timeout: time.Second,
	}
}

// fakeDuckDBServer captures SQL requests and returns canned JSON query payloads for adapter tests.
// fakeDuckDBServer 用于捕获 SQL 请求并返回预置 JSON 查询结果，供适配器测试使用。
type fakeDuckDBServer struct {
	duckdbv1.UnimplementedDuckDbServiceServer
	mu         sync.Mutex
	execs      []*duckdbv1.ExecuteRequest
	querys     []*duckdbv1.QueryRequest
	queryJSON  map[string]string
	execErrors map[string]string
}

// ExecuteScript records every execute request so tests can assert SQL shape and parameters.
// ExecuteScript 用于记录每一次执行请求，方便测试断言 SQL 形态和参数。
func (s *fakeDuckDBServer) ExecuteScript(_ context.Context, req *duckdbv1.ExecuteRequest) (*duckdbv1.ExecuteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, req)
	for fragment, message := range s.execErrors {
		if strings.Contains(req.Sql, fragment) {
			return &duckdbv1.ExecuteResponse{Success: false, Message: message}, nil
		}
	}
	return &duckdbv1.ExecuteResponse{Success: true, Message: "ok"}, nil
}

// QueryJson records every query request and returns the canned JSON payload that matches the SQL fragment.
// QueryJson 用于记录每一次查询请求，并按 SQL 片段返回预置 JSON 结果。
func (s *fakeDuckDBServer) QueryJson(_ context.Context, req *duckdbv1.QueryRequest) (*duckdbv1.QueryJsonResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.querys = append(s.querys, req)
	bestFragment := ""
	bestPayload := ""
	for fragment, payload := range s.queryJSON {
		if strings.Contains(req.Sql, fragment) && len(fragment) > len(bestFragment) {
			bestFragment = fragment
			bestPayload = payload
		}
	}
	if bestFragment != "" {
		return &duckdbv1.QueryJsonResponse{JsonData: bestPayload}, nil
	}
	return &duckdbv1.QueryJsonResponse{JsonData: "[]"}, nil
}

// QueryStream stays unused in these focused tests.
// QueryStream 用于在这些聚焦测试里保持未使用状态。
func (s *fakeDuckDBServer) QueryStream(req *duckdbv1.QueryRequest, stream grpc.ServerStreamingServer[duckdbv1.QueryResponse]) error {
	return nil
}

// execRequests returns a stable snapshot of all execute calls observed during the test.
// execRequests 用于返回测试期间观察到的全部执行请求快照。
func (s *fakeDuckDBServer) execRequests() []*duckdbv1.ExecuteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*duckdbv1.ExecuteRequest, len(s.execs))
	copy(out, s.execs)
	return out
}
