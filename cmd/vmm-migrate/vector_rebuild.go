// vector_rebuild.go implements the standalone vector rebuild command used for embedding-model and embedding-dimension migrations.
// vector_rebuild.go 用于实现独立的向量重建命令，服务 embedding 模型和 embedding 维度迁移。
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// runMaintenanceVectorRebuild prompts for confirmation, rebuilds active durable vectors with the currently configured embedding model, and exits without booting the gRPC runtime.
// runMaintenanceVectorRebuild 用于执行确认提示、按当前配置的 embedding 模型重建 active durable 向量，并在不启动 gRPC 运行时的情况下退出。
func runMaintenanceVectorRebuild(ctx context.Context, cfg config.Config, input io.Reader, output io.Writer) error {
	if input == nil {
		input = strings.NewReader("")
	}
	if output == nil {
		output = io.Discard
	}
	runtimeGuard, err := acquireVectorRebuildRuntimeGuard(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = runtimeGuard.Close() }()
	if err := confirmVectorRebuild(input, output, cfg); err != nil {
		return err
	}

	deps, err := app.BuildMaintenanceDependencies(cfg)
	if err != nil {
		return fmt.Errorf("build maintenance dependencies: %w", err)
	}
	defer func() {
		_ = deps.Shutdown(context.Background())
	}()

	logger := logx.New(output, logx.Config{Level: "info", Format: "text"})
	report, err := app.RunVectorRebuild(ctx, cfg, deps, logger)
	if err != nil {
		return fmt.Errorf("vector rebuild: %w", err)
	}
	fmt.Fprintf(output, "[vmm-migrate] vector rebuild completed: mode=%s projects=%d memories=%d durable_rows=%d vector_rows=%d\n",
		report.Mode, report.ProjectCount, report.MemoryCount, report.DurableRowsUpdated, report.VectorRowsRebuilt)
	return nil
}

// acquireVectorRebuildRuntimeGuard binds the configured runtime listen address for the whole maintenance window so vector rebuild can only start after the service stops and no later restart can race with the destructive rewrite.
// acquireVectorRebuildRuntimeGuard 用于在整个维护窗口期间占用配置里的运行时监听地址，确保向量重建只能在服务停掉后开始，并阻止后续重建过程中服务被重新拉起与破坏性改写并发。
func acquireVectorRebuildRuntimeGuard(cfg config.Config) (net.Listener, error) {
	listenAddr := strings.TrimSpace(cfg.GRPC.ListenAddr)
	if listenAddr == "" {
		return nil, fmt.Errorf("grpc.listen_addr is required before vector rebuild can verify the runtime is stopped")
	}
	probe, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("vector rebuild requires the runtime service to be stopped first: grpc.listen_addr %q is still occupied or unavailable: %w", listenAddr, err)
	}
	return probe, nil
}

// confirmVectorRebuild requires one explicit operator acknowledgement before destructive vector migration begins.
// confirmVectorRebuild 用于在破坏性向量迁移开始前要求操作者做一次显式确认。
func confirmVectorRebuild(input io.Reader, output io.Writer, cfg config.Config) error {
	mode := "split-lancedb"
	scope := "将先清空 SQLite durable 向量并重建 LanceDB，再重新生成 active 向量"
	if cfg.UsesCombinedPostgres() {
		mode = "combined-postgres"
		scope = "将重建 PostgreSQL embedding 列维度并回填 active 向量"
	}
	fmt.Fprintf(output, "[vmm-migrate] vector rebuild mode=%s\n", mode)
	fmt.Fprintf(output, "[vmm-migrate] 执行前必须先停止 vmm-local / gRPC 运行时服务；命令执行期间也会继续占住该监听地址，阻止重建途中重新拉起服务。\n")
	fmt.Fprintf(output, "[vmm-migrate] 仅会处理 active 且未过期的长期记忆，不会重建失活数据，也不会重建垃圾箱数据。\n")
	fmt.Fprintf(output, "[vmm-migrate] %s。\n", scope)
	fmt.Fprint(output, "[vmm-migrate] 请输入 Y 确认继续: ")

	reader := bufio.NewReader(input)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read vector rebuild confirmation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(line), "Y") {
		return fmt.Errorf("vector rebuild cancelled because confirmation was not Y")
	}
	return nil
}
