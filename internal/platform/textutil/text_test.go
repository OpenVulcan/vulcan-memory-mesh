// text_test.go implements shared platform helper tests.
// text_test.go 用于实现共享平台辅助能力测试。
package textutil

import "testing"

// TestCleanConversationTextFiltersFlattenedMedia verifies the TestCleanConversationTextFiltersFlattenedMedia behavior.
// TestCleanConversationTextFiltersFlattenedMedia 用于验证 TestCleanConversationTextFiltersFlattenedMedia 行为。
func TestCleanConversationTextFiltersFlattenedMedia(t *testing.T) {
	input := "请看 ![猫](https://cdn.example.com/cat.jpg) ![](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA) [logs](https://cdn.example.com/logs.zip) <video src=\"https://cdn.example.com/demo.mp4\"></video>"
	got := CleanConversationText(input)
	want := "请看 [图片: 猫] [图片已过滤] [压缩包: logs] [视频: demo.mp4]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestCleanConversationTextKeepsRegularLinks verifies the TestCleanConversationTextKeepsRegularLinks behavior.
// TestCleanConversationTextKeepsRegularLinks 用于验证 TestCleanConversationTextKeepsRegularLinks 行为。
func TestCleanConversationTextKeepsRegularLinks(t *testing.T) {
	input := "文档地址是 [OpenAI](https://platform.openai.com/docs)"
	got := CleanConversationText(input)
	if got != input {
		t.Fatalf("got %q want %q", got, input)
	}
}

// TestCleanConversationTextStripsThoughtAndRawResourceURL verifies the TestCleanConversationTextStripsThoughtAndRawResourceURL behavior.
// TestCleanConversationTextStripsThoughtAndRawResourceURL 用于验证 TestCleanConversationTextStripsThoughtAndRawResourceURL 行为。
func TestCleanConversationTextStripsThoughtAndRawResourceURL(t *testing.T) {
	input := "<think>hidden</think> 下载地址 https://cdn.example.com/assets/archive.7z"
	got := CleanConversationText(input)
	want := "下载地址 [压缩包: archive.7z]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
