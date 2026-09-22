// library_cache.go keeps legacy LanceDB FFI libraries resident for the process lifetime.
// library_cache.go 负责让旧版 LanceDB FFI 动态库在进程生命周期内保持驻留。
package lancedbffi

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// legacyLibraryCache serializes legacy-library loading and deduplicates one OS handle per canonical path.
// legacyLibraryCache 串行化旧库加载，并按规范路径去重每个操作系统句柄。
var legacyLibraryCache = struct {
	sync.Mutex
	handles map[string]uintptr
}{
	handles: make(map[string]uintptr),
}

// canonicalLibraryPath normalizes one library path for process-local load deduplication.
// canonicalLibraryPath 规范化动态库路径，用于进程内去重加载。
func canonicalLibraryPath(path string) string {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = filepath.Clean(absPath)
	} else {
		path = filepath.Clean(path)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
