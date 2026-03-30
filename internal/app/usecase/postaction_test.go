// postaction_test.go verifies the latest turn-level post-action workflow against the new resolved-session contract.
// postaction_test.go 用于围绕新的已解析 session 契约，验证最新的 turn 级 post-action 工作流。
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

// TestPostActionUseCaseDropsSingleRoundNoise verifies simple user-assistant pairs can still be rejected by the noise gate.
// TestPostActionUseCaseDropsSingleRoundNoise 用于验证简单的单轮 user-assistant 问答仍然会被噪声门拒绝。
func TestPostActionUseCaseDropsSingleRoundNoise(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil)

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
	if got := filter.seen[0].UserMessage; got != "你还记得我上次说过什么吗" {
		t.Fatalf("unexpected normalized user message: %q", got)
	}
	if store.turn.UserContent != "" || store.turn.AssistantContent != "" || len(store.turn.Timeline) != 0 {
		t.Fatalf("expected no persisted turn, got %#v", store.turn)
	}
}

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads bypass the single-round noise gate and persist one canonical turn.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷会跳过单轮噪声门，并持久化为一条标准 turn。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil)

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

// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval verifies accepted single-round payloads are stored as one canonical turn.
// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval 用于验证通过噪声门的单轮载荷会按新的标准 turn 落库。
func TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, nil, nil, nil, PostActionAnalysisConfig{}, nil)

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
	if len(filter.seen) != 1 {
		t.Fatalf("expected one normalized turn, got %d", len(filter.seen))
	}
	assertPersistedTurn(t, store.turn, "你好", "收到", nil)
}

// TestPostActionUseCaseAlwaysRunsTurnAnalysis verifies the debug-stage analysis now sends every persisted turn through the structured turn-analysis prompt and writes the extracted result back to the relational store.
// TestPostActionUseCaseAlwaysRunsTurnAnalysis 用于验证当前调试阶段会把每一条已落库 turn 都送入结构化逐轮分析提示词，并把提炼结果回写到关系存储。
func TestPostActionUseCaseAlwaysRunsTurnAnalysis(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "clean-user", AssistantReply: "clean-assistant"}}}
	store := &testRelationalStore{persistedTurn: logicdomain.PersistedTurnRecord{ID: 501, SessionID: 91, ProjectID: 12, DehydratedBudget: 9, CreatedAt: time.Date(2026, 3, 30, 8, 0, 0, 0, time.UTC)}}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.11, 0.22, 0.33}}}}
	vector := &stubVectorStore{}
	analyzer := &stubPostActionAnalyzer{result: logicdomain.TurnAnalysis{
		Details: "这轮对话明确了 AI 记忆子项目的方向建议需求。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "当前对话需要先提供 AI 记忆子项目的方向建议。", Details: "用户当前诉求是先获得 AI 记忆子项目的设计建议。"},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{ProfileType: logicdomain.ProfileTypeProject, Content: "当前项目关注 AI 记忆能力设计。"},
		},
	}}
	uc := NewPostActionUseCase(filter, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  91,
			SessionKey: "sess-always",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
			TurnCount:  0,
		},
		UserContent:         "clean-user",
		AssistantContent:    "clean-assistant",
		RawUserContent:      "raw-user",
		RawAssistantContent: "raw-assistant",
		RawTimeline: []PostActionTimelineItem{
			{Type: "assistant", Content: "raw-middle"},
		},
	})
	if err != nil {
		t.Fatalf("execute post-action with debug summary: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	assertPersistedTurn(t, store.turn, "clean-user", "clean-assistant", nil)
	if analyzer.calls != 1 {
		t.Fatalf("expected one turn analysis call, got %d", analyzer.calls)
	}
	if !strings.Contains(analyzer.transcript, `"user": "raw-user"`) {
		t.Fatalf("expected raw user content in analysis transcript, got %s", analyzer.transcript)
	}
	if !strings.Contains(analyzer.transcript, `"assistant": "raw-assistant"`) {
		t.Fatalf("expected raw assistant content in analysis transcript, got %s", analyzer.transcript)
	}
	if !strings.Contains(analyzer.transcript, `"content": "raw-middle"`) {
		t.Fatalf("expected raw timeline content in analysis transcript, got %s", analyzer.transcript)
	}
	if store.analysisTurn.ID != 501 {
		t.Fatalf("expected persisted turn id to flow into analysis persistence, got %+v", store.analysisTurn)
	}
	if got := store.analysis.Details; got != "这轮对话明确了 AI 记忆子项目的方向建议需求。" {
		t.Fatalf("unexpected stored analysis details: %+v", store.analysis)
	}
	if store.analysis.DetailsBudget <= 0 {
		t.Fatalf("expected computed details budget, got %+v", store.analysis)
	}
	if len(store.analysis.MemoryNodes) != 1 || len(store.analysis.ProfileNodes) != 1 {
		t.Fatalf("unexpected stored analysis nodes: %+v", store.analysis)
	}
	if strings.TrimSpace(store.analysis.MemoryNodes[0].VectorID) == "" {
		t.Fatalf("expected vector id to be attached back onto memory node, got %+v", store.analysis.MemoryNodes[0])
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected 1 vector upsert, got %d", len(vector.upserts))
	}
	if vector.upserts[0].ID != store.analysis.MemoryNodes[0].VectorID {
		t.Fatalf("expected duckdb vector_id to match lancedb row id, got upsert=%s node=%s", vector.upserts[0].ID, store.analysis.MemoryNodes[0].VectorID)
	}
	if vector.upserts[0].Filter.SessionID != 91 {
		t.Fatalf("expected post-action memory vectors to keep the source session id, got %+v", vector.upserts[0].Filter)
	}
	if vector.upserts[0].Metadata["turn_id"] != "501" {
		t.Fatalf("expected vector metadata to keep turn anchor, got %+v", vector.upserts[0].Metadata)
	}
}

// TestPostActionUseCaseBatchesProfileMergeOncePerTurn verifies one turn with multiple user/project profile nodes triggers exactly one batched merge call and maps the merge result back into DuckDB persistence fields.
// TestPostActionUseCaseBatchesProfileMergeOncePerTurn 用于验证单个 turn 中出现多条 user/project 画像节点时，只会触发一次批量合并调用，并把合并结果映射回 DuckDB 持久化字段。
func TestPostActionUseCaseBatchesProfileMergeOncePerTurn(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "clean-user", AssistantReply: "clean-assistant"}}}
	store := &testRelationalStore{
		persistedTurn: logicdomain.PersistedTurnRecord{ID: 601, SessionID: 91, ProjectID: 12, DehydratedBudget: 9, CreatedAt: time.Date(2026, 3, 30, 8, 0, 0, 0, time.UTC)},
		profileTargets: logicdomain.ProfileTargetsSnapshot{
			UserProfile:    "旧用户画像",
			ProjectProfile: "旧项目画像",
		},
	}
	analyzer := &stubPostActionAnalyzer{result: logicdomain.TurnAnalysis{
		Details: "这轮对话给出了新的用户偏好和项目约束。",
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{ProfileType: logicdomain.ProfileTypeUser, Content: "用户偏好 Rust 进行后端开发。"},
			{ProfileType: logicdomain.ProfileTypeProject, Content: "项目当前仍处于设计阶段，尚无代码。"},
			{ProfileType: logicdomain.ProfileTypeUser, Content: "用户希望面向多个 AI 编程工具做记忆扩展。"},
			{ProfileType: logicdomain.ProfileTypeProject, Content: "项目需要兼容 Opencode、OpenClaw、Claude Code、Codex。"},
		},
	}}
	merger := &stubPostActionProfileMerger{
		result: logicdomain.TurnProfileMergeResult{
			User: &logicdomain.ProfileMergeSection{
				UpdatedProfile:          "合并后的用户画像",
				MergedCandidateIndexes:  []int{0, 1},
				InvalidCandidateIndexes: []int{},
				Reason:                  "两条用户画像都应纳入长期偏好。",
			},
			Project: &logicdomain.ProfileMergeSection{
				UpdatedProfile:          "合并后的项目画像",
				MergedCandidateIndexes:  []int{0},
				InvalidCandidateIndexes: []int{1},
				Reason:                  "项目兼容工具范围暂不稳定，保留设计阶段信息，标记工具清单为无效候选。",
			},
		},
	}
	uc := NewPostActionUseCase(filter, store, nil, nil, analyzer, merger, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  91,
			SessionKey: "sess-profile-batch",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
		},
		UserContent:      "clean-user",
		AssistantContent: "clean-assistant",
	})
	if err != nil {
		t.Fatalf("execute post-action with profile merge: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if merger.calls != 1 {
		t.Fatalf("expected one batched profile merge call, got %d", merger.calls)
	}
	if merger.snapshot.UserProfile != "旧用户画像" || merger.snapshot.ProjectProfile != "旧项目画像" {
		t.Fatalf("unexpected merge snapshot: %+v", merger.snapshot)
	}
	if len(merger.nodes) != 4 {
		t.Fatalf("expected all profile nodes to flow into one merge call, got %+v", merger.nodes)
	}
	if !store.analysis.UserProfileMerged || store.analysis.MergedUserProfile != "合并后的用户画像" {
		t.Fatalf("unexpected merged user profile state: %+v", store.analysis)
	}
	if !store.analysis.ProjectProfileMerged || store.analysis.MergedProjectProfile != "合并后的项目画像" {
		t.Fatalf("unexpected merged project profile state: %+v", store.analysis)
	}
	if got := store.analysis.ProfileNodes[0].Status; got != logicdomain.ProfileStatusMerged {
		t.Fatalf("expected first user node to be merged, got %d", got)
	}
	if got := store.analysis.ProfileNodes[1].Status; got != logicdomain.ProfileStatusMerged {
		t.Fatalf("expected first project node to be merged, got %d", got)
	}
	if got := store.analysis.ProfileNodes[2].Status; got != logicdomain.ProfileStatusMerged {
		t.Fatalf("expected second user node to be merged, got %d", got)
	}
	if got := store.analysis.ProfileNodes[3].Status; got != logicdomain.ProfileStatusInvalid {
		t.Fatalf("expected second project node to be invalid, got %d", got)
	}
}

// TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails verifies the new debug-stage turn analysis never breaks the main post-action persistence flow.
// TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails 用于验证新的调试阶段逐轮分析即使失败，也绝不会破坏主 post-action 持久化链路。
func TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{persistedTurn: logicdomain.PersistedTurnRecord{ID: 777, SessionID: 92, ProjectID: 12, DehydratedBudget: 11}}
	analyzer := &stubPostActionAnalyzer{err: errors.New("llm failed")}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewPostActionUseCase(
		filter,
		store,
		nil,
		nil,
		analyzer,
		nil,
		PostActionAnalysisConfig{IdleTimeout: 15 * time.Minute},
		logger,
	)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  92,
			SessionKey: "sess-idle",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
			TurnCount:  1,
			UpdatedAt:  time.Now().Add(-20 * time.Minute),
		},
		UserContent:         "你好",
		AssistantContent:    "收到",
		RawUserContent:      "你好 raw",
		RawAssistantContent: "收到 raw",
	})
	if err != nil {
		t.Fatalf("execute post-action with failing debug summary: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	assertPersistedTurn(t, store.turn, "你好", "收到", nil)
	if analyzer.calls != 1 {
		t.Fatalf("expected one turn analysis attempt, got %d", analyzer.calls)
	}
	if store.analysisTurn.ID != 0 {
		t.Fatalf("did not expect failed analysis to be persisted, got %+v", store.analysisTurn)
	}
	if !strings.Contains(logBuf.String(), "post-action turn analysis failed") {
		t.Fatalf("expected turn analysis failure to be logged, got %s", logBuf.String())
	}
}

// TestPostActionUseCaseRollsBackVectorRowsWhenDuckDBAnalysisPersistenceFails verifies the vector-first flow deletes freshly written LanceDB rows if DuckDB cannot link them back.
// TestPostActionUseCaseRollsBackVectorRowsWhenDuckDBAnalysisPersistenceFails 用于验证当前“先向量后回写”流程在 DuckDB 无法回链时，会删除刚写入的 LanceDB 向量行。
func TestPostActionUseCaseRollsBackVectorRowsWhenDuckDBAnalysisPersistenceFails(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "clean-user", AssistantReply: "clean-assistant"}}}
	store := &testRelationalStore{
		persistedTurn: logicdomain.PersistedTurnRecord{ID: 808, SessionID: 93, ProjectID: 12, DehydratedBudget: 13},
		analysisErr:   errors.New("duckdb write failed"),
	}
	embedding := &stubEmbeddingClient{response: appports.EmbeddingResponse{Vectors: [][]float32{{0.3, 0.2, 0.1}}}}
	vector := &stubVectorStore{}
	analyzer := &stubPostActionAnalyzer{result: logicdomain.TurnAnalysis{
		Details: "这轮对话产出了一条需要持久化的记忆特征。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "当前对话需要先给出 AI 记忆子项目建议。", Details: "这是当前 turn 的核心任务诉求。"},
		},
	}}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewPostActionUseCase(filter, store, embedding, vector, analyzer, nil, PostActionAnalysisConfig{}, logger)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  93,
			SessionKey: "sess-rollback",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
		},
		UserContent:         "clean-user",
		AssistantContent:    "clean-assistant",
		RawUserContent:      "raw-user",
		RawAssistantContent: "raw-assistant",
	})
	if err != nil {
		t.Fatalf("execute post-action with rollback path: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected 1 vector upsert before rollback, got %d", len(vector.upserts))
	}
	if len(vector.deleteIDsCalls) != 1 {
		t.Fatalf("expected 1 rollback delete-by-ids call, got %d", len(vector.deleteIDsCalls))
	}
	if len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != vector.upserts[0].ID {
		t.Fatalf("expected rollback ids to match inserted vector row, got delete=%v upsert=%s", vector.deleteIDsCalls[0], vector.upserts[0].ID)
	}
	if store.analysisTurn.ID != 808 {
		t.Fatalf("expected relational store to attempt analysis persistence before rollback, got %+v", store.analysisTurn)
	}
	if !strings.Contains(logBuf.String(), "post-action turn analysis persistence failed") {
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

// testRelationalStore is the minimal relational-store stub needed by post-action tests after the move to turn-level persistence.
// testRelationalStore 用于在迁移到 turn 级持久化后，为 post-action 测试提供最小化关系存储桩。
type testRelationalStore struct {
	session        logicdomain.SessionRef
	turn           logicdomain.TurnRecord
	persistedTurn  logicdomain.PersistedTurnRecord
	profileTargets logicdomain.ProfileTargetsSnapshot
	analysisTurn   logicdomain.PersistedTurnRecord
	analysis       logicdomain.TurnAnalysis
	analysisErr    error
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
	return s.persistedTurn, nil
}

// LoadProfileTargets returns the canned durable user/project profile blobs so tests can verify the merge request input.
// LoadProfileTargets 用于返回预设的长期 user/project 画像 Blob，方便测试断言合并请求输入。
func (s *testRelationalStore) LoadProfileTargets(_ context.Context, _ logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	return s.profileTargets, nil
}

// ApplyTurnAnalysis records the extracted turn-analysis payload so tests can verify the post-action chain writes back structured results.
// ApplyTurnAnalysis 用于记录提炼后的 turn 分析载荷，方便测试验证 post-action 链路会回写结构化结果。
func (s *testRelationalStore) ApplyTurnAnalysis(_ context.Context, _ logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) error {
	s.analysisTurn = turn
	s.analysis = analysis
	return s.analysisErr
}

// Shutdown returns immediately because the stub does not own external resources.
// Shutdown 用于立即返回，因为该桩不持有外部资源。
func (s *testRelationalStore) Shutdown(context.Context) error { return nil }

// stubPostActionAnalyzer records debug turn-analysis invocations and returns one canned result or error.
// stubPostActionAnalyzer 用于记录调试逐轮分析调用，并返回预设结果或错误。
type stubPostActionAnalyzer struct {
	calls      int
	transcript string
	result     logicdomain.TurnAnalysis
	err        error
}

// Analyze captures the transcript so tests can assert the trigger path and payload shape.
// Analyze 用于捕获传入 transcript，方便测试断言触发路径和载荷形态。
func (s *stubPostActionAnalyzer) Analyze(_ context.Context, transcript string) (logicdomain.TurnAnalysis, error) {
	s.calls++
	s.transcript = transcript
	if s.err != nil {
		return logicdomain.TurnAnalysis{}, s.err
	}
	return s.result, nil
}

// stubPostActionProfileMerger records batched profile merge calls and returns one canned result or error.
// stubPostActionProfileMerger 用于记录批量画像合并调用，并返回预设结果或错误。
type stubPostActionProfileMerger struct {
	calls    int
	snapshot logicdomain.ProfileTargetsSnapshot
	nodes    []logicdomain.ProfileNodeCandidate
	result   logicdomain.TurnProfileMergeResult
	err      error
}

// Merge captures the full batch input so tests can assert the use case sends user/project nodes together.
// Merge 用于捕获完整批量输入，方便测试断言用例会把 user/project 节点一起送进合并器。
func (s *stubPostActionProfileMerger) Merge(_ context.Context, snapshot logicdomain.ProfileTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (logicdomain.TurnProfileMergeResult, error) {
	s.calls++
	s.snapshot = snapshot
	s.nodes = append([]logicdomain.ProfileNodeCandidate(nil), nodes...)
	if s.err != nil {
		return logicdomain.TurnProfileMergeResult{}, s.err
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

// DeleteByIDs records the rollback vector ids so tests can assert that DuckDB persistence failures clean up the just-inserted rows.
// DeleteByIDs 用于记录回滚时的向量 id，方便测试断言 DuckDB 持久化失败后会清理刚插入的向量行。
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
