// local_storage_layout.go resolves the packaged libs/database layout used by local FFI split storage.
// local_storage_layout.go 用于解析本地 FFI split 存储使用的打包 libs/database 布局。
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/openvulcan/vmm/internal/config"
)

var (
	workspaceStorageRootOnce sync.Once
	workspaceStorageRoot     string
	workspaceStorageRootErr  error
)

// localStorageLayout stores the packaged local FFI paths derived from the executable layout.
// localStorageLayout 用于保存根据可执行文件布局推导出的本地 FFI 路径集合。
type localStorageLayout struct {
	OutputRoot       string
	ArtifactRoot     string
	LibsDir          string
	DatabaseDir      string
	SQLiteLibrary    string
	LanceDBLibrary   string
	SQLiteDatabase   string
	LanceDBDirectory string
}

// ResolveLocalStorageLayout resolves the packaged libs/database layout and ensures the database directories exist before FFI stores boot.
// ResolveLocalStorageLayout 用于解析打包后的 libs/database 布局，并在 FFI 存储启动前确保数据库目录已经存在。
func ResolveLocalStorageLayout() (localStorageLayout, error) {
	return resolveLocalStorageLayoutForPromptLayout(config.PromptLayout{})
}

// ResolveLocalStorageLayoutForPromptLayout resolves the local FFI storage layout from one already-resolved prompt layout so maintenance tools can share the same packaged-root inference as the runtime.
// ResolveLocalStorageLayoutForPromptLayout 用于基于已解析的提示词布局解析本地 FFI 存储布局，让维护工具也能复用与运行时一致的打包根目录推导规则。
func ResolveLocalStorageLayoutForPromptLayout(promptLayout config.PromptLayout) (localStorageLayout, error) {
	return resolveLocalStorageLayoutForPromptLayout(promptLayout)
}

// resolveLocalStorageLayoutForPromptLayout resolves the storage layout from one already-resolved prompt layout when the caller needs test-specific packaged roots instead of the current process executable.
// resolveLocalStorageLayoutForPromptLayout 用于在调用方已经拿到提示词布局时解析存储布局，这样测试就可以显式指定打包根目录，而不是被当前进程的可执行文件位置绑死。
func resolveLocalStorageLayoutForPromptLayout(promptLayout config.PromptLayout) (localStorageLayout, error) {
	actualLayout, err := resolveCurrentPromptLayout()
	if err != nil {
		return localStorageLayout{}, err
	}
	effectiveSystemDir := strings.TrimSpace(promptLayout.SystemDir)
	if effectiveSystemDir == "" {
		effectiveSystemDir = actualLayout.SystemDir
	}
	if strings.TrimSpace(effectiveSystemDir) == "" {
		return localStorageLayout{}, fmt.Errorf("resolve local storage layout: system config dir is empty")
	}
	artifactRoot := filepath.Clean(filepath.Join(effectiveSystemDir, ".."))
	outputRoot := artifactRoot
	if !isPackagedSystemDir(effectiveSystemDir) {
		outputRoot, err = resolveWorkspaceStorageRoot()
		if err != nil {
			return localStorageLayout{}, err
		}
	}
	libsDir := filepath.Join(artifactRoot, "libs")
	databaseDir := filepath.Join(outputRoot, "database")
	sqliteDatabase := filepath.Join(databaseDir, "sqlite.db")
	lanceDBDirectory := filepath.Join(databaseDir, "lancedb")

	if err := os.MkdirAll(lanceDBDirectory, 0o755); err != nil {
		return localStorageLayout{}, fmt.Errorf("create packaged lancedb database dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sqliteDatabase), 0o755); err != nil {
		return localStorageLayout{}, fmt.Errorf("create packaged sqlite database dir: %w", err)
	}

	sqliteLibraryName, lanceDBLibraryName := resolveHostLibraryNames()
	actualArtifactRoot := filepath.Clean(filepath.Join(actualLayout.SystemDir, ".."))
	sqliteLibraryPath := resolveHostLibraryPath([]string{artifactRoot, actualArtifactRoot}, sqliteLibraryName)
	lanceDBLibraryPath := resolveHostLibraryPath([]string{artifactRoot, actualArtifactRoot}, lanceDBLibraryName)

	return localStorageLayout{
		OutputRoot:       outputRoot,
		ArtifactRoot:     artifactRoot,
		LibsDir:          libsDir,
		DatabaseDir:      databaseDir,
		SQLiteLibrary:    sqliteLibraryPath,
		LanceDBLibrary:   lanceDBLibraryPath,
		SQLiteDatabase:   sqliteDatabase,
		LanceDBDirectory: lanceDBDirectory,
	}, nil
}

// resolveCurrentPromptLayout resolves the active process prompt layout so storage fallback decisions can stay aligned with the same executable/cwd heuristics used by the config loader.
// resolveCurrentPromptLayout 用于解析当前进程的提示词布局，让存储路径的兜底策略与配置加载器对可执行文件和工作目录的判断保持一致。
func resolveCurrentPromptLayout() (config.PromptLayout, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return config.PromptLayout{}, fmt.Errorf("resolve executable path: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return config.PromptLayout{}, fmt.Errorf("resolve working directory: %w", err)
	}
	layout, err := config.ResolvePromptLayout(executablePath, cwd, "")
	if err != nil {
		return config.PromptLayout{}, fmt.Errorf("resolve packaged config layout: %w", err)
	}
	return layout, nil
}

// isPackagedSystemDir reports whether one prompt system directory comes from the packaged ../output/configs layout rather than the repository workspace configs root.
// isPackagedSystemDir 用于判断某个提示词系统目录是否来自正式打包的 ../output/configs 布局，而不是仓库工作区下的 configs 根目录。
func isPackagedSystemDir(systemDir string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(systemDir))
	if cleaned == "." || cleaned == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(cleaned), "configs") && strings.EqualFold(filepath.Base(filepath.Dir(cleaned)), "output")
}

// resolveWorkspaceStorageRoot creates one process-local temporary storage root for workspace/go-test execution so tests and direct go-run debugging never contend on the repository's shared database path.
// resolveWorkspaceStorageRoot 用于为工作区和 go test 场景创建一个进程级临时存储根目录，避免测试和直接 go run 调试去竞争仓库内共享的 database 路径。
func resolveWorkspaceStorageRoot() (string, error) {
	workspaceStorageRootOnce.Do(func() {
		root, err := os.MkdirTemp("", "vmm-local-storage-*")
		if err != nil {
			workspaceStorageRootErr = fmt.Errorf("create workspace storage root: %w", err)
			return
		}
		workspaceStorageRoot = root
	})
	if workspaceStorageRootErr != nil {
		return "", workspaceStorageRootErr
	}
	return workspaceStorageRoot, nil
}

// resolveHostLibraryPath prefers packaged libs first and then falls back to the workspace-cached third_party dependencies so tests can reuse one downloaded host library set without copying it into every temporary output root.
// resolveHostLibraryPath 用于优先解析打包后的 libs 目录，并在缺失时回退到工作区 third_party 依赖缓存，让测试不需要把动态库重复复制到每个临时输出目录。
func resolveHostLibraryPath(candidateRoots []string, libraryName string) string {
	for _, root := range candidateRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if direct := filepath.Join(root, "libs", libraryName); fileExists(direct) {
			return direct
		}
		if cached := filepath.Join(root, "third_party", "deps", libraryName); fileExists(cached) {
			return cached
		}
	}
	if len(candidateRoots) == 0 || strings.TrimSpace(candidateRoots[0]) == "" {
		return libraryName
	}
	return filepath.Join(candidateRoots[0], "libs", libraryName)
}

// resolveHostLibraryNames returns the SQLite and LanceDB dynamic-library filenames for the current host OS.
// resolveHostLibraryNames 用于返回当前宿主操作系统对应的 SQLite 与 LanceDB 动态库文件名。
func resolveHostLibraryNames() (string, string) {
	switch runtime.GOOS {
	case "windows":
		return "vldb_sqlite.dll", "vldb_lancedb.dll"
	case "darwin":
		return "libvldb_sqlite.dylib", "libvldb_lancedb.dylib"
	default:
		return "libvldb_sqlite.so", "libvldb_lancedb.so"
	}
}

// fileExists reports whether one regular file currently exists.
// fileExists 用于返回某个常规文件当前是否存在。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
