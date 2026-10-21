// Vector integrity tests cover SQLite write preparation and management restore before durable mutation.
// 向量完整性测试覆盖 SQLite 写入准备和人工恢复的持久化变更前校验。
package vldb_sqlite

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestDurableVectorWritesRejectNonFinite checks insertion and maintenance reject NaN/Inf before any database call.
// TestDurableVectorWritesRejectNonFinite 验证插入和维护在任何数据库调用前拒绝 NaN/Inf。
func TestDurableVectorWritesRejectNonFinite(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			record := logicdomain.MemoryNodeRecord{ID: 77, VectorID: "vector-77", Vector: []float32{float32(value)}}
			statement, err := parameterizedMemoryNodeInsertStatement(record)
			if err == nil || !strings.Contains(err.Error(), "vector-77") || statement.SQL != "" {
				t.Fatalf("invalid insert must fail before producing SQL: statement=%+v err=%v", statement, err)
			}
			store := &Store{database: &fakeSQLiteDatabase{}}
			err = store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{{ID: record.VectorID, Vector: record.Vector}})
			if err == nil || !strings.Contains(err.Error(), "vector-77") || strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("invalid maintenance vector reached database: %v", err)
			}
		})
	}
}

// TestManagementRestoreRejectsCorruptVectorBeforeWrites verifies a malformed snapshot cannot partially restore active rows.
// TestManagementRestoreRejectsCorruptVectorBeforeWrites 验证损坏快照不会先部分恢复活动行再报错。
func TestManagementRestoreRejectsCorruptVectorBeforeWrites(t *testing.T) {
	database := &fakeSQLiteDatabase{
		queryJSONFunc: func(_ context.Context, request *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
			if strings.Contains(request.SQL, "COUNT(*)") {
				return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`, RowCount: 1}, nil
			}
			if strings.Contains(request.SQL, "FROM vmm_memory_nodes_trash WHERE batch_id") {
				return &fakeQueryJSONResponse{JsonData: `[{"id":77,"vector_id":"vector-77","vector_json":"[null,2]"}]`, RowCount: 1}, nil
			}
			return nil, fmt.Errorf("unexpected restore query: %s", request.SQL)
		},
	}
	store := &Store{database: database}
	err := store.restoreSQLiteManagementBatchRows(context.Background(), 5)
	if err == nil || !strings.Contains(err.Error(), "vector-77") || !strings.Contains(err.Error(), "vector_json") {
		t.Fatalf("restore must report corrupt snapshot before executing any writes: %v", err)
	}
}
