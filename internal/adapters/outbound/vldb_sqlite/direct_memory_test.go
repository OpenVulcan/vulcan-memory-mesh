// SQLite adapter tests for direct memory test.
// SQLite 适配层主动记忆写入测试，使用同包夹具验证存储行为。
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

// TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites verifies direct-write fallback inserts keep request text out of executable SQL.
// TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites 用于验证主动写 fallback 插入会把请求文本隔离在可执行 SQL 之外。
func TestStoreCreateDirectMemoryNodeUsesTypedSQLiteParamsForMemoryTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "211")
	store := &Store{database: fake, timeout: time.Second}

	// Capture relational writes so the assertion can inspect the actual SQLite execution boundary.
	// 捕获关系写入请求，让断言能够检查真实的 SQLite 执行边界。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	// Use SQL-shaped text from the transport-facing fields so the test fails if raw interpolation returns.
	// 使用来自传输侧字段的 SQL 形状文本，确保原始字符串插值一旦回归就会触发测试失败。
	vectorID := "  vec-direct '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  direct abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  direct details '; DROP TABLE vmm_memory_nodes; --  "
	statusReason := "  direct reason '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  direct hash '; DROP TABLE vmm_memory_nodes; --  "
	createdAt := time.Date(2026, 7, 5, 10, 15, 0, 0, time.UTC)
	created, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:      vectorID,
			Vector:        []float32{0.125, 0.875},
			SourceKind:    logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:    logicdomain.MemoryScopeLevelProject,
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      memoryAbstract,
			Details:       memoryDetails,
			Status:        logicdomain.MemoryStatusActive,
			Priority:      logicdomain.MemoryPriorityP1,
			MemoryLevel:   logicdomain.MemoryLevelStable,
			RefreshWeight: 2,
			StatusReason:  statusReason,
			DedupeHash:    dedupeHash,
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		},
	)
	if err != nil {
		t.Fatalf("CreateDirectMemoryNode returned error: %v", err)
	}
	if created.ID != 211 || created.VectorID != strings.TrimSpace(vectorID) || created.Abstract != strings.TrimSpace(memoryAbstract) {
		t.Fatalf("unexpected created direct memory row: %+v", created)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one typed insert request, got %d", len(requests))
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "vec-direct", "direct abstract", "direct details", "direct reason", "direct hash"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected direct memory text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if strings.TrimSpace(requests[0].GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", requests[0].GetParamsJson())
	}
	for _, value := range []string{
		strings.TrimSpace(vectorID),
		"[0.125,0.875]",
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(statusReason),
		strings.TrimSpace(dedupeHash),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
}

// TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference verifies fallback direct-write insert metadata is checked before FTS sync.
// TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference 用于验证 fallback 主动写会在 FTS 同步前检查 insert 影响行数。
func TestStoreCreateDirectMemoryNodeRejectsZeroRowInsertBeforeFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "insert direct memory node") {
		t.Fatalf("expected zero-row fallback insert error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row fallback insert to remain a clean failure, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before fallback insert affects a row, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one fallback insert request before failure, got %d", len(requests))
	}
}

// TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert verifies fallback direct writes recover when the durable row already matches the insert.
// TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert 用于验证 fallback 主动写插入提交未知时，如果长期行已匹配插入目标即可恢复。
func TestStoreCreateDirectMemoryNodeReconcilesCommitUnknownInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "211")
	store := &Store{database: fake, timeout: time.Second}

	var insertRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			insertRequest = req
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if insertRequest == nil {
				t.Fatal("expected direct memory insert request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 211)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeRowJSONFromInsertParams(t, insertRequest.GetParams())}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	created, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err != nil {
		t.Fatalf("CreateDirectMemoryNode returned error: %v", err)
	}
	if created.ID != 211 || created.VectorID != "vec-direct" {
		t.Fatalf("unexpected recovered direct memory row: %+v", created)
	}
}

// TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh verifies unresolved fallback inserts keep the already-written vector protected.
// TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh 用于验证 fallback 插入无法对账时，会保留已经写入的新向量保护。
func TestStoreCreateDirectMemoryNodeKeepsUnmatchedCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			requireSQLiteInt64Params(t, req.GetParams(), 211)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain fallback insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved fallback insert to protect fresh vector reference, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain verifies fallback FTS failures retain the vector once the memory row exists.
// TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain 用于验证 fallback 记忆行已存在后，FTS 失败会保留新向量引用语义。
func TestStoreCreateDirectMemoryNodeMarksFTSFailureFreshVectorReferenceUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":211}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.upsertFtsDocumentFunc = func(string, sqliteffi.TokenizerMode, string, string, string, string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts unavailable")
	}

	_, err := store.CreateDirectMemoryNode(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-direct",
			Vector:      []float32{0.125, 0.875},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "direct abstract",
			Details:     "direct details",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected fallback fts failure to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fallback fts failure to retain fresh vector reference, got %v", err)
	}
}

// TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede verifies direct-write replacement keeps typed params without unsupported cross-call transaction control.
// TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede 用于验证主动写替代路径继续使用强类型参数，并且不依赖不受支持的跨调用事务控制。
func TestStoreApplyDirectMemoryWriteUsesTypedSQLiteParamsForInsertAndSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "301")
	expectSQLiteFTSDeletes(t, fake, "77", "78")
	store := &Store{database: fake, timeout: time.Second}

	// Capture every write statement because the direct-write applier splits one logical write into typed single statements.
	// 捕获每条写入语句，因为主动写 applier 会把一次逻辑写入拆成强类型单语句执行。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-direct"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	// Use duplicate and zero supersede ids to verify the status-update SQL binds the normalized numeric id list.
	// 使用重复和零值替代 id，验证状态更新 SQL 会绑定规范化后的数字 id 列表。
	vectorID := "  vec-apply '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  apply abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  apply details '; DROP TABLE vmm_memory_nodes; --  "
	statusReason := "  apply reason '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  apply hash '; DROP TABLE vmm_memory_nodes; --  "
	createdAt := time.Date(2026, 7, 5, 11, 45, 0, 0, time.UTC)
	result, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:      vectorID,
			Vector:        []float32{0.5, 0.75},
			SourceKind:    logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:    logicdomain.MemoryScopeLevelProject,
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      memoryAbstract,
			Details:       memoryDetails,
			Status:        logicdomain.MemoryStatusActive,
			Priority:      logicdomain.MemoryPriorityP1,
			MemoryLevel:   logicdomain.MemoryLevelStable,
			RefreshWeight: 2,
			StatusReason:  statusReason,
			DedupeHash:    dedupeHash,
			CreatedAt:     createdAt,
			UpdatedAt:     createdAt,
		},
		[]uint64{77, 0, 77, 78},
	)
	if err != nil {
		t.Fatalf("ApplyDirectMemoryWrite returned error: %v", err)
	}
	if result.InsertedMemoryNode.ID != 301 || result.InsertedMemoryNode.VectorID != strings.TrimSpace(vectorID) {
		t.Fatalf("unexpected direct write apply result: %+v", result)
	}
	if len(result.SupersededVectorIDs) != 1 || result.SupersededVectorIDs[0] != "vec-old-direct" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede requests, got %d", len(requests))
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "vec-apply", "apply abstract", "apply details", "apply reason", "apply hash"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected direct memory text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if !strings.Contains(requests[1].GetSql(), "id IN (?,?)") {
		t.Fatalf("expected typed supersede id placeholders, got %q", requests[1].GetSql())
	}
	if strings.Contains(requests[1].GetSql(), "77") || strings.Contains(requests[1].GetSql(), "78") {
		t.Fatalf("expected supersede memory ids to stay out of SQL text, got %q", requests[1].GetSql())
	}
	supersedeParams := requests[1].GetParams()
	if len(supersedeParams) != 5 {
		t.Fatalf("expected five supersede params, got %d: %#v", len(supersedeParams), supersedeParams)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{supersedeParams[0]}, int64(logicdomain.MemoryStatusSuperseded))
	if supersedeParams[1].Kind != sqliteffi.SQLValueInt64 || supersedeParams[1].Int64 <= 0 {
		t.Fatalf("expected positive supersede updated timestamp param, got %#v", supersedeParams[1])
	}
	requireSQLiteInt64Params(t, supersedeParams[2:], int64(logicdomain.MemoryStatusActive), 77, 78)
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	for _, value := range []string{
		strings.TrimSpace(vectorID),
		"[0.5,0.75]",
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(statusReason),
		strings.TrimSpace(dedupeHash),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
}

// TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain verifies stale replacement targets are reported as partial writes after the new memory row is inserted.
// TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain 用于验证新记忆行已经插入后，替代目标发生漂移会按部分写入上报。
func TestStoreApplyDirectMemoryWriteMarksSupersedeDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-direct-1"},{"vector_id":"vec-old-direct-2"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{77, 78},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected direct-write supersede drift to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected direct-write supersede drift to retain fresh vector reference, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede update before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh verifies unresolved fast-path inserts keep the already-written vector protected.
// TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh 用于验证 fast-path 插入无法对账时，会保留已经写入的新向量保护。
func TestStoreApplyDirectMemoryWriteKeepsUnmatchedInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit direct memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			requireSQLiteInt64Params(t, req.GetParams(), 301)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		nil,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain direct-write insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved direct-write insert to protect fresh vector reference, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede verifies uncertain fast-path supersede updates recover before FTS cleanup.
// TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede 用于验证 fast-path supersede 提交未知时，如果长期行已达标即可恢复并继续执行 FTS 清理。
func TestStoreApplyDirectMemoryWriteReconcilesCommitUnknownSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "301")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit direct memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"},{"vector_id":"vec-old-78"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?,?)"):
			if supersedeRequest == nil {
				t.Fatal("expected direct memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77, 78)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusSuperseded, supersedeRequest.GetParams()[1].Int64, 77, 78)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{78, 0, 77, 78},
	)
	if err != nil {
		t.Fatalf("ApplyDirectMemoryWrite returned error: %v", err)
	}
	if len(result.SupersededVectorIDs) != 2 || result.SupersededVectorIDs[0] != "vec-old-77" || result.SupersededVectorIDs[1] != "vec-old-78" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "77" || deletedFTSIDs[1] != "78" {
		t.Fatalf("expected recovered direct supersede to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected direct insert and supersede requests, got %d", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh verifies unresolved fast-path supersede ambiguity keeps fresh vectors protected.
// TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh 用于验证 fast-path supersede 无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyDirectMemoryWriteKeepsUnmatchedSupersedeCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit direct memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if supersedeRequest == nil {
				t.Fatal("expected direct memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusActive, supersedeRequest.GetParams()[1].Int64, 77)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		[]uint64{77},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain direct-write supersede error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved direct-write supersede to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede direct memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected direct-write supersede commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved direct supersede, got %+v", deletedFTSIDs)
	}
	if len(requests) != 2 {
		t.Fatalf("expected direct insert and supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference verifies a no-op first insert remains a clean direct-write failure.
// TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference 用于验证首个 insert 未影响行时，主动写仍保持为尚未引用新向量的干净失败。
func TestStoreApplyDirectMemoryWriteRejectsZeroRowInsertBeforeFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyDirectMemoryWrite(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.MemoryNodeRecord{
			VectorID:    "vec-new",
			Vector:      []float32{0.5, 0.75},
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "project phase",
			Details:     "project phase",
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "insert direct memory node") {
		t.Fatalf("expected zero-row direct insert error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row first insert to remain a clean failure, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before direct insert affects a row, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only direct memory insert before failure, got %d requests", len(requests))
	}
}
