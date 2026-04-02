// debug_clean.go implements the debug-only gateway cleanup entrypoint used by the local binary.
// debug_clean.go 用于实现本地二进制使用的调试清理入口。
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	"github.com/openvulcan/vmm/internal/config"
)

// debugCleanSelection records which gateway-backed stores should be wiped during one debug-clean run.
// debugCleanSelection 用于记录一次调试清理过程中应当清空哪些网关后端。
type debugCleanSelection struct {
	All     bool
	SQLite  bool
	LanceDB bool
}

// parseDebugCleanSelection normalizes the flag value and translates it into the concrete cleanup targets requested by the caller.
// parseDebugCleanSelection 用于规范化参数值，并把它转换成调用方请求的具体清理目标。
func parseDebugCleanSelection(raw string) (debugCleanSelection, error) {
	selection := debugCleanSelection{}
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(raw)), func(r rune) bool {
		return r == ',' || r == '+' || r == ';'
	})
	if len(parts) == 0 {
		return selection, fmt.Errorf("debug-clean target is required: use sqlite, lancedb, or all")
	}
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "all":
			selection.All = true
			selection.LanceDB = true
		case "sqlite":
			selection.SQLite = true
		case "lancedb":
			selection.LanceDB = true
		case "":
			continue
		default:
			return debugCleanSelection{}, fmt.Errorf("unsupported debug-clean target %q: use sqlite, lancedb, or all", part)
		}
	}
	if !selection.SQLite && !selection.LanceDB {
		return debugCleanSelection{}, fmt.Errorf("debug-clean target is required: use sqlite, lancedb, or all")
	}
	return selection, nil
}

// runDebugClean connects only to the requested storage gateways, wipes their runtime data, and exits without starting the gRPC service.
// runDebugClean 用于只连接被请求的存储网关，清空运行时数据，然后在不启动 gRPC 服务的情况下退出。
func runDebugClean(ctx context.Context, cfg config.Config, target string) error {
	selection, err := parseDebugCleanSelection(target)
	if err != nil {
		return err
	}

	// Execute the requested cleanup targets sequentially so failures report the exact backend that blocked the wipe.
	// 按顺序执行被请求的清理目标，确保失败时能明确指出是哪个后端阻塞了清空操作。
	if selection.All {
		selection.SQLite = true
	}
	if selection.SQLite {
		if err := vldb_sqlite.DebugCleanManagedSchema(ctx, cfg.SQLite.Address, cfg.SQLite.Timeout.Duration); err != nil {
			return err
		}
		fmt.Printf("[vmm-debug-clean] SQLite managed schema cleaned via %s\n", strings.TrimSpace(cfg.SQLite.Address))
	}
	if selection.LanceDB {
		tableName, err := vldb_lancedb.DebugDropConfiguredTable(ctx, cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.Embedding.Dimension)
		if err != nil {
			return err
		}
		fmt.Printf("[vmm-debug-clean] LanceDB table dropped: %s via %s\n", tableName, strings.TrimSpace(cfg.LanceDB.Address))
	}
	return nil
}
