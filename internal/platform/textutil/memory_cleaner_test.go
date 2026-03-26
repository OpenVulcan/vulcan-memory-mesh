// memory_cleaner_test.go verifies machine-text compression behavior used by the post-action storage sanitizer.
// memory_cleaner_test.go 用于验证 post-action 存储清洗器使用的机器文本压缩行为。
package textutil

import (
	"strings"
	"testing"
)

// TestCleanMemoryTextFoldsLongMachineBlocks verifies that long structured machine text is folded instead of stored verbatim.
// TestCleanMemoryTextFoldsLongMachineBlocks 用于验证长结构化机器文本会被折叠，而不是原样存储。
func TestCleanMemoryTextFoldsLongMachineBlocks(t *testing.T) {
	input := strings.Join([]string{
		`{"level":"info","msg":"boot","trace":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"cccccccccccccccccccccccccccccccc","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"dddddddddddddddddddddddddddddddd","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"ffffffffffffffffffffffffffffffff","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"gggggggggggggggggggggggggggggggg","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"iiiiiiiiiiiiiiiiiiiiiiiiiiiiiiii","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"jjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjj","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"llllllllllllllllllllllllllllllll","module":"sync"}`,
		`{"level":"info","msg":"sync","trace":"mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm","module":"sync"}`,
		`{"level":"info","msg":"finish","trace":"nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn","module":"sync"}`,
	}, "\n")

	got := CleanMemoryText(input)
	if !strings.Contains(got, "已按策略折叠") {
		t.Fatalf("expected folded marker, got %q", got)
	}
}
