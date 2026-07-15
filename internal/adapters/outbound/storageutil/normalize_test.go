// normalize_test.go characterizes the shared normalization contract used by relational storage adapters.
// normalize_test.go 用于刻画关系型存储适配器共同遵循的规范化契约。
package storageutil

import (
	"errors"
	"reflect"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNormalizeListsFiltersDeduplicatesAndSorts verifies deterministic identifier normalization for storage queries.
// TestNormalizeListsFiltersDeduplicatesAndSorts 用于验证存储查询标识会被确定性地过滤、去重和排序。
func TestNormalizeListsFiltersDeduplicatesAndSorts(t *testing.T) {
	if got, want := NormalizeUint64List([]uint64{3, 0, 1, 3}), []uint64{1, 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeUint64List() = %#v, want %#v", got, want)
	}
	if got, want := NormalizeStringList([]string{" b ", "", "a", "b"}), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeStringList() = %#v, want %#v", got, want)
	}
}

// TestParsePositiveUint64RejectsInvalidIdentifiers verifies the persisted-identifier boundary excludes zero and malformed values.
// TestParsePositiveUint64RejectsInvalidIdentifiers 用于验证持久化标识边界会拒绝零值和非法格式。
func TestParsePositiveUint64RejectsInvalidIdentifiers(t *testing.T) {
	if got, ok := ParsePositiveUint64(" 42 "); !ok || got != 42 {
		t.Fatalf("ParsePositiveUint64() = (%d, %t), want (42, true)", got, ok)
	}
	for _, raw := range []string{"", "0", "-1", "invalid"} {
		if _, ok := ParsePositiveUint64(raw); ok {
			t.Fatalf("ParsePositiveUint64(%q) unexpectedly succeeded", raw)
		}
	}
}

// TestParseProjectPathValidatesCanonicalShape verifies that both storage engines resolve project references identically.
// TestParseProjectPathValidatesCanonicalShape 用于验证两种存储引擎会以相同方式解析项目引用。
func TestParseProjectPathValidatesCanonicalShape(t *testing.T) {
	team, space, project, err := ParseProjectPath(" team / space / project ")
	if err != nil || team != "team" || space != "space" || project != "project" {
		t.Fatalf("ParseProjectPath() = (%q, %q, %q, %v)", team, space, project, err)
	}
	_, _, _, err = ParseProjectPath("team/project")
	if !errors.Is(err, logicdomain.ErrValidation) {
		t.Fatalf("ParseProjectPath() error = %v, want validation error", err)
	}
}

// TestProjectCreateConfirmationMessageListsMissingHierarchy verifies both storage engines expose the same confirmation text.
// TestProjectCreateConfirmationMessageListsMissingHierarchy 用于验证两种存储引擎会暴露一致的层级缺失确认文案。
func TestProjectCreateConfirmationMessageListsMissingHierarchy(t *testing.T) {
	got := ProjectCreateConfirmationMessage("team", "space", "project", false, false)
	want := "path team/space/project is incomplete; missing team, space, use confirm_create=1 to create them"
	if got != want {
		t.Fatalf("ProjectCreateConfirmationMessage() = %q, want %q", got, want)
	}
}
