// path.go supplies canonical temporary roots for cross-platform filesystem security tests.
// path.go 为跨平台文件系统安全测试提供规范化的临时根目录。
package testutil

import (
	"path/filepath"
	"testing"
)

// CanonicalTempDir resolves OS-provided temporary directory aliases before security-sensitive test fixtures use them.
// CanonicalTempDir 在安全敏感测试夹具使用系统临时目录前解析其路径别名。
func CanonicalTempDir(t testing.TB) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return resolved
}
