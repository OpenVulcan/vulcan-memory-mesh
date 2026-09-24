// local_storage_layout.go resolves the packaged libs/database layout used by local FFI split storage.
// local_storage_layout.go 用于解析本地 FFI split 存储使用的打包 libs/database 布局。
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/storageguard"
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
	ControllerBinary string
	SQLiteDatabase   string
	LanceDBDirectory string
}

// ResolveLocalStorageLayout resolves the packaged libs/database layout and ensures the database directories exist before FFI stores boot.
// ResolveLocalStorageLayout 用于解析打包后的 libs/database 布局，并在 FFI 存储启动前确保数据库目录已经存在。
func ResolveLocalStorageLayout() (localStorageLayout, error) {
	return resolveLocalStorageLayoutForConfig(config.Config{}, config.PromptLayout{})
}

// ResolveLocalStorageLayoutForPromptLayout resolves the local FFI storage layout from one already-resolved prompt layout so maintenance tools can share the same packaged-root inference as the runtime.
// ResolveLocalStorageLayoutForPromptLayout 用于基于已解析的提示词布局解析本地 FFI 存储布局，让维护工具也能复用与运行时一致的打包根目录推导规则。
func ResolveLocalStorageLayoutForPromptLayout(promptLayout config.PromptLayout) (localStorageLayout, error) {
	return resolveLocalStorageLayoutForConfig(config.Config{}, promptLayout)
}

// ResolveLocalStorageLayoutForConfig resolves split/controller storage using the supplied configuration and prompt layout.
// ResolveLocalStorageLayoutForConfig 根据传入的配置与提示词布局解析 split/controller 存储。
func ResolveLocalStorageLayoutForConfig(cfg config.Config, promptLayout config.PromptLayout) (localStorageLayout, error) {
	return resolveLocalStorageLayoutForConfig(cfg, promptLayout)
}

// PreflightLocalStorageLayout checks an explicit split/controller data root without creating files before a configuration is committed.
// PreflightLocalStorageLayout 在提交配置前只读检查显式 split/controller 数据根，不创建文件。
func PreflightLocalStorageLayout(cfg config.Config, promptLayout config.PromptLayout) error {
	if (cfg.StorageMode() != "split" && cfg.StorageMode() != "controller") || strings.TrimSpace(cfg.Storage.LocalDataRoot) == "" {
		return nil
	}
	if strings.TrimSpace(promptLayout.SystemDir) == "" {
		return errors.New("system config dir is empty")
	}
	artifactRoot := filepath.Clean(filepath.Join(promptLayout.SystemDir, ".."))
	legacyRoot := filepath.Join(artifactRoot, "database")
	if !isPackagedSystemDir(promptLayout.SystemDir) {
		// Workspace execution uses a process-local temporary legacy root, so there is no persistent legacy data to compare.
		// 工作区执行使用进程级临时历史根，因此不存在需要比较的持久历史数据。
		legacyRoot = ""
	}
	dataRoot, err := validateConfiguredLocalDataRoot(cfg.Storage.LocalDataRoot, artifactRoot, legacyRoot, false)
	if err != nil {
		return err
	}
	lanceDBDirectory := filepath.Join(dataRoot, "lancedb")
	if err := rejectDataPathSymlinks(filepath.Join(dataRoot, "sqlite.db"), lanceDBDirectory); err != nil {
		return err
	}
	if err := ensurePrivateDirectoryIfPresent(lanceDBDirectory); err != nil {
		return err
	}
	return nil
}

// resolveLocalStorageLayoutForPromptLayout resolves the storage layout from one already-resolved prompt layout when the caller needs test-specific packaged roots instead of the current process executable.
// resolveLocalStorageLayoutForPromptLayout 用于在调用方已经拿到提示词布局时解析存储布局，这样测试就可以显式指定打包根目录，而不是被当前进程的可执行文件位置绑死。
func resolveLocalStorageLayoutForPromptLayout(promptLayout config.PromptLayout) (localStorageLayout, error) {
	return resolveLocalStorageLayoutForConfig(config.Config{}, promptLayout)
}

// resolveLocalStorageLayoutForConfig resolves the packaged libraries and the effective split/controller data root.
// resolveLocalStorageLayoutForConfig 解析打包动态库以及 split/controller 生效的数据根目录。
func resolveLocalStorageLayoutForConfig(cfg config.Config, promptLayout config.PromptLayout) (localStorageLayout, error) {
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
	legacyDatabaseDir := filepath.Join(outputRoot, "database")
	databaseDir := legacyDatabaseDir
	explicitDataRoot := cfg.StorageMode() == "split" || cfg.StorageMode() == "controller"
	if explicitDataRoot && strings.TrimSpace(cfg.Storage.LocalDataRoot) != "" {
		databaseDir, err = resolveConfiguredLocalDataRoot(cfg.Storage.LocalDataRoot, artifactRoot, legacyDatabaseDir)
		if err != nil {
			return localStorageLayout{}, err
		}
	}
	sqliteDatabase := filepath.Join(databaseDir, "sqlite.db")
	lanceDBDirectory := filepath.Join(databaseDir, "lancedb")

	directoryMode := os.FileMode(0o755)
	if explicitDataRoot && strings.TrimSpace(cfg.Storage.LocalDataRoot) != "" {
		directoryMode = 0o700
		if err := ensurePrivateDirectory(databaseDir); err != nil {
			return localStorageLayout{}, err
		}
	}
	if explicitDataRoot && strings.TrimSpace(cfg.Storage.LocalDataRoot) != "" {
		if err := ensurePrivateDirectoryTree(lanceDBDirectory, directoryMode); err != nil {
			return localStorageLayout{}, fmt.Errorf("create private lancedb database dir: %w", err)
		}
	} else if err := os.MkdirAll(lanceDBDirectory, directoryMode); err != nil {
		return localStorageLayout{}, fmt.Errorf("create packaged lancedb database dir: %w", err)
	}
	if explicitDataRoot && strings.TrimSpace(cfg.Storage.LocalDataRoot) != "" {
		if err := rejectDataPathSymlinks(sqliteDatabase, lanceDBDirectory); err != nil {
			return localStorageLayout{}, err
		}
		if err := ensurePrivateDirectory(lanceDBDirectory); err != nil {
			return localStorageLayout{}, err
		}
	}
	if explicitDataRoot && strings.TrimSpace(cfg.Storage.LocalDataRoot) != "" {
		if err := ensurePrivateDirectoryTree(filepath.Dir(sqliteDatabase), directoryMode); err != nil {
			return localStorageLayout{}, fmt.Errorf("create private sqlite database dir: %w", err)
		}
	} else if err := os.MkdirAll(filepath.Dir(sqliteDatabase), directoryMode); err != nil {
		return localStorageLayout{}, fmt.Errorf("create packaged sqlite database dir: %w", err)
	}

	sqliteLibraryName, lanceDBLibraryName := resolveHostLibraryNames()
	controllerBinaryName := resolveControllerBinaryName()
	actualArtifactRoot := filepath.Clean(filepath.Join(actualLayout.SystemDir, ".."))
	sqliteLibraryPath := resolveHostLibraryPath([]string{artifactRoot, actualArtifactRoot}, sqliteLibraryName)
	lanceDBLibraryPath := resolveHostLibraryPath([]string{artifactRoot, actualArtifactRoot}, lanceDBLibraryName)
	controllerBinaryPath := resolveControllerBinaryPath([]string{artifactRoot, actualArtifactRoot}, controllerBinaryName)

	return localStorageLayout{
		OutputRoot:       outputRoot,
		ArtifactRoot:     artifactRoot,
		LibsDir:          libsDir,
		DatabaseDir:      databaseDir,
		SQLiteLibrary:    sqliteLibraryPath,
		LanceDBLibrary:   lanceDBLibraryPath,
		ControllerBinary: controllerBinaryPath,
		SQLiteDatabase:   sqliteDatabase,
		LanceDBDirectory: lanceDBDirectory,
	}, nil
}

// rejectDataPathSymlinks prevents an explicit data root from redirecting database writes through child symlinks.
// rejectDataPathSymlinks 防止显式数据根通过子级符号链接重定向数据库写入。
func rejectDataPathSymlinks(sqliteDatabase, lanceDBDirectory string) error {
	for _, path := range []string{sqliteDatabase, lanceDBDirectory} {
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect explicit storage path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("explicit storage path %q must not be a symlink", path)
		}
	}
	return nil
}

// resolveConfiguredLocalDataRoot validates and creates one explicit split/controller data root before any database adapter opens it.
// resolveConfiguredLocalDataRoot 在数据库适配器打开前校验并创建显式 split/controller 数据根目录。
func resolveConfiguredLocalDataRoot(rawRoot, artifactRoot, legacyDatabaseDir string) (string, error) {
	return validateConfiguredLocalDataRoot(rawRoot, artifactRoot, legacyDatabaseDir, true)
}

// validateConfiguredLocalDataRoot applies the same path checks for read-only preflight and runtime directory creation.
// validateConfiguredLocalDataRoot 对只读预检与运行时目录创建应用相同的路径检查。
func validateConfiguredLocalDataRoot(rawRoot, artifactRoot, legacyDatabaseDir string, create bool) (string, error) {
	root := strings.TrimSpace(rawRoot)
	if root == "" {
		return "", errors.New("storage.local_data_root is empty")
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve storage.local_data_root: %w", err)
	}
	if err := rejectProtectedDataRoot(absRoot, artifactRoot); err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(absRoot); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("storage.local_data_root %q must not be a symlink", absRoot)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("storage.local_data_root %q is not a directory", absRoot)
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect storage.local_data_root %q: %w", absRoot, statErr)
	}
	canonicalRoot, err := storageguard.CanonicalPath(absRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize storage.local_data_root: %w", err)
	}
	if info, statErr := os.Lstat(canonicalRoot); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("storage.local_data_root %q must not be a symlink", canonicalRoot)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("storage.local_data_root %q is not a directory", canonicalRoot)
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect canonical storage.local_data_root %q: %w", canonicalRoot, statErr)
	}
	if err := rejectProtectedDataRoot(canonicalRoot, artifactRoot); err != nil {
		return "", err
	}
	if legacyDatabaseDir != "" {
		if err := rejectLegacyDataRootSwitch(canonicalRoot, legacyDatabaseDir); err != nil {
			return "", err
		}
	}
	// Canonicalize and reject protected ancestors before creating any directory through a parent alias.
	// 创建目录之前先规范化并拒绝受保护祖先，避免经父目录别名向程序资源写入。
	if create {
		if err := ensurePrivateDirectoryTree(canonicalRoot, 0o700); err != nil {
			return "", fmt.Errorf("create storage.local_data_root %q: %w", canonicalRoot, err)
		}
	}
	if info, err := os.Stat(canonicalRoot); err == nil && info.IsDir() {
		if err := ensurePrivateDirectory(canonicalRoot); err != nil {
			return "", err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect storage.local_data_root %q: %w", canonicalRoot, err)
	}
	return canonicalRoot, nil
}

// rejectProtectedDataRoot prevents a user-selected data root from consuming packaged executable resources.
// rejectProtectedDataRoot 防止用户选择的数据根目录覆盖打包程序资源。
func rejectProtectedDataRoot(dataRoot, artifactRoot string) error {
	canonicalArtifact, err := canonicalPathForComparison(artifactRoot)
	if err != nil {
		return fmt.Errorf("resolve packaged artifact root: %w", err)
	}
	for _, name := range []string{"libs", "bin", "configs"} {
		protected := filepath.Join(canonicalArtifact, name)
		canonical, canonicalErr := canonicalPathForComparison(protected)
		if canonicalErr != nil {
			return fmt.Errorf("resolve packaged %s directory: %w", name, canonicalErr)
		}
		protected = canonical
		if pathsOverlap(dataRoot, protected) {
			return fmt.Errorf("storage.local_data_root %q overlaps packaged %s directory %q", dataRoot, name, protected)
		}
	}
	return nil
}

// rejectLegacyDataRootSwitch refuses to silently open a different empty database while the historical root still contains data.
// rejectLegacyDataRootSwitch 在历史根仍有数据时拒绝静默切换到另一份数据库。
func rejectLegacyDataRootSwitch(dataRoot, legacyRoot string) error {
	canonicalLegacy, err := canonicalPathForComparison(legacyRoot)
	if err != nil {
		return fmt.Errorf("resolve legacy database root: %w", err)
	}
	if pathsOverlap(dataRoot, canonicalLegacy) && !sameStoragePath(dataRoot, canonicalLegacy) {
		return fmt.Errorf("storage.local_data_root %q overlaps legacy database root %q", dataRoot, canonicalLegacy)
	}
	containsData, err := storageRootContainsData(canonicalLegacy)
	if err != nil {
		return fmt.Errorf("inspect legacy database root: %w", err)
	}
	if sameStoragePath(dataRoot, canonicalLegacy) || !containsData {
		return nil
	}
	return fmt.Errorf("storage.local_data_root %q differs from legacy database root %q that contains data; migrate or keep the legacy root before switching", dataRoot, canonicalLegacy)
}

// storageRootContainsData detects database artifacts that make an implicit root change unsafe.
// storageRootContainsData 检测会使隐式根目录切换不安全的数据库文件。
func storageRootContainsData(root string) (bool, error) {
	sqlitePath := filepath.Join(root, "sqlite.db")
	if info, err := os.Lstat(sqlitePath); err == nil {
		if info.IsDir() {
			return false, fmt.Errorf("legacy sqlite path is a directory")
		}
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	for _, marker := range []string{"sqlite.db.migration-incomplete", "sqlite.db.vector-rebuild-incomplete"} {
		if _, err := os.Lstat(filepath.Join(root, marker)); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	lancePath := filepath.Join(root, "lancedb")
	if info, err := os.Lstat(lancePath); err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("legacy lancedb path is not a directory")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	entries, err := os.ReadDir(lancePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}

// ensurePrivateDirectory enforces a private Unix data directory while leaving Windows ACL enforcement to the operating system.
// ensurePrivateDirectory 在 Unix 上强制数据目录为私有权限，Windows 则交由操作系统 ACL 保证。
func ensurePrivateDirectory(path string) error {
	return validatePrivateDirectory(path)
}

// ensurePrivateDirectoryIfPresent checks an existing child without creating or changing it.
// ensurePrivateDirectoryIfPresent 只读检查已存在的子目录，不创建或修改它。
func ensurePrivateDirectoryIfPresent(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect storage directory %q: %w", path, err)
	}
	return ensurePrivateDirectory(path)
}

// canonicalPathForComparison resolves an existing path or its existing ancestors for safe root comparisons.
// canonicalPathForComparison 为安全比较解析现有路径或其现有祖先。
func canonicalPathForComparison(path string) (string, error) {
	canonical, err := storageguard.CanonicalPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

// pathsOverlap reports whether either path is inside the other after absolute normalization.
// pathsOverlap 判断两个路径在绝对规范化后是否互相包含。
func pathsOverlap(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	if sameStoragePath(leftAbs, rightAbs) {
		return true
	}
	return isStoragePathWithin(leftAbs, rightAbs) || isStoragePathWithin(rightAbs, leftAbs)
}

// isStoragePathWithin reports whether a path is a strict child of a root using host path semantics.
// isStoragePathWithin 判断路径是否按当前主机规则严格位于根目录之下。
func isStoragePathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// sameStoragePath compares paths with Windows case folding so aliases cannot bypass protected-root checks.
// sameStoragePath 按 Windows 大小写规则比较路径，防止路径别名绕过受保护根检查。
func sameStoragePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
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

// isPackagedSystemDir recognizes both the standard output layout and a verified release-style root with VERSION and the runtime binary.
// isPackagedSystemDir 识别标准 output 布局，以及具有 VERSION 和运行二进制的发行包根目录。
func isPackagedSystemDir(systemDir string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(systemDir))
	if cleaned == "." || cleaned == "" {
		return false
	}
	if !strings.EqualFold(filepath.Base(cleaned), "configs") {
		return false
	}
	root := filepath.Dir(cleaned)
	if strings.EqualFold(filepath.Base(root), "output") {
		return true
	}
	binaryName := "vmm-local"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	return fileExists(filepath.Join(root, "VERSION")) && fileExists(filepath.Join(root, "bin", binaryName)) && fileExists(filepath.Join(cleaned, "base.yaml"))
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

// resolveControllerBinaryName returns the controller executable filename for the current host OS.
// resolveControllerBinaryName 用于返回当前宿主操作系统对应的 controller 可执行文件名。
func resolveControllerBinaryName() string {
	if runtime.GOOS == "windows" {
		return "vldb-controller.exe"
	}
	return "vldb-controller"
}

// resolveControllerBinaryPath prefers the packaged bin directory and then a workspace dependency cache before falling back to PATH lookup.
// resolveControllerBinaryPath 优先解析打包 bin 目录，其次解析工作区依赖缓存，最后回退到 PATH 查找。
func resolveControllerBinaryPath(candidateRoots []string, binaryName string) string {
	for _, root := range candidateRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if packaged := filepath.Join(root, "bin", binaryName); fileExists(packaged) {
			return packaged
		}
		if cached := filepath.Join(root, "third_party", "deps", binaryName); fileExists(cached) {
			return cached
		}
	}
	return binaryName
}

// fileExists reports whether one regular file currently exists.
// fileExists 用于返回某个常规文件当前是否存在。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
