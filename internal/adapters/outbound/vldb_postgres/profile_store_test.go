// profile_store_test.go verifies PostgreSQL profile-store guards that protect manual-instruction state transitions without requiring a live database.
// profile_store_test.go 用于验证 PostgreSQL profile store 在无真实数据库时仍能保护手工指令状态迁移边界。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// captureRenderedProfileQueryer records rendered-profile Exec calls for repository helper tests.
// captureRenderedProfileQueryer 用于记录渲染画像 helper 测试中的 Exec 调用。
type captureRenderedProfileQueryer struct {
	execArgs [][]any
	execSQL  []string
	tags     []pgconn.CommandTag
}

// Exec records one rendered-profile update and returns the next configured command tag.
// Exec 用于记录一次渲染画像更新，并返回下一条预设 command tag。
func (q *captureRenderedProfileQueryer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	q.execSQL = append(q.execSQL, sql)
	q.execArgs = append(q.execArgs, append([]any(nil), args...))
	if len(q.tags) == 0 {
		return pgconn.CommandTag{}, nil
	}
	tag := q.tags[0]
	q.tags = q.tags[1:]
	return tag, nil
}

// Query should stay unused in rendered-profile helper tests.
// Query 在渲染画像 helper 测试中不应被调用。
func (q *captureRenderedProfileQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query call")
}

// QueryRow should stay unused in rendered-profile helper tests.
// QueryRow 在渲染画像 helper 测试中不应被调用。
func (q *captureRenderedProfileQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

// TestRequirePostgresProfileInstructionRowsAffectedRejectsDrift verifies manual-instruction status updates cannot silently miss the pending instruction row.
// TestRequirePostgresProfileInstructionRowsAffectedRejectsDrift 用于验证手工指令状态更新不能静默漏掉 pending 指令行。
func TestRequirePostgresProfileInstructionRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresProfileInstructionRowsAffected("mark postgres profile instruction failed", 77, 1, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresProfileInstructionRowsAffected("mark postgres profile instruction failed", 77, 0, 1)
	if err == nil {
		t.Fatal("expected profile-instruction row drift to fail")
	}
	if !strings.Contains(err.Error(), "mark postgres profile instruction failed for instruction 77 affected 0 rows, want 1") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestRequirePostgresManualProfileRowsAffectedRejectsDrift verifies manual-profile transaction updates stop before commit when row counts drift.
// TestRequirePostgresManualProfileRowsAffectedRejectsDrift 用于验证手工画像事务更新在行数漂移时会在提交前停止。
func TestRequirePostgresManualProfileRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresManualProfileRowsAffected("retire postgres manual profile node 9", 1, 1); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresManualProfileRowsAffected("supersede postgres manual profile nodes for 101", 1, 2)
	if err == nil {
		t.Fatal("expected manual-profile row drift to fail")
	}
	if !strings.Contains(err.Error(), "supersede postgres manual profile nodes for 101 affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestStandalonePostgresProfileRetireDecisionsSkipsSuperseded verifies supersede decisions are not applied again by the standalone retire loop.
// TestStandalonePostgresProfileRetireDecisionsSkipsSuperseded 用于验证 supersede 决策不会被独立 retire 循环再次落库。
func TestStandalonePostgresProfileRetireDecisionsSkipsSuperseded(t *testing.T) {
	retired := []logicdomain.ProfileRetireDecision{
		{NodeID: 7, Reason: "replaced"},
		{NodeID: 9, Reason: "explicit retire"},
	}
	standalone := standalonePostgresProfileRetireDecisions(retired, map[uint64]struct{}{7: {}})
	if len(standalone) != 1 || standalone[0].NodeID != 9 {
		t.Fatalf("expected only standalone retire decision to remain, got %+v", standalone)
	}
}

// TestPostgresManualProfileCommitErrorMarksOutcomeUncertain verifies manual-profile commits expose ambiguous node, instruction, and rendered-profile writeback state.
// TestPostgresManualProfileCommitErrorMarksOutcomeUncertain 用于验证手工画像提交会暴露节点、指令状态和渲染画像写回结果不确定。
func TestPostgresManualProfileCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresManualProfileCommitOutcomeUncertainError(errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain manual profile commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect manual profile commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres manual profile tx: commit acknowledgement lost") {
		t.Fatalf("unexpected manual profile commit error detail: %v", err)
	}
}

// TestPostgresProfileConvergenceCommitErrorMarksOutcomeUncertain verifies lifecycle convergence commits expose ambiguous expired-node status flips.
// TestPostgresProfileConvergenceCommitErrorMarksOutcomeUncertain 用于验证画像生命周期收敛提交会暴露过期节点状态切换结果不确定。
func TestPostgresProfileConvergenceCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresProfileConvergenceCommitOutcomeUncertainError(errors.New("network closed"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain profile convergence commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect profile convergence commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres profile convergence tx: network closed") {
		t.Fatalf("unexpected profile convergence commit error detail: %v", err)
	}
}

// TestPostgresRenderedProfileReplaceCommitErrorMarksOutcomeUncertain verifies rendered-profile replace commits expose ambiguous scope-blob state.
// TestPostgresRenderedProfileReplaceCommitErrorMarksOutcomeUncertain 用于验证渲染画像替换提交会暴露 scope Blob 写入结果不确定。
func TestPostgresRenderedProfileReplaceCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresRenderedProfileReplaceCommitOutcomeUncertainError(errors.New("commit timeout"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain rendered-profile replace commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect rendered-profile replace commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres rendered-profile replace tx: commit timeout") {
		t.Fatalf("unexpected rendered-profile replace commit error detail: %v", err)
	}
}

// TestReplaceRenderedProfileBatchChecksRowsAffected verifies PostgreSQL rendered-profile helpers update every sorted target id.
// TestReplaceRenderedProfileBatchChecksRowsAffected 用于验证 PostgreSQL 渲染画像 helper 会更新每个排序后的目标 id。
func TestReplaceRenderedProfileBatchChecksRowsAffected(t *testing.T) {
	repo := &profileRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	queryer := &captureRenderedProfileQueryer{
		tags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1"), pgconn.NewCommandTag("UPDATE 1")},
	}

	err := repo.replaceRenderedProfileBatch(context.Background(), queryer, logicdomain.ProfileTypeProject, map[uint64]string{
		9: "project profile",
		7: "earlier project",
	}, time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("replaceRenderedProfileBatch returned error: %v", err)
	}
	if len(queryer.execArgs) != 2 {
		t.Fatalf("expected two rendered profile updates, got %d", len(queryer.execArgs))
	}
	if !strings.Contains(queryer.execSQL[0], `"vmm_projects"`) {
		t.Fatalf("expected project table update SQL, got %q", queryer.execSQL[0])
	}
	if queryer.execArgs[0][2] != int64(7) || queryer.execArgs[1][2] != int64(9) {
		t.Fatalf("expected sorted project ids 7 then 9, got %#v", queryer.execArgs)
	}
}

// TestReplaceRenderedProfileBatchRejectsRowsAffectedDrift verifies PostgreSQL rendered-profile helpers reject missing target rows.
// TestReplaceRenderedProfileBatchRejectsRowsAffectedDrift 用于验证 PostgreSQL 渲染画像 helper 会拒绝缺失目标行。
func TestReplaceRenderedProfileBatchRejectsRowsAffectedDrift(t *testing.T) {
	repo := &profileRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	queryer := &captureRenderedProfileQueryer{
		tags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 0")},
	}

	err := repo.replaceRenderedProfileBatch(context.Background(), queryer, logicdomain.ProfileTypeProject, map[uint64]string{
		9: "project profile",
	}, time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC))
	if err == nil {
		t.Fatal("expected rendered profile row drift to fail")
	}
	if !strings.Contains(err.Error(), "update postgres rendered profile target affected 0 rows, want 1") {
		t.Fatalf("unexpected rendered profile drift error: %v", err)
	}
}
