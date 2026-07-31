// main_test.go verifies the standalone maintenance CLI entrypoint wiring so signal-aware cancellation reaches one-shot maintenance flows.
// main_test.go 用于验证独立维护 CLI 入口的装配，确保带信号感知的取消能力能够传入一次性维护流程。
package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestBuildSignalAwareMainContextCanBeCanceled verifies the signal-aware main context produces one cancelable context so maintenance commands can share one abort signal.
// TestBuildSignalAwareMainContextCanBeCanceled 用于验证带信号感知的主上下文确实可被取消，让维护命令可以共享同一份中断信号。
func TestBuildSignalAwareMainContextCanBeCanceled(t *testing.T) {
	ctx, stop := buildSignalAwareMainContext()
	stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("expected canceled context, got %v", ctx.Err())
	}
}

// TestLoadMaintenanceRuntimeConfigurationRejectsTwoAuthorities verifies maintenance never guesses between layered and managed configuration.
// TestLoadMaintenanceRuntimeConfigurationRejectsTwoAuthorities 用于验证维护流程绝不会在分层配置与托管配置之间猜测。
func TestLoadMaintenanceRuntimeConfigurationRejectsTwoAuthorities(t *testing.T) {
	_, err := loadMaintenanceRuntimeConfiguration("vmm-migrate", "config.yaml", "managed.json")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected dual-authority error: %v", err)
	}
}

// TestLoadMaintenanceRuntimeConfigurationRoutesManagedErrors verifies a managed path is handled by the strict manifest loader instead of layered config.
// TestLoadMaintenanceRuntimeConfigurationRoutesManagedErrors 用于验证托管路径由严格清单加载器处理，而不会落入分层配置。
func TestLoadMaintenanceRuntimeConfigurationRoutesManagedErrors(t *testing.T) {
	manifestPath := t.TempDir() + string(os.PathSeparator) + "managed.json"
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write invalid managed fixture: %v", err)
	}

	_, err := loadMaintenanceRuntimeConfiguration("vmm-migrate", "", manifestPath)
	if err == nil || !strings.Contains(err.Error(), "load Vulcan managed config") {
		t.Fatalf("unexpected managed-loader error: %v", err)
	}
}
