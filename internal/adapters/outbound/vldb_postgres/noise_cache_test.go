// noise_cache_test.go verifies PostgreSQL noise-cache persistence error classification without requiring a live database.
// noise_cache_test.go 用于在不依赖真实数据库的情况下验证 PostgreSQL 噪声缓存持久化错误分类。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestPostgresNoiseCacheCommitErrorMarksOutcomeUncertain verifies cache replace commit failures expose the uncertain transaction outcome.
// TestPostgresNoiseCacheCommitErrorMarksOutcomeUncertain 用于验证缓存替换提交失败会暴露事务结果不确定。
func TestPostgresNoiseCacheCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresNoiseCacheCommitOutcomeUncertainError(errors.New("network closed"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain noise cache commit error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "commit postgres noise embedding cache replace: network closed") {
		t.Fatalf("unexpected noise cache commit error detail: %v", err)
	}
}
