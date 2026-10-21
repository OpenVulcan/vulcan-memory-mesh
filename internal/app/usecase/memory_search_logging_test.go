// Memory use-case tests for memory search logging test.
// 记忆应用用例层检索日志测试，覆盖召回或写入链路。
package usecase

import (
	"bytes"
	"context"
	"errors"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"strings"
	"testing"
)

// TestMemoryUseCaseSearchLogsRetrievalStageCounts verifies unified retrieval now logs vector, hybrid, and final-stage hit counts plus the top candidate snapshots so upstream recall misses are directly observable.
// TestMemoryUseCaseSearchLogsRetrievalStageCounts 用于验证统一检索现在会记录向量、混合和最终阶段的命中数量以及 top 候选快照，让上游召回缺口可以直接观测。
func TestMemoryUseCaseSearchLogsRetrievalStageCounts(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户正在比较保时捷卡宴与同价位车型。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "预算范围", Details: "用户把购车预算下调到 100-150 万。", VectorID: "vec-2"},
		},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户正在比较保时捷卡宴与同价位车型。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "预算范围", Details: "用户把购车预算下调到 100-150 万。", VectorID: "vec-2"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 202, Score: 0.99},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "用户正在比较保时捷卡宴与同价位车型。", Score: 0.91},
			{ID: "vec-2", Text: "用户把购车预算下调到 100-150 万。", Score: 0.82},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, logger)
	uc.ConfigureHybrid(true, 5, 60)

	if _, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"用户购车预算和车型偏好"},
		TopK:      5,
	}); err != nil {
		t.Fatalf("search memory events: %v", err)
	}

	logs := logBuf.String()
	expectedMessages := []string{
		`MSG："memory search vector stage completed"`,
		`MSG："memory search hybrid stage completed"`,
		`MSG："memory search final stage completed"`,
	}
	for _, message := range expectedMessages {
		if !strings.Contains(logs, message) {
			t.Fatalf("expected retrieval stage log %s, got %s", message, logs)
		}
	}
	expectedFragments := []string{
		`vector_hit_count：2`,
		`lexical_hit_count：1`,
		`final_hit_count：2`,
		`"top_vector_hit":`,
		`"top_hybrid_candidate":`,
		`"top_final_hit":`,
		`"memory_id": 201`,
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected retrieval logs to contain %s, got %s", fragment, logs)
		}
	}
}

// TestMemoryUseCaseSearchRedactsDegradedQueryLogs verifies retrieval degradation logs keep query diagnostics without writing the raw user query into runtime logs.
// TestMemoryUseCaseSearchRedactsDegradedQueryLogs 用于验证检索降级日志会保留 query 诊断信息，但不会把原始用户 query 写入运行时日志。
func TestMemoryUseCaseSearchRedactsDegradedQueryLogs(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "部署方案", Score: 0.91},
			{ID: "vec-2", Text: "备用方案", Score: 0.87},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "warn", Format: "text"})

	firstStore := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "部署方案", Details: "向量召回仍然命中。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "备用方案", Details: "用于触发 rerank 降级。", VectorID: "vec-2"},
		},
		lexicalErr: errors.New("fts gateway unavailable"),
	}
	firstReranker := &stubRerankerClient{err: errors.New("rerank timeout")}
	firstQuery := "用户银行卡 1234 的部署方案"
	firstUseCase := NewMemoryUseCase(profiles, firstStore, embedding, vector, logger)
	firstUseCase.ConfigureHybrid(true, 5, 60)
	firstUseCase.ConfigureRerank(firstReranker, 2)

	if _, err := firstUseCase.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{firstQuery},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with lexical/rerank degradation: %v", err)
	}

	secondStore := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "本地部署", Details: "向量召回仍然命中。", VectorID: "vec-1"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 201, Score: 0.99},
		},
	}
	secondQuery := "用户身份证 5678 的本地部署"
	secondUseCase := NewMemoryUseCase(profiles, secondStore, embedding, vector, logger)
	secondUseCase.ConfigureHybrid(true, 5, 60)

	if _, err := secondUseCase.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{secondQuery},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with lexical materialization degradation: %v", err)
	}

	logs := logBuf.String()
	if strings.Contains(logs, firstQuery) || strings.Contains(logs, secondQuery) {
		t.Fatalf("expected degraded logs to redact raw queries, got %s", logs)
	}
	if !strings.Contains(logs, "memory lexical search degraded") || !strings.Contains(logs, "memory lexical materialization degraded") || !strings.Contains(logs, "memory search rerank degraded") {
		t.Fatalf("expected all degradation events to be logged, got %s", logs)
	}
	if !strings.Contains(logs, "rerank_disabled") {
		t.Fatalf("expected rerank degradation logs to mention rerank-disabled fallback, got %s", logs)
	}
	if !strings.Contains(logs, "query_len") || !strings.Contains(logs, "query_sha256") {
		t.Fatalf("expected redacted query diagnostics in logs, got %s", logs)
	}
}

// TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled verifies degraded retrieval logs switch from redacted query summaries to full query text when the shared payload-debug logger flag is enabled for local troubleshooting.
// TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled 用于验证当共享 payload 调试开关开启时，检索降级日志会从脱敏 query 摘要切换为完整 query 文本，方便本地排障。
func TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "部署方案", Score: 0.91},
			{ID: "vec-2", Text: "备用方案", Score: 0.87},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "warn", Format: "text", DebugPayloads: true})

	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "部署方案", Details: "向量召回仍然命中。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "备用方案", Details: "用于触发 rerank 降级。", VectorID: "vec-2"},
		},
		lexicalErr: errors.New("fts gateway unavailable"),
	}
	reranker := &stubRerankerClient{err: errors.New("rerank timeout")}
	query := "用户银行卡 1234 的部署方案"
	uc := NewMemoryUseCase(profiles, store, embedding, vector, logger)
	uc.ConfigureHybrid(true, 5, 60)
	uc.ConfigureRerank(reranker, 2)

	if _, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{query},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with payload-debug degradation logs: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, query) || !strings.Contains(logs, `query："用户银行卡 1234 的部署方案"`) {
		t.Fatalf("expected payload-debug logs to include raw query text, got %s", logs)
	}
	if strings.Contains(logs, "query_len") || strings.Contains(logs, "query_sha256") {
		t.Fatalf("expected payload-debug logs to bypass redacted query diagnostics, got %s", logs)
	}
}
