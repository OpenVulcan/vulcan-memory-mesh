// context_item.go centralizes caller-facing context-item helpers so pre-check assembly can expose tool-callable turn references without leaking storage-only ids.
// context_item.go 用于集中管理面向调用方的上下文项辅助逻辑，让 pre-check 组装层能暴露可供工具调用的 turn 引用，同时避免泄露仅供存储使用的内部 id。
package domain

import (
	"strconv"
	"strings"
)

// MemoryHitTurnID resolves the source turn id carried by one recalled memory hit so downstream assembly can expose tool-callable references without leaking storage-only ids.
// MemoryHitTurnID 用于解析一条召回记忆命中携带的来源 turn id，让下游组装层能暴露可供工具调用的引用，而不泄露仅供存储使用的内部 id。
func MemoryHitTurnID(hit MemoryHit) uint64 {
	raw := strings.TrimSpace(hit.Metadata["turn_id"])
	if raw == "" {
		return 0
	}
	turnID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return turnID
}
