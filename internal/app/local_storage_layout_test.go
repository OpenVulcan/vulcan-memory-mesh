// local_storage_layout_test.go verifies explicit split/controller data roots and their safety boundaries.
// local_storage_layout_test.go 用于验证显式 split/controller 数据根目录及其安全边界。
package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/testutil"
)

// TestResolveLocalStorageLayoutForConfigKeepsLegacyLayout verifies an empty override retains the packaged database layout.
// TestResolveLocalStorageLayoutForConfigKeepsLegacyLayout 用于验证空覆盖仍保持打包数据库布局。
func TestResolveLocalStorageLayoutForConfigKeepsLegacyLayout(t *testing.T) {
	promptLayout, outputRoot := packagedPromptLayout(t)
	layout, err := resolveLocalStorageLayoutForConfig(config.DefaultLocal(), promptLayout)
	if err != nil {
		t.Fatalf("resolve legacy layout: %v", err)
	}
	wantRoot := filepath.Join(outputRoot, "database")
	if layout.DatabaseDir != wantRoot {
		t.Fatalf("database root = %q, want %q", layout.DatabaseDir, wantRoot)
	}
	if layout.SQLiteDatabase != filepath.Join(wantRoot, "sqlite.db") {
		t.Fatalf("sqlite path = %q", layout.SQLiteDatabase)
	}
	if layout.LanceDBDirectory != filepath.Join(wantRoot, "lancedb") {
		t.Fatalf("lancedb path = %q", layout.LanceDBDirectory)
	}
}

// TestResolveLocalStorageLayoutForConfigUsesExplicitRoot verifies split/controller modes share one configured physical root.
// TestResolveLocalStorageLayoutForConfigUsesExplicitRoot 用于验证 split/controller 模式共同使用显式物理数据根目录。
func TestResolveLocalStorageLayoutForConfigUsesExplicitRoot(t *testing.T) {
	promptLayout, _ := packagedPromptLayout(t)
	dataRoot := filepath.Join(testutil.CanonicalTempDir(t), "vmm-data")
	for _, mode := range []string{"split", "controller"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.DefaultLocal()
			cfg.Storage.Mode = mode
			cfg.Storage.LocalDataRoot = dataRoot
			layout, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout)
			if err != nil {
				t.Fatalf("resolve explicit layout: %v", err)
			}
			if layout.DatabaseDir != dataRoot {
				t.Fatalf("database root = %q, want %q", layout.DatabaseDir, dataRoot)
			}
			if layout.SQLiteDatabase != filepath.Join(dataRoot, "sqlite.db") {
				t.Fatalf("sqlite path = %q", layout.SQLiteDatabase)
			}
			if layout.LanceDBDirectory != filepath.Join(dataRoot, "lancedb") {
				t.Fatalf("lancedb path = %q", layout.LanceDBDirectory)
			}
			if info, err := os.Stat(dataRoot); err != nil || !info.IsDir() {
				t.Fatalf("explicit data root was not created: info=%v err=%v", info, err)
			}
		})
	}
}

// TestResolveLocalStorageLayoutForConfigRejectsPackageOverlap prevents data files from replacing packaged runtime assets.
// TestResolveLocalStorageLayoutForConfigRejectsPackageOverlap 用于防止数据文件覆盖打包运行时资源。
func TestResolveLocalStorageLayoutForConfigRejectsPackageOverlap(t *testing.T) {
	promptLayout, outputRoot := packagedPromptLayout(t)
	if err := os.MkdirAll(filepath.Join(outputRoot, "libs"), 0o755); err != nil {
		t.Fatalf("create package libs: %v", err)
	}
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = filepath.Join(outputRoot, "libs")
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil || !strings.Contains(err.Error(), "overlaps packaged libs") {
		t.Fatalf("expected package overlap rejection, got %v", err)
	}
}

// TestResolveLocalStorageLayoutForConfigRejectsLegacySwitch prevents a configured empty root from hiding an existing legacy database.
// TestResolveLocalStorageLayoutForConfigRejectsLegacySwitch 用于防止显式空根目录隐藏已有的历史数据库。
func TestResolveLocalStorageLayoutForConfigRejectsLegacySwitch(t *testing.T) {
	promptLayout, outputRoot := packagedPromptLayout(t)
	legacyRoot := filepath.Join(outputRoot, "database")
	if err := os.MkdirAll(filepath.Join(legacyRoot, "lancedb"), 0o755); err != nil {
		t.Fatalf("create legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "sqlite.db"), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy database: %v", err)
	}
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = filepath.Join(t.TempDir(), "new-data")
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil || !strings.Contains(err.Error(), "differs from legacy database root") {
		t.Fatalf("expected legacy switch rejection, got %v", err)
	}
}

// TestPreflightLocalStorageLayoutRejectsUnsafeRoots verifies validation fails without creating the proposed data root.
// TestPreflightLocalStorageLayoutRejectsUnsafeRoots 验证预检拒绝危险路径且不创建候选数据根。
func TestPreflightLocalStorageLayoutRejectsUnsafeRoots(t *testing.T) {
	promptLayout, outputRoot := packagedPromptLayout(t)
	legacyRoot := filepath.Join(outputRoot, "database")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "sqlite.db"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-data")
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = newRoot
	if err := PreflightLocalStorageLayout(cfg, promptLayout); err == nil {
		t.Fatal("expected legacy data rejection")
	}
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("preflight created data root: %v", err)
	}
	cfg.Storage.LocalDataRoot = filepath.Join(outputRoot, "libs")
	if err := PreflightLocalStorageLayout(cfg, promptLayout); err == nil {
		t.Fatal("expected packaged library overlap rejection")
	}
}

// TestStorageRootContainsDataFailsClosed verifies a malformed legacy LanceDB path cannot be mistaken for an empty database.
// TestStorageRootContainsDataFailsClosed 验证异常历史 LanceDB 路径不会被误判为空数据库。
func TestStorageRootContainsDataFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lancedb"), []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storageRootContainsData(root); err == nil {
		t.Fatal("expected legacy storage inspection failure")
	}
}

// TestPreflightLocalStorageLayoutRejectsLegacyNest verifies an initially empty legacy tree cannot become the new data root's parent.
// TestPreflightLocalStorageLayoutRejectsLegacyNest 验证即使历史根初始为空也不能把新数据根嵌入其中。
func TestPreflightLocalStorageLayoutRejectsLegacyNest(t *testing.T) {
	promptLayout, outputRoot := packagedPromptLayout(t)
	newRoot := filepath.Join(outputRoot, "database", "lancedb", "nested")
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = newRoot
	if err := PreflightLocalStorageLayout(cfg, promptLayout); err == nil {
		t.Fatal("expected nested legacy root rejection")
	}
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil {
		t.Fatal("expected runtime nested legacy root rejection")
	}
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("nested candidate was created: %v", err)
	}
}

// TestPackagedSystemDirAcceptsReleaseRoot verifies extracted releases retain persistent storage paths outside output/.
// TestPackagedSystemDirAcceptsReleaseRoot 验证解压发行包在 output/ 之外仍保持持久数据路径。
func TestPackagedSystemDirAcceptsReleaseRoot(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"bin", "configs"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	binary := "vmm-local"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	for _, name := range []string{"VERSION", filepath.Join("bin", binary), filepath.Join("configs", "base.yaml")} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !isPackagedSystemDir(filepath.Join(root, "configs")) {
		t.Fatal("release-style root was not recognized")
	}
}

// TestResolveLocalStorageLayoutForConfigRejectsRootSymlink prevents aliases from bypassing data-root ownership checks.
// TestResolveLocalStorageLayoutForConfigRejectsRootSymlink 用于防止符号链接别名绕过数据根目录所有权检查。
func TestResolveLocalStorageLayoutForConfigRejectsRootSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires elevated Windows privileges")
	}
	promptLayout, _ := packagedPromptLayout(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = link
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

// TestResolveLocalStorageLayoutForConfigRejectsProtectedParentAlias verifies validation runs before a parent symlink can create packaged files.
// TestResolveLocalStorageLayoutForConfigRejectsProtectedParentAlias 验证父目录符号链接写入打包资源之前会先被拒绝。
func TestResolveLocalStorageLayoutForConfigRejectsProtectedParentAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires elevated Windows privileges")
	}
	promptLayout, outputRoot := packagedPromptLayout(t)
	libs := filepath.Join(outputRoot, "libs")
	if err := os.MkdirAll(libs, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(libs, alias); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = filepath.Join(alias, "unexpected")
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil {
		t.Fatal("expected protected parent alias rejection")
	}
	if _, err := os.Stat(filepath.Join(libs, "unexpected")); !os.IsNotExist(err) {
		t.Fatalf("protected package directory was modified: %v", err)
	}
}

// TestResolveLocalStorageLayoutForConfigRejectsBroadUnixPermissions prevents shared-readable roots from holding local memory data.
// TestResolveLocalStorageLayoutForConfigRejectsBroadUnixPermissions 用于防止权限过宽的目录保存本地记忆数据。
func TestResolveLocalStorageLayoutForConfigRejectsBroadUnixPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits are not authoritative on Windows")
	}
	promptLayout, _ := packagedPromptLayout(t)
	dataRoot := filepath.Join(t.TempDir(), "vmm-data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatalf("create data root: %v", err)
	}
	if err := os.Chmod(dataRoot, 0o755); err != nil {
		t.Fatalf("set broad data root permissions: %v", err)
	}
	cfg := config.DefaultLocal()
	cfg.Storage.LocalDataRoot = dataRoot
	if _, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout); err == nil || !strings.Contains(err.Error(), "must be private mode 0700") {
		t.Fatalf("expected private permission rejection, got %v", err)
	}
}

// packagedPromptLayout creates an isolated output/configs tree and returns its prompt layout and artifact root.
// packagedPromptLayout 创建隔离的 output/configs 树，并返回提示词布局与打包根目录。
func packagedPromptLayout(t *testing.T) (config.PromptLayout, string) {
	t.Helper()
	outputRoot := filepath.Join(t.TempDir(), "output")
	systemDir := filepath.Join(outputRoot, "configs")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatalf("create packaged config root: %v", err)
	}
	return config.PromptLayout{SystemDir: systemDir}, outputRoot
}
