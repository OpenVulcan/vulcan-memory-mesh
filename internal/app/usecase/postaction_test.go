// postaction_test.go verifies the async single-turn post-action workflow plus the profile-maintenance helpers that stay on the current runtime path.
// postaction_test.go 用于验证异步单轮的 post-action 工作流，以及仍然属于当前运行时主链的画像维护辅助逻辑。
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
	uc := newPostActionUseCase(filter, store, nil, nil, &stubPostActionTurnAnalyzer{}, nil, PostActionAnalysisConfig{}, nil, false)

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

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads still bypass the single-round noise gate and persist one canonical turn row before async processing begins.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷仍会跳过单轮噪声门，并在异步处理开始前先持久化成一条标准 turn 记录。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{TurnID: 1, Details: "timeline turn details"}}
	uc := newPostActionUseCase(filter, store, nil, nil, analyzer, nil, PostActionAnalysisConfig{}, nil, false)

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
	if analyzer.calls != 0 {
		t.Fatalf("expected analyzer not to run during Execute, got %d", analyzer.calls)
	}
	if store.analysisTurn.ID != 0 || store.analysis.Details != "" {
		t.Fatalf("expected no analysis write-back during Execute, got turn=%+v analysis=%+v", store.analysisTurn, store.analysis)
	}
}

// TestPostActionUseCaseQueuesAcceptedTurnWithoutBlocking verifies accepted single-round payloads are persisted and returned immediately without running the analyzer inline.
// TestPostActionUseCaseQueuesAcceptedTurnWithoutBlocking 用于验证通过噪声门的单轮载荷会先持久化并立即返回，而不会在 Execute 内联执行分析器。
func TestPostActionUseCaseQueuesAcceptedTurnWithoutBlocking(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{
		persistedTurn: logicdomain.PersistedTurnRecord{
			ID:               88,
			SessionID:        88,
			ProjectID:        12,
			DehydratedBudget: 10,
			CreatedAt:        time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC),
			UpdatedAt:        time.Date(2026, 4, 2, 9, 0, 1, 0, time.UTC),
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{}
	uc := newPostActionUseCase(filter, store, nil, nil, analyzer, nil, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, nil, false)

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
	if analyzer.calls != 0 {
		t.Fatalf("expected analyzer not to run during Execute, got %d", analyzer.calls)
	}
	if store.analysisTurn.ID != 0 {
		t.Fatalf("expected no inline analysis write-back, got %+v", store.analysisTurn)
	}
}

// TestPostActionUseCaseProcessesQueuedTurnsAsynchronously verifies the async worker uses refined history and active-memory references when it drains pending turns.
// TestPostActionUseCaseProcessesQueuedTurnsAsynchronously 用于验证异步工作器在消化 pending turn 时，会结合已提炼历史和活跃记忆锚点完成分析。
func TestPostActionUseCaseProcessesQueuedTurnsAsynchronously(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                88,
				SessionID:         88,
				ProjectID:         12,
				DehydratedContent: `{"user":"你好","timeline":[],"assistant":"收到"}`,
				DehydratedBudget:  10,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC),
				UpdatedAt:         time.Date(2026, 4, 2, 9, 0, 1, 0, time.UTC),
			},
		},
		historyTurns: []logicdomain.SessionTurnRecord{
			{ID: 77, SessionID: 88, ProjectID: 12, Details: "上一轮已经确认并发部分会改成 channel。", DetailsBudget: 20, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
		},
		activeMemoryNodes: []logicdomain.SessionMemoryNodeRecord{
			{ID: 701, TurnID: 77, VectorID: "old-vector", Category: logicdomain.MemoryNodeCategoryArchitectureDecision, Abstract: "旧并发方案", Details: "历史上使用过 mutex。", NodeStatus: logicdomain.MemoryNodeStatusActive},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		TurnID:              88,
		Details:             "当前轮确认保持异步提炼链路。",
		SupersededMemoryIDs: []uint64{701},
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryArchitectureDecision, Abstract: "当前项目确认改用 channel 管理并发。", Details: "这是新的并发决策。"},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}}}
	vector := &stubVectorStore{}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, nil, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  88,
		SessionKey: "sess-ok",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	if analyzer.calls != 1 {
		t.Fatalf("expected queued turn analyzer to run once, got %d", analyzer.calls)
	}
	if analyzer.input.TargetTurn.TurnID != 88 {
		t.Fatalf("expected target turn id 88, got %+v", analyzer.input.TargetTurn)
	}
	if len(analyzer.input.ReferenceTurns) != 1 || analyzer.input.ReferenceTurns[0].TurnID != 77 {
		t.Fatalf("expected one refined reference turn, got %+v", analyzer.input.ReferenceTurns)
	}
	if len(analyzer.input.ActiveMemoryNodes) != 1 || analyzer.input.ActiveMemoryNodes[0].MemoryID != 701 {
		t.Fatalf("expected one active memory anchor, got %+v", analyzer.input.ActiveMemoryNodes)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one vector upsert, got %d", len(vector.upserts))
	}
	if store.analysisTurn.ID != 88 {
		t.Fatalf("expected apply turn analysis to use persisted turn 88, got %+v", store.analysisTurn)
	}
	if store.analysis.Details != "当前轮确认保持异步提炼链路。" {
		t.Fatalf("unexpected persisted analysis: %+v", store.analysis)
	}
	if len(store.analysis.SupersededMemoryIDs) != 1 || store.analysis.SupersededMemoryIDs[0] != 701 {
		t.Fatalf("expected superseded memory id 701, got %+v", store.analysis.SupersededMemoryIDs)
	}
}

// TestPostActionUseCaseRollsBackQueuedTurnVectorsWhenPersistenceFails verifies freshly inserted vectors are deleted again when async turn write-back fails.
// TestPostActionUseCaseRollsBackQueuedTurnVectorsWhenPersistenceFails 用于验证异步 turn 回写失败时，刚插入的向量会被立即回滚删除。
func TestPostActionUseCaseRollsBackQueuedTurnVectorsWhenPersistenceFails(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                91,
				SessionID:         91,
				ProjectID:         12,
				DehydratedContent: `{"user":"你好","timeline":[],"assistant":"收到"}`,
				DehydratedBudget:  10,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
			},
		},
		analysisErr: errors.New("sqlite write failed"),
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		TurnID:  91,
		Details: "当前轮需要落一条记忆。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "需要记录当前异步回写失败回滚场景。", Details: "用于验证向量回滚。"},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.9, 0.8, 0.7}}}}
	vector := &stubVectorStore{}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{}, nil, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  91,
		SessionKey: "sess-rollback-immediate",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	if len(vector.upserts) != 1 {
		t.Fatalf("expected 1 vector upsert before rollback, got %d", len(vector.upserts))
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != vector.upserts[0].ID {
		t.Fatalf("expected rollback ids to match inserted vector row, got delete=%v upsert=%s", vector.deleteIDsCalls, vector.upserts[0].ID)
	}
}

// TestPostActionUseCaseBatchesProfileReviewAcrossTurns verifies user/project profile evidence from multiple pending turns is reviewed in one batched call and then rendered back into durable profile text.
// TestPostActionUseCaseBatchesProfileReviewAcrossTurns 用于验证来自多条待处理 turn 的 user/project 画像证据会通过一次批量评审调用统一处理，并回写成长期画像文本。
func TestPostActionUseCaseBatchesProfileMergeAcrossTurns(t *testing.T) {
	store := &testRelationalStore{
		profileReviewTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{
				{ID: 41, ProfileType: logicdomain.ProfileTypeUser, ProfileDate: "2026-03-20", Priority: logicdomain.ProfilePriorityP1, ProfileLevel: logicdomain.ProfileLevelStable, RefreshWeight: 1, Content: "用户偏好 Rust。"},
			},
			ProjectNodes: []logicdomain.ProfileActiveNodeRecord{
				{ID: 52, ProfileType: logicdomain.ProfileTypeProject, ProfileDate: "2026-03-18", Priority: logicdomain.ProfilePriorityP2, ProfileLevel: logicdomain.ProfileLevelSituational, RefreshWeight: 0, Content: "项目当前没有代码。"},
			},
		},
	}
	analysis := logicdomain.SessionBatchAnalysis{
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
	}
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
	uc := newPostActionUseCase(nil, store, nil, &stubVectorStore{}, nil, reviewer, PostActionAnalysisConfig{}, nil, false)
	turns := []logicdomain.SessionTurnRecord{
		{ID: 601, SessionID: 91, ProjectID: 12, CreatedAt: time.Date(2026, 3, 29, 9, 0, 0, 0, time.UTC)},
		{ID: 602, SessionID: 91, ProjectID: 12, CreatedAt: time.Date(2026, 3, 30, 10, 0, 0, 0, time.UTC)},
	}
	session := logicdomain.SessionRef{
		SessionID:  91,
		SessionKey: "sess-profile-batch",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}

	if err := uc.reviewSessionBatchProfiles(context.Background(), session, turns, &analysis); err != nil {
		t.Fatalf("review session batch profiles: %v", err)
	}

	if reviewer.calls != 1 {
		t.Fatalf("expected one batched review call, got %d", reviewer.calls)
	}
	if len(reviewer.nodes) != 3 {
		t.Fatalf("expected all profile nodes to flow into one review call, got %+v", reviewer.nodes)
	}
	if strings.Contains(analysis.MergedUserProfile, "[Profile Legend]") || !strings.Contains(analysis.MergedUserProfile, "用户偏好 Rust，并希望面向多个 AI 编程工具做记忆扩展。") {
		t.Fatalf("unexpected merged user profile state: %+v", analysis)
	}
	if strings.Contains(analysis.MergedProjectProfile, "[Profile Legend]") || !strings.Contains(analysis.MergedProjectProfile, "项目目前没有代码，仍处于早期设计阶段。") {
		t.Fatalf("unexpected merged project profile state: %+v", analysis)
	}
	if len(analysis.RetiredProfileNodeIDs) != 2 || analysis.RetiredProfileNodeIDs[0] != 41 || analysis.RetiredProfileNodeIDs[1] != 52 {
		t.Fatalf("unexpected retired profile node ids: %+v", analysis.RetiredProfileNodeIDs)
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
	if len(store.renderedUserProfiles) != 1 || strings.Contains(store.renderedUserProfiles[9], "[Profile Legend]") || !strings.Contains(store.renderedUserProfiles[9], "用户偏好使用 Rust 进行项目开发。") {
		t.Fatalf("unexpected rendered user profiles: %#v", store.renderedUserProfiles)
	}
	if len(store.renderedProjectProfiles) != 1 || !strings.Contains(store.renderedProjectProfiles[12], "[P0][L3][W1] 项目必须优先支持多种 AI 编程工具。") {
		t.Fatalf("unexpected rendered project profiles: %#v", store.renderedProjectProfiles)
	}
	if !strings.Contains(logBuf.String(), "post-action expired profiles converged") {
		t.Fatalf("expected expired profile convergence log, got %s", logBuf.String())
	}
}

// TestPostActionUseCaseBacksOffMaintenanceAfterDeadlock verifies periodic maintenance stops retrying immediately once the storage layer starts reporting poisoned-connection deadlock symptoms.
// TestPostActionUseCaseBacksOffMaintenanceAfterDeadlock 用于验证当存储层开始报告“连接污染式”的死锁症状后，周期性维护会先进入退避，而不是继续立刻重试。
func TestPostActionUseCaseBacksOffMaintenanceAfterDeadlock(t *testing.T) {
	store := &testRelationalStore{
		expiredProfileErr: errors.New("sqlite prepare failed: Invalid Error: resource deadlock would occur: resource deadlock would occur"),
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, nil, nil, nil, nil, PostActionAnalysisConfig{
		QueueScanInterval: 30 * time.Second,
	}, logger, false)

	uc.convergeExpiredProfiles()

	if !uc.queueMaintenanceBackoffActive(time.Now()) {
		t.Fatalf("expected queue maintenance to enter backoff after deadlock symptom")
	}
	output := logBuf.String()
	if !strings.Contains(output, "post-action queue maintenance paused") {
		t.Fatalf("expected maintenance backoff log, got %s", output)
	}
	if !strings.Contains(output, "resource deadlock would occur") {
		t.Fatalf("expected deadlock reason to stay visible in logs, got %s", output)
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
	recentTurns             []logicdomain.SessionTurnRecord
	historyTurns            []logicdomain.SessionTurnRecord
	activeMemoryNodes       []logicdomain.SessionMemoryNodeRecord
	recentDirectWrites      []logicdomain.TurnAnalysisDirectWrite
	idleSessions            []logicdomain.SessionRef
	idleSessionsErr         error
	profileTargets          logicdomain.ProfileTargetsSnapshot
	profileReviewTargets    logicdomain.ProfileReviewTargetsSnapshot
	expiredProfileTargets   []logicdomain.ProfileRenderTargetSnapshot
	expiredProfileErr       error
	replaceRenderedErr      error
	renderedUserProfiles    map[uint64]string
	renderedTeamProfiles    map[uint64]string
	renderedSpaceProfiles   map[uint64]string
	renderedProjectProfiles map[uint64]string
	expiredProfileScanCalls int
	analysisTurn            logicdomain.PersistedTurnRecord
	analysis                logicdomain.TurnAnalysis
	analysisApplyResult     logicdomain.TurnAnalysisApplyResult
	analysisErr             error
	advancedSessionID       uint64
	advancedObservedAt      time.Time
	advancedCompletedAt     time.Time
	adoptedMemoryIDs        []uint64
	adoptedAt               time.Time
	adoptionErr             error
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

// LoadRecentSessionTurns returns the canned recent turns used by pre-check and async-queue tests that need mixed refined/raw windows.
// LoadRecentSessionTurns 用于返回 pre-check 和异步队列测试使用的预设最近 turn 窗口。
func (s *testRelationalStore) LoadRecentSessionTurns(_ context.Context, _ logicdomain.SessionRef, _ int) ([]logicdomain.SessionTurnRecord, error) {
	if len(s.recentTurns) == 0 {
		return append([]logicdomain.SessionTurnRecord(nil), s.historyTurns...), nil
	}
	return append([]logicdomain.SessionTurnRecord(nil), s.recentTurns...), nil
}

// LoadRecentSessionHistory returns the canned extracted history turns used as single-turn analyzer references.
// LoadRecentSessionHistory 用于返回预设的已提炼历史 turn，作为单轮分析器的参考上下文。
func (s *testRelationalStore) LoadRecentSessionHistory(_ context.Context, _ logicdomain.SessionRef, _ int) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.historyTurns...), nil
}

// LoadActiveSessionMemoryNodes returns the canned active memory anchors exposed to the current single-turn analyzer.
// LoadActiveSessionMemoryNodes 用于返回暴露给当前单轮分析器的预设活跃记忆锚点。
func (s *testRelationalStore) LoadActiveSessionMemoryNodes(_ context.Context, _ logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error) {
	return append([]logicdomain.SessionMemoryNodeRecord(nil), s.activeMemoryNodes...), nil
}

// LoadRecentDirectMemoryWrites returns the canned recent direct writes used by the single-turn exclusion-window tests.
// LoadRecentDirectMemoryWrites 用于返回单轮排斥窗口测试使用的预设 direct write 记录。
func (s *testRelationalStore) LoadRecentDirectMemoryWrites(_ context.Context, _ logicdomain.SessionRef, _, _ time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	return append([]logicdomain.TurnAnalysisDirectWrite(nil), s.recentDirectWrites...), nil
}

// ListIdlePendingSessions returns the canned idle sessions used by queue-scan tests.
// ListIdlePendingSessions 用于返回队列扫描测试中预设的空闲 session。
func (s *testRelationalStore) ListIdlePendingSessions(_ context.Context, _ time.Duration, _ int) ([]logicdomain.SessionRef, error) {
	if s.idleSessionsErr != nil {
		return nil, s.idleSessionsErr
	}
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
	if s.expiredProfileErr != nil {
		return nil, s.expiredProfileErr
	}
	return append([]logicdomain.ProfileRenderTargetSnapshot(nil), s.expiredProfileTargets...), nil
}

// ReplaceRenderedProfiles records the rebuilt durable scope profiles so maintenance tests can assert the final rendered blobs.
// ReplaceRenderedProfiles 用于记录重建后的长期 scope 画像，方便维护路径测试断言最终渲染结果。
func (s *testRelationalStore) ReplaceRenderedProfiles(_ context.Context, updates logicdomain.RenderedProfileSet) error {
	if s.replaceRenderedErr != nil {
		return s.replaceRenderedErr
	}
	if len(updates.UserProfiles) > 0 {
		s.renderedUserProfiles = map[uint64]string{}
		for id, profile := range updates.UserProfiles {
			s.renderedUserProfiles[id] = profile
		}
	}
	if len(updates.TeamProfiles) > 0 {
		s.renderedTeamProfiles = map[uint64]string{}
		for id, profile := range updates.TeamProfiles {
			s.renderedTeamProfiles[id] = profile
		}
	}
	if len(updates.SpaceProfiles) > 0 {
		s.renderedSpaceProfiles = map[uint64]string{}
		for id, profile := range updates.SpaceProfiles {
			s.renderedSpaceProfiles[id] = profile
		}
	}
	if len(updates.ProjectProfiles) > 0 {
		s.renderedProjectProfiles = map[uint64]string{}
		for id, profile := range updates.ProjectProfiles {
			s.renderedProjectProfiles[id] = profile
		}
	}
	return nil
}

// AdvanceSessionExtractWindow records the latest successful exclusion-window checkpoint so tests can assert it advances after immediate extraction succeeds.
// AdvanceSessionExtractWindow 用于记录最近一次成功的排斥窗口推进点，方便测试断言即时提炼成功后会推进观察游标。
func (s *testRelationalStore) AdvanceSessionExtractWindow(_ context.Context, sessionID uint64, observedAt, completedAt time.Time) error {
	s.advancedSessionID = sessionID
	s.advancedObservedAt = observedAt
	s.advancedCompletedAt = completedAt
	return nil
}

// ApplyMemoryAdoption keeps interface completeness for tests that only exercise post-action paths.
// ApplyMemoryAdoption 用于在只覆盖 post-action 路径的测试里补齐接口。
func (s *testRelationalStore) ApplyMemoryAdoption(_ context.Context, _ logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error {
	s.adoptedMemoryIDs = append([]uint64(nil), memoryIDs...)
	s.adoptedAt = adoptedAt
	return s.adoptionErr
}

// ApplyTurnAnalysis keeps interface completeness for legacy tests that still compile against the expanded port.
// ApplyTurnAnalysis 用于补齐接口，让扩展后的端口在遗留测试场景下仍可编译。
func (s *testRelationalStore) ApplyTurnAnalysis(_ context.Context, _ logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	s.analysisTurn = turn
	s.analysis = analysis
	if s.analysisErr != nil {
		return logicdomain.TurnAnalysisApplyResult{}, s.analysisErr
	}
	return s.analysisApplyResult, nil
}

// Shutdown returns immediately because the stub does not own external resources.
// Shutdown 用于立即返回，因为该桩不持有外部资源。
func (s *testRelationalStore) Shutdown(context.Context) error { return nil }

// stubPostActionTurnAnalyzer records immediate single-turn analysis calls and returns one canned result or error.
// stubPostActionTurnAnalyzer 用于记录即时单轮分析调用，并返回预设结果或错误。
type stubPostActionTurnAnalyzer struct {
	calls  int
	input  logicdomain.TurnAnalysisInput
	result logicdomain.TurnAnalysis
	err    error
}

// Analyze captures the structured single-turn input so tests can assert the new synchronous post-action request shape.
// Analyze 用于捕获结构化单轮输入，方便测试断言新的同步 post-action 请求形态。
func (s *stubPostActionTurnAnalyzer) Analyze(_ context.Context, input logicdomain.TurnAnalysisInput) (logicdomain.TurnAnalysis, error) {
	s.calls++
	s.input = logicdomain.TurnAnalysisInput{
		ReferenceTurns:         append([]logicdomain.TurnAnalysisReferenceTurn(nil), input.ReferenceTurns...),
		TargetTurn:             input.TargetTurn,
		ActiveMemoryNodes:      append([]logicdomain.TurnAnalysisActiveMemoryNode(nil), input.ActiveMemoryNodes...),
		RecentGRPCMemoryWrites: append([]logicdomain.TurnAnalysisDirectWrite(nil), input.RecentGRPCMemoryWrites...),
	}
	if s.err != nil {
		return logicdomain.TurnAnalysis{}, s.err
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
	searchTopKs    []int
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

// Search returns the configured hits and records the requested top-k because memory-query tests also reuse this stub to assert candidate-pool sizing.
// Search 用于返回预设检索结果并记录请求的 top-k，因为 memory-query 测试也会复用这个桩来断言候选池大小。
func (s *stubVectorStore) Search(_ context.Context, _ []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	s.searchTopKs = append(s.searchTopKs, topK)
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

// DeleteByIDs records rollback or obsolete vector ids so tests can assert the async queue cleans up the right rows.
// DeleteByIDs 用于记录回滚或淘汰时的向量 id，方便测试断言异步队列会清理正确的行。
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
