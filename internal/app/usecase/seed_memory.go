// seed_memory.go implements application use cases.
// seed_memory.go 用于实现应用用例层。
package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// SeedMemoryCommand carries one admin seed request into the vector preloading workflow.
// SeedMemoryCommand 用于承载一条管理员灌库请求进入向量预热流程。
type SeedMemoryCommand struct{ UserID, ProjectID, MemoryText, SpaceID string }

// SeedMemoryResult returns the seed-memory execution acknowledgement and generated memory id.
// SeedMemoryResult 用于返回 seed-memory 执行确认结果和生成的记忆 ID。
type SeedMemoryResult struct {
	Accepted bool
	MemoryID string
	TraceID  string
}

// SeedMemoryExecutor is the interface consumed by the HTTP adapter to preload vector memory entries.
// SeedMemoryExecutor 用于让 HTTP 适配层预热向量记忆条目。
type SeedMemoryExecutor interface {
	Execute(ctx context.Context, cmd SeedMemoryCommand) (SeedMemoryResult, error)
}

// SeedMemoryUseCase orchestrates embedding generation and vector upsert for admin-seeded memories.
// SeedMemoryUseCase 用于编排管理员灌库场景下的 embedding 生成与向量写入。
type SeedMemoryUseCase struct {
	embedding      appports.EmbeddingClient
	vector         appports.VectorStore
	ids            appports.IDGenerator
	logger         *logx.Logger
	embedModel     string
	embedDimension int
}

// NewSeedMemoryUseCase creates a SeedMemoryUseCase instance.
// NewSeedMemoryUseCase 用于创建 SeedMemoryUseCase 实例。
func NewSeedMemoryUseCase(embedding appports.EmbeddingClient, vector appports.VectorStore, ids appports.IDGenerator, logger *logx.Logger, embedModel string, embedDimension int) *SeedMemoryUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &SeedMemoryUseCase{embedding: embedding, vector: vector, ids: ids, logger: logger, embedModel: embedModel, embedDimension: embedDimension}
}

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (u *SeedMemoryUseCase) Execute(ctx context.Context, cmd SeedMemoryCommand) (SeedMemoryResult, error) {
	// Validate the seed request before performing any outbound call.
	// 在发起任何出站调用之前先校验灌库请求。
	if strings.TrimSpace(cmd.UserID) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "user_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.ProjectID) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "project_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.MemoryText) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "memory_text", Message: "is required"}
	}

	// Generate the embedding first so the memory record can be stored with its vector payload.
	// 先生成向量表示，再携带向量内容写入记忆记录。
	resp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Model: u.embedModel, Texts: []string{strings.TrimSpace(cmd.MemoryText)}, Dimension: u.embedDimension})
	if err != nil {
		return SeedMemoryResult{}, fmt.Errorf("embed memory text: %w", err)
	}
	if len(resp.Vectors) == 0 {
		return SeedMemoryResult{}, fmt.Errorf("embed memory text: empty vectors")
	}

	// Create the record ID, build the memory payload, and store it in the vector backend.
	// 生成记录 ID、构造记忆载荷，并写入向量后端。
	id := u.ids.NewID("mem")
	record := logicdomain.MemoryRecord{ID: id, Text: strings.TrimSpace(cmd.MemoryText), Vector: resp.Vectors[0], Filter: logicdomain.SearchFilter{UserID: cmd.UserID, ProjectID: cmd.ProjectID, SpaceID: cmd.SpaceID}, CreatedAt: time.Now().UTC(), Metadata: map[string]string{"source": "admin_seed"}}
	if err := u.vector.Upsert(ctx, record); err != nil {
		return SeedMemoryResult{}, fmt.Errorf("upsert memory: %w", err)
	}
	return SeedMemoryResult{Accepted: true, MemoryID: id, TraceID: trace.IDFromContext(ctx)}, nil
}
