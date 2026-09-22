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

// TestHasRemovedManagedConfigFlagRecognizesLegacySpellings verifies both supported flag prefixes are rejected before maintenance parsing.
// TestHasRemovedManagedConfigFlagRecognizesLegacySpellings 用于验证两种旧参数前缀都会在维护参数解析前被拒绝。
func TestHasRemovedManagedConfigFlagRecognizesLegacySpellings(t *testing.T) {
	for _, arg := range []string{"-vulcan-managed-config", "--vulcan-managed-config=managed.json"} {
		if !hasRemovedManagedConfigFlag([]string{arg}) {
			t.Fatalf("hasRemovedManagedConfigFlag(%q) = false", arg)
		}
	}
	if hasRemovedManagedConfigFlag([]string{"-config", "standalone.yaml"}) {
		t.Fatal("standalone config flag was classified as removed managed flag")
	}
}
