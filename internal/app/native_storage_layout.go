// native_storage_layout.go resolves native database paths without probing VLDB or host-owned directories.
// native_storage_layout.go 在应用装配层解析原生数据库路径，不探测 VLDB 或宿主所有的目录。
package app

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/storageguard"
)

// nativeStorageLayout records the single authoritative location of each native storage resource.
// nativeStorageLayout 保存各原生存储资源唯一确定的位置，供运行时和维护入口共同使用。
type nativeStorageLayout struct {
	SQLiteDatabase   string
	LanceDBDirectory string
	LanceDBLibrary   string
}

// PreflightNativeStorageLayout checks packaged native paths and overlap without opening a database or creating files.
// PreflightNativeStorageLayout 不打开数据库或创建文件，仅检查打包环境中的原生路径及相互包含关系。
func PreflightNativeStorageLayout(cfg config.Config, promptLayout config.PromptLayout) error {
	if cfg.StorageMode() != "native" || !isPackagedSystemDir(promptLayout.SystemDir) {
		return nil
	}
	_, err := resolveNativeStorageLayout(cfg, promptLayout)
	return err
}

// resolveNativeStorageLayout resolves configured paths against packaged roots without creating files.
// resolveNativeStorageLayout 将配置路径相对于打包根解析，不创建文件，失败时返回具体路径错误。
func resolveNativeStorageLayout(cfg config.Config, promptLayout config.PromptLayout) (nativeStorageLayout, error) {
	systemDir := strings.TrimSpace(promptLayout.SystemDir)
	if systemDir == "" {
		current, err := resolveCurrentPromptLayout()
		if err != nil {
			return nativeStorageLayout{}, err
		}
		systemDir = current.SystemDir
	}
	artifactRoot, err := filepath.Abs(filepath.Join(systemDir, ".."))
	if err != nil {
		return nativeStorageLayout{}, fmt.Errorf("resolve native artifact root: %w", err)
	}
	outputRoot := artifactRoot
	if !isPackagedSystemDir(systemDir) {
		outputRoot, err = resolveWorkspaceStorageRoot()
		if err != nil {
			return nativeStorageLayout{}, err
		}
	}
	sqlitePath, err := resolveNativePath(outputRoot, cfg.SQLite.Native.Path)
	if err != nil {
		return nativeStorageLayout{}, fmt.Errorf("sqlite.native.path: %w", err)
	}
	lancePath, err := resolveNativePath(outputRoot, cfg.LanceDB.Native.Path)
	if err != nil {
		return nativeStorageLayout{}, fmt.Errorf("lancedb.native.path: %w", err)
	}
	// Keep native database files outside packaged binaries, libraries, and system configuration across upgrades.
	// 将原生数据库文件与打包二进制、动态库和系统配置隔离，避免升级时污染或删除数据。
	if err := rejectProtectedDataRoot(sqlitePath, artifactRoot); err != nil {
		return nativeStorageLayout{}, fmt.Errorf("sqlite.native.path: %w", err)
	}
	if err := rejectProtectedDataRoot(lancePath, artifactRoot); err != nil {
		return nativeStorageLayout{}, fmt.Errorf("lancedb.native.path: %w", err)
	}
	if nativePathContains(lancePath, sqlitePath) || nativePathContains(sqlitePath, lancePath) {
		return nativeStorageLayout{}, fmt.Errorf("native SQLite file and LanceDB directory must not overlap")
	}
	libraryPath := strings.TrimSpace(cfg.LanceDB.Native.LibraryPath)
	if libraryPath == "" {
		libraryName, err := nativeLanceLibraryName(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return nativeStorageLayout{}, err
		}
		libraryPath = filepath.Join("libs", libraryName)
	}
	libraryPath, err = resolveNativePath(artifactRoot, libraryPath)
	if err != nil {
		return nativeStorageLayout{}, fmt.Errorf("lancedb.native.library_path: %w", err)
	}
	return nativeStorageLayout{SQLiteDatabase: sqlitePath, LanceDBDirectory: lancePath, LanceDBLibrary: libraryPath}, nil
}

// resolveNativePath canonicalizes one explicit absolute or root-relative path without fallback probing.
// resolveNativePath 规范化一条显式绝对路径或相对根路径，不通过候选目录回退查找资源。
func resolveNativePath(root, configured string) (string, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return storageguard.CanonicalPath(path)
}

// nativePathContains reports equality or directory containment between already canonical paths.
// nativePathContains 判断已规范化路径是否相等或存在目录包含关系，用于拒绝资源覆盖。
func nativePathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// nativeLanceLibraryName maps a supported target to its exact library filename or an explicit error.
// nativeLanceLibraryName 将支持的目标平台映射到确定库名，对未支持平台明确返回错误。
func nativeLanceLibraryName(goos, goarch string) (string, error) {
	switch {
	case goos == "windows" && goarch == "amd64":
		return "vmm_lancedb_native.dll", nil
	case goos == "linux" && (goarch == "amd64" || goarch == "arm64"):
		return "libvmm_lancedb_native.so", nil
	case goos == "darwin" && (goarch == "amd64" || goarch == "arm64"):
		return "libvmm_lancedb_native.dylib", nil
	default:
		return "", fmt.Errorf("native LanceDB is unsupported on %s/%s", goos, goarch)
	}
}
