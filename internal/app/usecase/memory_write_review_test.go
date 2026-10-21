// Memory use-case tests for memory write review test.
// 记忆应用用例层写入语义评审测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes verifies in-request duplicate collapse only applies to truly identical writes and keeps separate persistence paths when the caller changes category or lifecycle semantics.
// TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes 用于验证单请求内的重复折叠只作用于真正相同的写入；当调用方改变 category 或生命周期语义时，仍应保留独立持久化路径。
func TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  48,
			SessionKey: "sess-direct-same-text-different-semantics",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{
			{
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "记录新的实施约束。",
				Details:     "记录新的实施约束。",
				Category:    logicdomain.MemoryNodeCategoryTechSpecAPI,
				Priority:    logicdomain.MemoryPriorityP2,
				MemoryLevel: logicdomain.MemoryLevelStable,
			},
			{
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "记录新的实施约束。",
				Details:     "记录新的实施约束。",
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelPersistent,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].Ref.ID == 0 || result.Items[1].Ref.ID == 0 {
		t.Fatalf("unexpected write results: %+v", result.Items)
	}
	if result.Items[0].Ref.ID == result.Items[1].Ref.ID {
		t.Fatalf("expected distinct refs for different semantic attributes, got %+v", result.Items)
	}
	if result.Items[0].Deduped || result.Items[1].Deduped {
		t.Fatalf("expected both writes to persist independently, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 2 {
		t.Fatalf("expected two atomic persistence calls, got %+v", store.directWriteApplyCalls)
	}
	if len(vector.upserts) != 2 {
		t.Fatalf("expected two vector upserts, got %+v", vector.upserts)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 2 {
		t.Fatalf("expected one embedding batch covering both writes, got %+v", embedding.requests)
	}
}

// TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash verifies cross-request soft idempotency still reuses the durable row when the new semantic hash matches exactly.
// TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash 用于验证当新的语义哈希完全一致时，跨请求软幂等仍会正确复用长期记忆行。
func TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash(t *testing.T) {
	session := logicdomain.SessionRef{
		SessionID:  49,
		SessionKey: "sess-direct-current-soft-dedupe",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	item := WriteMemoryItem{
		ScopeLevel:  logicdomain.MemoryScopeLevelProject,
		Abstract:    "记录当前稳定的部署约束。",
		Details:     "记录当前稳定的部署约束。",
		Category:    logicdomain.MemoryNodeCategoryProjectContext,
		Priority:    logicdomain.MemoryPriorityP1,
		MemoryLevel: logicdomain.MemoryLevelStable,
		ExpiresAt:   time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
	}
	currentHash := buildDirectMemoryDedupeHash(session, item, time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC))

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		recentDedupeRowsByHash: map[string]logicdomain.MemoryNodeRecord{
			currentHash: {
				ID:          1501,
				Status:      logicdomain.MemoryStatusActive,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				Abstract:    "记录当前稳定的部署约束。",
				Details:     "记录当前稳定的部署约束。",
				ExpiresAt:   item.ExpiresAt,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: session,
		Items:   []WriteMemoryItem{item},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 1501 {
		t.Fatalf("expected current semantic hash to reuse memory 1501, got %+v", result.Items)
	}
	if len(store.recentDedupeQueries) != 1 || store.recentDedupeQueries[0] != currentHash {
		t.Fatalf("expected one current-hash lookup, got %+v", store.recentDedupeQueries)
	}
	if len(vector.upserts) != 0 || len(store.directWriteApplyCalls) != 0 || len(embedding.requests) != 0 {
		t.Fatalf("expected soft idempotency to skip new writes, got upserts=%+v apply=%+v embeds=%+v", vector.upserts, store.directWriteApplyCalls, embedding.requests)
	}
}

// buildLegacyDirectMemoryDedupeHashForTest builds the retired direct-write hash layout so tests can prove production no longer probes it.
// buildLegacyDirectMemoryDedupeHashForTest 用于构造已退役的主动写入哈希布局，让测试证明生产路径不再探测它。
func buildLegacyDirectMemoryDedupeHashForTest(session logicdomain.SessionRef, item WriteMemoryItem) string {
	body := strings.Join([]string{
		strconv.Itoa(logicdomain.MemorySourceKindGRPCAIWrite),
		strconv.Itoa(item.ScopeLevel),
		strconv.FormatUint(session.SessionID, 10),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// TestMemoryUseCaseWriteSoftIdempotencyIgnoresLegacyHashRows verifies retired legacy-hash rows no longer participate in direct-write soft idempotency.
// TestMemoryUseCaseWriteSoftIdempotencyIgnoresLegacyHashRows 用于验证已退役的旧哈希行不再参与主动写入软幂等。
func TestMemoryUseCaseWriteSoftIdempotencyIgnoresLegacyHashRows(t *testing.T) {
	fixedNow := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	session := logicdomain.SessionRef{
		SessionID:  50,
		SessionKey: "sess-direct-legacy-soft-dedupe",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	item := normalizeWriteMemoryItem(WriteMemoryItem{
		ScopeLevel:  logicdomain.MemoryScopeLevelProject,
		Abstract:    "记录默认生命周期下的部署约束。",
		Details:     "记录默认生命周期下的部署约束。",
		Category:    logicdomain.MemoryNodeCategoryProjectContext,
		Priority:    logicdomain.MemoryPriorityP2,
		MemoryLevel: logicdomain.MemoryLevelStable,
	}, fixedNow)
	currentHash := buildDirectMemoryDedupeHash(session, item, fixedNow)
	legacyHash := buildLegacyDirectMemoryDedupeHashForTest(session, item)

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		recentDedupeRowsByHash: map[string]logicdomain.MemoryNodeRecord{
			legacyHash: {
				ID:          1601,
				Status:      logicdomain.MemoryStatusActive,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP2,
				MemoryLevel: logicdomain.MemoryLevelStable,
				Abstract:    item.Abstract,
				Details:     item.Details,
				CreatedAt:   fixedNow,
				ExpiresAt:   fixedNow.Add(defaultMemoryTTL(logicdomain.MemoryScopeLevelProject)),
			},
		},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1602,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   item.Abstract,
				Details:    item.Details,
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
		Session: session,
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    item.Abstract,
			Details:     item.Details,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP2,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1602 {
		t.Fatalf("expected legacy-only hash row to be ignored and a fresh row to be created, got %+v", result.Items)
	}
	if len(store.recentDedupeQueries) != 1 || store.recentDedupeQueries[0] != currentHash {
		t.Fatalf("expected only current-hash lookup and no legacy probe %q, got %+v", legacyHash, store.recentDedupeQueries)
	}
	if len(vector.upserts) != 1 || len(store.directWriteApplyCalls) != 1 || len(embedding.requests) != 1 {
		t.Fatalf("expected legacy-only hash miss to persist a fresh row, got upserts=%+v apply=%+v embeds=%+v", vector.upserts, store.directWriteApplyCalls, embedding.requests)
	}
}

// TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate verifies dropped candidates with similar memories no longer auto-reuse the first hit unless reviewer explicitly names the dedupe target.
// TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate 用于验证当 reviewer 只丢弃候选但没有显式给出 dedupe 目标时，即使存在 similar memory，也不会再自动复用第一条旧记忆。
func TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1201,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
		},
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
				DroppedCandidates:        []logicdomain.PostActionDroppedMemoryCandidate{{CandidateIndex: 0}},
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "保守丢弃，但没有明确指定可复用旧记忆。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  44,
			SessionKey: "sess-direct-drop-without-explicit-dedupe",
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
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1201 {
		t.Fatalf("expected explicit-dedupe-missing path to create a new row, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected fallback create without supersedes, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory verifies direct-write semantic replacement passes reviewer-approved supersede ids into the atomic store path and cleans up obsolete vectors after commit.
// TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory 用于验证主动写入的语义替代会把 reviewer 批准的 supersede id 传入原子存储路径，并在提交后清理旧向量。
func TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段仍然是 A。",
			Details:    "当前项目阶段仍然是 A。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1001,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
			SupersededVectorIDs: []string{"vec-901"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段仍然是 A。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
					CandidateIndex:     0,
					SupersedeMemoryIDs: []uint64{901},
				}},
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "新阶段事实覆盖旧阶段事实。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  42,
			SessionKey: "sess-direct-replace",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    "当前项目阶段已经切换到 B。",
			Details:     "当前项目阶段已经切换到 B。",
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1001 {
		t.Fatalf("expected fresh memory write result, got %+v", result.Items)
	}
	if len(reviewer.inputs) != 1 {
		t.Fatalf("expected one reviewer input, got %+v", reviewer.inputs)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 1 || store.directWriteApplyCalls[0].SupersededMemoryIDs[0] != 901 {
		t.Fatalf("expected atomic direct-write apply to receive supersede id 901, got %+v", store.directWriteApplyCalls)
	}
	// Keep the assertion aligned with the shared caller-facing local-date contract used by reviewer payloads,
	// so the test stays stable when the local calendar day differs from the same instant's UTC date.
	// 让断言与 reviewer 载荷使用的共享本地日期契约保持一致，
	// 避免本地自然日与同一时刻的 UTC 日期不一致时测试再次失稳。
	expectedReviewDate := logicdomain.FormatDisplayDate(store.directWriteApplyCalls[0].Record.CreatedAt)
	if reviewer.inputs[0].CurrentTurnDate != expectedReviewDate {
		t.Fatalf("expected reviewer current_turn_date %q to match stable write time, got %+v", expectedReviewDate, reviewer.inputs[0].CurrentTurnDate)
	}
	if len(reviewer.inputs[0].MemoryCandidates) != 1 || reviewer.inputs[0].MemoryCandidates[0].CandidateDate != expectedReviewDate {
		t.Fatalf("expected reviewer candidate_date %q to match stable write time, got %+v", expectedReviewDate, reviewer.inputs[0].MemoryCandidates)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one new vector upsert, got %+v", vector.upserts)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != "vec-901" {
		t.Fatalf("expected obsolete vector cleanup for vec-901, got %+v", vector.deleteIDsCalls)
	}
}

// TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist verifies direct-write review still honors legacy index-only accepted payloads returned by older or non-LLM reviewer implementations.
// TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist 用于验证当 reviewer 仍返回旧版 index-only accepted 结果时，主动写入链路依然会正确保留候选而不是误判成 dropped。
func TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1251,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录一条新的稳定工程约束。",
				Details:    "记录一条新的稳定工程约束。",
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
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "旧实现仍只返回 accepted_candidate_indexes。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  45,
			SessionKey: "sess-direct-legacy-accepted",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一条新的稳定工程约束。",
			Details:    "记录一条新的稳定工程约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1251 {
		t.Fatalf("expected legacy accepted indexes to still create a new row, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one persistence call, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate verifies reviewer drops without any trustworthy similar memories degrade to creating a fresh row instead of losing an explicit tool write.
// TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate 用于验证当 reviewer 丢弃候选但没有可信相似旧记忆时，会退化成新建，而不是丢失显式工具写入。
func TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1101,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录一次新的稳定技术约束。",
				Details:    "记录一次新的稳定技术约束。",
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
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "这条候选没有高相似旧记忆，但 reviewer 仍然保守拒绝。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  43,
			SessionKey: "sess-direct-fallback-create",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一次新的稳定技术约束。",
			Details:    "记录一次新的稳定技术约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1101 {
		t.Fatalf("expected fallback create result, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected fallback create to persist without supersedes, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate verifies a dedupe target that became superseded or expired after reviewer selection degrades into a fresh insert instead of returning stale memory refs.
// TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate 用于验证当 dedupe 目标在 reviewer 选中后变成 superseded 或过期时，会退化成新建，而不是返回陈旧记忆引用。
func TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			Status:     logicdomain.MemoryStatusSuperseded,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			Status:     logicdomain.MemoryStatusActive,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1301,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
		},
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
				Reason:                   "理论上可复用旧记忆，但目标在并发窗口里已经退役。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90, 0)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  46,
			SessionKey: "sess-direct-stale-dedupe",
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
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1301 {
		t.Fatalf("expected stale dedupe target to fall back to create, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected stale dedupe target to create without supersedes, got %+v", store.directWriteApplyCalls)
	}
}
