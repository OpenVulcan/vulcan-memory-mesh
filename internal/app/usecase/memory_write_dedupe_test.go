// Memory use-case tests for memory write dedupe test.
// 记忆应用用例层写入去重测试，覆盖召回或写入链路。
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

// TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory verifies reviewer-side semantic dedupe reuses the recalled durable memory instead of inserting a second equivalent direct-write row.
// TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory 用于验证 reviewer 触发的语义去重会复用已召回的长期记忆，而不是再插入第二条等价主动写入行。
func TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段已经切换到 B。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				DroppedCandidates:        []logicdomain.PostActionDroppedMemoryCandidate{{CandidateIndex: 0, DedupeMemoryID: 901}},
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "旧记忆已经完整表达这条阶段事实。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-direct-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 901 {
		t.Fatalf("expected semantic dedupe to reuse memory 901, got %+v", result.Items)
	}
	if len(vector.upserts) != 0 {
		t.Fatalf("expected semantic dedupe to skip new vector upsert, got %+v", vector.upserts)
	}
	if len(store.directWriteApplyCalls) != 0 {
		t.Fatalf("expected semantic dedupe to avoid persistence writes, got apply=%+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteSemanticReviewerReceivesCurrentTimestamp verifies direct-write semantic review now passes the same current-time anchor as the turn-driven post-action path, so both reviewer entry points share the same temporal contract.
// TestMemoryUseCaseWriteSemanticReviewerReceivesCurrentTimestamp 用于验证主动写记忆的语义评审现在会传入与 turn 驱动 post-action 相同的当前时间锚点，确保两条 reviewer 入口共享一致的时序契约。
func TestMemoryUseCaseWriteSemanticReviewerReceivesCurrentTimestamp(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1201,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录一条新的工程规则。",
				Details:    "记录一条新的工程规则。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "候选可作为新的长期记忆保留。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  47,
			SessionKey: "sess-direct-review-timestamp",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一条新的工程规则。",
			Details:    "记录一条新的工程规则。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1201 {
		t.Fatalf("expected accepted direct write to create a new row, got %+v", result.Items)
	}
	if reviewer.calls != 1 || len(reviewer.inputs) != 1 {
		t.Fatalf("expected one reviewer call with captured input, got calls=%d inputs=%+v", reviewer.calls, reviewer.inputs)
	}
	if reviewer.inputs[0].CurrentTimestamp <= 0 {
		t.Fatalf("expected direct-write reviewer to receive current timestamp, got %+v", reviewer.inputs[0])
	}
	if reviewer.inputs[0].CurrentTurnDate == "" {
		t.Fatalf("expected direct-write reviewer to receive current review date, got %+v", reviewer.inputs[0])
	}
}

// TestMemoryUseCaseWriteLogsSemanticReviewerInvalidOutput verifies direct-write reviewer contract failures keep raw model diagnostics in server logs without continuing into vector or relational persistence.
// TestMemoryUseCaseWriteLogsSemanticReviewerInvalidOutput 用于验证主动写 reviewer 契约失败会把原始模型诊断留在服务端日志中，并且不会继续执行向量或关系持久化。
func TestMemoryUseCaseWriteLogsSemanticReviewerInvalidOutput(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	rawOutput := strings.Join([]string{
		"```json",
		"{\"memory\":{\"accepted_candidate_indexes\":[0]}",
		"```",
	}, "\n")
	reviewer := &stubPostActionCandidateReviewer{
		err: logicdomain.InvalidLLMOutputError{
			Scene:   "postaction_l2_main",
			Message: "json decode failed",
			Raw:     rawOutput,
		},
		model: "Qwen/Qwen3-235B-A22B",
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewMemoryUseCase(profiles, store, embedding, vector, logger)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	_, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  48,
			SessionKey: "sess-direct-invalid-reviewer",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一条新的工程规则。",
			Details:    "记录一条新的工程规则。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})

	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) || invalid.Scene != "postaction_l2_main" {
		t.Fatalf("expected postaction_l2_main InvalidLLMOutputError, got %v", err)
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected one reviewer call, got %d", reviewer.calls)
	}
	if len(vector.upserts) != 0 || len(store.directWriteApplyCalls) != 0 {
		t.Fatalf("expected reviewer invalid output to stop before persistence, upserts=%+v apply=%+v", vector.upserts, store.directWriteApplyCalls)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "direct memory candidate reviewer output invalid") {
		t.Fatalf("expected direct-write reviewer failure log, got %s", logs)
	}
	if !strings.Contains(logs, "llm_scene：\"postaction_l2_main\"") {
		t.Fatalf("expected l2 scene in failure log, got %s", logs)
	}
	if !strings.Contains(logs, "model：\"Qwen/Qwen3-235B-A22B\"") {
		t.Fatalf("expected reviewer model in failure log, got %s", logs)
	}
	if !strings.Contains(logs, "TEXT(llm_raw_output)：\n"+rawOutput+"\n") {
		t.Fatalf("expected raw reviewer output in failure log, got %s", logs)
	}
	if !strings.Contains(logs, "invalid llm output for postaction_l2_main: json decode failed") {
		t.Fatalf("expected invalid llm output summary in failure log, got %s", logs)
	}
}

// TestMemoryUseCaseWriteHardDedupeReturnsExistingMemory verifies real-cosine hard dedupe can reuse an existing durable memory from the dedicated pre-MMR pool before reviewer execution, even when the reviewer-visible top hit is a different memory.
// TestMemoryUseCaseWriteHardDedupeReturnsExistingMemory 用于验证真实 cosine 硬排重可以在 reviewer 执行前，从专用的 MMR 前候选池里复用已有长期记忆；即使 reviewer 可见的 top 命中是另一条记忆也同样生效。
func TestMemoryUseCaseWriteHardDedupeReturnsExistingMemory(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{
				ID:         900,
				SourceKind: logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Category:   logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:   "排序第一但向量不等价。",
				Details:    "排序第一但向量不等价。",
				VectorID:   "vec-900",
				Vector:     []float32{1, 0},
			},
			{
				ID:         901,
				SourceKind: logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Category:   logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-901",
				Vector:     []float32{0, 1},
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0, 1}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{
				ID:    "vec-900",
				Text:  "排序第一但向量不等价。",
				Score: 0.99,
			},
			{
				ID:    "vec-901",
				Text:  "当前项目阶段已经切换到 B。",
				Score: 0.94,
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMMR(true, 0.75)
	uc.ConfigureHardDedupePoolTopK(16)
	uc.ConfigureMemoryReplace(nil, 1, memoryReplaceScopeProject, 0.80, 0.99)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-direct-hard-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 901 {
		t.Fatalf("expected hard dedupe to reuse memory 901, got %+v", result.Items)
	}
	if len(vector.searchTopKs) != 1 || vector.searchTopKs[0] != 16 {
		t.Fatalf("expected hard dedupe review search to widen candidate pool to 16, got %+v", vector.searchTopKs)
	}
	if len(vector.upserts) != 0 {
		t.Fatalf("expected hard dedupe to skip new vector upsert, got %+v", vector.upserts)
	}
	if len(store.directWriteApplyCalls) != 0 {
		t.Fatalf("expected hard dedupe to avoid persistence writes, got apply=%+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteMixedHardDedupeAndReviewerBatch verifies one direct-write batch can simultaneously reuse a locally hard-deduped memory and persist a second reviewer-approved memory without tripping reviewer-only dedupe validation.
// TestMemoryUseCaseWriteMixedHardDedupeAndReviewerBatch 用于验证单次 direct-write 批次可以同时复用本地 hard dedupe 命中的旧记忆，并持久化另一条 reviewer 接纳的新记忆，而不会误触 reviewer 专属 dedupe 校验。
func TestMemoryUseCaseWriteMixedHardDedupeAndReviewerBatch(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			Status:     logicdomain.MemoryStatusActive,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{
				ID:         900,
				Status:     logicdomain.MemoryStatusActive,
				SourceKind: logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Category:   logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:   "排序第一但向量不等价。",
				Details:    "排序第一但向量不等价。",
				VectorID:   "vec-900",
				Vector:     []float32{1, 0},
			},
			{
				ID:         901,
				Status:     logicdomain.MemoryStatusActive,
				SourceKind: logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Category:   logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-901",
				Vector:     []float32{0, 1},
			},
		},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1002,
				Status:     logicdomain.MemoryStatusActive,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "新增稳定工程约束：发布前必须跑 smoke test。",
				Details:    "新增稳定工程约束：发布前必须跑 smoke test。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		responseQueue: []appports.EmbeddingResponse{
			{Vectors: [][]float32{{0, 1}, {0.5, 0.5}}},
			{Vectors: [][]float32{{0.3, 0.4, 0.5}}},
		},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{
				ID:    "vec-900",
				Text:  "排序第一但向量不等价。",
				Score: 0.79,
			},
			{
				ID:    "vec-901",
				Text:  "当前项目阶段已经切换到 B。",
				Score: 0.78,
			},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "第二条候选是新的稳定工程约束，应保留。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.80, 0.99)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  47,
			SessionKey: "sess-direct-mixed-hard-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				Category:   logicdomain.MemoryNodeCategoryProjectContext,
			},
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "新增稳定工程约束：发布前必须跑 smoke test。",
				Details:    "新增稳定工程约束：发布前必须跑 smoke test。",
				Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected exactly one reviewer call for the non-hard-deduped candidate, got %d", reviewer.calls)
	}
	if len(reviewer.inputs) != 1 || len(reviewer.inputs[0].MemoryCandidates) != 1 || reviewer.inputs[0].MemoryCandidates[0].Abstract != "新增稳定工程约束：发布前必须跑 smoke test。" {
		t.Fatalf("expected reviewer to receive only the second candidate, got %+v", reviewer.inputs)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected two write results, got %+v", result.Items)
	}
	if !result.Items[0].Deduped || result.Items[0].Ref.ID != 901 {
		t.Fatalf("expected first candidate to reuse memory 901, got %+v", result.Items[0])
	}
	if result.Items[1].Deduped || result.Items[1].Ref.ID != 1002 {
		t.Fatalf("expected second candidate to create memory 1002, got %+v", result.Items[1])
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected only one fresh vector upsert for the reviewer-approved create path, got %+v", vector.upserts)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected exactly one persistence call without supersedes for the second candidate, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates verifies one direct-write RPC collapses repeated identical items before embedding and persistence so the caller does not create duplicate durable memories inside the same batch.
// TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates 用于验证单次主动写入 RPC 会先折叠请求内重复的相同条目，避免同一批请求里生成重复长期记忆。
func TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1401,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  47,
			SessionKey: "sess-direct-in-request-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
			},
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("result items len = %d, want 2", len(result.Items))
	}
	if result.Items[0].Ref.ID != 1401 || result.Items[0].Deduped {
		t.Fatalf("expected first item to be the canonical create result, got %+v", result.Items[0])
	}
	if result.Items[1].Ref.ID != 1401 || !result.Items[1].Deduped {
		t.Fatalf("expected second item to reuse the same created ref as in-request dedupe, got %+v", result.Items[1])
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one atomic persistence call, got %+v", store.directWriteApplyCalls)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one vector upsert, got %+v", vector.upserts)
	}
	if len(embedding.requests) != 1 {
		t.Fatalf("expected one embedding request, got %+v", embedding.requests)
	}
}
