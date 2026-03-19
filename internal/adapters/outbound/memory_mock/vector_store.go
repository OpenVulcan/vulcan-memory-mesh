package memory_mock

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type VectorStore struct {
	mu      sync.RWMutex
	records map[string]logicdomain.MemoryRecord
}

func NewVectorStore() *VectorStore {
	return &VectorStore{records: map[string]logicdomain.MemoryRecord{}}
}
func (s *VectorStore) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(record.ID) == "" {
		return fmt.Errorf("memory record id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.ID] = record
	return nil
}
func (s *VectorStore) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hits := make([]logicdomain.MemoryHit, 0, len(s.records))
	for _, record := range s.records {
		if !matchFilter(record.Filter, filter) {
			continue
		}
		score, err := cosine(record.Vector, vector)
		if err != nil {
			return nil, err
		}
		hits = append(hits, logicdomain.MemoryHit{ID: record.ID, Text: record.Text, Score: score, Filter: record.Filter, Metadata: record.Metadata})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}
func matchFilter(record, target logicdomain.SearchFilter) bool {
	userOK := record.UserID == target.UserID || record.UserID == "" || record.UserID == "0"
	projectOK := target.ProjectID == "" || record.ProjectID == target.ProjectID
	spaceOK := target.SpaceID == "" || record.SpaceID == "" || record.SpaceID == target.SpaceID
	return userOK && projectOK && spaceOK
}
func (s *VectorStore) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
