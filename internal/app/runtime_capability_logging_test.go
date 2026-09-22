// runtime_capability_logging_test.go verifies startup diagnostics expose paths and tokenizer versions without payloads or credentials.
// runtime_capability_logging_test.go 用于验证启动诊断暴露路径与分词版本，但不输出正文或凭据。
package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// TestLogRuntimeStorageCapabilitiesRecordsSafeNativeFacts verifies native paths and tokenizer metadata are logged while embedding credentials stay absent.
// TestLogRuntimeStorageCapabilitiesRecordsSafeNativeFacts 验证 native 路径与分词元数据会记录，同时 embedding 凭据不会出现。
func TestLogRuntimeStorageCapabilitiesRecordsSafeNativeFacts(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "native"
	cfg.SQLite.Native.Path = filepath.Join(root, "database", "native.db")
	cfg.LanceDB.Native.Path = filepath.Join(root, "database", "lancedb")
	cfg.LanceDB.Native.LibraryPath = filepath.Join(root, "libs", "vmm_lancedb_native.dll")
	cfg.Embedding.APIKeys = []string{"secret-api-key"}
	cfg.SQLite.Native.Tokenizer = "gse"
	layout, err := resolveNativeStorageLayout(cfg, config.PromptLayout{})
	if err != nil {
		t.Fatalf("resolve native logging layout: %v", err)
	}
	var output bytes.Buffer
	logger := logx.New(&output, logx.Config{Level: "info", Format: "json"})
	logRuntimeStorageCapabilities(logger, cfg, config.PromptLayout{})
	log := output.String()
	var record struct {
		StorageMode        string `json:"storage_mode"`
		SQLitePath         string `json:"sqlite_path"`
		LanceDBPath        string `json:"lancedb_path"`
		LanceDBLibraryPath string `json:"lancedb_library_path"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode startup capability log: %v", err)
	}
	if record.StorageMode != "native" || record.SQLitePath != layout.SQLiteDatabase || record.LanceDBPath != layout.LanceDBDirectory || record.LanceDBLibraryPath != layout.LanceDBLibrary {
		t.Fatalf("startup log paths = %+v, want mode=%q sqlite=%q lancedb=%q library=%q", record, "native", layout.SQLiteDatabase, layout.LanceDBDirectory, layout.LanceDBLibrary)
	}
	for _, expected := range []string{
		`"tokenizer_algorithm":"gse-pretokenize-v1"`,
		`"tokenizer_normalization":"normalize-whitespace-v1"`,
		`"gse_version":"v1.0.2"`,
		`"native_lancedb_abi_version":1`,
		`"native_lancedb_engine_version":"0.39.0"`,
	} {
		if !strings.Contains(log, expected) {
			t.Fatalf("startup log missing %q: %s", expected, log)
		}
	}
	if strings.Contains(log, "secret-api-key") {
		t.Fatalf("startup log exposed embedding credentials: %s", log)
	}
}

// TestLogRuntimeStorageCapabilitiesRecordsNonNativeMode verifies non-native startup still emits the selected storage mode.
// TestLogRuntimeStorageCapabilitiesRecordsNonNativeMode 验证非 native 启动也会输出选定的存储模式。
func TestLogRuntimeStorageCapabilitiesRecordsNonNativeMode(t *testing.T) {
	cfg := config.DefaultLocal()
	var output bytes.Buffer
	logger := logx.New(&output, logx.Config{Level: "info", Format: "json"})
	logRuntimeStorageCapabilities(logger, cfg, config.PromptLayout{})
	if !strings.Contains(output.String(), `"storage_mode":"split"`) {
		t.Fatalf("non-native startup log missing storage mode: %s", output.String())
	}
	if strings.Contains(output.String(), "tokenizer") {
		t.Fatalf("non-native startup log unexpectedly exposed native tokenizer details: %s", output.String())
	}
}
