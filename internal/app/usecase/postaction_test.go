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
	"github.com/openvulcan/vmm/internal/testutil"
)

// TestPostActionExecuteRejectsNilReceiver verifies the exported post-action use case returns one stable error instead of panicking when direct tests or manual integrations accidentally invoke Execute on a nil receiver.
// TestPostActionExecuteRejectsNilReceiver 用于验证导出的 post-action 用例在直接测试或手工集成误把 Execute 调到 nil 接收者上时，会返回稳定错误，而不是直接 panic。
func TestPostActionExecuteRejectsNilReceiver(t *testing.T) {
	var uc *PostActionUseCase

	_, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			ProjectID:  9,
		},
		UserContent:      "第一问",
		AssistantContent: "收到",
	})
	if err == nil || err.Error() != "post-action use case is nil" {
		t.Fatalf("unexpected nil receiver error: %v", err)
	}
}

// TestPostActionExecuteRejectsNilStore verifies the exported post-action use case fails fast with one stable error when partial construction omits the relational store required for durable turn persistence.
// TestPostActionExecuteRejectsNilStore 用于验证当部分装配遗漏 turn 持久化所需的关系存储时，导出的 post-action 用例会快速返回稳定错误。
func TestPostActionExecuteRejectsNilStore(t *testing.T) {
	uc := newPostActionUseCase(nil, nil, nil, nil, &stubPostActionTurnAnalyzer{}, nil, nil, PostActionAnalysisConfig{}, nil, false)

	_, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			ProjectID:  9,
		},
		UserContent:      "第一问",
		AssistantContent: "收到",
	})
	if err == nil || err.Error() != "post-action relational store is nil" {
		t.Fatalf("unexpected nil store error: %v", err)
	}
}

// TestPostActionShutdownAcceptsNilContext verifies the exported queue shutdown hook normalizes nil contexts so direct callers can stop a partially constructed worker without panicking on ctx.Done().
// TestPostActionShutdownAcceptsNilContext 用于验证导出的队列关闭钩子会归一 nil context，确保直接调用方在关闭部分装配工作器时不会因为 ctx.Done() 触发 panic。
func TestPostActionShutdownAcceptsNilContext(t *testing.T) {
	queueCtx, queueCancel := context.WithCancel(context.Background())
	uc := &PostActionUseCase{
		queueCtx:    queueCtx,
		queueCancel: queueCancel,
	}

	if err := uc.Shutdown(nil); err != nil {
		t.Fatalf("unexpected shutdown error: %v", err)
	}
}

// TestPostActionPushQueueIDSkipsFallbackWithoutQueueContext verifies a partially constructed queue path can still absorb one overflowing enqueue attempt without panicking when the worker lifetime context is missing.
// TestPostActionPushQueueIDSkipsFallbackWithoutQueueContext 用于验证在缺少工作器生命周期 context 的部分装配场景下，队列满载时的兜底入队会被安全跳过，而不是 panic。
func TestPostActionPushQueueIDSkipsFallbackWithoutQueueContext(t *testing.T) {
	uc := &PostActionUseCase{
		queueCh: make(chan uint64, 1),
	}
	uc.queueCh <- 1

	uc.pushQueueID(2)
	time.Sleep(20 * time.Millisecond)

	if got := len(uc.queueCh); got != 1 {
		t.Fatalf("expected queue length to remain 1, got %d", got)
	}
}

// TestPostActionPushQueueIDDefersOverflowUntilCapacityReturns verifies queue overflow is captured in the in-memory deferred backlog and later flushed back into the worker channel without spawning one blocked goroutine per missed send.
// TestPostActionPushQueueIDDefersOverflowUntilCapacityReturns 用于验证队列溢出会先进入内存延迟 backlog，并在容量恢复后重新刷回工作通道，而不是为每次失败发送都启动一个阻塞 goroutine。
func TestPostActionPushQueueIDDefersOverflowUntilCapacityReturns(t *testing.T) {
	uc := &PostActionUseCase{
		queueCtx:         context.Background(),
		queueCh:          make(chan uint64, 1),
		deferredQueueSet: map[uint64]struct{}{},
	}
	uc.queueCh <- 1

	uc.pushQueueID(2)

	if got := len(uc.queueCh); got != 1 {
		t.Fatalf("expected queue length to remain full while overflow is deferred, got %d", got)
	}
	if len(uc.deferredQueueIDs) != 1 || uc.deferredQueueIDs[0] != 2 {
		t.Fatalf("deferred queue ids = %v, want [2]", uc.deferredQueueIDs)
	}
	if _, ok := uc.deferredQueueSet[2]; !ok {
		t.Fatalf("expected deferred queue set to track session 2")
	}

	<-uc.queueCh
	uc.flushDeferredQueueIDs()

	if got := len(uc.queueCh); got != 1 {
		t.Fatalf("expected flushed queue length to become 1, got %d", got)
	}
	if flushed := <-uc.queueCh; flushed != 2 {
		t.Fatalf("flushed queue id = %d, want 2", flushed)
	}
	if len(uc.deferredQueueIDs) != 0 {
		t.Fatalf("expected deferred queue to be empty after flush, got %v", uc.deferredQueueIDs)
	}
	if _, ok := uc.deferredQueueSet[2]; ok {
		t.Fatalf("expected deferred queue set to drop session 2 after flush")
	}
}

// TestPostActionApplyImmediateTurnAnalysisEnqueuesVectorRollbackCompensation verifies a relational failure after vector upsert persists a retry job when the immediate rollback delete also fails, so vector rows cannot become untracked orphans.
// TestPostActionApplyImmediateTurnAnalysisEnqueuesVectorRollbackCompensation 用于验证当关系写入在向量 upsert 之后失败，且即时回滚删除也失败时，会持久化一个重试任务，避免向量行变成无主孤儿。
func TestPostActionApplyImmediateTurnAnalysisEnqueuesVectorRollbackCompensation(t *testing.T) {
	store := &testRelationalStore{analysisErr: errors.New("apply turn analysis failed")}
	vector := &stubVectorStore{deleteErr: errors.New("vector delete unavailable")}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        91,
		Details:       "记住用户刚确认的新项目事实。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Abstract:       "当前项目已经切换到灰度发布。",
			Details:        "当前项目已经切换到灰度发布。",
			Category:       logicdomain.MemoryNodeCategoryProjectContext,
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, nil, PostActionAnalysisConfig{}, nil, false)
	session := logicdomain.SessionRef{SessionID: 41, SessionKey: "sess-rollback", UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9}
	turn := logicdomain.PersistedTurnRecord{ID: 91, SessionID: session.SessionID, ProjectID: session.ProjectID, CreatedAt: time.Now().UTC(), DehydratedBudget: 12}

	err := uc.applyImmediateTurnAnalysis(context.Background(), session, turn, logicdomain.TurnRecord{
		UserContent:      "我们现在已经切到灰度发布了。",
		AssistantContent: "收到，我会记住。",
	})
	if err == nil || !strings.Contains(err.Error(), "apply turn analysis failed") {
		t.Fatalf("expected relational apply error, got %v", err)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one vector upsert before relational failure, got %+v", vector.upserts)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 {
		t.Fatalf("expected one failed rollback delete attempt, got %+v", vector.deleteIDsCalls)
	}
	if store.vectorGCEnqueueQuery.JobType != logicdomain.VectorGCJobTypeTurnAnalysisRollback {
		t.Fatalf("vector gc job type = %q, want %q", store.vectorGCEnqueueQuery.JobType, logicdomain.VectorGCJobTypeTurnAnalysisRollback)
	}
	if len(store.vectorGCEnqueueQuery.VectorIDs) != 1 || store.vectorGCEnqueueQuery.VectorIDs[0] == "" {
		t.Fatalf("vector gc compensation ids = %+v, want one inserted vector id", store.vectorGCEnqueueQuery.VectorIDs)
	}
	if store.vectorGCEnqueueQuery.VectorIDs[0] != vector.deleteIDsCalls[0][0] {
		t.Fatalf("vector gc compensation id = %q, want rollback delete id %q", store.vectorGCEnqueueQuery.VectorIDs[0], vector.deleteIDsCalls[0][0])
	}
	if store.vectorGCEnqueueQuery.NextRunAt.IsZero() {
		t.Fatal("expected vector gc compensation to schedule a retry time")
	}
}

// TestPostActionApplyImmediateTurnAnalysisDropsInvalidEmbeddedMemoryNode verifies post-action keeps the healthy memory-node vectors while dropping only the provider-rejected single item before durable persistence.
// TestPostActionApplyImmediateTurnAnalysisDropsInvalidEmbeddedMemoryNode 用于验证 post-action 会在长期持久化前保留健康记忆节点向量，并只丢弃 provider 明确拒绝的单条异常输入。
func TestPostActionApplyImmediateTurnAnalysisDropsInvalidEmbeddedMemoryNode(t *testing.T) {
	store := &testRelationalStore{}
	vector := &stubVectorStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{
			Vectors:       [][]float32{{0.1, 0.2, 0.3}},
			ResultIndices: []int{0},
			Dropped: []appports.EmbeddingDroppedInput{{
				Index:  1,
				Text:   "第二条会被丢弃的记忆摘要",
				Reason: "input_too_large",
			}},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        92,
		Details:       "保留第一条，丢弃第二条。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Abstract:       "第一条保留的记忆摘要",
				Details:        "第一条保留的记忆详情",
				Category:       logicdomain.MemoryNodeCategoryProjectContext,
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
			{
				Abstract:       "第二条会被丢弃的记忆摘要",
				Details:        "第二条会被丢弃的记忆详情",
				Category:       logicdomain.MemoryNodeCategoryProjectContext,
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, nil, PostActionAnalysisConfig{}, nil, false)
	session := logicdomain.SessionRef{SessionID: 42, SessionKey: "sess-drop-invalid-memory", UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9}
	turn := logicdomain.PersistedTurnRecord{ID: 92, SessionID: session.SessionID, ProjectID: session.ProjectID, CreatedAt: time.Now().UTC(), DehydratedBudget: 12}

	err := uc.applyImmediateTurnAnalysis(context.Background(), session, turn, logicdomain.TurnRecord{
		UserContent:      "请记住这两条，但第二条异常。",
		AssistantContent: "收到。",
	})
	if err != nil {
		t.Fatalf("applyImmediateTurnAnalysis returned error: %v", err)
	}
	if got := len(embedding.requests); got != 1 {
		t.Fatalf("embedding request count = %d, want 1", got)
	}
	if !embedding.requests[0].AllowPartialInvalidTexts {
		t.Fatal("expected post-action embedding request to allow partial invalid texts")
	}
	if got := len(vector.upserts); got != 1 {
		t.Fatalf("vector upsert count = %d, want 1", got)
	}
	if vector.upserts[0].Text != "第一条保留的记忆摘要" {
		t.Fatalf("unexpected kept vector text: %+v", vector.upserts[0])
	}
	if got := len(store.analysis.MemoryNodes); got != 1 {
		t.Fatalf("persisted memory node count = %d, want 1", got)
	}
	if store.analysis.MemoryNodes[0].Abstract != "第一条保留的记忆摘要" {
		t.Fatalf("unexpected persisted kept memory node: %+v", store.analysis.MemoryNodes[0])
	}
}

// TestPostActionApplyImmediateTurnAnalysisReconcilesSupersedesAfterDroppedMemoryNode verifies post-action removes supersede ids contributed only by dropped memory nodes so partial embedding success cannot retire unrelated old memories.
// TestPostActionApplyImmediateTurnAnalysisReconcilesSupersedesAfterDroppedMemoryNode 用于验证当部分 memory node 在 embedding 阶段被丢弃时，post-action 会同步清理仅由被丢弃节点贡献的 supersede id，避免误退役无关旧记忆。
func TestPostActionApplyImmediateTurnAnalysisReconcilesSupersedesAfterDroppedMemoryNode(t *testing.T) {
	store := &testRelationalStore{}
	vector := &stubVectorStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{
			Vectors:       [][]float32{{0.1, 0.2, 0.3}},
			ResultIndices: []int{0},
			Dropped: []appports.EmbeddingDroppedInput{{
				Index:  1,
				Text:   "第二条会被丢弃的记忆摘要",
				Reason: "input_too_large",
			}},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        93,
		Details:       "只保留第一条替代关系。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Abstract:           "第一条保留的记忆摘要",
				Details:            "第一条保留的记忆详情",
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{701},
			},
			{
				Abstract:           "第二条会被丢弃的记忆摘要",
				Details:            "第二条会被丢弃的记忆详情",
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{702},
			},
		},
	}}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, nil, PostActionAnalysisConfig{}, nil, false)
	session := logicdomain.SessionRef{SessionID: 43, SessionKey: "sess-reconcile-supersede-after-drop", UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9}
	turn := logicdomain.PersistedTurnRecord{ID: 93, SessionID: session.SessionID, ProjectID: session.ProjectID, CreatedAt: time.Now().UTC(), DehydratedBudget: 12}

	err := uc.applyImmediateTurnAnalysis(context.Background(), session, turn, logicdomain.TurnRecord{
		UserContent:      "请保留第一条，并且第二条有异常。",
		AssistantContent: "收到。",
	})
	if err != nil {
		t.Fatalf("applyImmediateTurnAnalysis returned error: %v", err)
	}
	if got := store.analysis.MemoryNodes; len(got) != 1 || len(got[0].SupersedeMemoryIDs) != 1 || got[0].SupersedeMemoryIDs[0] != 701 {
		t.Fatalf("persisted memory supersede ids = %+v, want surviving node to keep [701]", got)
	}
}

// TestPostActionApplyImmediateTurnAnalysisAllowsAllDroppedInvalidMemoryNodes verifies post-action accepts a best-effort embedding response that contains only dropped items, so a single invalid memory node no longer aborts the whole turn.
// TestPostActionApplyImmediateTurnAnalysisAllowsAllDroppedInvalidMemoryNodes 用于验证当 embedding best-effort 响应只包含 dropped 条目时，post-action 仍会接受该结果，不再因为单条无效记忆节点而中断整轮处理。
func TestPostActionApplyImmediateTurnAnalysisAllowsAllDroppedInvalidMemoryNodes(t *testing.T) {
	store := &testRelationalStore{}
	vector := &stubVectorStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{
			Dropped: []appports.EmbeddingDroppedInput{{
				Index:  0,
				Text:   "唯一一条会被丢弃的记忆摘要",
				Reason: "input_too_large",
			}},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        94,
		Details:       "该轮只有一条异常记忆候选。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Abstract:           "唯一一条会被丢弃的记忆摘要",
			Details:            "唯一一条会被丢弃的记忆详情",
			Category:           logicdomain.MemoryNodeCategoryProjectContext,
			EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:          logicdomain.TurnAnalysisAdmissionKeep,
			SupersedeMemoryIDs: []uint64{801},
		}},
	}}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, nil, nil, PostActionAnalysisConfig{}, nil, false)
	session := logicdomain.SessionRef{SessionID: 44, SessionKey: "sess-all-dropped-invalid-memory", UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9}
	turn := logicdomain.PersistedTurnRecord{ID: 94, SessionID: session.SessionID, ProjectID: session.ProjectID, CreatedAt: time.Now().UTC(), DehydratedBudget: 12}

	err := uc.applyImmediateTurnAnalysis(context.Background(), session, turn, logicdomain.TurnRecord{
		UserContent:      "这条提炼结果异常。",
		AssistantContent: "收到。",
	})
	if err != nil {
		t.Fatalf("applyImmediateTurnAnalysis returned error: %v", err)
	}
	if got := len(vector.upserts); got != 0 {
		t.Fatalf("vector upsert count = %d, want 0", got)
	}
	if got := len(store.analysis.MemoryNodes); got != 0 {
		t.Fatalf("persisted memory node count = %d, want 0", got)
	}
}

// TestPostActionUseCaseDropsSingleRoundNoise verifies simple user-assistant pairs can still be rejected by the noise gate before relational persistence.
// TestPostActionUseCaseDropsSingleRoundNoise 用于验证简单的单轮 user-assistant 问答仍然会在关系持久化前被噪声门拒绝。
func TestPostActionUseCaseDropsSingleRoundNoise(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := newPostActionUseCase(filter, store, nil, nil, &stubPostActionTurnAnalyzer{}, nil, nil, PostActionAnalysisConfig{}, nil, false)

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

// TestPostActionExecuteScrubsPIIBeforeNoiseGateAndPersistence verifies post-action redacts the incoming turn before the noise gate inspects it and before the canonical turn is written into durable storage.
// TestPostActionExecuteScrubsPIIBeforeNoiseGateAndPersistence 用于验证 post-action 会在噪声门检查之前、以及标准 turn 写入长期存储之前，先完成当前请求的 PII 脱敏。
func TestPostActionExecuteScrubsPIIBeforeNoiseGateAndPersistence(t *testing.T) {
	filter := &stubNoiseTurnFilter{
		filtered: []logicdomain.NormalizedTurn{{
			TurnIndex:      1,
			UserMessage:    "[PHONE]",
			AssistantReply: "[PHONE]",
		}},
	}
	store := &testRelationalStore{}
	uc := newPostActionUseCase(filter, store, nil, nil, &stubPostActionTurnAnalyzer{}, nil, nil, PostActionAnalysisConfig{}, nil, false)
	uc.ConfigurePIIScrubber(stubPIIScrubber{
		replacements: map[string]string{
			"13800138000": "[PHONE]",
		},
	})

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-pii",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent:         "用户电话是 13800138000",
		AssistantContent:    "收到，联系电话 13800138000",
		RawUserContent:      "raw 用户电话 13800138000",
		RawAssistantContent: "raw 联系电话 13800138000",
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted post-action result, got %#v", result)
	}
	if len(filter.seen) != 1 || strings.Contains(filter.seen[0].UserMessage, "13800138000") || strings.Contains(filter.seen[0].AssistantReply, "13800138000") {
		t.Fatalf("expected noise gate to see scrubbed text, got %#v", filter.seen)
	}
	if strings.Contains(store.turn.UserContent, "13800138000") || strings.Contains(store.turn.AssistantContent, "13800138000") {
		t.Fatalf("expected persisted turn to stay scrubbed, got %#v", store.turn)
	}
}

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads still bypass the single-round noise gate and persist one canonical turn row before async processing begins.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷仍会跳过单轮噪声门，并在异步处理开始前先持久化成一条标准 turn 记录。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputMixed,
		TurnID:        1,
		Details:       "timeline turn details",
	}}
	uc := newPostActionUseCase(filter, store, nil, nil, analyzer, nil, nil, PostActionAnalysisConfig{}, nil, false)

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
	uc := newPostActionUseCase(filter, store, nil, nil, analyzer, nil, nil, PostActionAnalysisConfig{
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

// TestBuildTurnAnalysisInputReusesScrubbedStoredData verifies post-action reuses already scrubbed turn/history/direct-write text from durable storage and does not trigger one extra masking pass before analyzer input assembly.
// TestBuildTurnAnalysisInputReusesScrubbedStoredData 用于验证 post-action 会复用库里已经脱敏的 turn、历史与 direct-write 文本，并且在组装分析器输入时不再额外触发一次脱敏。
func TestBuildTurnAnalysisInputReusesScrubbedStoredData(t *testing.T) {
	store := &testRelationalStore{
		historyTurns: []logicdomain.SessionTurnRecord{
			{ID: 10, Details: "历史里记录的电话是 [PHONE]。"},
		},
		recentDirectWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 30, ScopeLevel: "project", Abstract: "最近直写电话 [PHONE]", Details: "工具记下了 [PHONE]", CreatedTimestamp: 123},
		},
	}
	uc := newPostActionUseCase(nil, store, nil, nil, &stubPostActionTurnAnalyzer{}, nil, nil, PostActionAnalysisConfig{HistoryTurns: 4, MaxInputTokens: 200}, nil, false)
	scrubber := &countingPIIScrubber{}
	uc.ConfigurePIIScrubber(scrubber)

	input, _, err := uc.buildTurnAnalysisInput(
		context.Background(),
		logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-pii-analysis",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		logicdomain.PersistedTurnRecord{
			ID:               91,
			SessionID:        41,
			ProjectID:        9,
			CreatedAt:        time.Now().UTC(),
			DehydratedBudget: 12,
		},
		logicdomain.TurnRecord{
			UserContent:      "当前用户电话是 [PHONE]",
			AssistantContent: "助手复述 [PHONE]",
		},
	)
	if err != nil {
		t.Fatalf("build turn analysis input: %v", err)
	}
	if scrubber.calls != 0 {
		t.Fatalf("expected no extra scrub calls during analyzer input assembly, got %d", scrubber.calls)
	}
	if strings.Contains(input.TargetTurn.RawTurn, "13800138000") || !strings.Contains(input.TargetTurn.RawTurn, "[PHONE]") {
		t.Fatalf("expected target turn payload to reuse scrubbed text, got %q", input.TargetTurn.RawTurn)
	}
	if len(input.ReferenceTurns) != 1 || strings.Contains(input.ReferenceTurns[0].Details, "13800138000") || !strings.Contains(input.ReferenceTurns[0].Details, "[PHONE]") {
		t.Fatalf("expected reference turns to reuse scrubbed text, got %#v", input.ReferenceTurns)
	}
	if len(input.RecentGRPCMemoryWrites) != 1 || strings.Contains(input.RecentGRPCMemoryWrites[0].Abstract, "13800138000") || strings.Contains(input.RecentGRPCMemoryWrites[0].Details, "13800138000") {
		t.Fatalf("expected recent direct writes to reuse scrubbed text, got %#v", input.RecentGRPCMemoryWrites)
	}
}

// TestPostActionUseCaseProcessesQueuedTurnsAsynchronously verifies the async worker uses refined history plus direct-write exclusions when it drains pending turns.
// TestPostActionUseCaseProcessesQueuedTurnsAsynchronously 用于验证异步工作器在消化 pending turn 时，会结合已提炼历史与 direct-write 排斥窗口完成分析。
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
		recentDirectWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 9901, ScopeLevel: "PROJECT", Abstract: "已主动写入的并发规则", Details: "这条并发规则已经由工具直接写入。"},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        88,
		Details:       "当前轮确认保持异步提炼链路。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:           logicdomain.MemoryNodeCategoryArchitectureDecision,
				Abstract:           "当前项目确认改用 channel 管理并发。",
				Details:            "这是新的并发决策。",
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{701},
			},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}}}
	vector := &stubVectorStore{}
	searcher := &stubPostActionMemorySearcher{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "保留当前轮新增架构决策。",
			},
		},
	}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, searcher, reviewer, PostActionAnalysisConfig{
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
	if len(analyzer.input.RecentGRPCMemoryWrites) != 1 || analyzer.input.RecentGRPCMemoryWrites[0].MemoryID != 9901 {
		t.Fatalf("expected one recent direct write exclusion, got %+v", analyzer.input.RecentGRPCMemoryWrites)
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
	if len(store.analysis.MemoryNodes) != 1 || len(store.analysis.MemoryNodes[0].SupersedeMemoryIDs) != 1 || store.analysis.MemoryNodes[0].SupersedeMemoryIDs[0] != 701 {
		t.Fatalf("expected persisted memory node to carry supersede id 701, got %+v", store.analysis.MemoryNodes)
	}
}

// TestPostActionUseCaseDegradesUnifiedReviewerFailureAndStillPersistsAnalyzerOutput verifies transient reviewer failures no longer abort queued turn persistence and instead fall back to the analyzer output.
// TestPostActionUseCaseDegradesUnifiedReviewerFailureAndStillPersistsAnalyzerOutput 用于验证统一 reviewer 短暂失败时不会再中断排队 turn 的持久化，而是回退到分析器输出继续落库。
func TestPostActionUseCaseDegradesUnifiedReviewerFailureAndStillPersistsAnalyzerOutput(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                90,
				SessionID:         90,
				ProjectID:         12,
				DehydratedContent: `{"user":"帮我记住默认部署方式","timeline":[],"assistant":"我来整理成长期记忆"}`,
				DehydratedBudget:  16,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
				UpdatedAt:         time.Date(2026, 4, 2, 10, 0, 1, 0, time.UTC),
			},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        90,
		Details:       "用户确认默认部署方式需要长期保留。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
				Abstract:       "当前项目默认使用本地部署方式。",
				Details:        "用户确认当前项目默认使用本地部署方式。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{
				ProfileType:    logicdomain.ProfileTypeProject,
				Content:        "当前项目默认采用本地部署方式。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.4, 0.5, 0.6}}}}
	vector := &stubVectorStore{}
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex: 0,
				Query:      "当前项目默认使用本地部署方式。",
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{err: errors.New("llm timeout")}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, searcher, reviewer, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, logger, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  90,
		SessionKey: "sess-degrade",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	if reviewer.calls != 1 {
		t.Fatalf("expected unified reviewer to run once before degrading, got %d", reviewer.calls)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected degraded path to keep vector persistence, got %d", len(vector.upserts))
	}
	if store.analysisTurn.ID != 90 {
		t.Fatalf("expected degraded path to still apply turn analysis, got %+v", store.analysisTurn)
	}
	if len(store.analysis.MemoryNodes) != 1 {
		t.Fatalf("expected analyzer memory nodes to survive degraded persistence, got %+v", store.analysis.MemoryNodes)
	}
	if len(store.analysis.ProfileNodes) != 1 {
		t.Fatalf("expected analyzer profile nodes to survive degraded persistence, got %+v", store.analysis.ProfileNodes)
	}
	if store.analysis.ProfileNodes[0].Status != logicdomain.ProfileStatusPending {
		t.Fatalf("expected degraded profile node to stay pending before later review, got %+v", store.analysis.ProfileNodes[0])
	}
	if store.advancedSessionID != 90 {
		t.Fatalf("expected degraded path to still advance extract window, got session_id=%d", store.advancedSessionID)
	}
	if !strings.Contains(logBuf.String(), "post-action candidate review degraded to analyzer output") {
		t.Fatalf("expected degraded warning log, got %s", logBuf.String())
	}
}

// TestPostActionUseCaseRedactsAnalysisResultLogs verifies the async analysis-result log keeps throughput diagnostics without writing derived analysis text into runtime logs.
// TestPostActionUseCaseRedactsAnalysisResultLogs 用于验证异步分析结果日志会保留吞吐诊断字段，但不会把提炼出的分析文本写入运行时日志。
func TestPostActionUseCaseRedactsAnalysisResultLogs(t *testing.T) {
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
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        88,
		Details:       "用户银行卡 1234 的部署方案需要切到本地模式。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
				Abstract:       "用户身份证 5678 的部署偏好",
				Details:        "这是敏感派生文本。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{
				ProfileType:    logicdomain.ProfileTypeUser,
				Content:        "用户更偏好本地模式。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}}}
	vector := &stubVectorStore{}
	searcher := &stubPostActionMemorySearcher{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "保留当前轮新增部署决策。",
			},
			User: &logicdomain.ProfileReviewSection{
				AcceptedCandidates: []logicdomain.ProfileReviewAcceptedCandidate{{
					CandidateIndex:    0,
					NormalizedContent: "用户更偏好本地部署模式",
					Priority:          logicdomain.ProfilePriorityP1,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "这是稳定部署偏好。",
				}},
				Reason: "保留用户长期部署偏好。",
			},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, searcher, reviewer, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, logger, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  88,
		SessionKey: "sess-redact",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	logs := logBuf.String()
	if !strings.Contains(logs, "post-action turn analysis result") {
		t.Fatalf("expected analysis result log, got %s", logs)
	}
	if strings.Contains(logs, "用户银行卡 1234 的部署方案需要切到本地模式。") || strings.Contains(logs, "用户身份证 5678 的部署偏好") || strings.Contains(logs, "这是敏感派生文本。") {
		t.Fatalf("expected analysis result log to redact derived text, got %s", logs)
	}
	if !strings.Contains(logs, "details_len") || !strings.Contains(logs, "memory_node_count") || !strings.Contains(logs, "analysis_sha256") {
		t.Fatalf("expected redacted analysis diagnostics, got %s", logs)
	}
}

// TestPostActionUseCaseLogsRawAnalyzeTurnJSONDecodeFailure verifies queued postaction_l1_main JSON decode failures emit both the configured model label and the verbatim raw model output needed to debug malformed responses.
// TestPostActionUseCaseLogsRawAnalyzeTurnJSONDecodeFailure 用于验证排队 postaction_l1_main 的 JSON 解码失败会同时输出当前模型标识和原样模型返回体，方便定位畸形响应。
func TestPostActionUseCaseLogsRawAnalyzeTurnJSONDecodeFailure(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                92,
				SessionID:         92,
				ProjectID:         12,
				DehydratedContent: `{"user":"我弟弟牙齿什么情况","timeline":[],"assistant":"我来分析一下"}`,
				DehydratedBudget:  10,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 4, 8, 1, 0, 0, 0, time.UTC),
				UpdatedAt:         time.Date(2026, 4, 8, 1, 0, 1, 0, time.UTC),
			},
		},
	}
	rawOutput := strings.Join([]string{
		"```json",
		"{\"turn_id\":92,",
		"\"details\":\"牙齿问题\"",
	}, "\n")
	analyzer := &stubPostActionTurnAnalyzer{
		err: logicdomain.InvalidLLMOutputError{
			Scene:   "postaction_l1_main",
			Message: "json decode failed",
			Raw:     rawOutput,
		},
		model: "Qwen/Qwen3-32B",
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, store, nil, nil, analyzer, nil, nil, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, logger, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  92,
		SessionKey: "sess-json-decode",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	logs := logBuf.String()
	if analyzer.calls != 1 {
		t.Fatalf("expected queued turn analyzer to run once, got %d", analyzer.calls)
	}
	if !strings.Contains(logs, "post-action queued turn analysis failed") {
		t.Fatalf("expected queued turn analysis failure log, got %s", logs)
	}
	if !strings.Contains(logs, "model：\"Qwen/Qwen3-32B\"") {
		t.Fatalf("expected model field in failure log, got %s", logs)
	}
	if !strings.Contains(logs, "TEXT(llm_raw_output)：\n"+rawOutput+"\n") {
		t.Fatalf("expected raw postaction_l1_main output in failure log, got %s", logs)
	}
	if !strings.Contains(logs, "invalid llm output for postaction_l1_main: json decode failed") {
		t.Fatalf("expected invalid llm output error summary in failure log, got %s", logs)
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
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        91,
		Details:       "当前轮需要落一条记忆。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:       logicdomain.MemoryNodeCategoryRequirementTODO,
				Abstract:       "需要记录当前异步回写失败回滚场景。",
				Details:        "用于验证向量回滚。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.9, 0.8, 0.7}}}}
	vector := &stubVectorStore{}
	searcher := &stubPostActionMemorySearcher{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "保留用于验证回滚的长期记忆。",
			},
		},
	}
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, searcher, reviewer, PostActionAnalysisConfig{}, nil, false)

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

// TestPostActionAnalysisLogsRawPayloadsWhenPayloadDebugEnabled verifies the shared payload-debug logger switch still emits full analysis JSON for local troubleshooting, while omitting the high-dimensional vector arrays that add no diagnostic value.
// TestPostActionAnalysisLogsRawPayloadsWhenPayloadDebugEnabled 用于验证共享 payload 调试开关开启后，分析结果日志仍会输出完整分析 JSON 供本地排障，但会省略没有诊断价值的高维向量数组。
func TestPostActionAnalysisLogsRawPayloadsWhenPayloadDebugEnabled(t *testing.T) {
	store := &testRelationalStore{
		pendingTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                89,
				SessionID:         89,
				ProjectID:         12,
				DehydratedContent: `{"user":"你好","timeline":[],"assistant":"收到"}`,
				DehydratedBudget:  10,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
				CreatedAt:         time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
			},
		},
	}
	analyzer := &stubPostActionTurnAnalyzer{result: logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		TurnID:        89,
		Details:       "用户银行卡 1234 的部署方案需要切到本地模式。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
				Abstract:       "用户身份证 5678 的部署偏好",
				Details:        "这是敏感派生文本。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}}}
	vector := &stubVectorStore{}
	searcher := &stubPostActionMemorySearcher{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "保留当前轮新增部署记忆。",
			},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
	uc := newPostActionUseCase(nil, store, embedding, vector, analyzer, searcher, reviewer, PostActionAnalysisConfig{
		HistoryTurns:   3,
		MaxInputTokens: 200,
	}, logger, false)

	uc.processQueuedTurns(logicdomain.SessionRef{
		SessionID:  89,
		SessionKey: "sess-debug-analysis",
		UserID:     9,
		TeamID:     4,
		SpaceID:    6,
		ProjectID:  12,
	}, "test")

	logs := logBuf.String()
	if !strings.Contains(logs, "post-action turn analysis result") || !strings.Contains(logs, "JSON(analysis_json)：") {
		t.Fatalf("expected payload-debug analysis JSON log, got %s", logs)
	}
	if !strings.Contains(logs, "用户银行卡 1234 的部署方案需要切到本地模式。") || !strings.Contains(logs, "这是敏感派生文本。") {
		t.Fatalf("expected payload-debug analysis log to include derived text, got %s", logs)
	}
	if !strings.Contains(logs, `vector_payload_notice："embedding vectors omitted from analysis_json"`) || !strings.Contains(logs, "vector_payload_redacted_nodes：1") {
		t.Fatalf("expected payload-debug analysis log to announce vector redaction, got %s", logs)
	}
	if strings.Contains(logs, `"Vector":`) {
		t.Fatalf("expected payload-debug analysis log to omit the Vector field entirely, got %s", logs)
	}
	if strings.Contains(logs, "analysis_sha256") || strings.Contains(logs, "analysis_len") {
		t.Fatalf("expected payload-debug analysis log to bypass redacted digest fields, got %s", logs)
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
	uc := newPostActionUseCase(nil, store, nil, nil, nil, nil, nil, PostActionAnalysisConfig{}, logger, false)

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
	uc := newPostActionUseCase(nil, store, nil, nil, nil, nil, nil, PostActionAnalysisConfig{
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

// TestProfileDateFromTurnUsesLocalCalendarDay verifies fresh profile nodes inherit their profile_date from the shared local calendar day instead of a UTC-truncated day.
// TestProfileDateFromTurnUsesLocalCalendarDay 用于验证新画像节点继承的 profile_date 来自共享本地自然日，而不是 UTC 截断后的日期。
func TestProfileDateFromTurnUsesLocalCalendarDay(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	date := profileDateFromTurn(logicdomain.SessionTurnRecord{
		CreatedAt: time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
	})
	if date != "2026-04-02" {
		t.Fatalf("expected local calendar day 2026-04-02, got %q", date)
	}
}

// TestNormalizeRenderDateUsesLocalCalendarDayFallback verifies rendered profile timelines use the shared local calendar-day fallback when one node has no stored profile_date.
// TestNormalizeRenderDateUsesLocalCalendarDayFallback 用于验证当画像节点缺失 profile_date 时，最终渲染时间线会使用共享本地自然日 fallback。
func TestNormalizeRenderDateUsesLocalCalendarDayFallback(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	date := normalizeRenderDate("", time.Time{}, time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC))
	if date != "2026-04-02" {
		t.Fatalf("expected local calendar day 2026-04-02, got %q", date)
	}
}

// TestNormalizeRenderDateCorrectsLegacyUTCProfileDate verifies rendered profile timelines transparently repair legacy UTC-derived profile_date values when node creation time proves the local natural day is different.
// TestNormalizeRenderDateCorrectsLegacyUTCProfileDate 用于验证当节点创建时间明确表明本地自然日不同，最终画像渲染会自动修正历史上按 UTC 推导出的 legacy profile_date。
func TestNormalizeRenderDateCorrectsLegacyUTCProfileDate(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	date := normalizeRenderDate("2026-04-01", time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC), time.Time{})
	if date != "2026-04-02" {
		t.Fatalf("expected legacy UTC-derived date to be corrected to 2026-04-02, got %q", date)
	}
}

// TestRenderProfileTimelineCorrectsLegacyUTCProfileDateUsingAnchor verifies the renderer uses the explicit profile-date anchor instead of the delayed node write time when repairing legacy UTC-derived dates.
// TestRenderProfileTimelineCorrectsLegacyUTCProfileDateUsingAnchor 用于验证渲染器在修正历史 UTC 日期时会使用显式 profile 日期锚点，而不是延迟入库后的节点写入时间。
func TestRenderProfileTimelineCorrectsLegacyUTCProfileDateUsingAnchor(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	rendered := renderProfileTimeline([]logicdomain.ProfileActiveNodeRecord{{
		ID:                  301,
		ProfileType:         logicdomain.ProfileTypeProject,
		BindID:              9,
		Content:             "项目保持本地优先部署。",
		Status:              logicdomain.ProfileStatusActive,
		Priority:            logicdomain.ProfilePriorityP1,
		ProfileLevel:        logicdomain.ProfileLevelStable,
		RefreshWeight:       2,
		ProfileDate:         "2026-04-01",
		ProfileDateAnchorAt: time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
		CreatedAt:           time.Date(2026, 4, 2, 3, 0, 0, 0, time.UTC),
	}}, nil, nil)
	if !strings.Contains(rendered, "2026-04-02:") {
		t.Fatalf("expected rendered timeline to use corrected anchored date, got %q", rendered)
	}
	if strings.Contains(rendered, "2026-04-01:") {
		t.Fatalf("expected legacy UTC-derived date to be removed from rendered timeline, got %q", rendered)
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
	vectorGCEnqueueQuery    logicdomain.VectorGCJobEnqueueQuery
	advancedSessionID       uint64
	advancedObservedAt      time.Time
	advancedCompletedAt     time.Time
	adoptedMemoryIDs        []uint64
	adoptedAt               time.Time
	adoptionErr             error
	markedCorruptedTurnIDs  []uint64
	markCorruptedErr        error
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

// MarkTurnAsCorrupted keeps interface completeness for tests that need corrupted-turn handling.
// MarkTurnAsCorrupted 用于补齐接口，让需要损坏 turn 处理的测试场景仍可编译。
func (s *testRelationalStore) MarkTurnAsCorrupted(_ context.Context, _ logicdomain.SessionRef, turnID uint64) error {
	s.markedCorruptedTurnIDs = append(s.markedCorruptedTurnIDs, turnID)
	return s.markCorruptedErr
}

// EnqueueVectorGCJobs records the latest compensation enqueue request so post-action tests can assert failed vector cleanup is bridged into the persistent retry queue.
// EnqueueVectorGCJobs 用于记录最近一次补偿入队请求，让 post-action 测试验证失败的向量清理会桥接到持久化重试队列。
func (s *testRelationalStore) EnqueueVectorGCJobs(_ context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	s.vectorGCEnqueueQuery = query
	return nil
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
	model  string
}

// Analyze captures the structured single-turn input so tests can assert the new synchronous post-action request shape.
// Analyze 用于捕获结构化单轮输入，方便测试断言新的同步 post-action 请求形态。
func (s *stubPostActionTurnAnalyzer) Analyze(_ context.Context, input logicdomain.TurnAnalysisInput) (logicdomain.TurnAnalysis, error) {
	s.calls++
	s.input = logicdomain.TurnAnalysisInput{
		CurrentTimestamp:       input.CurrentTimestamp,
		ReferenceTurns:         append([]logicdomain.TurnAnalysisReferenceTurn(nil), input.ReferenceTurns...),
		TargetTurn:             input.TargetTurn,
		RecentGRPCMemoryWrites: append([]logicdomain.TurnAnalysisDirectWrite(nil), input.RecentGRPCMemoryWrites...),
	}
	if s.err != nil {
		return logicdomain.TurnAnalysis{}, s.err
	}
	return s.result, nil
}

// AnalyzeModel returns the configured stub model label so queued failure-log tests can verify the runtime logs which post-action first-stage model produced malformed output.
// AnalyzeModel 用于返回桩对象配置的模型标识，方便排队失败日志测试验证运行时会记录是哪个 post-action 第一层模型产出了畸形输出。
func (s *stubPostActionTurnAnalyzer) AnalyzeModel() string {
	if s == nil {
		return ""
	}
	return s.model
}

// stubEmbeddingClient records embedding requests and returns one canned response so post-action tests can verify vector persistence without a real model backend.
// stubEmbeddingClient 用于记录 embedding 请求，并返回预设结果，让 post-action 测试在没有真实模型后端时也能验证向量持久化。
type stubEmbeddingClient struct {
	requests      []appports.EmbeddingRequest
	response      appports.EmbeddingResponse
	responseQueue []appports.EmbeddingResponse
	err           error
}

// Embed captures the request order and replays one queued response when configured, otherwise it falls back to the default canned response.
// Embed 用于记录请求顺序；若配置了响应队列则按顺序回放，否则退回默认预设响应。
func (s *stubEmbeddingClient) Embed(_ context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return appports.EmbeddingResponse{}, s.err
	}
	if len(s.responseQueue) > 0 {
		resp := s.responseQueue[0]
		s.responseQueue = s.responseQueue[1:]
		return resp, nil
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
