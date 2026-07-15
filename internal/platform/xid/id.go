// id.go implements shared platform helpers.
// id.go 用于实现共享平台辅助能力。
package xid

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// Generator produces lightweight prefixed identifiers for traces, memories, and other local runtime entities.
// Generator 用于为 trace、记忆记录和其他本地运行时实体生成轻量前缀 ID。
type Generator struct{}

// NewGenerator returns the stateless identifier generator used by inbound request and persistence flows.
// NewGenerator 用于返回入站请求与持久化链路使用的无状态标识生成器。
func NewGenerator() Generator { return Generator{} }

// NewID combines the caller's prefix, current UTC milliseconds, and cryptographic randomness into a sortable identifier.
// NewID 用于把调用方前缀、当前 UTC 毫秒时间与密码学随机数组合成可排序标识。
func (Generator) NewID(prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		prefix = "id"
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(b[:])
}
