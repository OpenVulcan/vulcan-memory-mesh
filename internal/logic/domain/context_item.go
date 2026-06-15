// context_item.go centralizes caller-facing context-item helpers so pre-check assembly can expose tool-callable memory and turn references consistently.
// context_item.go 用于集中管理面向调用方的上下文项辅助逻辑，让 pre-check 组装层稳定暴露可供工具调用的 memory 与 turn 引用。
package domain

import (
	"strconv"
	"strings"
	"time"
)

// MemoryHitMemoryID resolves the durable memory id carried by one recalled memory hit so returned context items can be passed back into memory-management RPCs.
// MemoryHitMemoryID 用于解析一条召回命中携带的长期记忆 id，让返回的上下文项可继续传入记忆管理 RPC。
func MemoryHitMemoryID(hit MemoryHit) uint64 {
	raw := strings.TrimSpace(hit.ID)
	if raw == "" {
		return 0
	}
	memoryID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return memoryID
}

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

// MemoryHitCreatedTimestamp resolves the durable creation time carried by one recalled memory hit into a millisecond timestamp suitable for caller-facing context payloads.
// MemoryHitCreatedTimestamp 用于把一条召回记忆命中携带的长期创建时间解析成毫秒时间戳，供面向调用方的上下文载荷直接使用。
func MemoryHitCreatedTimestamp(hit MemoryHit) int64 {
	if hit.CreatedAt.IsZero() {
		return 0
	}
	return hit.CreatedAt.UTC().UnixMilli()
}

// MemoryHitCreatedDateTime resolves the durable creation time carried by one recalled memory hit into the shared caller-facing local datetime string.
// MemoryHitCreatedDateTime 用于把一条召回记忆命中的长期创建时间解析成统一的本地 datetime 字符串，供面向调用方的载荷直接使用。
func MemoryHitCreatedDateTime(hit MemoryHit) string {
	return FormatDisplayDateTime(hit.CreatedAt)
}

// MemoryHitCreatedDate resolves the durable creation time carried by one recalled memory hit into the shared caller-facing local date string.
// MemoryHitCreatedDate 用于把一条召回记忆命中的长期创建时间解析成统一的本地日期字符串，供面向调用方的辅助推理使用。
func MemoryHitCreatedDate(hit MemoryHit) string {
	return FormatDisplayDate(hit.CreatedAt)
}

// MemoryHitCreatedAtFromUnixMillis restores one millisecond timestamp back into time.Time so callers can rebuild stable memory-hit timestamps during fallback assembly.
// MemoryHitCreatedAtFromUnixMillis 用于把毫秒时间戳恢复成 time.Time，方便调用方在回退组装阶段重建稳定的记忆创建时间。
func MemoryHitCreatedAtFromUnixMillis(timestamp int64) time.Time {
	if timestamp <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(timestamp)
}
