// vector_rebuild.go implements the standalone vector rebuild command used for embedding-model and embedding-dimension migrations.
// vector_rebuild.go 用于实现独立的向量重建命令，服务 embedding 模型和 embedding 维度迁移。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// runMaintenanceVectorRebuild rebuilds durable vectors with the configured standalone provider.
// runMaintenanceVectorRebuild 用于通过配置的独立供应商重建持久向量。
func runMaintenanceVectorRebuild(
	ctx context.Context,
	cfg config.Config,
	input io.Reader,
	output io.Writer,
	confirmed bool,
) error {
	return runMaintenanceVectorRebuildWithProgressFile(ctx, cfg, input, output, confirmed, "")
}

// runMaintenanceVectorRebuildWithProgressFile runs standalone vector rebuild with an optional progress file.
// runMaintenanceVectorRebuildWithProgressFile 使用可选的进度文件执行独立向量重建。
func runMaintenanceVectorRebuildWithProgressFile(
	ctx context.Context,
	cfg config.Config,
	input io.Reader,
	output io.Writer,
	confirmed bool,
	progressFile string,
) error {
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
	if !confirmed {
		if err := confirmVectorRebuild(input, output, cfg); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(output, "[vmm-migrate] vector rebuild confirmation accepted from the explicit host flag")
	}

	deps, err := app.BuildMaintenanceDependencies(cfg)
	if err != nil {
		return fmt.Errorf("build maintenance dependencies: %w", err)
	}
	defer func() {
		_ = deps.Shutdown(context.Background())
	}()

	logger := logx.New(output, logx.Config{Level: "info", Format: "text"})
	report, err := app.RunVectorRebuildWithProgress(ctx, cfg, deps, logger, newVectorRebuildProgressReporter(progressFile))
	if err != nil {
		return fmt.Errorf("vector rebuild: %w", err)
	}
	fmt.Fprintf(output, "[vmm-migrate] vector rebuild completed: mode=%s projects=%d memories=%d durable_rows=%d vector_rows=%d\n",
		report.Mode, report.ProjectCount, report.MemoryCount, report.DurableRowsUpdated, report.VectorRowsRebuilt)
	return nil
}

// vectorRebuildProgressFilePayload is the stable machine-readable contract consumed by the host.
// vectorRebuildProgressFilePayload 是宿主消费的稳定机器可读契约。
type vectorRebuildProgressFilePayload struct {
	// Stage identifies the current machine-readable rebuild sub-stage.
	// Stage 标识机器可读的当前重建子阶段。
	Stage string `json:"stage"`
	// Processed records the completed items in the current sub-stage.
	// Processed 记录当前子阶段已完成的项目数。
	Processed int `json:"processed"`
	// Total records the total items in the current sub-stage.
	// Total 记录当前子阶段的项目总数。
	Total int `json:"total"`
	// UpdatedAtUnixMs prevents the host from accepting an empty or uninitialized record.
	// UpdatedAtUnixMs 防止宿主接受空记录或未初始化记录。
	UpdatedAtUnixMs int64 `json:"updated_at_unix_ms"`
}

// newVectorRebuildProgressReporter creates an atomic JSON writer for completed rebuild batches.
// newVectorRebuildProgressReporter 为已完成的重建批次创建原子 JSON 写入器。
func newVectorRebuildProgressReporter(path string) app.VectorRebuildProgressReporter {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	// writeMu serializes replacement writes so one progress record is always complete.
	// writeMu 串行化替换写入，确保每条进度记录始终完整。
	var writeMu sync.Mutex
	return func(progress app.VectorRebuildProgress) {
		writeMu.Lock()
		defer writeMu.Unlock()
		// payload is the bounded record exchanged between the child and host.
		// payload 是子进程与宿主之间交换的有界记录。
		payload := vectorRebuildProgressFilePayload{
			Stage:           progress.Stage,
			Processed:       progress.Processed,
			Total:           progress.Total,
			UpdatedAtUnixMs: time.Now().UnixMilli(),
		}
		// bytes is serialized before the destination is replaced, preventing partial JSON reads.
		// bytes 在替换目标前完成序列化，防止宿主读到半截 JSON。
		bytes, err := json.Marshal(payload)
		if err != nil {
			return
		}
		if parent := filepath.Dir(path); parent != "." {
			if err := os.MkdirAll(parent, 0o700); err != nil {
				return
			}
		}
		// temporary is the private staging file used for the atomic handoff.
		// temporary 是用于原子交接的私有暂存文件。
		temporary := path + ".partial"
		if err := os.WriteFile(temporary, bytes, 0o600); err != nil {
			return
		}
		// Windows cannot rename over an existing destination, so remove only the exact host-owned target before the final rename.
		// Windows 不能直接覆盖已有目标，因此只删除宿主明确提供的目标，再完成最终改名。
		_ = os.Remove(path)
		if err := os.Rename(temporary, path); err != nil {
			_ = os.Remove(temporary)
		}
	}
}

// acquireVectorRebuildRuntimeGuard binds the configured runtime listen address for the whole maintenance window so vector rebuild can only start after the service stops and no later restart can race with the destructive rewrite.
// acquireVectorRebuildRuntimeGuard 用于在整个维护窗口期间占用配置里的运行时监听地址，确保向量重建只能在服务停掉后开始，并阻止后续重建过程中服务被重新拉起与破坏性改写并发。
func acquireVectorRebuildRuntimeGuard(cfg config.Config) (net.Listener, error) {
	return acquireMaintenanceRuntimeGuard(cfg, "vector rebuild")
}

// acquireMaintenanceRuntimeGuard binds the configured VMM listener for one maintenance action so the runtime cannot concurrently mutate the same stores.
// acquireMaintenanceRuntimeGuard 为一次维护动作占用 VMM 监听地址，防止运行时并发修改相同存储。
func acquireMaintenanceRuntimeGuard(cfg config.Config, action string) (net.Listener, error) {
	listenAddr := strings.TrimSpace(cfg.GRPC.ListenAddr)
	if listenAddr == "" {
		return nil, fmt.Errorf("grpc.listen_addr is required before %s can verify the runtime is stopped", action)
	}
	_, portText, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("%s requires grpc.listen_addr in host:port form: %w", action, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("%s requires grpc.listen_addr port between 1 and 65535, got %q", action, portText)
	}
	probe, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("%s requires the runtime service to be stopped first: grpc.listen_addr %q is still occupied or unavailable: %w", action, listenAddr, err)
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
