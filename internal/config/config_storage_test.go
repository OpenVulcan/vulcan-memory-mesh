// Behavioral tests grouped by config storage test.
// 配置层存储模式与路径行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigValidateAcceptsControllerMode verifies one loopback controller can own both split-store backends without activating PostgreSQL validation.
// TestConfigValidateAcceptsControllerMode 用于验证 loopback controller 可以统一持有两种 split 后端，且不会触发 PostgreSQL 校验。
func TestConfigValidateAcceptsControllerMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Postgres.DSN = ""
	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate controller mode: %v", err)
	}
	if !cfg.UsesController() {
		t.Fatal("expected controller mode helper to be enabled")
	}
}

// TestConfigValidateAcceptsExplicitSplitDataRoot verifies the installer-facing root is accepted for both local split topologies.
// TestConfigValidateAcceptsExplicitSplitDataRoot 用于验证安装器使用的本地数据根目录可用于 split 与 controller 两种本地拓扑。
func TestConfigValidateAcceptsExplicitSplitDataRoot(t *testing.T) {
	for _, mode := range []string{"split", "controller"} {
		t.Run(mode, func(t *testing.T) {
			cfg := newValidConfigForTest()
			cfg.Storage.Mode = mode
			cfg.Storage.LocalDataRoot = "  " + filepath.Join(t.TempDir(), "vmm-data") + "  "
			cfg.Normalize()
			if err := cfg.Validate(); err != nil {
				t.Fatalf("validate explicit local data root: %v", err)
			}
			if !filepath.IsAbs(cfg.Storage.LocalDataRoot) {
				t.Fatalf("local data root is not absolute: %q", cfg.Storage.LocalDataRoot)
			}
		})
	}
}

// TestConfigValidateRejectsExplicitDataRootOutsideLegacyModes prevents native and combined configurations from silently ignoring the new field.
// TestConfigValidateRejectsExplicitDataRootOutsideLegacyModes 用于防止 native 与 combined 配置静默忽略新字段。
func TestConfigValidateRejectsExplicitDataRootOutsideLegacyModes(t *testing.T) {
	for _, mode := range []string{"native", "combined"} {
		t.Run(mode, func(t *testing.T) {
			cfg := newValidConfigForTest()
			cfg.Storage.Mode = mode
			cfg.Storage.LocalDataRoot = filepath.Join(t.TempDir(), "vmm-data")
			cfg.Normalize()
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "storage.local_data_root is only supported") {
				t.Fatalf("expected mode boundary error, got %v", err)
			}
		})
	}
}

// TestConfigValidateRejectsRelativeLocalDataRoot keeps runtime root resolution deterministic across service working directories.
// TestConfigValidateRejectsRelativeLocalDataRoot 用于保证服务工作目录变化时数据根解析不会产生歧义。
func TestConfigValidateRejectsRelativeLocalDataRoot(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.LocalDataRoot = "database/vmm"
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "storage.local_data_root must be an absolute path" {
		t.Fatalf("expected absolute path error, got %v", err)
	}
}

// TestConfigValidateLoggingDirectoryKeepsLegacyDefaultAndRequiresAbsoluteOverride verifies service logs can move without changing old configurations.
// TestConfigValidateLoggingDirectoryKeepsLegacyDefaultAndRequiresAbsoluteOverride 验证服务日志可以迁出程序包，同时旧配置保持兼容。
func TestConfigValidateLoggingDirectoryKeepsLegacyDefaultAndRequiresAbsoluteOverride(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("legacy logging default: %v", err)
	}
	cfg.Logging.Directory = filepath.Join(t.TempDir(), "logs")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("absolute logging.directory: %v", err)
	}
	cfg.Logging.Directory = "relative/logs"
	if err := cfg.Validate(); err == nil {
		t.Fatal("relative logging.directory unexpectedly accepted")
	}
}

// TestConfigValidateAcceptsNativeMode verifies native storage uses its own paths and tokenizer while keeping shared vector fields active.
// TestConfigValidateAcceptsNativeMode 用于验证原生存储使用独立路径和分词器，同时继续使用共用向量字段。
func TestConfigValidateAcceptsNativeMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "native"
	cfg.SQLite.TokenizerMode = "none"
	cfg.SQLite.Native.Path = filepath.Join(t.TempDir(), "native.db")
	cfg.SQLite.Native.Tokenizer = "unicode61"
	cfg.LanceDB.Native.Path = filepath.Join(t.TempDir(), "lancedb")
	cfg.LanceDB.Native.LibraryPath = filepath.Join(t.TempDir(), "vmm_lancedb_native.dll")
	cfg.Postgres.DSN = ""
	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate native mode: %v", err)
	}
	if !cfg.UsesNative() {
		t.Fatal("expected native mode helper to be enabled")
	}
	if got, want := cfg.SQLite.Native.Tokenizer, "unicode61"; got != want {
		t.Fatalf("native tokenizer = %q, want %q", got, want)
	}
}

// TestConfigValidateRejectsNativeStorageValues verifies unsupported native tokens and invalid path syntax fail explicitly without falling back to split mode.
// TestConfigValidateRejectsNativeStorageValues 用于验证不支持的原生 token 与非法路径语法会显式失败，不会静默回退到 split 模式。
func TestConfigValidateRejectsNativeStorageValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "unsupported storage mode",
			mutate: func(cfg *Config) {
				cfg.Storage.Mode = "legacy"
			},
			wantErr: "storage.mode must be one of split, controller, combined, or native",
		},
		{
			name: "native tokenizer",
			mutate: func(cfg *Config) {
				cfg.Storage.Mode = "native"
				cfg.SQLite.Native.Tokenizer = "jieba"
			},
			wantErr: "sqlite.native.tokenizer must be one of gse or unicode61 when storage.mode=native",
		},
		{
			name: "native sqlite path",
			mutate: func(cfg *Config) {
				cfg.Storage.Mode = "native"
				cfg.SQLite.Native.Path = "database/native/\x00sqlite.db"
			},
			wantErr: "sqlite.native.path contains an invalid control character",
		},
		{
			name: "native lancedb path",
			mutate: func(cfg *Config) {
				cfg.Storage.Mode = "native"
				cfg.LanceDB.Native.Path = "database/native/\x00lancedb"
			},
			wantErr: "lancedb.native.path contains an invalid control character",
		},
		{
			name: "native library path",
			mutate: func(cfg *Config) {
				cfg.Storage.Mode = "native"
				cfg.LanceDB.Native.LibraryPath = "libs/\x00vmm_lancedb_native.dll"
			},
			wantErr: "lancedb.native.library_path contains an invalid control character",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newValidConfigForTest()
			tc.mutate(&cfg)
			cfg.Normalize()
			if err := cfg.Validate(); err == nil || err.Error() != tc.wantErr {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestApplyEnvOverridesSetsNativeStorageFields verifies the four native environment variables follow the existing explicit-reference override mechanism.
// TestApplyEnvOverridesSetsNativeStorageFields 用于验证四个原生环境变量遵循现有的显式引用覆盖机制。
func TestApplyEnvOverridesSetsNativeStorageFields(t *testing.T) {
	keys := map[string]struct{}{
		"VMM_SQLITE_NATIVE_PATH":          {},
		"VMM_SQLITE_NATIVE_TOKENIZER":     {},
		"VMM_LANCEDB_NATIVE_PATH":         {},
		"VMM_LANCEDB_NATIVE_LIBRARY_PATH": {},
	}
	t.Setenv("VMM_SQLITE_NATIVE_PATH", "custom/sqlite.db")
	t.Setenv("VMM_SQLITE_NATIVE_TOKENIZER", "unicode61")
	t.Setenv("VMM_LANCEDB_NATIVE_PATH", "custom/lancedb")
	t.Setenv("VMM_LANCEDB_NATIVE_LIBRARY_PATH", "custom/native.dll")

	cfg := newValidConfigForTest()
	if failures := applyEnvOverrides(&cfg, keys); len(failures) != 0 {
		t.Fatalf("applyEnvOverrides() failures = %#v", failures)
	}
	cfg.Normalize()

	if got, want := cfg.SQLite.Native.Path, "custom/sqlite.db"; got != want {
		t.Fatalf("sqlite native path = %q, want %q", got, want)
	}
	if got, want := cfg.SQLite.Native.Tokenizer, "unicode61"; got != want {
		t.Fatalf("sqlite native tokenizer = %q, want %q", got, want)
	}
	if got, want := cfg.LanceDB.Native.Path, "custom/lancedb"; got != want {
		t.Fatalf("lancedb native path = %q, want %q", got, want)
	}
	if got, want := cfg.LanceDB.Native.LibraryPath, "custom/native.dll"; got != want {
		t.Fatalf("lancedb native library path = %q, want %q", got, want)
	}
}

// TestApplyEnvOverridesSetsLocalDataRoot verifies explicit storage root overrides require the same opt-in placeholder mechanism as other VMM settings.
// TestApplyEnvOverridesSetsLocalDataRoot 用于验证本地数据根环境变量遵循现有的显式占位符覆盖机制。
func TestApplyEnvOverridesSetsLocalDataRoot(t *testing.T) {
	keys := map[string]struct{}{"VMM_STORAGE_LOCAL_DATA_ROOT": {}}
	root := filepath.Join(t.TempDir(), "vmm-data")
	t.Setenv("VMM_STORAGE_LOCAL_DATA_ROOT", root)
	cfg := newValidConfigForTest()
	if failures := applyEnvOverrides(&cfg, keys); len(failures) != 0 {
		t.Fatalf("applyEnvOverrides() failures = %#v", failures)
	}
	cfg.Normalize()
	if got := cfg.Storage.LocalDataRoot; got != root {
		t.Fatalf("local data root = %q, want %q", got, root)
	}
}

// TestConfigValidateRejectsRemoteControllerEndpoint verifies the unauthenticated first release cannot expose its database control plane on a remote host.
// TestConfigValidateRejectsRemoteControllerEndpoint 用于验证首版无认证控制面不能通过远程主机暴露数据库能力。
func TestConfigValidateRejectsRemoteControllerEndpoint(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Controller.Endpoint = "http://192.168.31.10:19801"
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "controller.endpoint must use a loopback host when storage.mode=controller" {
		t.Fatalf("expected loopback controller endpoint error, got %v", err)
	}
}

// TestConfigValidateRejectsControllerLeaseRenewalAtTTL verifies renewal must happen before the registered lease expires.
// TestConfigValidateRejectsControllerLeaseRenewalAtTTL 用于验证续租必须早于已注册租约到期。
func TestConfigValidateRejectsControllerLeaseRenewalAtTTL(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Controller.LeaseTTL = Duration{30 * time.Second}
	cfg.Controller.LeaseRenewInterval = Duration{30 * time.Second}
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "controller.lease_renew_interval must be less than controller.lease_ttl" {
		t.Fatalf("expected controller lease interval error, got %v", err)
	}
}
