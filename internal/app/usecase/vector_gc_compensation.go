// vector_gc_compensation.go bridges best-effort vector cleanup failures into the persistent retry queue shared with retention maintenance.
// vector_gc_compensation.go 用于把 best-effort 的向量清理失败桥接到与 retention 共享的持久化重试队列。
package usecase

import (
	"context"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// vectorGCJobEnqueuer is the narrow persistent retry-queue contract reused by post-action and direct-write compensation paths.
// vectorGCJobEnqueuer 用于声明 post-action 与 direct-write 补偿路径复用的狭窄持久化重试队列契约。
type vectorGCJobEnqueuer interface {
	EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error
}

// enqueueVectorGCCompensation persists one failed sidecar vector delete into the retry queue when the backing store exposes the retention queue contract.
// enqueueVectorGCCompensation 用于在底层存储暴露 retention 队列契约时，把一次失败的旁路向量删除持久化到重试队列。
func enqueueVectorGCCompensation(ctx context.Context, store any, logger *logx.Logger, jobType string, vectorIDs []string, now time.Time, logFields ...any) {
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
		NextRunAt: chooseRetentionTimeOrNow(now).Add(defaultRetentionVectorGCRetryDelay),
	}); err != nil {
		if logger != nil {
			fields := append([]any{"job_type", jobType, "vector_count", len(vectorIDs), "err", err}, logFields...)
			logger.Error("vector gc compensation enqueue failed", fields...)
		}
	}
}
