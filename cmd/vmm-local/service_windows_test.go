//go:build windows

// service_windows_test.go verifies Windows service-control helpers without touching the real Service Control Manager.
// service_windows_test.go 用于在不触碰真实服务控制管理器的情况下验证 Windows 服务控制辅助逻辑。
package main

import (
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

// TestWaitWindowsServiceRunningExtendsDeadlineWhenCheckpointAdvances verifies slow startup remains healthy while SCM keeps reporting progress.
// TestWaitWindowsServiceRunningExtendsDeadlineWhenCheckpointAdvances 用于验证只要 SCM 持续报告进度，缓慢启动仍会被视为健康。
func TestWaitWindowsServiceRunningExtendsDeadlineWhenCheckpointAdvances(t *testing.T) {
	statuses := []svc.Status{
		{State: svc.StartPending, CheckPoint: 1, WaitHint: 2000},
		{State: svc.StartPending, CheckPoint: 2, WaitHint: 2000},
		{State: svc.StartPending, CheckPoint: 3, WaitHint: 2000},
		{State: svc.Running},
	}
	index := 0
	current := time.Unix(0, 0)

	err := waitWindowsServiceRunningWithQuery(
		func() (svc.Status, error) {
			if index >= len(statuses) {
				t.Fatalf("query called after scripted statuses were exhausted")
			}
			status := statuses[index]
			index++
			return status, nil
		},
		func() time.Time {
			return current
		},
		func(duration time.Duration) {
			current = current.Add(duration)
		},
	)
	if err != nil {
		t.Fatalf("expected progressing startup to succeed: %v", err)
	}
	if index != len(statuses) {
		t.Fatalf("expected all statuses to be consumed, got %d of %d", index, len(statuses))
	}
}

// TestWaitWindowsServiceRunningTimesOutWhenCheckpointStalls verifies a pending service without checkpoint progress eventually fails.
// TestWaitWindowsServiceRunningTimesOutWhenCheckpointStalls 用于验证 pending 服务在 checkpoint 停滞时最终失败。
func TestWaitWindowsServiceRunningTimesOutWhenCheckpointStalls(t *testing.T) {
	current := time.Unix(0, 0)

	err := waitWindowsServiceRunningWithQuery(
		func() (svc.Status, error) {
			return svc.Status{State: svc.StartPending, CheckPoint: 1, WaitHint: 2000}, nil
		},
		func() time.Time {
			return current
		},
		func(duration time.Duration) {
			current = current.Add(duration)
		},
	)
	if err == nil {
		t.Fatal("expected stalled checkpoint to time out")
	}
}

// TestWaitWindowsServiceRunningHonorsHardDeadline verifies endlessly advancing checkpoints cannot keep the CLI waiting forever.
// TestWaitWindowsServiceRunningHonorsHardDeadline 用于验证持续推进的 checkpoint 不能让 CLI 永久等待。
func TestWaitWindowsServiceRunningHonorsHardDeadline(t *testing.T) {
	current := time.Unix(0, 0)
	checkpoint := uint32(0)

	err := waitWindowsServiceRunningWithQuery(
		func() (svc.Status, error) {
			checkpoint++
			return svc.Status{State: svc.StartPending, CheckPoint: checkpoint, WaitHint: 2000}, nil
		},
		func() time.Time {
			return current
		},
		func(duration time.Duration) {
			current = current.Add(duration)
		},
	)
	if err == nil {
		t.Fatal("expected endlessly progressing startup to hit the hard deadline")
	}
}

// TestNormalizeWindowsServiceWaitHintClampsExtremes verifies missing and extreme SCM hints stay within the supported checkpoint window.
// TestNormalizeWindowsServiceWaitHintClampsExtremes 用于验证缺失和极端 SCM hint 会被钳制在受支持的 checkpoint 窗口内。
func TestNormalizeWindowsServiceWaitHintClampsExtremes(t *testing.T) {
	if got := normalizeWindowsServiceWaitHint(0); got != windowsServiceDefaultStartWaitHint {
		t.Fatalf("zero wait hint = %v, want %v", got, windowsServiceDefaultStartWaitHint)
	}
	if got := normalizeWindowsServiceWaitHint(1); got != windowsServiceMinStartWaitHint {
		t.Fatalf("tiny wait hint = %v, want %v", got, windowsServiceMinStartWaitHint)
	}
	if got := normalizeWindowsServiceWaitHint(uint32((time.Hour) / time.Millisecond)); got != windowsServiceMaxStartWaitHint {
		t.Fatalf("huge wait hint = %v, want %v", got, windowsServiceMaxStartWaitHint)
	}
}
