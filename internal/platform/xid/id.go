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

// NewGenerator creates a Generator instance.
// NewGenerator 用于创建 Generator 实例。
func NewGenerator() Generator { return Generator{} }

// NewID creates a ID instance.
// NewID 用于创建 ID 实例。
func (Generator) NewID(prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		prefix = "id"
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(b[:])
}
