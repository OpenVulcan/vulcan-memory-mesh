package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type SeedMemoryService struct {
	embedding ports.EmbeddingClient
	vector    ports.VectorStore
	ids       ports.IDGenerator
	logger    *log.Logger
}

func NewSeedMemoryService(embedding ports.EmbeddingClient, vector ports.VectorStore, ids ports.IDGenerator, logger *log.Logger) *SeedMemoryService {
	if logger == nil { logger = log.Default() }
	return &SeedMemoryService{embedding: embedding, vector: vector, ids: ids, logger: logger}
}

func (s *SeedMemoryService) Execute(ctx context.Context, req domain.SeedMemoryRequest) (domain.SeedMemoryResponse, error) {
	if strings.TrimSpace(req.UserID) == "" { return domain.SeedMemoryResponse{}, domain.ValidationError{Field: "user_id", Message: "is required"} }
	if strings.TrimSpace(req.ProjectID) == "" { return domain.SeedMemoryResponse{}, domain.ValidationError{Field: "project_id", Message: "is required"} }
	if strings.TrimSpace(req.MemoryText) == "" { return domain.SeedMemoryResponse{}, domain.ValidationError{Field: "memory_text", Message: "is required"} }
	vec, err := s.embedding.EmbedText(ctx, req.MemoryText)
	if err != nil { return domain.SeedMemoryResponse{}, fmt.Errorf("embed memory text: %w", err) }
	id := s.ids.NewID("mem")
	record := domain.MemoryRecord{
		ID: id, Text: strings.TrimSpace(req.MemoryText), Vector: vec,
		Filter: domain.SearchFilter{UserID: req.UserID, ProjectID: req.ProjectID, SpaceID: req.SpaceID},
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]string{"source":"admin_seed"},
	}
	if err := s.vector.Upsert(ctx, record); err != nil { return domain.SeedMemoryResponse{}, fmt.Errorf("upsert memory: %w", err) }
	return domain.SeedMemoryResponse{Accepted:true, MemoryID:id, TraceID: trace.IDFromContext(ctx)}, nil
}
