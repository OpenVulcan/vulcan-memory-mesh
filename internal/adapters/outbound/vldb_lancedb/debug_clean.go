// debug_clean.go implements the debug-only gateway cleanup helper for the configured LanceDB vector table.
// debug_clean.go 用于实现调试专用的网关清理辅助逻辑，负责删除配置指定的 LanceDB 向量表。
package vldb_lancedb

import (
	"context"
	"fmt"
	"strings"
	"time"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb/proto/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// DebugDropConfiguredTable connects to the LanceDB gateway, drops the resolved runtime table, and treats missing tables as already clean.
// DebugDropConfiguredTable 用于连接 LanceDB 网关，删除解析后的运行时表，并把“表不存在”视为已经清理完成。
func DebugDropConfiguredTable(ctx context.Context, address string, timeout time.Duration, baseTableName string, dimension int) (string, error) {
	if strings.TrimSpace(address) == "" {
		return "", fmt.Errorf("lancedb address is required")
	}
	if strings.TrimSpace(baseTableName) == "" {
		return "", fmt.Errorf("lancedb table_name is required")
	}
	if dimension <= 0 {
		return "", fmt.Errorf("lancedb dimension must be > 0")
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
		return "", fmt.Errorf("dial lancedb gateway for debug clean: %w", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	tableName := resolveVectorTableName(baseTableName, dimension)
	if err := debugDropTableWithClient(ctx, lancedbv1.NewLanceDbServiceClient(conn), tableName, timeout); err != nil {
		return "", err
	}
	return tableName, nil
}

// debugDropTableWithClient sends one drop-table request through an already prepared LanceDB gateway client.
// debugDropTableWithClient 用于通过已准备好的 LanceDB 网关客户端发送删表请求。
func debugDropTableWithClient(ctx context.Context, client lancedbv1.LanceDbServiceClient, tableName string, timeout time.Duration) error {
	if client == nil {
		return fmt.Errorf("lancedb client is not initialized")
	}
	if strings.TrimSpace(tableName) == "" {
		return fmt.Errorf("lancedb table_name is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.DropTable(callCtx, &lancedbv1.DropTableRequest{TableName: strings.TrimSpace(tableName)})
	if err != nil {
		if isTableNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("drop lancedb table %s: %w", strings.TrimSpace(tableName), err)
	}
	if !resp.GetSuccess() {
		if isTableNotFoundMessage(resp.GetMessage()) {
			return nil
		}
		return fmt.Errorf("drop lancedb table %s: %s", strings.TrimSpace(tableName), strings.TrimSpace(resp.GetMessage()))
	}
	return nil
}

// isTableNotFoundError classifies gateway transport errors that mean the requested table does not exist anymore.
// isTableNotFoundError 用于识别那些实际表示“目标表已经不存在”的网关传输层错误。
func isTableNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.NotFound {
		return true
	}
	return isTableNotFoundMessage(err.Error())
}

// isTableNotFoundMessage matches the gateway messages used when DropTable is retried on a missing table.
// isTableNotFoundMessage 用于匹配网关在重复删除缺失表时返回的典型消息。
func isTableNotFoundMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "not found") || strings.Contains(normalized, "does not exist")
}
