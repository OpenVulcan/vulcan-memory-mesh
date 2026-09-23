//go:build windows

// service_windows.go integrates vmm-local with the Windows Service Control Manager.
// service_windows.go 用于把 vmm-local 接入 Windows 服务控制管理器。
package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// serviceDescription is the native service description shown by Windows service-management tools.
// serviceDescription 是 Windows 服务管理工具中显示的原生服务描述。
const serviceDescription = "VulcanMemoryMesh local gRPC runtime service"

const (
	// windowsServiceStartWaitHintMillis tells SCM that VMM may spend time loading config, dynamic libraries, and storage before it binds gRPC.
	// windowsServiceStartWaitHintMillis 用于告诉 SCM，VMM 在绑定 gRPC 前可能需要加载配置、动态库和存储。
	windowsServiceStartWaitHintMillis = 10000

	// windowsServiceStartCheckpointInterval controls how often long startup progress is reported while waiting for the runtime listener.
	// windowsServiceStartCheckpointInterval 用于控制等待运行时监听器期间上报长启动进度的频率。
	windowsServiceStartCheckpointInterval = 5 * time.Second

	// windowsServiceStartPollInterval controls how frequently the CLI observes SCM state while waiting for service start completion.
	// windowsServiceStartPollInterval 用于控制 CLI 等待服务启动完成时观察 SCM 状态的频率。
	windowsServiceStartPollInterval = 500 * time.Millisecond

	// windowsServiceDefaultStartWaitHint is used when SCM reports no wait hint while the service is still pending.
	// windowsServiceDefaultStartWaitHint 用于在服务仍处于 pending 但 SCM 未报告 wait hint 时提供默认等待窗口。
	windowsServiceDefaultStartWaitHint = 10 * time.Second

	// windowsServiceMinStartWaitHint prevents tiny or transient wait hints from causing false startup timeouts.
	// windowsServiceMinStartWaitHint 用于避免过小或瞬态 wait hint 导致启动超时误判。
	windowsServiceMinStartWaitHint = 2 * time.Second

	// windowsServiceMaxStartWaitHint caps one stagnant checkpoint window while still allowing the deadline to extend whenever SCM progress advances.
	// windowsServiceMaxStartWaitHint 用于限制单个停滞 checkpoint 的等待窗口，同时仍允许在 SCM 进度推进时延长截止时间。
	windowsServiceMaxStartWaitHint = 30 * time.Second

	// windowsServiceStartHardTimeout caps the full service startup window so checkpoint progress cannot mask a permanently stuck runtime.
	// windowsServiceStartHardTimeout 用于限制完整服务启动窗口，避免 checkpoint 进度掩盖永久卡住的运行时。
	windowsServiceStartHardTimeout = 5 * time.Minute
)

// windowsServiceHandler bridges SCM stop/shutdown events into the shared application context.
// windowsServiceHandler 用于把 SCM 的停止和关机事件桥接到共享应用上下文。
type windowsServiceHandler struct {
	// run starts the shared VMM runtime with a cancellable context supplied by SCM stop events.
	// run 用于使用 SCM 停止事件提供的可取消上下文启动共享 VMM 运行时。
	run func(context.Context, func()) error
}

// Execute runs the normal VMM runtime under Windows SCM supervision and reports lifecycle state transitions.
// Execute 用于在 Windows SCM 监管下运行普通 VMM 运行时，并回报生命周期状态变化。
func (h windowsServiceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checkPoint := uint32(1)
	statuses <- svc.Status{State: svc.StartPending, CheckPoint: checkPoint, WaitHint: windowsServiceStartWaitHintMillis}
	errCh := make(chan error, 1)
	readyCh := make(chan struct{}, 1)
	startTicker := time.NewTicker(windowsServiceStartCheckpointInterval)
	defer startTicker.Stop()
	startTick := startTicker.C
	startDeadline := time.NewTimer(windowsServiceStartHardTimeout)
	defer startDeadline.Stop()
	startDeadlineC := startDeadline.C
	go func() {
		errCh <- h.run(ctx, func() {
			select {
			case readyCh <- struct{}{}:
			default:
			}
		})
	}()

	for {
		select {
		case <-startTick:
			checkPoint++
			statuses <- svc.Status{State: svc.StartPending, CheckPoint: checkPoint, WaitHint: windowsServiceStartWaitHintMillis}
		case <-startDeadlineC:
			cancel()
			statuses <- svc.Status{State: svc.StopPending}
			return false, 1
		case <-readyCh:
			startTicker.Stop()
			startTick = nil
			if !startDeadline.Stop() {
				select {
				case <-startDeadlineC:
				default:
				}
			}
			startDeadlineC = nil
			statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-errCh
				if err != nil {
					return false, 1
				}
				return false, 0
			default:
				continue
			}
		case err := <-errCh:
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}

// runServiceRuntime enters SCM mode when started by Windows, otherwise it keeps the hidden run command debuggable as a foreground process.
// runServiceRuntime 用于在 Windows 启动服务时进入 SCM 模式，否则让隐藏 run 命令仍可作为前台进程调试。
func runServiceRuntime(name string, run func(context.Context, func()) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect windows service mode: %w", err)
	}
	if !isService {
		return run(context.Background(), nil)
	}
	if err := svc.Run(name, windowsServiceHandler{run: run}); err != nil {
		return fmt.Errorf("run windows service %q: %w", name, err)
	}
	return nil
}

// applyServiceCommand maps portable service lifecycle actions onto Windows SCM operations.
// applyServiceCommand 用于把跨平台服务生命周期动作映射到 Windows SCM 操作。
func applyServiceCommand(command serviceCommand, exePath string, _ string) error {
	if command.user != "" {
		return fmt.Errorf("-user is supported only on Linux and macOS service installations")
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect windows service manager: %w", err)
	}
	defer manager.Disconnect()

	switch command.action {
	case "install":
		startType := uint32(mgr.StartManual)
		if command.autoStart {
			startType = mgr.StartAutomatic
		}
		service, err := manager.OpenService(command.name)
		if err == nil {
			defer service.Close()
			config, configErr := service.Config()
			if configErr != nil {
				return fmt.Errorf("query existing windows service %q configuration: %w", command.name, configErr)
			}
			if err := validateWindowsServiceConfig(config, command.name, exePath); err != nil {
				return fmt.Errorf("refusing to replace non-VMM windows service %q: %w", command.name, err)
			}
			config.BinaryPathName = windowsServiceCommandLine(exePath, command)
			config.DisplayName = command.name
			config.Description = serviceDescription
			config.StartType = startType
			if err := service.UpdateConfig(config); err != nil {
				return fmt.Errorf("update windows service %q: %w", command.name, err)
			}
			fmt.Printf("updated windows service %q auto_start=%s start_type=%s\n", command.name, windowsServiceAutoStartName(startType), windowsServiceStartTypeName(startType))
			return nil
		}
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("inspect existing windows service %q: %w", command.name, err)
		}
		service, err = manager.CreateService(command.name, exePath, mgr.Config{
			DisplayName: command.name,
			Description: serviceDescription,
			StartType:   startType,
		}, windowsServiceArguments(command)...)
		if err != nil {
			return fmt.Errorf("install windows service %q: %w", command.name, err)
		}
		defer service.Close()
		fmt.Printf("installed windows service %q auto_start=%s start_type=%s\n", command.name, windowsServiceAutoStartName(startType), windowsServiceStartTypeName(startType))
		return nil
	case "uninstall":
		service, err := openWindowsService(manager, command.name, exePath)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			fmt.Print("state=not-installed\nauto_start=false\n")
			return nil
		}
		if err != nil {
			return err
		}
		defer service.Close()
		if err := stopWindowsService(service); err != nil {
			return fmt.Errorf("stop windows service %q before uninstall: %w", command.name, err)
		}
		if err := service.Delete(); err != nil {
			return fmt.Errorf("uninstall windows service %q: %w", command.name, err)
		}
		fmt.Printf("uninstalled windows service %q\n", command.name)
		return nil
	case "start":
		service, err := openWindowsService(manager, command.name, exePath)
		if err != nil {
			return err
		}
		defer service.Close()
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("query windows service %q before start: %w", command.name, err)
		}
		if status.State != svc.Running {
			if err := service.Start(); err != nil {
				return fmt.Errorf("start windows service %q: %w", command.name, err)
			}
		}
		if err := waitWindowsServiceRunning(service); err != nil {
			return err
		}
		fmt.Printf("started windows service %q\n", command.name)
		return nil
	case "stop":
		service, err := openWindowsService(manager, command.name, exePath)
		if err != nil {
			return err
		}
		defer service.Close()
		if err := stopWindowsService(service); err != nil {
			return err
		}
		fmt.Printf("stopped windows service %q\n", command.name)
		return nil
	case "restart":
		service, err := openWindowsService(manager, command.name, exePath)
		if err != nil {
			return err
		}
		defer service.Close()
		if err := stopWindowsService(service); err != nil {
			return err
		}
		if err := service.Start(); err != nil {
			return fmt.Errorf("restart windows service %q: %w", command.name, err)
		}
		if err := waitWindowsServiceRunning(service); err != nil {
			return err
		}
		fmt.Printf("restarted windows service %q\n", command.name)
		return nil
	case "enable", "disable":
		service, err := openWindowsService(manager, command.name, exePath)
		if err != nil {
			return err
		}
		defer service.Close()
		config, err := service.Config()
		if err != nil {
			return fmt.Errorf("query windows service %q configuration: %w", command.name, err)
		}
		if command.action == "enable" {
			config.StartType = mgr.StartAutomatic
		} else {
			// Manual start keeps an explicitly disabled service available for operator-controlled starts.
			// 手动启动会保留服务的人工启动能力，不会把服务锁定为不可启动。
			config.StartType = mgr.StartManual
		}
		if err := service.UpdateConfig(config); err != nil {
			return fmt.Errorf("set windows service %q start type: %w", command.name, err)
		}
		fmt.Printf("service %q auto_start=%s start_type=%s\n", command.name, windowsServiceAutoStartName(config.StartType), windowsServiceStartTypeName(config.StartType))
		return nil
	case "status":
		service, err := openWindowsService(manager, command.name, exePath)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			fmt.Print("state=not-installed\nauto_start=false\n")
			return nil
		}
		if err != nil {
			return err
		}
		defer service.Close()
		status, err := service.Query()
		if err != nil {
			return fmt.Errorf("query windows service %q: %w", command.name, err)
		}
		config, err := service.Config()
		if err != nil {
			return fmt.Errorf("query windows service %q configuration: %w", command.name, err)
		}
		fmt.Print(renderWindowsServiceStatus(status.State, config.StartType))
		return nil
	default:
		return fmt.Errorf("unsupported service action %q", command.action)
	}
}

// windowsServiceArguments builds the argument vector SCM stores with the service executable, preserving config paths as separate escaped arguments.
// windowsServiceArguments 用于构建 SCM 随服务可执行文件保存的参数向量，把配置路径保持为独立参数并交给原生转义。
func windowsServiceArguments(command serviceCommand) []string {
	arguments := []string{"service", "run", command.name}
	if command.configPath != "" {
		arguments = append(arguments, "-config", command.configPath)
	}
	return arguments
}

// windowsServiceCommandLine reproduces the exact command line stored by SCM for a generated VMM service.
// windowsServiceCommandLine 用于复现 SCM 为生成的 VMM 服务保存的精确命令行。
func windowsServiceCommandLine(exePath string, command serviceCommand) string {
	arguments := append([]string{exePath}, windowsServiceArguments(command)...)
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, syscall.EscapeArg(argument))
	}
	return strings.Join(quoted, " ")
}

// validateWindowsServiceConfig accepts only the generated executable, arguments, service type, and identity metadata.
// validateWindowsServiceConfig 只接受生成的可执行文件、参数、服务类型和身份元数据。
func validateWindowsServiceConfig(config mgr.Config, name string, exePath string) error {
	if config.ServiceType != windows.SERVICE_WIN32_OWN_PROCESS {
		return fmt.Errorf("service type mismatch")
	}
	if config.DisplayName != name {
		return fmt.Errorf("display name mismatch")
	}
	if config.Description != serviceDescription {
		return fmt.Errorf("description mismatch")
	}
	arguments, err := windows.DecomposeCommandLine(config.BinaryPathName)
	if err != nil {
		return fmt.Errorf("decode binary path: %w", err)
	}
	if len(arguments) != 4 && len(arguments) != 6 {
		return fmt.Errorf("unexpected service argument count %d", len(arguments))
	}
	if !strings.EqualFold(filepath.Clean(arguments[0]), filepath.Clean(exePath)) {
		return fmt.Errorf("executable mismatch")
	}
	if arguments[1] != "service" || arguments[2] != "run" || arguments[3] != name {
		return fmt.Errorf("service arguments mismatch")
	}
	if len(arguments) == 6 {
		if arguments[4] != "-config" {
			return fmt.Errorf("config flag mismatch")
		}
		if _, err := validateServiceConfigPath(arguments[5]); err != nil {
			return fmt.Errorf("invalid persisted config path: %w", err)
		}
	}
	return nil
}

// renderWindowsServiceStatus emits one key-value field per line for strict manager parsing.
// renderWindowsServiceStatus 按每行一个键值对输出，满足管理器的严格解析契约。
func renderWindowsServiceStatus(state svc.State, startType uint32) string {
	return fmt.Sprintf("state=%s\nauto_start=%s\nstart_type=%s\n", windowsServiceStateName(state), windowsServiceAutoStartName(startType), windowsServiceStartTypeName(startType))
}

// openWindowsService opens an existing service and wraps the platform error with the requested service name.
// openWindowsService 用于打开一个已存在服务，并用请求的服务名包装平台错误。
func openWindowsService(manager *mgr.Mgr, name string, exePath string) (*mgr.Service, error) {
	service, err := manager.OpenService(name)
	if err != nil {
		return nil, fmt.Errorf("open windows service %q: %w", name, err)
	}
	config, err := service.Config()
	if err != nil {
		service.Close()
		return nil, fmt.Errorf("query windows service %q configuration: %w", name, err)
	}
	if err := validateWindowsServiceConfig(config, name, exePath); err != nil {
		service.Close()
		return nil, fmt.Errorf("refusing to operate on non-VMM windows service %q: %w", name, err)
	}
	return service, nil
}

// stopWindowsService requests a graceful stop and waits briefly so dependent storage resources can flush before deletion or restart.
// stopWindowsService 用于请求优雅停服并短暂等待，让依赖存储资源在删除或重启前完成收尾。
func stopWindowsService(service *mgr.Service) error {
	status, err := service.Query()
	if err != nil {
		return fmt.Errorf("query windows service before stop: %w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := service.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop windows service: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err = service.Query()
		if err != nil {
			return fmt.Errorf("query windows service while stopping: %w", err)
		}
		if status.State == svc.Stopped {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("stop windows service: timed out waiting for stopped state")
}

// waitWindowsServiceRunning waits until SCM reports Running so the CLI success message matches the service handler readiness contract.
// waitWindowsServiceRunning 用于等待 SCM 报告 Running，使 CLI 成功信息与服务处理器的就绪契约一致。
func waitWindowsServiceRunning(service *mgr.Service) error {
	return waitWindowsServiceRunningWithQuery(service.Query, time.Now, time.Sleep)
}

// waitWindowsServiceRunningWithQuery follows SCM checkpoint progress instead of using a fixed wall-clock timeout, so slow but healthy startup is not reported as failure.
// waitWindowsServiceRunningWithQuery 用于跟随 SCM checkpoint 进度而不是使用固定墙钟超时，避免把缓慢但健康的启动误报为失败。
func waitWindowsServiceRunningWithQuery(query func() (svc.Status, error), now func() time.Time, sleep func(time.Duration)) error {
	lastCheckpoint := uint32(0)
	startedAt := now()
	hardDeadline := startedAt.Add(windowsServiceStartHardTimeout)
	deadline := startedAt.Add(windowsServiceDefaultStartWaitHint)
	for {
		current := now()
		if current.After(hardDeadline) {
			return fmt.Errorf("start windows service: timed out waiting for running state after %s hard limit", windowsServiceStartHardTimeout)
		}
		if current.After(deadline) {
			return fmt.Errorf("start windows service: timed out waiting for running state after checkpoint %d", lastCheckpoint)
		}

		status, err := query()
		if err != nil {
			return fmt.Errorf("query windows service while starting: %w", err)
		}
		switch status.State {
		case svc.Running:
			return nil
		case svc.Stopped:
			return fmt.Errorf("start windows service: service stopped before reaching running state")
		case svc.StartPending:
			if status.CheckPoint != lastCheckpoint {
				lastCheckpoint = status.CheckPoint
				deadline = minTime(current.Add(normalizeWindowsServiceWaitHint(status.WaitHint)), hardDeadline)
			}
		}

		sleep(windowsServiceStartPollInterval)
	}
}

// normalizeWindowsServiceWaitHint clamps SCM wait hints into a practical single-checkpoint window while preserving progress-based deadline extension.
// normalizeWindowsServiceWaitHint 用于把 SCM wait hint 钳制为实用的单 checkpoint 等待窗口，同时保留基于进度的截止时间延长能力。
func normalizeWindowsServiceWaitHint(waitHintMillis uint32) time.Duration {
	if waitHintMillis == 0 {
		return windowsServiceDefaultStartWaitHint
	}
	waitHint := time.Duration(waitHintMillis) * time.Millisecond
	if waitHint < windowsServiceMinStartWaitHint {
		return windowsServiceMinStartWaitHint
	}
	if waitHint > windowsServiceMaxStartWaitHint {
		return windowsServiceMaxStartWaitHint
	}
	return waitHint
}

// minTime returns the earlier of two timestamps so progress-based wait windows cannot exceed the global startup deadline.
// minTime 用于返回两个时间点中更早的一个，确保基于进度的等待窗口不会越过全局启动截止时间。
func minTime(a time.Time, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// windowsServiceStateName renders SCM states in stable lowercase text for scripts and humans.
// windowsServiceStateName 用于把 SCM 状态渲染成便于脚本和人工读取的稳定小写文本。
func windowsServiceStateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown-%d", state)
	}
}

// windowsServiceAutoStartName renders the SCM start policy as a stable manager-facing value.
// windowsServiceAutoStartName 用于把 SCM 启动策略渲染成稳定的管理器状态值。
func windowsServiceAutoStartName(startType uint32) string {
	if startType == mgr.StartAutomatic {
		return "enabled"
	}
	return "disabled"
}

// windowsServiceStartTypeName preserves the distinction between manual and disabled native SCM policies.
// windowsServiceStartTypeName 用于保留手动与禁用这两种原生 SCM 策略的区别。
func windowsServiceStartTypeName(startType uint32) string {
	switch startType {
	case mgr.StartAutomatic:
		return "automatic"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("unknown-%d", startType)
	}
}
