// vector_gc_compensation.go bridges best-effort vector cleanup failures into the persistent retry queue shared with retention maintenance.
// vector_gc_compensation.go 用于把 best-effort 的向量清理失败桥接到与 retention 共享的持久化重试队列。
package usecase

import (
	"context"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/timeutil"
)

const (
	// defaultPostCommitVectorCleanupTimeout bounds sidecar cleanup that must survive caller cancellation after the relational outcome has already committed.
	// defaultPostCommitVectorCleanupTimeout 用于限制关系结果已提交后仍需脱离调用方取消继续执行的旁路向量清理时长。
	defaultPostCommitVectorCleanupTimeout = 30 * time.Second
)

// vectorGCJobEnqueuer is the narrow persistent retry-queue contract reused by post-action and direct-write compensation paths.
// vectorGCJobEnqueuer 用于声明 post-action 与 direct-write 补偿路径复用的狭窄持久化重试队列契约。
type vectorGCJobEnqueuer interface {
	EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error
}

// enqueueVectorGCCompensation persists one failed sidecar vector delete into the retry queue when the backing store exposes the retention queue contract.
// enqueueVectorGCCompensation 用于在底层存储暴露 retention 队列契约时，把一次失败的旁路向量删除持久化到重试队列。
func enqueueVectorGCCompensation(ctx context.Context, store any, logger *logx.Logger, jobType string, vectorIDs []string, now time.Time, logFields ...any) {
	vectorIDs = normalizeVectorGCIDs(vectorIDs)
	if len(vectorIDs) == 0 || strings.TrimSpace(jobType) == "" {
		return
	}
	enqueuer, ok := store.(vectorGCJobEnqueuer)
	if !ok || enqueuer == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := enqueuer.EnqueueVectorGCJobs(ctx, logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   0,
		JobType:   jobType,
		VectorIDs: append([]string(nil), vectorIDs...),
		NextRunAt: timeutil.UTCOrNow(now).Add(defaultRetentionVectorGCRetryDelay),
	}); err != nil {
		if logger != nil {
			fields := append([]any{"job_type", jobType, "vector_count", len(vectorIDs), "err", err}, logFields...)
			logger.Error("vector gc compensation enqueue failed", fields...)
		}
	}
}

// newPostCommitVectorCleanupContext creates a bounded detached context for sidecar cleanup after a durable relational outcome already exists.
// newPostCommitVectorCleanupContext 用于在长期关系结果已经存在后，为旁路向量清理创建一个有界且脱离请求取消的 context。
func newPostCommitVectorCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), defaultPostCommitVectorCleanupTimeout)
}

// normalizeVectorGCIDs trims invalid vector ids and removes duplicates before best-effort deletes or retry jobs cross the usecase boundary.
// normalizeVectorGCIDs 用于在尽力删除或重试任务跨越用例边界前，裁剪无效向量 id 并移除重复项。
func normalizeVectorGCIDs(vectorIDs []string) []string {
	if len(vectorIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(vectorIDs))
	out := make([]string, 0, len(vectorIDs))
	for _, vectorID := range vectorIDs {
		vectorID = strings.TrimSpace(vectorID)
		if vectorID == "" {
			continue
		}
		if _, ok := seen[vectorID]; ok {
			continue
		}
		seen[vectorID] = struct{}{}
		out = append(out, vectorID)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
