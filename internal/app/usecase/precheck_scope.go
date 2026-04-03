// precheck_scope.go centralizes the pre-check-only search-scope policy so the live pre-check workflow can widen or narrow memory recall without changing generic memory-query defaults.
// precheck_scope.go 用于集中管理 pre-check 专用的检索作用域策略，让实时 pre-check 可以独立调节记忆召回边界，而不改变通用 memory-query 的默认行为。
package usecase

import (
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// preCheckSearchScopeTeam keeps pre-check recall at the whole-team shared layer so one query can reuse memories across every space and project inside the same team.
	// preCheckSearchScopeTeam 用于把 pre-check 召回限定在整个 team 共享层，让一次查询可以复用同一 team 下所有 space 与 project 的记忆。
	preCheckSearchScopeTeam = "team"

	// preCheckSearchScopeSpace keeps pre-check recall inside the current space so sibling projects can share memory while unrelated spaces stay isolated.
	// preCheckSearchScopeSpace 用于把 pre-check 召回限定在当前 space 内，让同空间下的兄弟项目共享记忆，同时隔离无关空间。
	preCheckSearchScopeSpace = "space"

	// preCheckSearchScopeProject keeps pre-check recall limited to the current project when callers need the narrowest isolation.
	// preCheckSearchScopeProject 用于把 pre-check 召回限定在当前 project 内，供需要最强隔离的调用方使用。
	preCheckSearchScopeProject = "project"
)

// normalizePreCheckSearchScope applies the pre-check scope default and canonical spelling so config, app wiring, and recall filtering all interpret the same values.
// normalizePreCheckSearchScope 用于对 pre-check 作用域做默认值和规范拼写归一，保证配置、应用装配和召回过滤始终按同一套取值解释。
func normalizePreCheckSearchScope(scope string) string {
	normalized := normalizeConfigToken(scope)
	switch normalized {
	case preCheckSearchScopeTeam:
		return preCheckSearchScopeTeam
	case preCheckSearchScopeProject:
		return preCheckSearchScopeProject
	case preCheckSearchScopeSpace:
		return preCheckSearchScopeSpace
	case "":
		return preCheckSearchScopeSpace
	default:
		return normalized
	}
}

// isSupportedPreCheckSearchScope reports whether the caller provided one of the explicit supported scope values before defaults are applied.
// isSupportedPreCheckSearchScope 用于判断调用方在默认值生效前，是否提供了受支持的显式作用域取值。
func isSupportedPreCheckSearchScope(scope string) bool {
	switch normalizeConfigToken(scope) {
	case preCheckSearchScopeTeam, preCheckSearchScopeSpace, preCheckSearchScopeProject:
		return true
	default:
		return false
	}
}

// buildScopedMemorySearchFilter derives the effective vector and lexical filter from the resolved user/project hierarchy plus one optional pre-check scope override.
// buildScopedMemorySearchFilter 用于根据已解析的 user/project 层级以及可选 pre-check 作用域覆盖，生成实际的向量与 lexical 过滤条件。
func buildScopedMemorySearchFilter(userTarget, projectTarget logicdomain.ProfileTargetRef, scope string) logicdomain.SearchFilter {
	filter := logicdomain.SearchFilter{
		UserID:    userTarget.UserID,
		TeamID:    projectTarget.TeamID,
		SpaceID:   projectTarget.SpaceID,
		ProjectID: projectTarget.ProjectID,
	}
	switch normalizeConfigToken(scope) {
	case preCheckSearchScopeTeam:
		filter.SpaceID = 0
		filter.ProjectID = 0
	case preCheckSearchScopeSpace:
		filter.ProjectID = 0
	}
	return filter
}

// normalizeConfigToken trims and lowercases lightweight enum-like config strings so they can be compared consistently across runtime layers.
// normalizeConfigToken 用于对轻量枚举型配置字符串执行裁剪和小写化，让各运行时层都能稳定比较。
func normalizeConfigToken(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(raw))
}
