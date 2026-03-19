// vector_store.go implements the in-memory mock outbound adapters.
// vector_store.go 用于实现内存版 mock 出站适配器。
package memory_mock

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// VectorStore is the in-memory vector adapter used for local recall, seeding, and deterministic tests.
// VectorStore 用于作为本地召回、灌库和确定性测试中的内存向量适配器。
type VectorStore struct {
	mu      sync.RWMutex
	records map[string]logicdomain.MemoryRecord
}

// NewVectorStore creates a VectorStore instance.
// NewVectorStore 用于创建 VectorStore 实例。
func NewVectorStore() *VectorStore {
	return &VectorStore{records: map[string]logicdomain.MemoryRecord{}}
}

// Upsert executes the Upsert logic.
// Upsert 用于执行 Upsert 逻辑。
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

// Search executes the Search logic.
// Search 用于执行 Search 逻辑。
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

// matchFilter executes the matchFilter logic.
// matchFilter 用于执行 matchFilter 逻辑。
func matchFilter(record, target logicdomain.SearchFilter) bool {
	userOK := record.UserID == target.UserID || record.UserID == "" || record.UserID == "0"
	projectOK := target.ProjectID == "" || record.ProjectID == target.ProjectID
	spaceOK := target.SpaceID == "" || record.SpaceID == "" || record.SpaceID == target.SpaceID
	return userOK && projectOK && spaceOK
}

// Shutdown executes the Shutdown logic.
// Shutdown 用于执行 Shutdown 逻辑。
func (s *VectorStore) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
