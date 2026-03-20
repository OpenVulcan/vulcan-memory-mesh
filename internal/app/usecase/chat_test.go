// chat_test.go implements the local chat archive use case tests.
// chat_test.go 用于实现本地聊天归档用例测试。
package usecase

import (
	"context"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// fakeScrubber is a test double that returns predictable scrubbed text for chat use case tests.
// fakeScrubber 用于作为聊天用例测试中的脱敏器替身，返回可预测的脱敏文本。
type fakeScrubber struct {
	lastText string
	lastLang string
	out      string
}

// Scrub executes the Scrub logic.
// Scrub 用于执行 Scrub 逻辑。
func (f *fakeScrubber) Scrub(text string, lang string) string {
	f.lastText = text
	f.lastLang = lang
	return f.out
}

// fakeArchiveStore is a test double that records the scrubbed archive writes emitted by the chat use case.
// fakeArchiveStore 用于作为聊天用例测试中的存储替身，记录脱敏归档写入。
type fakeArchiveStore struct {
	record logicdomain.ArchivedMemory
	called int
}

// SaveMemory executes the SaveMemory logic.
// SaveMemory 用于执行 SaveMemory 逻辑。
func (f *fakeArchiveStore) SaveMemory(ctx context.Context, record logicdomain.ArchivedMemory) error {
	f.called++
	f.record = record
	return nil
}

// Shutdown executes the Shutdown logic.
// Shutdown 用于执行 Shutdown 逻辑。
func (f *fakeArchiveStore) Shutdown(ctx context.Context) error { return nil }

// fakeIDGenerator is a deterministic id generator used by chat use case tests.
// fakeIDGenerator 用于作为聊天用例测试中的确定性 ID 生成器。
type fakeIDGenerator struct{}

// NewID executes the NewID logic.
// NewID 用于执行 NewID 逻辑。
func (fakeIDGenerator) NewID(prefix string) string { return prefix + "_fixed" }

var _ appports.TextScrubber = (*fakeScrubber)(nil)
var _ appports.MemoryArchiveStore = (*fakeArchiveStore)(nil)
var _ appports.IDGenerator = fakeIDGenerator{}

// TestChatUseCaseScrubsAndStores verifies the TestChatUseCaseScrubsAndStores behavior.
// TestChatUseCaseScrubsAndStores 用于验证 TestChatUseCaseScrubsAndStores 行为。
func TestChatUseCaseScrubsAndStores(t *testing.T) {
	scrubber := &fakeScrubber{out: "你好，我的电话是 [MOBILE_MASKED]"}
	store := &fakeArchiveStore{}
	uc := NewChatUseCase(scrubber, store, fakeIDGenerator{}, nil)

	res, err := uc.Execute(context.Background(), ChatCommand{
		SessionID: "sess_001",
		Message:   "你好，我的电话是 13800138000",
		Language:  "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message != "你好，我的电话是 [MOBILE_MASKED]" {
		t.Fatalf("scrubbed message = %q", res.Message)
	}
	if scrubber.lastLang != "zh-CN" {
		t.Fatalf("scrubber language = %q", scrubber.lastLang)
	}
	if store.called != 1 {
		t.Fatalf("store called %d times", store.called)
	}
	if store.record.Content != "你好，我的电话是 [MOBILE_MASKED]" {
		t.Fatalf("stored content = %q", store.record.Content)
	}
}
