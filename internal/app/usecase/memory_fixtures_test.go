// Memory use-case tests for memory fixtures test.
// 记忆应用用例层共享测试夹具测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"time"
)

// stubTurnLookupStore supplies deterministic turn rows for memory-query tests.
// stubTurnLookupStore 用于为记忆查询测试提供确定性的 turn 行。
type stubTurnLookupStore struct {
	turnIDs                 []uint64
	rows                    []logicdomain.SessionTurnRecord
	windows                 map[uint64]logicdomain.TurnDetailWindow
	memoryRowsByID          []logicdomain.MemoryNodeRecord
	memoryRowsByIDErr       error
	memoryRowsByIDCalls     [][]uint64
	memoryContextEdges      []logicdomain.MemoryContextEdge
	memoryRowsByVector      []logicdomain.MemoryNodeRecord
	memoryRowsByVectorCalls [][]string
	lexicalHits             []logicdomain.MemoryLexicalHit
	lexicalErr              error
	lexicalQueries          []string
	lexicalTopKs            []int
	lexicalFilters          []logicdomain.SearchFilter
	contextLookupIDs        []uint64
	// contextLookupIDCalls records every context-edge lookup so multi-query tests can assert request-local cache behavior.
	// contextLookupIDCalls 用于记录每一次 context-edge 查询，方便多 query 测试断言请求级缓存行为。
	contextLookupIDCalls   [][]uint64
	recentDedupeQueries    []string
	recentDedupeRowsByHash map[string]logicdomain.MemoryNodeRecord
	recentDedupeRow        logicdomain.MemoryNodeRecord
	recentDedupeHit        bool
	directWriteApplyCalls  []stubDirectMemoryWriteApplyCall
	directWriteApplyResult logicdomain.DirectMemoryWriteApplyResult
	directWriteApplyErr    error
	afterDirectWriteApply  func()
	deleteMemoryCalls      []stubDeleteMemoryCall
	deleteMemoryResult     logicdomain.MemoryDeleteResult
	deleteMemoryErr        error
	vectorGCEnqueueQuery   logicdomain.VectorGCJobEnqueueQuery
	vectorGCEnqueueCtxErrs []error
	err                    error
}

// stubDeleteMemoryCall records one manual memory delete request so use-case tests can assert scope and audit reason propagation.
// stubDeleteMemoryCall 用于记录一次手工删除记忆请求，方便用例测试断言 scope 与审计原因传递。
type stubDeleteMemoryCall struct {
	MemoryIDs []uint64
	Filter    logicdomain.SearchFilter
	DeletedAt time.Time
	Reason    string
}

// stubDirectMemoryWriteApplyCall records one atomic direct-memory write attempt so tests can assert the store sees the new row plus the intended supersede targets together.
// stubDirectMemoryWriteApplyCall 用于记录一次原子主动写记忆尝试，让测试可以断言存储层同时看到了新行和预期 supersede 目标。
type stubDirectMemoryWriteApplyCall struct {
	Session             logicdomain.SessionRef
	Record              logicdomain.MemoryNodeRecord
	SupersededMemoryIDs []uint64
}

// LoadTurnsByIDs records the requested ids and returns the canned rows.
// LoadTurnsByIDs 用于记录请求的 id，并返回预设行。
func (s *stubTurnLookupStore) LoadTurnsByIDs(_ context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	s.turnIDs = append([]uint64(nil), turnIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.SessionTurnRecord(nil), s.rows...), nil
}

// LoadTurnWindows returns canned neighboring turn ids for deterministic turn-detail assertions.
// LoadTurnWindows 用于返回预设的相邻 turn 编号，保证 turn 详情断言稳定。
func (s *stubTurnLookupStore) LoadTurnWindows(_ context.Context, _ []uint64, _ int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.windows == nil {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
	cloned := make(map[uint64]logicdomain.TurnDetailWindow, len(s.windows))
	for key, value := range s.windows {
		cloned[key] = logicdomain.TurnDetailWindow{
			TurnID:          value.TurnID,
			PreviousTurnIDs: append([]uint64(nil), value.PreviousTurnIDs...),
			NextTurnIDs:     append([]uint64(nil), value.NextTurnIDs...),
		}
	}
	return cloned, nil
}

// LoadMemoryNodesByIDs records requested memory ids and returns canned memory rows for mixed memory-detail assertions.
// LoadMemoryNodesByIDs 用于记录请求的 memory id，并返回混合记忆详情断言所需的预设记忆行。
func (s *stubTurnLookupStore) LoadMemoryNodesByIDs(_ context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	s.memoryRowsByIDCalls = append(s.memoryRowsByIDCalls, append([]uint64(nil), memoryIDs...))
	if s.memoryRowsByIDErr != nil {
		return nil, s.memoryRowsByIDErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryNodeRecord(nil), s.memoryRowsByID...), nil
}

// LoadMemoryContextEdgesByMemoryIDs returns canned context edges for context-aware retrieval assertions.
// LoadMemoryContextEdgesByMemoryIDs 用于返回情境感知检索断言需要的预设 context edges。
func (s *stubTurnLookupStore) LoadMemoryContextEdgesByMemoryIDs(_ context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	copiedIDs := append([]uint64(nil), memoryIDs...)
	s.contextLookupIDs = copiedIDs
	s.contextLookupIDCalls = append(s.contextLookupIDCalls, copiedIDs)
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryContextEdge(nil), s.memoryContextEdges...), nil
}

// LoadMemoryNodesByVectorIDs records requested vector ids and returns canned memory rows for search-hit enrichment assertions.
// LoadMemoryNodesByVectorIDs 用于记录请求的 vector id，并返回搜索命中补全断言所需的预设记忆行。
func (s *stubTurnLookupStore) LoadMemoryNodesByVectorIDs(_ context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	s.memoryRowsByVectorCalls = append(s.memoryRowsByVectorCalls, append([]string(nil), vectorIDs...))
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryNodeRecord(nil), s.memoryRowsByVector...), nil
}

// SearchLexicalMemory records the lexical search request and returns canned materialized lexical hits for hybrid-recall assertions.
// SearchLexicalMemory 用于记录 lexical 搜索请求，并返回预设的已物化 lexical 命中，供混合召回断言使用。
func (s *stubTurnLookupStore) SearchLexicalMemory(_ context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	s.lexicalQueries = append(s.lexicalQueries, query)
	s.lexicalTopKs = append(s.lexicalTopKs, topK)
	s.lexicalFilters = append(s.lexicalFilters, filter)
	if s.lexicalErr != nil {
		return nil, s.lexicalErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return materializeStubLexicalHits(s.lexicalHits, s.memoryRowsByID), nil
}

// materializeStubLexicalHits mirrors the real lexical-store contract by attaching deterministic canned rows to test hits that only specify a memory id.
// materializeStubLexicalHits 用于镜像真实 lexical 存储契约，把确定性的预设行补到只声明 memory id 的测试命中上。
func materializeStubLexicalHits(hits []logicdomain.MemoryLexicalHit, rows []logicdomain.MemoryNodeRecord) []logicdomain.MemoryLexicalHit {
	if len(hits) == 0 {
		return []logicdomain.MemoryLexicalHit{}
	}
	rowsByID := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		rowsByID[row.ID] = cloneMemoryNodeRecordForTest(row)
	}
	out := make([]logicdomain.MemoryLexicalHit, 0, len(hits))
	for _, hit := range hits {
		cloned := hit
		if cloned.Record.ID == 0 {
			cloned.Record = rowsByID[cloned.MemoryID]
		} else {
			cloned.Record = cloneMemoryNodeRecordForTest(cloned.Record)
		}
		out = append(out, cloned)
	}
	return out
}

// cloneMemoryNodeRecordForTest copies slice fields so test stubs cannot share mutable vector backing arrays across assertions.
// cloneMemoryNodeRecordForTest 用于复制切片字段，避免测试桩在断言之间共享可变的向量底层数组。
func cloneMemoryNodeRecordForTest(row logicdomain.MemoryNodeRecord) logicdomain.MemoryNodeRecord {
	row.Vector = append([]float32(nil), row.Vector...)
	return row
}

// FindRecentActiveMemoryByDedupe records short-window dedupe lookups and can return either hash-specific rows or the legacy canned row for older tests.
// FindRecentActiveMemoryByDedupe 用于记录短窗口幂等查询，并支持按哈希返回特定行；旧测试若未配置哈希映射，则继续走兼容的单行预设返回。
func (s *stubTurnLookupStore) FindRecentActiveMemoryByDedupe(_ context.Context, _ logicdomain.SessionRef, _, _ int, dedupeHash string, _ time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	s.recentDedupeQueries = append(s.recentDedupeQueries, dedupeHash)
	if s.err != nil {
		return logicdomain.MemoryNodeRecord{}, false, s.err
	}
	if len(s.recentDedupeRowsByHash) > 0 {
		row, ok := s.recentDedupeRowsByHash[dedupeHash]
		if !ok {
			return logicdomain.MemoryNodeRecord{}, false, nil
		}
		return row, true, nil
	}
	if !s.recentDedupeHit {
		return logicdomain.MemoryNodeRecord{}, false, nil
	}
	return s.recentDedupeRow, true, nil
}

// ApplyDirectMemoryWrite records one atomic direct-write apply call and returns the canned result so write-flow tests can verify supersede ids and vector cleanup behavior.
// ApplyDirectMemoryWrite 用于记录一次原子主动写入调用，并返回预设结果，方便主动写流程测试校验 supersede id 和向量清理行为。
func (s *stubTurnLookupStore) ApplyDirectMemoryWrite(_ context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	call := stubDirectMemoryWriteApplyCall{
		Session:             session,
		Record:              record,
		SupersededMemoryIDs: append([]uint64(nil), supersededMemoryIDs...),
	}
	s.directWriteApplyCalls = append(s.directWriteApplyCalls, call)
	if s.directWriteApplyErr != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, s.directWriteApplyErr
	}
	if s.afterDirectWriteApply != nil {
		s.afterDirectWriteApply()
	}
	if s.directWriteApplyResult.InsertedMemoryNode.ID == 0 {
		record.ID = uint64(len(s.directWriteApplyCalls))
		return logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode:  record,
			SupersededVectorIDs: append([]string(nil), s.directWriteApplyResult.SupersededVectorIDs...),
		}, nil
	}
	result := s.directWriteApplyResult
	result.InsertedMemoryNode.Vector = append([]float32(nil), result.InsertedMemoryNode.Vector...)
	result.SupersededVectorIDs = append([]string(nil), result.SupersededVectorIDs...)
	return result, nil
}

// DeleteMemoryNodes records one manual memory delete request and returns the canned relational delete outcome.
// DeleteMemoryNodes 用于记录一次手工删除记忆请求，并返回预设的关系侧删除结果。
func (s *stubTurnLookupStore) DeleteMemoryNodes(_ context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error) {
	s.deleteMemoryCalls = append(s.deleteMemoryCalls, stubDeleteMemoryCall{
		MemoryIDs: append([]uint64(nil), memoryIDs...),
		Filter:    filter,
		DeletedAt: deletedAt,
		Reason:    reason,
	})
	result := s.deleteMemoryResult
	result.DeletedMemoryIDs = append([]uint64(nil), result.DeletedMemoryIDs...)
	result.NotFoundMemoryIDs = append([]uint64(nil), result.NotFoundMemoryIDs...)
	result.DeletedVectorIDs = append([]string(nil), result.DeletedVectorIDs...)
	if s.deleteMemoryErr != nil {
		return result, s.deleteMemoryErr
	}
	if s.err != nil {
		return logicdomain.MemoryDeleteResult{}, s.err
	}
	return result, nil
}

// EnqueueVectorGCJobs records the latest compensation enqueue request so direct-write tests can assert failed vector cleanup is persisted for later retry.
// EnqueueVectorGCJobs 用于记录最近一次补偿入队请求，让 direct-write 测试验证失败的向量清理会被持久化等待后续重试。
func (s *stubTurnLookupStore) EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	s.vectorGCEnqueueQuery = query
	if ctx == nil {
		s.vectorGCEnqueueCtxErrs = append(s.vectorGCEnqueueCtxErrs, nil)
	} else {
		s.vectorGCEnqueueCtxErrs = append(s.vectorGCEnqueueCtxErrs, ctx.Err())
	}
	return nil
}

// stubCombinedHybridVectorStore extends the shared vector stub with the optional SQL-level hybrid recall fast path used by the PostgreSQL combined-store optimization.
// stubCombinedHybridVectorStore 用于在共享向量桩之上扩展可选的 SQL 级混合召回快速路径，服务 PostgreSQL 组合库优化断言。
type stubCombinedHybridVectorStore struct {
	stubVectorStore
	hybridQueries    []string
	hybridTopKs      []int
	hybridFilters    []logicdomain.SearchFilter
	hybridRRFKs      []int
	hybridSearchHits []logicdomain.MemoryHit
	hybridSearchErr  error
}

// SearchHybridMemory records the combined first-stage request and returns the canned SQL-fused hits.
// SearchHybridMemory 用于记录组合库一阶段请求，并返回预设的 SQL 融合命中结果。
func (s *stubCombinedHybridVectorStore) SearchHybridMemory(_ context.Context, query string, _ []float32, topK int, filter logicdomain.SearchFilter, rrfK int) ([]logicdomain.MemoryHit, error) {
	s.hybridQueries = append(s.hybridQueries, query)
	s.hybridTopKs = append(s.hybridTopKs, topK)
	s.hybridFilters = append(s.hybridFilters, filter)
	s.hybridRRFKs = append(s.hybridRRFKs, rrfK)
	if s.hybridSearchErr != nil {
		return nil, s.hybridSearchErr
	}
	return append([]logicdomain.MemoryHit(nil), s.hybridSearchHits...), nil
}

// stubRerankerClient records rerank calls and returns one canned response so search tests can verify ordering changes without a live provider.
// stubRerankerClient 用于记录 rerank 调用并返回预设结果，让搜索测试无需真实 provider 也能验证排序变化。
type stubRerankerClient struct {
	requests []stubRerankRequest
	results  []appports.RerankerResult
	err      error
}

// stubRerankRequest stores one recorded rerank query plus the candidate documents seen by the stub.
// stubRerankRequest 用于保存一条被桩记录下来的 rerank query 以及其候选文档。
type stubRerankRequest struct {
	query string
	docs  []appports.RerankerDocument
	topN  int
}

// Rerank records the request and returns the canned provider result.
// Rerank 用于记录请求并返回预设 provider 结果。
func (s *stubRerankerClient) Rerank(_ context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	s.requests = append(s.requests, stubRerankRequest{
		query: query,
		docs:  append([]appports.RerankerDocument(nil), docs...),
		topN:  topN,
	})
	if s.err != nil {
		return nil, s.err
	}
	return append([]appports.RerankerResult(nil), s.results...), nil
}
