// runtime_capability_logging.go records safe storage and tokenizer facts at application startup.
// runtime_capability_logging.go 用于在应用启动时记录安全的存储与分词能力事实。
package app

import (
	"strings"

	"github.com/openvulcan/vmm/internal/buildinfo"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// logRuntimeStorageCapabilities records storage mode for every runtime and resolved native paths plus tokenizer versions for native mode.
// logRuntimeStorageCapabilities 为所有运行模式记录存储模式，并为 native 模式记录解析后的路径与分词版本。
func logRuntimeStorageCapabilities(logger *logx.Logger, cfg config.Config, promptLayout config.PromptLayout) {
	if logger == nil {
		return
	}
	mode := cfg.StorageMode()
	if mode != "native" {
		logger.Info("runtime storage mode", "storage_mode", mode)
		return
	}
	layout, err := resolveNativeStorageLayout(cfg, promptLayout)
	if err != nil {
		logger.Warn("runtime storage capabilities unavailable", "storage_mode", mode, "err", err)
		return
	}
	compiled := buildinfo.CompiledNativeCapabilities
	logger.Info(
		"runtime storage capabilities",
		"storage_mode", mode,
		"sqlite_path", layout.SQLiteDatabase,
		"lancedb_path", layout.LanceDBDirectory,
		"lancedb_library_path", layout.LanceDBLibrary,
		"tokenizer", strings.TrimSpace(cfg.SQLite.Native.Tokenizer),
		"tokenizer_algorithm", compiled.NativeSQLite.TokenizerAlgorithm,
		"tokenizer_normalization", compiled.NativeSQLite.NormalizationVersion,
		"gse_version", compiled.NativeSQLite.GSEVersion,
		"native_lancedb_abi_version", compiled.NativeLanceDB.ABIVersion,
		"native_lancedb_engine_version", compiled.NativeLanceDB.EngineVersion,
		"native_lancedb_requires_external_library", compiled.NativeLanceDB.RequiresExternalLibrary,
	)
}
