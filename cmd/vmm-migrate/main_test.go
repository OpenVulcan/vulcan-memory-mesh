// main_test.go verifies the standalone maintenance CLI entrypoint wiring so signal-aware cancellation reaches one-shot maintenance flows.
// main_test.go 用于验证独立维护 CLI 入口的装配，确保带信号感知的取消能力能够传入一次性维护流程。
package main

import (
	"context"
	"errors"
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
