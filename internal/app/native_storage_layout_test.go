// These tests exercise native layout and writer exclusion with isolated packaged roots.
// 本文件使用隔离的打包根目录验证原生路径解析和写入者互斥。
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// TestNativeLayoutUsesPackagedRoots checks paths, defaults, and absence of eager database creation.
// TestNativeLayoutUsesPackagedRoots 验证路径、默认库名以及解析阶段不会提前创建数据库。
func TestNativeLayoutUsesPackagedRoots(t *testing.T) {
	// Match the runtime's canonical path identity before checking the packaged layout.
	// 在检查打包布局前先采用与运行时一致的规范路径身份。
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tempRoot, "output")
	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "native"
	got, err := resolveNativeStorageLayout(cfg, config.PromptLayout{SystemDir: filepath.Join(root, "configs")})
	if err != nil {
		t.Fatal(err)
	}
	if got.SQLiteDatabase != filepath.Join(root, "database", "native", "sqlite.db") || got.LanceDBDirectory != filepath.Join(root, "database", "native", "lancedb") {
		t.Fatalf("unexpected native layout: %+v", got)
	}
	name, err := nativeLanceLibraryName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if got.LanceDBLibrary != filepath.Join(root, "libs", name) {
		t.Fatalf("library path = %q", got.LanceDBLibrary)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("path resolution must not create output: %v", err)
	}
}

// nativeShutdownProbe models a resource whose close can fail independently of the caller's cancellation.
// nativeShutdownProbe 模拟关闭可能独立于调用方取消而失败的资源。
type nativeShutdownProbe struct {
	fail   bool
	closed bool
}

// Shutdown records successful closure only after checking the context passed by the owner.
// Shutdown 仅在检查所有者传入的上下文后记录成功关闭。
func (p *nativeShutdownProbe) Shutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.fail {
		return errors.New("close failed")
	}
	p.closed = true
	return nil
}

// TestNativeOwnerRetainsLocksUntilResourcesClose covers canceled shutdown and failed resource cleanup.
// TestNativeOwnerRetainsLocksUntilResourcesClose 覆盖取消关闭以及资源清理失败时的锁保持行为。
func TestNativeOwnerRetainsLocksUntilResourcesClose(t *testing.T) {
	root := t.TempDir()
	layout := nativeStorageLayout{SQLiteDatabase: filepath.Join(root, "sqlite.db"), LanceDBDirectory: filepath.Join(root, "vectors")}
	owner, err := acquireNativeStorageOwner(layout)
	if err != nil {
		t.Fatal(err)
	}
	probe := &nativeShutdownProbe{fail: true}
	parent := &nativeShutdownProbe{}
	owner.resources = []appports.Shutdowner{parent, probe}
	t.Cleanup(func() { probe.fail = false; _ = owner.Shutdown(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Shutdown(ctx); err == nil {
		t.Fatal("failed resource close was hidden")
	}
	if parent.closed || len(owner.resources) != 2 {
		t.Fatal("parent resource ownership lost after child close failed")
	}
	if second, err := acquireNativeStorageOwner(layout); err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("lock released while a resource remained open")
	}
	probe.fail = false
	if err := owner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !probe.closed || !parent.closed {
		t.Fatal("canceled caller prevented resource closure")
	}
	second, err := acquireNativeStorageOwner(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestNativeLayoutHonorsAbsolutePaths verifies that the user's explicit resource paths stay authoritative.
// TestNativeLayoutHonorsAbsolutePaths 验证用户显式指定的绝对资源路径具有唯一权威性。
func TestNativeLayoutHonorsAbsolutePaths(t *testing.T) {
	// The configured absolute paths are authoritative after canonical symlink resolution.
	// 显式绝对路径在完成符号链接规范化后仍是唯一权威路径。
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultLocal()
	cfg.SQLite.Native.Path = filepath.Join(root, "custom", "sqlite.db")
	cfg.LanceDB.Native.Path = filepath.Join(root, "vectors")
	cfg.LanceDB.Native.LibraryPath = filepath.Join(root, "custom-lib.dll")
	got, err := resolveNativeStorageLayout(cfg, config.PromptLayout{SystemDir: filepath.Join(root, "output", "configs")})
	if err != nil {
		t.Fatal(err)
	}
	if got.SQLiteDatabase != cfg.SQLite.Native.Path || got.LanceDBDirectory != cfg.LanceDB.Native.Path || got.LanceDBLibrary != cfg.LanceDB.Native.LibraryPath {
		t.Fatalf("absolute paths were replaced: %+v", got)
	}
	cfg.SQLite.Native.Path = filepath.Join(root, "vectors", "sqlite.db")
	if _, err := resolveNativeStorageLayout(cfg, config.PromptLayout{SystemDir: filepath.Join(root, "output", "configs")}); err == nil {
		t.Fatal("overlapping native resources accepted")
	}
}

// TestNativeStorageOwnerReleasesPartialLocks proves failed construction cannot strand a database lock.
// TestNativeStorageOwnerReleasesPartialLocks 验证构造失败后不会遗留数据库路径锁。
func TestNativeStorageOwnerReleasesPartialLocks(t *testing.T) {
	root := t.TempDir()
	layout := nativeStorageLayout{SQLiteDatabase: filepath.Join(root, "sqlite.db"), LanceDBDirectory: filepath.Join(root, "vectors")}
	first, err := acquireNativeStorageOwner(layout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })
	if second, err := acquireNativeStorageOwner(layout); err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("second runtime acquired the same native databases")
	}
	// The new SQLite lock sorts before the busy vector lock, so failure must release that first lock.
	// 新 SQLite 锁排在被占用的向量锁之前，因此失败必须释放已取得的第一个锁。
	partial := nativeStorageLayout{SQLiteDatabase: filepath.Join(root, "a-new.db"), LanceDBDirectory: layout.LanceDBDirectory}
	if unexpected, err := acquireNativeStorageOwner(partial); err == nil {
		_ = unexpected.Shutdown(context.Background())
		t.Fatal("accepted a shared busy vector directory")
	}
	partial.LanceDBDirectory = filepath.Join(root, "other-vectors")
	recovered, err := acquireNativeStorageOwner(partial)
	if err != nil {
		t.Fatalf("partial acquisition stranded the SQLite lock: %v", err)
	}
	if err := recovered.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.CheckHealth(context.Background()); err == nil {
		t.Fatal("uninitialized database checks reported healthy")
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.CheckHealth(context.Background()); err == nil {
		t.Fatal("closed owner reported healthy")
	}
	second, err := acquireNativeStorageOwner(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestNativeLibraryTargetMatrix checks filenames for supported targets and rejects unsupported ones.
// TestNativeLibraryTargetMatrix 验证支持平台的库名，并拒绝未支持的目标平台。
func TestNativeLibraryTargetMatrix(t *testing.T) {
	for _, target := range [][3]string{{"windows", "amd64", "vmm_lancedb_native.dll"}, {"linux", "arm64", "libvmm_lancedb_native.so"}, {"darwin", "arm64", "libvmm_lancedb_native.dylib"}} {
		name, err := nativeLanceLibraryName(target[0], target[1])
		if err != nil || name != target[2] {
			t.Fatalf("target %v: %q, %v", target, name, err)
		}
	}
	if _, err := nativeLanceLibraryName("windows", "arm64"); err == nil {
		t.Fatal("unsupported target accepted")
	}
}
