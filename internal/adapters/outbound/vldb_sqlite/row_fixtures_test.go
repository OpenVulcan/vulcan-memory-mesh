// SQLite adapter tests for row fixtures test.
// SQLite 适配层行夹具测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strconv"
	"strings"
	"testing"
)

// sqliteAnalyzedTurnRowJSON renders one analyzed turn row for first-mutation reconciliation tests.
// sqliteAnalyzedTurnRowJSON 用于为首个写入对账测试渲染一条已分析 turn 行。
func sqliteAnalyzedTurnRowJSON(turnID, sessionID, projectID uint64, details string, detailsBudget int, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"session_id":%d,"project_id":%d,"dehydrated_content":%s,"dehydrated_budget":3,"extracted_status":%d,"details":%s,"details_budget":%d,"created_timestamp":10,"updated_timestamp":%d}]`,
		turnID,
		sessionID,
		projectID,
		strconv.Quote(`{"user":"analyzed"}`),
		logicdomain.TurnExtractedStatusDone,
		strconv.Quote(details),
		detailsBudget,
		updatedMs,
	)
}

// sqliteMemoryNodeRowJSONFromInsertParams mirrors parameterizedMemoryNodeInsertStatement so reconciliation tests use the captured typed write as the read-back source.
// sqliteMemoryNodeRowJSONFromInsertParams 用于镜像 parameterizedMemoryNodeInsertStatement，让对账测试直接用捕获到的强类型写入构造回读事实源。
func sqliteMemoryNodeRowJSONFromInsertParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 33 {
		t.Fatalf("expected thirty-three memory insert params, got %d: %#v", len(params), params)
	}
	sourceTurnID := "null"
	if params[6].Kind == sqliteffi.SQLValueInt64 {
		sourceTurnID = strconv.FormatInt(params[6].Int64, 10)
	}
	return fmt.Sprintf(
		`[{"id":%d,"team_id":%d,"space_id":%d,"project_id":%d,"user_id":%d,"origin_session_id":%d,"source_turn_id":%s,"vector_id":%s,"vector_json":%s,"source_kind":%d,"scope_level":%d,"category":%d,"abstract":%s,"details":%s,"memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":%d,"support_count":%d,"rebuttal_count":%d,"status_reason":%s,"expires_timestamp":%d,"last_recalled_timestamp":%d,"last_adopted_timestamp":%d,"last_reinforced_timestamp":%d,"recalled_count":%d,"adopted_count":%d,"reinforcement_count":%d,"cross_session_adopted_count":%d,"decay_disabled":%d,"dedupe_hash":%s,"created_timestamp":%d,"updated_timestamp":%d}]`,
		params[0].Int64,
		params[1].Int64,
		params[2].Int64,
		params[3].Int64,
		params[4].Int64,
		params[5].Int64,
		sourceTurnID,
		strconv.Quote(params[7].String),
		strconv.Quote(params[8].String),
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		strconv.Quote(params[12].String),
		strconv.Quote(params[13].String),
		params[14].Int64,
		params[15].Int64,
		params[16].Int64,
		params[17].Int64,
		params[18].Int64,
		params[19].Int64,
		strconv.Quote(params[20].String),
		params[21].Int64,
		params[22].Int64,
		params[23].Int64,
		params[24].Int64,
		params[25].Int64,
		params[26].Int64,
		params[27].Int64,
		params[28].Int64,
		params[29].Int64,
		strconv.Quote(params[30].String),
		params[31].Int64,
		params[32].Int64,
	)
}

// sqliteAdoptableMemoryNodeRowsJSON renders one active memory row used as the factual source for adoption lifecycle tests.
// sqliteAdoptableMemoryNodeRowsJSON 用于渲染采纳生命周期测试所需的一条 active memory 事实源行。
func sqliteAdoptableMemoryNodeRowsJSON(expiresMs int64) string {
	return fmt.Sprintf(`[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":%d,"category":3,"abstract":"adopted memory","details":"details","memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":%d,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":1,"reinforcement_count":1,"cross_session_adopted_count":3,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`,
		logicdomain.MemoryScopeLevelSession,
		logicdomain.MemoryStatusActive,
		logicdomain.MemoryPriorityP0,
		logicdomain.MemoryLevelSession,
		expiresMs,
	)
}

// sqliteMemoryNodeRowJSONFromAdoptionUpdateParams mirrors parameterizedMemoryAdoptionUpdateStatement so commit-unknown reconciliation tests read back the captured lifecycle target.
// sqliteMemoryNodeRowJSONFromAdoptionUpdateParams 用于镜像 parameterizedMemoryAdoptionUpdateStatement，让提交未知对账测试回读捕获到的生命周期目标。
func sqliteMemoryNodeRowJSONFromAdoptionUpdateParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 15 {
		t.Fatalf("expected fifteen memory adoption update params, got %d: %#v", len(params), params)
	}
	for index, param := range params {
		if param.Kind != sqliteffi.SQLValueInt64 {
			t.Fatalf("expected memory adoption update param %d to be int64, got %#v", index, param)
		}
	}
	return fmt.Sprintf(
		`[{"id":%d,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":11,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":%d,"category":3,"abstract":"adopted memory","details":"details","memory_status":%d,"priority":%d,"memory_level":%d,"refresh_weight":%d,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":%d,"last_recalled_timestamp":%d,"last_adopted_timestamp":%d,"last_reinforced_timestamp":%d,"recalled_count":%d,"adopted_count":%d,"reinforcement_count":%d,"cross_session_adopted_count":%d,"decay_disabled":%d,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}]`,
		params[14].Int64,
		params[0].Int64,
		params[1].Int64,
		int64(logicdomain.MemoryPriorityP0),
		params[2].Int64,
		params[3].Int64,
		params[4].Int64,
		params[5].Int64,
		params[6].Int64,
		params[7].Int64,
		params[8].Int64,
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		params[12].Int64,
		params[13].Int64,
	)
}

// sqliteManualDeleteCandidateRowsJSON renders the active rows used by manual-delete relation and cleanup-coordinate tests.
// sqliteManualDeleteCandidateRowsJSON 用于渲染手工删除关系写入与清理坐标测试所需的 active 行。
func sqliteManualDeleteCandidateRowsJSON() string {
	return `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`
}

// sqliteMemoryNodeDeleteRowsJSON renders durable memory rows with a controlled delete status, reason, and update timestamp for manual-delete reconciliation tests.
// sqliteMemoryNodeDeleteRowsJSON 用于为手工删除对账测试渲染可控制删除状态、原因和更新时间的长期 memory 行。
func sqliteMemoryNodeDeleteRowsJSON(status int, reason string, updatedMs int64, memoryIDs ...uint64) string {
	rows := make([]string, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":%s,"vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":%d,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":%s,"expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}`,
			memoryID,
			strconv.Quote(fmt.Sprintf("vec-%d", memoryID)),
			status,
			strconv.Quote(strings.TrimSpace(reason)),
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteMemoryNodeStatusRowsJSON renders durable memory rows with controlled lifecycle status for supersede reconciliation tests.
// sqliteMemoryNodeStatusRowsJSON 用于为 supersede 对账测试渲染可控制生命周期状态的长期 memory 行。
func sqliteMemoryNodeStatusRowsJSON(status int, updatedMs int64, memoryIDs ...uint64) string {
	rows := make([]string, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"team_id":1,"space_id":2,"project_id":9,"user_id":7,"origin_session_id":33,"source_turn_id":44,"vector_id":%s,"vector_json":"[0.25,0.75]","source_kind":0,"scope_level":1,"category":3,"abstract":"old memory","details":"old memory details","memory_status":%d,"priority":2,"memory_level":1,"refresh_weight":8,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":%d}`,
			memoryID,
			strconv.Quote(fmt.Sprintf("vec-old-%d", memoryID)),
			status,
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteProfileInstructionRowJSON renders one durable manual-instruction row with controlled status payload for commit reconciliation tests.
// sqliteProfileInstructionRowJSON 用于为提交对账测试渲染一条带有可控状态载荷的长期手工指令行。
func sqliteProfileInstructionRowJSON(id uint64, status int, reviewResult, failureReason string, updatedMs int64) string {
	return fmt.Sprintf(
		`[{"id":%d,"profile_type":%d,"bind_id":9,"instruction":"keep deployment notes current","instruction_status":%d,"review_result_json":%s,"failure_reason":%s,"created_timestamp":10,"updated_timestamp":%d}]`,
		id,
		logicdomain.ProfileTypeProject,
		status,
		strconv.Quote(strings.TrimSpace(reviewResult)),
		strconv.Quote(strings.TrimSpace(failureReason)),
		updatedMs,
	)
}

// sqliteProfileNodeStatusRowsJSON renders durable profile-node lifecycle rows with controlled status payload for reconciliation tests.
// sqliteProfileNodeStatusRowsJSON 用于为对账测试渲染带有可控状态载荷的长期画像节点生命周期行。
func sqliteProfileNodeStatusRowsJSON(status int, reason string, updatedMs int64, nodeIDs ...uint64) string {
	rows := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"profile_status":%d,"superseded_by_id":0,"status_reason":%s,"updated_timestamp":%d}`,
			nodeID,
			status,
			strconv.Quote(strings.TrimSpace(reason)),
			updatedMs,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteRenderedProfileBatchRowsJSON renders scope profile rows that mirror one captured ReplaceRenderedProfiles batch for reconciliation tests.
// sqliteRenderedProfileBatchRowsJSON 用于为对账测试渲染与捕获的 ReplaceRenderedProfiles 批量写入一致的 scope 画像行。
func sqliteRenderedProfileBatchRowsJSON(profiles map[uint64]string, updatedAt string, ids ...uint64) string {
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"profile":%s,"updated_at":%s}`,
			id,
			strconv.Quote(profiles[id]),
			strconv.Quote(updatedAt),
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteMemoryContextEdgeRowsJSONFromInsertRequests mirrors context-edge insert params so reconciliation tests use captured typed writes as read-back rows.
// sqliteMemoryContextEdgeRowsJSONFromInsertRequests 用于镜像情境边插入参数，让对账测试直接使用捕获的强类型写入作为回读行。
func sqliteMemoryContextEdgeRowsJSONFromInsertRequests(t *testing.T, requests ...*fakeExecuteRequest) string {
	t.Helper()
	rows := make([]string, 0, len(requests))
	for _, request := range requests {
		if request == nil {
			t.Fatal("expected non-nil context edge insert request")
		}
		params := request.GetParams()
		if len(params) != 9 {
			t.Fatalf("expected nine context edge insert params, got %d: %#v", len(params), params)
		}
		rows = append(rows, fmt.Sprintf(
			`{"memory_id":%d,"context_key":%s,"context_value":%s,"support_count":%d,"rebuttal_count":%d,"last_supported_timestamp":%d,"last_rebutted_timestamp":%d,"created_timestamp":%d,"updated_timestamp":%d}`,
			params[0].Int64,
			strconv.Quote(params[1].String),
			strconv.Quote(params[2].String),
			params[3].Int64,
			params[4].Int64,
			params[5].Int64,
			params[6].Int64,
			params[7].Int64,
			params[8].Int64,
		))
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// sqliteProfileNodeRowJSONFromInsertParams mirrors parameterizedProfileNodeInsertStatement so profile insert reconciliation tests use captured typed params.
// sqliteProfileNodeRowJSONFromInsertParams 用于镜像 parameterizedProfileNodeInsertStatement，让画像插入对账测试使用捕获到的强类型参数。
func sqliteProfileNodeRowJSONFromInsertParams(t *testing.T, params []sqliteffi.SQLValue) string {
	t.Helper()
	if len(params) != 17 {
		t.Fatalf("expected seventeen profile insert params, got %d: %#v", len(params), params)
	}
	turnID := "null"
	if params[1].Kind == sqliteffi.SQLValueInt64 {
		turnID = strconv.FormatInt(params[1].Int64, 10)
	}
	return fmt.Sprintf(
		`[{"id":%d,"turn_id":%s,"profile_type":%d,"bind_id":%d,"content":%s,"profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":%s,"refresh_weight":%d,"source_kind":%d,"source_id":%d,"status_reason":%s,"expires_timestamp":%d,"superseded_by_id":0,"profile_date":%s,"created_timestamp":%d,"updated_timestamp":%d,"profile_date_anchor_timestamp":%d}]`,
		params[0].Int64,
		turnID,
		params[2].Int64,
		params[3].Int64,
		strconv.Quote(params[4].String),
		params[5].Int64,
		params[6].Int64,
		params[7].Int64,
		strconv.Quote(params[8].String),
		params[9].Int64,
		params[10].Int64,
		params[11].Int64,
		strconv.Quote(params[12].String),
		params[13].Int64,
		strconv.Quote(params[14].String),
		params[15].Int64,
		params[16].Int64,
		params[15].Int64,
	)
}
