// postaction_test.go verifies the queued post-action workflow that now persists turns first and analyzes them later in session batches.
// postaction_test.go 用于验证新的排队式 post-action 工作流：先持久化 turn，再按 session 批量分析。
package usecase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// TestPostActionUseCaseDropsSingleRoundNoise verifies simple user-assistant pairs can still be rejected by the noise gate before relational persistence.
// TestPostActionUseCaseDropsSingleRoundNoise 用于验证简单的单轮 user-assistant 问答仍然会在关系持久化前被噪声门拒绝。
func TestPostActionUseCaseDropsSingleRoundNoise(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := newPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil, false)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-noise",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent:      "你还记得我上次说过什么吗",
		AssistantContent: "我不记得",
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 1 {
		t.Fatalf("expected one normalized turn to be inspected, got %d", len(filter.seen))
	}
	if store.turn.UserContent != "" || store.turn.AssistantContent != "" || len(store.turn.Timeline) != 0 {
		t.Fatalf("expected no persisted turn, got %#v", store.turn)
	}
}

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads still bypass the single-round noise gate and persist one canonical turn row.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷仍会跳过单轮噪声门，并持久化成一条标准 turn 记录。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := newPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil, false)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  52,
			SessionKey: "sess-timeline",
			UserID:     8,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  10,
		},
		UserContent:      "最开始的问题",
		AssistantContent: "最后的回答",
		Timeline: []PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 0 {
		t.Fatalf("expected noise gate to be skipped, got %#v", filter.seen)
	}
	assertPersistedTurn(t, store.turn, "最开始的问题", "最后的回答", []logicdomain.TurnTimelineItem{
		{Type: "assistant", Content: "中间回答"},
		{Type: "user", Content: "补充问题"},
	})
}

// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval verifies accepted single-round payloads are stored immediately even though later analysis moved into the queue.
// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval 用于验证通过噪声门的单轮载荷会立即落库，即使后续分析已经移到队列中。
func TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{}
	uc := newPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil, false)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  88,
			SessionKey: "sess-ok",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
		},
		UserContent:      "你好",
		AssistantContent: "收到",
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	assertPersistedTurn(t, store.turn, "你好", "收到", nil)
}

// TestPostActionUseCaseProcessesQueuedSessionBatch verifies one queued batch uses recent refined history, persists vectors, and writes the batch result back to DuckDB.
// TestPostActionUseCaseProcessesQueuedSessionBatch 用于验证一次排队批处理会读取最近历史精要、持久化向量，并把批处理结果回写到 DuckDB。
func TestPostActionUseCaseProcessesQueuedSessionBatch(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                501,
				SessionID:         91,
				ProjectID:         12,
				DehydratedContent: `{"user":"raw-user","timeline":[{"type":"assistant","content":"raw-middle"}],"assistant":"raw-assistant"}`,
				DehydratedBudget:  90,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 3, 30, 8, 0, 0, 0, time.UTC),
			},
			{
				ID:                502,
				SessionID:         91,
				ProjectID:         12,
				DehydratedContent: `{"user":"raw-user-2","timeline":[],"assistant":"raw-assistant-2"}`,
				DehydratedBudget:  80,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 3, 30, 8, 1, 0, 0, time.UTC),
			},
		},
		historyTurns: []logicdomain.SessionTurnRecord{
			{ID: 401, SessionID: 91, ProjectID: 12, Details: "历史精要", DetailsBudget: 30, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
		},
		activeMemoryNodes: []logicdomain.SessionMemoryNodeRecord{
			{ID: 701, TurnID: 401, VectorID: "old-vector", Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "旧需求", Details: "旧需求详情", NodeStatus: logicdomain.MemoryNodeStatusActive},
		},
		batchApplyResult: logicdomain.SessionAnalysisApplyResult{ObsoleteVectorIDs: []string{"old-vector"}},
	}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.11, 0.22, 0.33}}}}
	vector := &stubVectorStore{}
	analyzer := &stubPostActionBatchAnalyzer{result: logicdomain.SessionBatchAnalysis{
		Turns: []logicdomain.SessionBatchTurnAnalysis{
			{
				TurnID:       501,
				Details:      "第一条待处理 turn 的精要。",
				MemoryNodes:  []logicdomain.MemoryNodeCandidate{{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "当前对话需要先给出 AI 记忆项目建议。", Details: "用户当前诉求是先获得 AI 记忆项目建议。"}},
				ProfileNodes: []logicdomain.ProfileNodeCandidate{{ProfileType: logicdomain.ProfileTypeProject, Content: "当前项目仍处于早期设计阶段。"}},
			},
			{
				TurnID:       502,
				Details:      "第二条待处理 turn 的精要。",
				MemoryNodes:  []logicdomain.MemoryNodeCandidate{},
				ProfileNodes: []logicdomain.ProfileNodeCandidate{},
			},
		},
		ObsoleteMemoryTurnIDs: []uint64{401},
	}}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{
		TurnThreshold:     2,
		TokenThreshold:    9999,
		IdleTimeout:       15 * time.Minute,
		HistoryTurns:      3,
		MaxInputTokens:    300,
		QueueScanInterval: 30 * time.Second,
	}, nil, false)

	uc.processQueuedSession(logicdomain.SessionRef{
		SessionID:  91,
		SessionKey: "sess-batch",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
		TurnCount:  2,
		UpdatedAt:  time.Now(),
	}, false, "test")

	if analyzer.calls != 1 {
		t.Fatalf("expected one batch analysis call, got %d", analyzer.calls)
	}
	if !strings.Contains(analyzer.requestBody, `"history_turns"`) || !strings.Contains(analyzer.requestBody, `"pending_turns"`) {
		t.Fatalf("expected history and pending turns in request body, got %s", analyzer.requestBody)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one vector upsert, got %d", len(vector.upserts))
	}
	if vector.upserts[0].Filter.SessionID != 91 {
		t.Fatalf("expected persisted vector to keep session id, got %+v", vector.upserts[0].Filter)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != "old-vector" {
		t.Fatalf("expected obsolete vector delete to be called with old-vector, got %#v", vector.deleteIDsCalls)
	}
	if len(store.batchTurns) != 2 || store.batchTurns[0].ID != 501 || store.batchTurns[1].ID != 502 {
		t.Fatalf("unexpected batch turns: %+v", store.batchTurns)
	}
	if len(store.batchAnalysis.Turns) != 2 {
		t.Fatalf("unexpected persisted batch analysis: %+v", store.batchAnalysis)
	}
	if strings.TrimSpace(store.batchAnalysis.Turns[0].MemoryNodes[0].VectorID) == "" {
		t.Fatalf("expected vector id to be backfilled onto batch analysis: %+v", store.batchAnalysis)
	}
}

// TestPostActionUseCaseBatchesProfileReviewAcrossTurns verifies user/project profile evidence from multiple pending turns is reviewed in one batched call and then rendered back into durable profile text.
// TestPostActionUseCaseBatchesProfileReviewAcrossTurns 用于验证来自多条待处理 turn 的 user/project 画像证据会通过一次批量评审调用统一处理，并回写成长期画像文本。
func TestPostActionUseCaseBatchesProfileMergeAcrossTurns(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{ID: 601, SessionID: 91, ProjectID: 12, DehydratedContent: `{"user":"u1","timeline":[],"assistant":"a1"}`, DehydratedBudget: 50, CreatedAt: time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC)},
			{ID: 602, SessionID: 91, ProjectID: 12, DehydratedContent: `{"user":"u2","timeline":[],"assistant":"a2"}`, DehydratedBudget: 50, CreatedAt: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
		},
		profileReviewTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{
				{ID: 41, ProfileType: logicdomain.ProfileTypeUser, ProfileDate: "2026-03-20", Priority: logicdomain.ProfilePriorityP1, ProfileLevel: logicdomain.ProfileLevelStable, RefreshWeight: 1, Content: "用户偏好 Rust。"},
			},
			ProjectNodes: []logicdomain.ProfileActiveNodeRecord{
				{ID: 52, ProfileType: logicdomain.ProfileTypeProject, ProfileDate: "2026-03-18", Priority: logicdomain.ProfilePriorityP2, ProfileLevel: logicdomain.ProfileLevelSituational, RefreshWeight: 0, Content: "项目当前没有代码。"},
			},
		},
	}
	analyzer := &stubPostActionBatchAnalyzer{result: logicdomain.SessionBatchAnalysis{
		Turns: []logicdomain.SessionBatchTurnAnalysis{
			{
				TurnID:       601,
				Details:      "第一条。",
				ProfileNodes: []logicdomain.ProfileNodeCandidate{{ProfileType: logicdomain.ProfileTypeUser, Content: "用户偏好 Rust。"}},
			},
			{
				TurnID:  602,
				Details: "第二条。",
				ProfileNodes: []logicdomain.ProfileNodeCandidate{
					{ProfileType: logicdomain.ProfileTypeProject, Content: "项目目前没有代码。"},
					{ProfileType: logicdomain.ProfileTypeUser, Content: "用户希望面向多个 AI 编程工具做记忆扩展。"},
				},
			},
		},
	}}
	reviewer := &stubPostActionProfileReviewer{
		result: logicdomain.TurnProfileReviewResult{
			User: &logicdomain.ProfileReviewSection{
				AcceptedCandidates: []logicdomain.ProfileReviewAcceptedCandidate{
					{
						CandidateIndex:    0,
						NormalizedContent: "用户偏好 Rust，并希望面向多个 AI 编程工具做记忆扩展。",
						Priority:          logicdomain.ProfilePriorityP1,
						ProfileLevel:      logicdomain.ProfileLevelStable,
						LevelReason:       "这是稳定开发偏好与目标方向。",
						SupersedeNodeIDs:  []uint64{41},
					},
					{
						CandidateIndex:    1,
						NormalizedContent: "用户希望产品支持多个 AI 编程工具。",
						Priority:          logicdomain.ProfilePriorityP2,
						ProfileLevel:      logicdomain.ProfileLevelSituational,
						LevelReason:       "这是当前阶段的重要范围偏好。",
					},
				},
				InvalidCandidateIndexes: []int{},
			},
			Project: &logicdomain.ProfileReviewSection{
				AcceptedCandidates: []logicdomain.ProfileReviewAcceptedCandidate{
					{
						CandidateIndex:    0,
						NormalizedContent: "项目目前没有代码，仍处于早期设计阶段。",
						Priority:          logicdomain.ProfilePriorityP1,
						ProfileLevel:      logicdomain.ProfileLevelSituational,
						LevelReason:       "这是当前阶段的重要项目背景。",
						SupersedeNodeIDs:  []uint64{52},
					},
				},
				InvalidCandidateIndexes: []int{},
			},
		},
	}
	uc := newPostActionUseCase(nil, store, nil, &stubVectorStore{}, analyzer, reviewer, PostActionAnalysisConfig{
		TurnThreshold:  2,
		TokenThreshold: 9999,
		IdleTimeout:    15 * time.Minute,
		HistoryTurns:   3,
		MaxInputTokens: 300,
	}, nil, false)

	uc.processQueuedSession(logicdomain.SessionRef{
		SessionID:  91,
		SessionKey: "sess-profile-batch",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
		UpdatedAt:  time.Now(),
	}, false, "test")

	if reviewer.calls != 1 {
		t.Fatalf("expected one batched review call, got %d", reviewer.calls)
	}
	if len(reviewer.nodes) != 3 {
		t.Fatalf("expected all profile nodes to flow into one review call, got %+v", reviewer.nodes)
	}
	if !strings.Contains(store.batchAnalysis.MergedUserProfile, "[Profile Legend]") || !strings.Contains(store.batchAnalysis.MergedUserProfile, "用户偏好 Rust，并希望面向多个 AI 编程工具做记忆扩展。") {
		t.Fatalf("unexpected merged user profile state: %+v", store.batchAnalysis)
	}
	if !strings.Contains(store.batchAnalysis.MergedProjectProfile, "[Profile Legend]") || !strings.Contains(store.batchAnalysis.MergedProjectProfile, "项目目前没有代码，仍处于早期设计阶段。") {
		t.Fatalf("unexpected merged project profile state: %+v", store.batchAnalysis)
	}
	if len(store.batchAnalysis.RetiredProfileNodeIDs) != 2 || store.batchAnalysis.RetiredProfileNodeIDs[0] != 41 || store.batchAnalysis.RetiredProfileNodeIDs[1] != 52 {
		t.Fatalf("unexpected retired profile node ids: %+v", store.batchAnalysis.RetiredProfileNodeIDs)
	}
}

// TestPostActionUseCaseConvergesExpiredProfiles verifies the periodic maintenance path marks due nodes as expired and rebuilds the durable user/project profile blobs from the remaining active nodes.
// TestPostActionUseCaseConvergesExpiredProfiles 用于验证周期性维护路径会把到期节点收敛为 expired，并基于剩余 active 节点重建长期 user/project 画像文本。
func TestPostActionUseCaseConvergesExpiredProfiles(t *testing.T) {
	store := &testRelationalStore{
		expiredProfileTargets: []logicdomain.ProfileRenderTargetSnapshot{
			{
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      9,
				Nodes: []logicdomain.ProfileActiveNodeRecord{
					{
						ID:            101,
						ProfileType:   logicdomain.ProfileTypeUser,
						BindID:        9,
						ProfileDate:   "2026-03-29",
						Priority:      logicdomain.ProfilePriorityP1,
						ProfileLevel:  logicdomain.ProfileLevelStable,
						RefreshWeight: 2,
						Content:       "用户偏好使用 Rust 进行项目开发。",
						CreatedAt:     time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC),
					},
				},
			},
			{
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      12,
				Nodes: []logicdomain.ProfileActiveNodeRecord{
					{
						ID:            202,
						ProfileType:   logicdomain.ProfileTypeProject,
						BindID:        12,
						ProfileDate:   "2026-03-30",
						Priority:      logicdomain.ProfilePriorityP0,
						ProfileLevel:  logicdomain.ProfileLevelPersistent,
						RefreshWeight: 1,
						Content:       "项目必须优先支持多种 AI 编程工具。",
						CreatedAt:     time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC),
					},
				},
			},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, logger, false)

	uc.convergeExpiredProfiles()

	if store.expiredProfileScanCalls != 1 {
		t.Fatalf("expected one expired-profile convergence scan, got %d", store.expiredProfileScanCalls)
	}
	if len(store.renderedUserProfiles) != 1 || !strings.Contains(store.renderedUserProfiles[9], "[Profile Legend]") || !strings.Contains(store.renderedUserProfiles[9], "用户偏好使用 Rust 进行项目开发。") {
		t.Fatalf("unexpected rendered user profiles: %#v", store.renderedUserProfiles)
	}
	if len(store.renderedProjectProfiles) != 1 || !strings.Contains(store.renderedProjectProfiles[12], "[P0][L3][W1] 项目必须优先支持多种 AI 编程工具。") {
		t.Fatalf("unexpected rendered project profiles: %#v", store.renderedProjectProfiles)
	}
	if !strings.Contains(logBuf.String(), "post-action expired profiles converged") {
		t.Fatalf("expected expired profile convergence log, got %s", logBuf.String())
	}
}

// TestPostActionUseCaseSkipsBatchBelowThreshold verifies queued sessions remain pending when neither count threshold nor token threshold has been met.
// TestPostActionUseCaseSkipsBatchBelowThreshold 用于验证当条数阈值和 token 阈值都未达到时，排队 session 会继续保持待处理状态。
func TestPostActionUseCaseSkipsBatchBelowThreshold(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{ID: 701, SessionID: 91, ProjectID: 12, DehydratedContent: `{"user":"u1","timeline":[],"assistant":"a1"}`, DehydratedBudget: 30},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	analyzer := &stubPostActionBatchAnalyzer{}
	uc := newPostActionUseCase(nil, store, nil, nil, analyzer, nil, PostActionAnalysisConfig{
		TurnThreshold:  2,
		TokenThreshold: 100,
		IdleTimeout:    15 * time.Minute,
		HistoryTurns:   3,
		MaxInputTokens: 300,
	}, logger, false)

	uc.processQueuedSession(logicdomain.SessionRef{
		SessionID:  91,
		SessionKey: "sess-skip",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
		UpdatedAt:  time.Now(),
	}, false, "test")

	if analyzer.calls != 0 {
		t.Fatalf("expected batch analyzer not to run, got %d calls", analyzer.calls)
	}
	if len(store.batchTurns) != 0 {
		t.Fatalf("expected no batch persistence, got %+v", store.batchTurns)
	}
	if !strings.Contains(logBuf.String(), "post-action queue skipped by thresholds") {
		t.Fatalf("expected threshold skip log, got %s", logBuf.String())
	}
}

// TestPostActionUseCaseRollsBackVectorRowsWhenBatchPersistenceFails verifies freshly inserted vectors are deleted again when DuckDB cannot commit the batch result.
// TestPostActionUseCaseRollsBackVectorRowsWhenBatchPersistenceFails 用于验证当 DuckDB 无法提交批处理结果时，刚写入的向量会被回滚删除。
func TestPostActionUseCaseRollsBackVectorRowsWhenBatchPersistenceFails(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{ID: 808, SessionID: 93, ProjectID: 12, DehydratedContent: `{"user":"u1","timeline":[],"assistant":"a1"}`, DehydratedBudget: 50},
			{ID: 809, SessionID: 93, ProjectID: 12, DehydratedContent: `{"user":"u2","timeline":[],"assistant":"a2"}`, DehydratedBudget: 50},
		},
		batchApplyErr: errors.New("duckdb write failed"),
	}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.3, 0.2, 0.1}}}}
	vector := &stubVectorStore{}
	analyzer := &stubPostActionBatchAnalyzer{result: logicdomain.SessionBatchAnalysis{
		Turns: []logicdomain.SessionBatchTurnAnalysis{
			{
				TurnID:      808,
				Details:     "这轮对话产出了一条需要持久化的记忆特征。",
				MemoryNodes: []logicdomain.MemoryNodeCandidate{{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "当前对话需要先给出 AI 记忆子项目建议。", Details: "这是当前 turn 的核心任务诉求。"}},
			},
			{
				TurnID:  809,
				Details: "第二条待处理 turn。",
			},
		},
	}}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{
		TurnThreshold:  2,
		TokenThreshold: 9999,
		IdleTimeout:    15 * time.Minute,
		HistoryTurns:   3,
		MaxInputTokens: 300,
	}, logger, false)

	uc.processQueuedSession(logicdomain.SessionRef{
		SessionID:  93,
		SessionKey: "sess-rollback",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
		UpdatedAt:  time.Now(),
	}, false, "test")

	if len(vector.upserts) != 1 {
		t.Fatalf("expected 1 vector upsert before rollback, got %d", len(vector.upserts))
	}
	if len(vector.deleteIDsCalls) != 1 {
		t.Fatalf("expected 1 rollback delete-by-ids call, got %d", len(vector.deleteIDsCalls))
	}
	if len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != vector.upserts[0].ID {
		t.Fatalf("expected rollback ids to match inserted vector row, got delete=%v upsert=%s", vector.deleteIDsCalls[0], vector.upserts[0].ID)
	}
	if !strings.Contains(logBuf.String(), "post-action session batch persistence failed") {
		t.Fatalf("expected persistence failure to be logged, got %s", logBuf.String())
	}
}

// assertPersistedTurn keeps turn-level persistence assertions compact and readable.
// assertPersistedTurn 用于让 turn 级持久化断言保持紧凑和易读。
func assertPersistedTurn(t *testing.T, turn logicdomain.TurnRecord, userContent, assistantContent string, timeline []logicdomain.TurnTimelineItem) {
	t.Helper()
	if turn.UserContent != userContent || turn.AssistantContent != assistantContent {
		t.Fatalf("unexpected persisted turn header: %+v", turn)
	}
	if len(turn.Timeline) != len(timeline) {
		t.Fatalf("unexpected persisted timeline length: %+v", turn.Timeline)
	}
	for idx := range timeline {
		if turn.Timeline[idx] != timeline[idx] {
			t.Fatalf("unexpected persisted timeline item[%d]: %+v", idx, turn.Timeline[idx])
		}
	}
}

// stubNoiseTurnFilter records observed turns and returns the configured keep set.
// stubNoiseTurnFilter 用于记录观察到的轮次，并返回预设的保留集合。
type stubNoiseTurnFilter struct {
	seen     []logicdomain.NormalizedTurn
	filtered []logicdomain.NormalizedTurn
}

// FilterPersistableTurns captures the incoming turns so tests can assert when the noise gate ran.
// FilterPersistableTurns 用于捕获传入轮次，方便测试断言噪声门是否执行。
func (s *stubNoiseTurnFilter) FilterPersistableTurns(_ context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn {
	s.seen = append([]logicdomain.NormalizedTurn(nil), turns...)
	return append([]logicdomain.NormalizedTurn(nil), s.filtered...)
}

// testRelationalStore is the minimal relational-store stub needed by the queued post-action tests.
// testRelationalStore 用于为排队式 post-action 测试提供最小化的关系存储桩。
type testRelationalStore struct {
	session                 logicdomain.SessionRef
	turn                    logicdomain.TurnRecord
	persistedTurn           logicdomain.PersistedTurnRecord
	pendingTurns            []logicdomain.SessionTurnRecord
	historyTurns            []logicdomain.SessionTurnRecord
	activeMemoryNodes       []logicdomain.SessionMemoryNodeRecord
	idleSessions            []logicdomain.SessionRef
	profileTargets          logicdomain.ProfileTargetsSnapshot
	profileReviewTargets    logicdomain.ProfileReviewTargetsSnapshot
	expiredProfileTargets   []logicdomain.ProfileRenderTargetSnapshot
	renderedUserProfiles    map[uint64]string
	renderedProjectProfiles map[uint64]string
	expiredProfileScanCalls int
	analysisTurn            logicdomain.PersistedTurnRecord
	analysis                logicdomain.TurnAnalysis
	analysisErr             error
	batchTurns              []logicdomain.SessionTurnRecord
	batchAnalysis           logicdomain.SessionBatchAnalysis
	batchApplyResult        logicdomain.SessionAnalysisApplyResult
	batchApplyErr           error
}

// AppendTurnRecord records the latest session scope and canonical turn payload for assertions.
// AppendTurnRecord 用于记录最新的 session 范围和标准 turn 载荷，供测试断言使用。
func (s *testRelationalStore) AppendTurnRecord(_ context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	s.session = session
	s.turn = logicdomain.TurnRecord{
		UserContent:      turn.UserContent,
		AssistantContent: turn.AssistantContent,
		CreatedAt:        turn.CreatedAt,
		Timeline:         append([]logicdomain.TurnTimelineItem(nil), turn.Timeline...),
	}
	if s.persistedTurn.ID != 0 {
		return s.persistedTurn, nil
	}
	return logicdomain.PersistedTurnRecord{ID: 1, SessionID: session.SessionID, ProjectID: session.ProjectID, DehydratedBudget: 10}, nil
}

// LoadPendingSessionTurns returns the canned pending turns used by the queue-processing tests.
// LoadPendingSessionTurns 用于返回队列处理测试中预设的待处理 turn。
func (s *testRelationalStore) LoadPendingSessionTurns(_ context.Context, _ logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.pendingTurns...), nil
}

// LoadRecentSessionHistory returns the canned extracted history turns used as batch references.
// LoadRecentSessionHistory 用于返回预设的已提炼历史 turn，作为批处理参考上下文。
func (s *testRelationalStore) LoadRecentSessionHistory(_ context.Context, _ logicdomain.SessionRef, _ int) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.historyTurns...), nil
}

// LoadActiveSessionMemoryNodes returns the canned active memory anchors exposed to the batch analyzer.
// LoadActiveSessionMemoryNodes 用于返回暴露给批处理分析器的预设活跃记忆锚点。
func (s *testRelationalStore) LoadActiveSessionMemoryNodes(_ context.Context, _ logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error) {
	return append([]logicdomain.SessionMemoryNodeRecord(nil), s.activeMemoryNodes...), nil
}

// ListIdlePendingSessions returns the canned idle sessions used by queue-scan tests.
// ListIdlePendingSessions 用于返回队列扫描测试中预设的空闲 session。
func (s *testRelationalStore) ListIdlePendingSessions(_ context.Context, _ time.Duration, _ int) ([]logicdomain.SessionRef, error) {
	return append([]logicdomain.SessionRef(nil), s.idleSessions...), nil
}

// LoadProfileTargets returns the canned durable user/project profile blobs so tests can verify the merge request input.
// LoadProfileTargets 用于返回预设的长期 user/project 画像 Blob，方便测试断言合并请求输入。
func (s *testRelationalStore) LoadProfileTargets(_ context.Context, _ logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	return s.profileTargets, nil
}

// LoadProfileReviewTargets returns the canned active user/project profile nodes so tests can verify the review request input.
// LoadProfileReviewTargets 用于返回预设的活跃 user/project 画像节点，方便测试断言评审请求输入。
func (s *testRelationalStore) LoadProfileReviewTargets(_ context.Context, _ logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	return s.profileReviewTargets, nil
}

// ConvergeExpiredProfileNodes returns the canned expired-target snapshots so maintenance tests can verify lifecycle convergence behavior.
// ConvergeExpiredProfileNodes 用于返回预设的过期目标快照，方便维护路径测试验证生命周期收敛行为。
func (s *testRelationalStore) ConvergeExpiredProfileNodes(_ context.Context, _ int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	s.expiredProfileScanCalls++
	return append([]logicdomain.ProfileRenderTargetSnapshot(nil), s.expiredProfileTargets...), nil
}

// ReplaceRenderedProfiles records the rebuilt durable user/project profiles so maintenance tests can assert the final rendered blobs.
// ReplaceRenderedProfiles 用于记录重建后的长期 user/project 画像，方便维护路径测试断言最终渲染结果。
func (s *testRelationalStore) ReplaceRenderedProfiles(_ context.Context, userProfiles map[uint64]string, projectProfiles map[uint64]string) error {
	if len(userProfiles) > 0 {
		s.renderedUserProfiles = map[uint64]string{}
		for id, profile := range userProfiles {
			s.renderedUserProfiles[id] = profile
		}
	}
	if len(projectProfiles) > 0 {
		s.renderedProjectProfiles = map[uint64]string{}
		for id, profile := range projectProfiles {
			s.renderedProjectProfiles[id] = profile
		}
	}
	return nil
}

// ApplyTurnAnalysis keeps interface completeness for legacy tests that still compile against the expanded port.
// ApplyTurnAnalysis 用于补齐接口，让扩展后的端口在遗留测试场景下仍可编译。
func (s *testRelationalStore) ApplyTurnAnalysis(_ context.Context, _ logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) error {
	s.analysisTurn = turn
	s.analysis = analysis
	return s.analysisErr
}

// ApplySessionBatchAnalysis records the selected turns and the structured batch payload so tests can assert the queue pipeline writes back the expected result.
// ApplySessionBatchAnalysis 用于记录所选 turn 和结构化批处理载荷，方便测试断言队列流水线会回写正确结果。
func (s *testRelationalStore) ApplySessionBatchAnalysis(_ context.Context, _ logicdomain.SessionRef, turns []logicdomain.SessionTurnRecord, analysis logicdomain.SessionBatchAnalysis) (logicdomain.SessionAnalysisApplyResult, error) {
	s.batchTurns = append([]logicdomain.SessionTurnRecord(nil), turns...)
	s.batchAnalysis = analysis
	if s.batchApplyErr != nil {
		return logicdomain.SessionAnalysisApplyResult{}, s.batchApplyErr
	}
	return s.batchApplyResult, nil
}

// Shutdown returns immediately because the stub does not own external resources.
// Shutdown 用于立即返回，因为该桩不持有外部资源。
func (s *testRelationalStore) Shutdown(context.Context) error { return nil }

// stubPostActionBatchAnalyzer records queued batch-analysis invocations and returns one canned result or error.
// stubPostActionBatchAnalyzer 用于记录排队批处理分析调用，并返回预设结果或错误。
type stubPostActionBatchAnalyzer struct {
	calls       int
	requestBody string
	result      logicdomain.SessionBatchAnalysis
	err         error
}

// Analyze captures the request body so tests can assert the queued batch window shape.
// Analyze 用于捕获请求体，方便测试断言排队批处理窗口的形态。
func (s *stubPostActionBatchAnalyzer) Analyze(_ context.Context, requestBody string) (logicdomain.SessionBatchAnalysis, error) {
	s.calls++
	s.requestBody = requestBody
	if s.err != nil {
		return logicdomain.SessionBatchAnalysis{}, s.err
	}
	return s.result, nil
}

// stubPostActionProfileReviewer records batched profile review calls and returns one canned result or error.
// stubPostActionProfileReviewer 用于记录批量画像评审调用，并返回预设结果或错误。
type stubPostActionProfileReviewer struct {
	calls    int
	snapshot logicdomain.ProfileReviewTargetsSnapshot
	nodes    []logicdomain.ProfileNodeCandidate
	result   logicdomain.TurnProfileReviewResult
	err      error
}

// Review captures the full batch input so tests can assert the use case sends user/project nodes together.
// Review 用于捕获完整批量输入，方便测试断言用例会把 user/project 节点一起送进评审器。
func (s *stubPostActionProfileReviewer) Review(_ context.Context, snapshot logicdomain.ProfileReviewTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (logicdomain.TurnProfileReviewResult, error) {
	s.calls++
	s.snapshot = snapshot
	s.nodes = append([]logicdomain.ProfileNodeCandidate(nil), nodes...)
	if s.err != nil {
		return logicdomain.TurnProfileReviewResult{}, s.err
	}
	return s.result, nil
}

// stubEmbeddingClient records embedding requests and returns one canned response so post-action tests can verify vector persistence without a real model backend.
// stubEmbeddingClient 用于记录 embedding 请求，并返回预设结果，让 post-action 测试在没有真实模型后端时也能验证向量持久化。
type stubEmbeddingClient struct {
	requests []appports.EmbeddingRequest
	response appports.EmbeddingResponse
	err      error
}

// Embed captures the request order and replays the configured response or error.
// Embed 用于记录请求顺序，并回放预设响应或错误。
func (s *stubEmbeddingClient) Embed(_ context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return appports.EmbeddingResponse{}, s.err
	}
	return s.response, nil
}

// stubVectorStore captures vector writes and rollback deletions so post-action tests can assert the LanceDB-facing contract.
// stubVectorStore 用于捕获向量写入和回滚删除，让 post-action 测试可以断言面向 LanceDB 的契约。
type stubVectorStore struct {
	upserts        []logicdomain.MemoryRecord
	deleteFilters  []logicdomain.SearchFilter
	deleteIDsCalls [][]string
	searchFilters  []logicdomain.SearchFilter
	searchHits     []logicdomain.MemoryHit
	upsertErr      error
	searchErr      error
	deleteErr      error
}

// Upsert records one memory vector row and optionally returns the configured failure.
// Upsert 用于记录一条记忆向量行，并按需返回预设失败。
func (s *stubVectorStore) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	s.upserts = append(s.upserts, record)
	return s.upsertErr
}

// Search returns the configured hits because these post-action tests only need interface completeness.
// Search 用于返回预设检索结果，因为这些 post-action 测试只需要补全接口。
func (s *stubVectorStore) Search(_ context.Context, _ []float32, _ int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	s.searchFilters = append(s.searchFilters, filter)
	return append([]logicdomain.MemoryHit(nil), s.searchHits...), nil
}

// DeleteByFilter records the destructive filter call and reuses the configured delete error when needed.
// DeleteByFilter 用于记录按过滤条件删除的调用，并在需要时复用预设删除错误。
func (s *stubVectorStore) DeleteByFilter(_ context.Context, filter logicdomain.SearchFilter) (uint64, error) {
	s.deleteFilters = append(s.deleteFilters, filter)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return 0, nil
}

// DeleteByIDs records rollback or obsolete vector ids so tests can assert the queued batch pipeline cleans up the right rows.
// DeleteByIDs 用于记录回滚或淘汰时的向量 id，方便测试断言队列批处理流水线会清理正确的行。
func (s *stubVectorStore) DeleteByIDs(_ context.Context, ids []string) (uint64, error) {
	copied := append([]string(nil), ids...)
	s.deleteIDsCalls = append(s.deleteIDsCalls, copied)
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return uint64(len(ids)), nil
}

// Shutdown returns immediately because the vector stub holds no external resources.
// Shutdown 用于立即返回，因为该向量桩不持有外部资源。
func (s *stubVectorStore) Shutdown(context.Context) error { return nil }
