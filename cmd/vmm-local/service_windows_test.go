//go:build windows

// service_windows_test.go verifies Windows service-control helpers without touching the real Service Control Manager.
// service_windows_test.go 用于在不触碰真实服务控制管理器的情况下验证 Windows 服务控制辅助逻辑。
package main

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
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

// TestWindowsServiceStartTypeNames verifies status output distinguishes automatic, manual, and disabled SCM policies.
// TestWindowsServiceStartTypeNames 用于验证状态输出能区分自动、手动和禁用的 SCM 策略。
func TestWindowsServiceStartTypeNames(t *testing.T) {
	cases := []struct {
		startType uint32
		name      string
		autoStart string
	}{
		{startType: mgr.StartAutomatic, name: "automatic", autoStart: "enabled"},
		{startType: mgr.StartManual, name: "manual", autoStart: "disabled"},
		{startType: mgr.StartDisabled, name: "disabled", autoStart: "disabled"},
	}
	for _, test := range cases {
		if got := windowsServiceStartTypeName(test.startType); got != test.name {
			t.Fatalf("start type %d = %q, want %q", test.startType, got, test.name)
		}
		if got := windowsServiceAutoStartName(test.startType); got != test.autoStart {
			t.Fatalf("auto-start for %d = %q, want %q", test.startType, got, test.autoStart)
		}
	}
}

// TestWindowsServiceArgumentsPersistAbsoluteConfig verifies SCM receives the same config path that service run will parse.
// TestWindowsServiceArgumentsPersistAbsoluteConfig 用于验证 SCM 接收的配置路径与 service run 解析的路径一致。
func TestWindowsServiceArgumentsPersistAbsoluteConfig(t *testing.T) {
	command := serviceCommand{
		name:       "VMM",
		configPath: `C:\ProgramData\VMM Manager\config.yaml`,
	}
	arguments := windowsServiceArguments(command)
	if len(arguments) != 5 || arguments[0] != "service" || arguments[1] != "run" || arguments[2] != "VMM" || arguments[3] != "-config" || arguments[4] != command.configPath {
		t.Fatalf("unexpected SCM arguments: %#v", arguments)
	}
}

// TestValidateWindowsServiceConfigRejectsNameCollisions verifies SCM identity includes the exact executable and arguments.
// TestValidateWindowsServiceConfigRejectsNameCollisions 验证 SCM 身份包含精确的可执行文件和参数，拒绝同名冲突。
func TestValidateWindowsServiceConfigRejectsNameCollisions(t *testing.T) {
	command := serviceCommand{
		name:       "VMM",
		configPath: `C:\ProgramData\VMM Manager\config.yaml`,
	}
	exePath := `C:\Program Files\VMM\vmm-local.exe`
	config := mgr.Config{
		ServiceType:    windows.SERVICE_WIN32_OWN_PROCESS,
		DisplayName:    command.name,
		Description:    serviceDescription,
		BinaryPathName: windowsServiceCommandLine(exePath, command),
	}
	if err := validateWindowsServiceConfig(config, command.name, exePath); err != nil {
		t.Fatalf("generated Windows service identity rejected: %v", err)
	}
	foreign := config
	foreign.BinaryPathName = windowsServiceCommandLine(`C:\Program Files\Other\other.exe`, command)
	if err := validateWindowsServiceConfig(foreign, command.name, exePath); err == nil {
		t.Fatal("foreign executable should be rejected")
	}
	foreign = config
	foreign.Description = "unrelated service"
	if err := validateWindowsServiceConfig(foreign, command.name, exePath); err == nil {
		t.Fatal("foreign description should be rejected")
	}
	foreign = config
	foreign.BinaryPathName = `"C:\Program Files\VMM\vmm-local.exe" service run VMM --unsafe`
	if err := validateWindowsServiceConfig(foreign, command.name, exePath); err == nil {
		t.Fatal("unexpected service argument should be rejected")
	}
}

// TestRenderWindowsServiceStatusUsesStableKeys verifies Windows status is line-oriented and uses the running vocabulary.
// TestRenderWindowsServiceStatusUsesStableKeys 验证 Windows 状态逐行输出，并使用 running 状态词汇。
func TestRenderWindowsServiceStatusUsesStableKeys(t *testing.T) {
	want := "state=running\nauto_start=enabled\nstart_type=automatic\n"
	if got := renderWindowsServiceStatus(svc.Running, mgr.StartAutomatic); got != want {
		t.Fatalf("Windows service status = %q, want %q", got, want)
	}
}
