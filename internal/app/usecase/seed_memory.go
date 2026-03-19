package usecase

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type SeedMemoryCommand struct{ UserID, ProjectID, MemoryText, SpaceID string }
type SeedMemoryResult struct {
	Accepted bool
	MemoryID string
	TraceID  string
}
type SeedMemoryExecutor interface {
	Execute(ctx context.Context, cmd SeedMemoryCommand) (SeedMemoryResult, error)
}

type SeedMemoryUseCase struct {
	embedding      appports.EmbeddingClient
	vector         appports.VectorStore
	ids            appports.IDGenerator
	logger         *log.Logger
	embedModel     string
	embedDimension int
}

func NewSeedMemoryUseCase(embedding appports.EmbeddingClient, vector appports.VectorStore, ids appports.IDGenerator, logger *log.Logger, embedModel string, embedDimension int) *SeedMemoryUseCase {
	if logger == nil {
		logger = log.Default()
	}
	return &SeedMemoryUseCase{embedding: embedding, vector: vector, ids: ids, logger: logger, embedModel: embedModel, embedDimension: embedDimension}
}

func (u *SeedMemoryUseCase) Execute(ctx context.Context, cmd SeedMemoryCommand) (SeedMemoryResult, error) {
	if strings.TrimSpace(cmd.UserID) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "user_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.ProjectID) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "project_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.MemoryText) == "" {
		return SeedMemoryResult{}, logicdomain.ValidationError{Field: "memory_text", Message: "is required"}
	}
	resp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Model: u.embedModel, Texts: []string{strings.TrimSpace(cmd.MemoryText)}, Dimension: u.embedDimension})
	if err != nil {
		return SeedMemoryResult{}, fmt.Errorf("embed memory text: %w", err)
	}
	if len(resp.Vectors) == 0 {
		return SeedMemoryResult{}, fmt.Errorf("embed memory text: empty vectors")
	}
	id := u.ids.NewID("mem")
	record := logicdomain.MemoryRecord{ID: id, Text: strings.TrimSpace(cmd.MemoryText), Vector: resp.Vectors[0], Filter: logicdomain.SearchFilter{UserID: cmd.UserID, ProjectID: cmd.ProjectID, SpaceID: cmd.SpaceID}, CreatedAt: time.Now().UTC(), Metadata: map[string]string{"source": "admin_seed"}}
	if err := u.vector.Upsert(ctx, record); err != nil {
		return SeedMemoryResult{}, fmt.Errorf("upsert memory: %w", err)
	}
	return SeedMemoryResult{Accepted: true, MemoryID: id, TraceID: trace.IDFromContext(ctx)}, nil
}
