// vector_rebuild_test.go verifies the standalone vector rebuild CLI guardrails for confirmation prompts and action selection.
// vector_rebuild_test.go 用于验证独立向量重建 CLI 的确认提示与动作选择护栏。
package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestConfirmVectorRebuildRequiresExplicitY verifies destructive rebuilds only continue after the operator explicitly enters Y.
// TestConfirmVectorRebuildRequiresExplicitY 用于验证破坏性重建只有在操作者显式输入 Y 后才会继续。
func TestConfirmVectorRebuildRequiresExplicitY(t *testing.T) {
	cfg := config.DefaultLocal()
	output := &bytes.Buffer{}
	err := confirmVectorRebuild(strings.NewReader("n\n"), output, cfg)
	if err == nil {
		t.Fatal("expected confirmation failure")
	}
	if !strings.Contains(output.String(), "请输入 Y 确认继续") {
		t.Fatalf("expected confirmation prompt, got %q", output.String())
	}
	if !strings.Contains(output.String(), "必须先停止 vmm-local / gRPC 运行时服务") {
		t.Fatalf("expected offline-only explanation, got %q", output.String())
	}
}

// TestConfirmVectorRebuildExplainsCombinedMode verifies combined PostgreSQL mode describes the embedding-column rebuild before confirmation.
// TestConfirmVectorRebuildExplainsCombinedMode 用于验证合并 PostgreSQL 模式会在确认前说明 embedding 列重建行为。
func TestConfirmVectorRebuildExplainsCombinedMode(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "combined"
	cfg.Storage.CombinedProvider = "postgres"
	output := &bytes.Buffer{}
	if err := confirmVectorRebuild(strings.NewReader("Y\n"), output, cfg); err != nil {
		t.Fatalf("confirmVectorRebuild returned error: %v", err)
	}
	if !strings.Contains(output.String(), "重建 PostgreSQL embedding 列维度") {
		t.Fatalf("expected combined-mode explanation, got %q", output.String())
	}
}

// TestAcquireVectorRebuildRuntimeGuardAllowsFreeListenAddr verifies the offline guard can claim a free runtime listen address and hold it for the maintenance window.
// TestAcquireVectorRebuildRuntimeGuardAllowsFreeListenAddr 用于验证当运行时监听地址空闲时，停服保护可以成功占住它并在整个维护窗口内持有。
func TestAcquireVectorRebuildRuntimeGuardAllowsFreeListenAddr(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	cfg := config.DefaultLocal()
	cfg.GRPC.ListenAddr = addr
	guard, err := acquireVectorRebuildRuntimeGuard(cfg)
	if err != nil {
		t.Fatalf("acquireVectorRebuildRuntimeGuard returned error: %v", err)
	}
	defer func() { _ = guard.Close() }()
}

// TestAcquireVectorRebuildRuntimeGuardRejectsOccupiedListenAddr verifies vector rebuild refuses to start while another process is still bound to the configured runtime address.
// TestAcquireVectorRebuildRuntimeGuardRejectsOccupiedListenAddr 用于验证当其他进程仍占用配置中的运行时地址时，向量重建会直接拒绝启动。
func TestAcquireVectorRebuildRuntimeGuardRejectsOccupiedListenAddr(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied addr: %v", err)
	}
	defer func() { _ = listener.Close() }()

	cfg := config.DefaultLocal()
	cfg.GRPC.ListenAddr = listener.Addr().String()
	_, err = acquireVectorRebuildRuntimeGuard(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires the runtime service to be stopped first") {
		t.Fatalf("unexpected occupied-listen error: %v", err)
	}
}

// TestRunMaintenanceVectorRebuildRejectsRunningRuntimeBeforeConfirmation verifies the command fails before prompting or touching maintenance dependencies when the runtime service is still listening.
// TestRunMaintenanceVectorRebuildRejectsRunningRuntimeBeforeConfirmation 用于验证当运行时服务仍在监听时，命令会在确认提示和维护依赖装配之前直接失败。
func TestRunMaintenanceVectorRebuildRejectsRunningRuntimeBeforeConfirmation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen occupied addr: %v", err)
	}
	defer func() { _ = listener.Close() }()

	cfg := config.DefaultLocal()
	cfg.GRPC.ListenAddr = listener.Addr().String()
	output := &bytes.Buffer{}
	err = runMaintenanceVectorRebuild(context.Background(), cfg, strings.NewReader("Y\n"), output)
	if err == nil || !strings.Contains(err.Error(), "requires the runtime service to be stopped first") {
		t.Fatalf("unexpected runMaintenanceVectorRebuild error: %v", err)
	}
	if strings.Contains(output.String(), "请输入 Y 确认继续") {
		t.Fatalf("expected no confirmation prompt when runtime is still listening, got %q", output.String())
	}
}
