// memory_replace_scope.go centralizes the memory-replacement scope policy so post-action can widen or narrow supersede decisions without coupling that behavior to pre-check recall scope.
// memory_replace_scope.go 用于集中管理记忆更替作用域策略，让 post-action 可以独立调节 supersede 决策边界，而不与 pre-check 召回作用域耦合。
package usecase

import logicdomain "github.com/openvulcan/vmm/internal/logic/domain"

const (
	// memoryReplaceScopeSession keeps replacement decisions inside the current session so only facts from the same origin session can be replaced.
	// memoryReplaceScopeSession 用于把记忆更替限定在当前 session 内，确保只替换同来源 session 的事实。
	memoryReplaceScopeSession = "session"

	// memoryReplaceScopeProject keeps replacement decisions inside the current project so sibling sessions can converge on fresher shared facts.
	// memoryReplaceScopeProject 用于把记忆更替限定在当前 project 内，让兄弟 session 可以收敛到更新的共享事实。
	memoryReplaceScopeProject = "project"

	// memoryReplaceScopeSpace keeps replacement decisions inside the current space so related projects can share one updated fact surface.
	// memoryReplaceScopeSpace 用于把记忆更替限定在当前 space 内，让相关项目共享同一套更新后的事实表面。
	memoryReplaceScopeSpace = "space"

	// memoryReplaceScopeTeam keeps replacement decisions at the whole-team layer when one stable rule must supersede older copies across every space and project.
	// memoryReplaceScopeTeam 用于把记忆更替扩大到整个 team 层，让一条稳定规则可以替换各个 space 和 project 中较旧的副本。
	memoryReplaceScopeTeam = "team"
)

// normalizeMemoryReplaceScope applies the dedicated replacement-scope default and canonical spelling so config, app wiring, and reviewer-side dedupe all interpret the same values.
// normalizeMemoryReplaceScope 用于对专用记忆更替作用域做默认值和规范拼写归一，保证配置、应用装配和 reviewer 判重始终按同一套取值解释。
func normalizeMemoryReplaceScope(scope string) string {
	switch normalizeConfigToken(scope) {
	case memoryReplaceScopeSession:
		return memoryReplaceScopeSession
	case memoryReplaceScopeSpace:
		return memoryReplaceScopeSpace
	case memoryReplaceScopeTeam:
		return memoryReplaceScopeTeam
	case memoryReplaceScopeProject, "":
		return memoryReplaceScopeProject
	default:
		return normalizeConfigToken(scope)
	}
}

// isSupportedMemoryReplaceScope reports whether one caller-provided config token belongs to the explicit supported replacement-scope enum before defaults are applied.
// isSupportedMemoryReplaceScope 用于判断调用方提供的配置 token 在默认值介入前，是否属于受支持的显式记忆更替作用域。
func isSupportedMemoryReplaceScope(scope string) bool {
	switch normalizeConfigToken(scope) {
	case memoryReplaceScopeSession, memoryReplaceScopeProject, memoryReplaceScopeSpace, memoryReplaceScopeTeam:
		return true
	default:
		return false
	}
}

// isSupportedMemoryQueryScope reports whether one internal memory-query scope override belongs to the supported enum shared by pre-check and post-action replace search.
// isSupportedMemoryQueryScope 用于判断内部记忆查询作用域覆盖是否属于 pre-check 与 post-action replace search 共用的受支持枚举。
func isSupportedMemoryQueryScope(scope string) bool {
	switch normalizeConfigToken(scope) {
	case memoryReplaceScopeSession, memoryReplaceScopeProject, memoryReplaceScopeSpace, memoryReplaceScopeTeam:
		return true
	default:
		return false
	}
}

// buildMemoryReplaceSearchFilter derives the effective replacement-search filter from the resolved hierarchy plus the dedicated memory-replace scope override.
// buildMemoryReplaceSearchFilter 用于根据已解析层级和专用记忆更替作用域覆盖，推导实际的更替检索过滤条件。
func buildMemoryReplaceSearchFilter(session logicdomain.SessionRef, scope string) logicdomain.SearchFilter {
	filter := buildScopedMemorySearchFilter(
		logicdomain.ProfileTargetRef{UserID: session.UserID},
		logicdomain.ProfileTargetRef{TeamID: session.TeamID, SpaceID: session.SpaceID, ProjectID: session.ProjectID},
		scope,
	)
	if normalizeConfigToken(scope) == memoryReplaceScopeSession && session.SessionID > 0 {
		filter.SessionID = session.SessionID
	}
	return filter
}
