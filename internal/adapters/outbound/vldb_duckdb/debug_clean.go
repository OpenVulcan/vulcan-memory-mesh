// debug_clean.go implements the debug-only gateway cleanup helper for managed DuckDB tables.
// debug_clean.go 用于实现调试专用的网关清理辅助逻辑，负责清空受管 DuckDB 表。
package vldb_duckdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_duckdb/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const debugCleanManagedSchemaSQL = resetManagedSchemaSQL + `
DROP TABLE IF EXISTS vmm_version;
`

// DebugCleanManagedSchema connects to the DuckDB gateway, drops all VMM-managed tables, and then returns immediately.
// DebugCleanManagedSchema 用于连接 DuckDB 网关，删除所有 VMM 受管表，然后立即返回。
func DebugCleanManagedSchema(ctx context.Context, address string, timeout time.Duration) error {
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("duckdb address is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		callCtx,
		strings.TrimSpace(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("dial duckdb gateway for debug clean: %w", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	return debugCleanWithClient(ctx, duckdbv1.NewDuckDbServiceClient(conn), timeout)
}

// debugCleanWithClient sends the destructive cleanup script through an already prepared DuckDB gateway client.
// debugCleanWithClient 用于通过已准备好的 DuckDB 网关客户端发送破坏性清理脚本。
func debugCleanWithClient(ctx context.Context, client duckdbv1.DuckDbServiceClient, timeout time.Duration) error {
	if client == nil {
		return fmt.Errorf("duckdb client is not initialized")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.ExecuteScript(callCtx, &duckdbv1.ExecuteRequest{
		Sql: strings.TrimSpace(debugCleanManagedSchemaSQL),
	})
	if err != nil {
		return fmt.Errorf("debug clean duckdb schema: %w", err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("debug clean duckdb schema: %s", strings.TrimSpace(resp.GetMessage()))
	}
	return nil
}
