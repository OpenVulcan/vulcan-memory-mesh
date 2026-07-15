// normalize.go centralizes storage-adapter input normalization shared by SQLite and PostgreSQL implementations.
// normalize.go 用于集中管理 SQLite 与 PostgreSQL 存储适配器共享的输入规范化逻辑。
package storageutil

import (
	"sort"
	"strconv"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// NormalizeUint64List removes zero and duplicate identifiers, then sorts the result for deterministic storage operations.
// NormalizeUint64List 用于移除零值与重复标识，并排序结果以保证存储操作的确定性。
func NormalizeUint64List(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := map[uint64]struct{}{}
	normalized := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left] < normalized[right]
	})
	return normalized
}

// NormalizeStringList trims blank values, removes duplicates, and sorts the result for deterministic storage operations.
// NormalizeStringList 用于裁剪空白值、移除重复项并排序结果，以保证存储操作的确定性。
func NormalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

// ParsePositiveUint64 parses one trimmed decimal identifier and rejects zero because persisted identifiers are strictly positive.
// ParsePositiveUint64 用于解析经过裁剪的十进制标识，并拒绝零值，因为持久化标识必须为正数。
func ParsePositiveUint64(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
}

// ParseProjectPath validates and splits the canonical TeamName/SpaceName/ProjectName storage reference.
// ParseProjectPath 用于校验并拆分标准的 TeamName/SpaceName/ProjectName 存储引用。
func ParseProjectPath(path string) (string, string, string, error) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	if len(parts) != 3 {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	teamName := strings.TrimSpace(parts[0])
	spaceName := strings.TrimSpace(parts[1])
	projectName := strings.TrimSpace(parts[2])
	if teamName == "" || spaceName == "" || projectName == "" {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	return teamName, spaceName, projectName, nil
}
