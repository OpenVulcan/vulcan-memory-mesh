package xid

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

type Generator struct{}

func NewGenerator() Generator { return Generator{} }

func (Generator) NewID(prefix string) string {
	if strings.TrimSpace(prefix) == "" {
		prefix = "id"
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(b[:])
}
