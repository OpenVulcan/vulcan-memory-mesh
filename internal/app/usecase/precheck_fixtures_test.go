// Behavioral tests grouped by precheck fixtures test.
// 应用用例层 pre-check 共享夹具行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"time"
)

// stubPreCheckMemories is the memory search double used by pre-check tests.
// stubPreCheckMemories 用于作为 pre-check 测试里的记忆检索桩。
type stubPreCheckMemories struct {
	cmd         MemoryQueryCommand
	searchCalls int
	result      MemoryQueryResult
	err         error
}

// Search records the memory query command and returns the configured recall outcome.
// Search 用于记录记忆查询命令并返回预设的召回结果。
func (s *stubPreCheckMemories) Search(_ context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	s.searchCalls++
	s.cmd = cmd
	return s.result, s.err
}

// GetTurns keeps the stub interface-complete for tests that only exercise Search.
// GetTurns 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) GetTurns(context.Context, TurnDetailCommand) (TurnDetailResult, error) {
	return TurnDetailResult{}, nil
}

// GetDetails keeps the stub interface-complete for tests that only exercise Search.
// GetDetails 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) GetDetails(context.Context, MemoryDetailCommand) (MemoryDetailResult, error) {
	return MemoryDetailResult{}, nil
}

// Write keeps the stub interface-complete for tests that only exercise Search.
// Write 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) Write(context.Context, WriteMemoriesCommand) (WriteMemoriesResult, error) {
	return WriteMemoriesResult{}, nil
}

// Delete keeps the stub interface-complete for tests that only exercise Search.
// Delete 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) Delete(context.Context, DeleteMemoriesCommand) (DeleteMemoriesResult, error) {
	return DeleteMemoriesResult{}, nil
}

// stubPreCheckStore is the relational store double used by pre-check tests.
// stubPreCheckStore 用于作为 pre-check 测试里的关系存储桩。
type stubPreCheckStore struct {
	recentTurns    []logicdomain.SessionTurnRecord
	recentTurnsErr error
	adoptedSession logicdomain.SessionRef
	adoptedIDs     []uint64
	adoptedAt      time.Time
	adoptedRecords []logicdomain.MemoryRecord
	adoptionErr    error
	adoptionCalls  int
}

// LoadRecentSessionTurns returns a copy of the configured recent-turn window and error.
// LoadRecentSessionTurns 用于返回预设最近轮次窗口及错误的副本。
func (s *stubPreCheckStore) LoadRecentSessionTurns(context.Context, logicdomain.SessionRef, int) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.recentTurns...), s.recentTurnsErr
}

// ApplyMemoryAdoption captures lifecycle write-back arguments and returns configured adopted records.
// ApplyMemoryAdoption 用于捕获生命周期回写参数并返回预设的已采纳记录。
func (s *stubPreCheckStore) ApplyMemoryAdoption(_ context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) ([]logicdomain.MemoryRecord, error) {
	s.adoptionCalls++
	s.adoptedSession = session
	s.adoptedIDs = append([]uint64(nil), memoryIDs...)
	s.adoptedAt = adoptedAt
	return append([]logicdomain.MemoryRecord(nil), s.adoptedRecords...), s.adoptionErr
}

// stubPreCheckLifecycleVectorStore captures vector lifecycle upserts emitted after relational memory adoption.
// stubPreCheckLifecycleVectorStore 用于记录关系侧记忆采纳后触发的向量生命周期 upsert。
type stubPreCheckLifecycleVectorStore struct {
	records []logicdomain.MemoryRecord
	err     error
}

// Upsert records successful lifecycle vector writes and returns configured failures before mutation.
// Upsert 用于记录成功的生命周期向量写入，并在预设失败时于变更前返回。
func (s *stubPreCheckLifecycleVectorStore) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, record)
	return nil
}

// stubPreCheckIntentExtractor is the first-stage extractor double used by pre-check tests.
// stubPreCheckIntentExtractor 用于作为 pre-check 测试里的第一层意图提取桩。
type stubPreCheckIntentExtractor struct {
	turns   []logicdomain.PreCheckTurnContext
	current string
	result  logicdomain.IntentResult
	err     error
	model   string
}

// Extract captures recent-turn inputs and returns the configured first-stage intent outcome.
// Extract 用于捕获最近轮次输入并返回预设的第一层意图结果。
func (s *stubPreCheckIntentExtractor) Extract(_ context.Context, turns []logicdomain.PreCheckTurnContext, current string) (logicdomain.IntentResult, error) {
	s.turns = append([]logicdomain.PreCheckTurnContext(nil), turns...)
	s.current = current
	return s.result, s.err
}

// ExtractModel returns the configured stub intent model label so degraded invalid-output tests can assert first-stage model attribution.
// ExtractModel 用于返回桩对象配置的意图模型标识，方便降级畸形输出测试断言第一层模型归因。
func (s *stubPreCheckIntentExtractor) ExtractModel() string {
	if s == nil {
		return ""
	}
	return s.model
}

// stubPreCheckReviewer is the second-stage reviewer double used by pre-check tests.
// stubPreCheckReviewer 用于作为 pre-check 测试里的第二层评审桩。
type stubPreCheckReviewer struct {
	input  logicdomain.PreCheckMemoryReviewInput
	result logicdomain.PreCheckMemoryReviewResult
	err    error
	model  string
}

// Review captures the reviewer input and returns the configured second-stage selection outcome.
// Review 用于捕获评审输入并返回预设的第二层筛选结果。
func (s *stubPreCheckReviewer) Review(_ context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	s.input = input
	return s.result, s.err
}

// ReviewModel returns the configured stub reviewer model label so degraded invalid-output tests can assert second-stage model attribution.
// ReviewModel 用于返回桩对象配置的 reviewer 模型标识，方便降级畸形输出测试断言第二层模型归因。
func (s *stubPreCheckReviewer) ReviewModel() string {
	if s == nil {
		return ""
	}
	return s.model
}

// stubPreCheckAssembler is the context assembler double used by pre-check tests.
// stubPreCheckAssembler 用于作为 pre-check 测试里的上下文组装桩。
type stubPreCheckAssembler struct {
	called  bool
	persona logicdomain.PersonaContext
	hits    []logicdomain.MemoryHit
	text    string
	items   []logicdomain.ContextItem
	err     error
}

// Assemble records persona and memory inputs before returning configured context text and item copies.
// Assemble 用于记录画像与记忆输入，并返回预设上下文文本及条目副本。
func (s *stubPreCheckAssembler) Assemble(_ context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	s.called = true
	s.persona = persona
	s.hits = append([]logicdomain.MemoryHit(nil), hits...)
	return s.text, append([]logicdomain.ContextItem(nil), s.items...), s.err
}
