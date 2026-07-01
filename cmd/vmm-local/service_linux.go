//go:build linux

// service_linux.go registers vmm-local as a systemd service on Linux.
// service_linux.go 用于在 Linux 上把 vmm-local 注册为 systemd 服务。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runServiceRuntime runs the same foreground runtime because systemd supervises ordinary long-running processes directly.
// runServiceRuntime 用于运行同一套前台运行时，因为 systemd 可以直接监管普通长驻进程。
func runServiceRuntime(_ string, run func(context.Context, func()) error) error {
	return run(context.Background(), nil)
}

// applyServiceCommand maps portable service lifecycle actions onto systemd unit operations.
// applyServiceCommand 用于把跨平台服务生命周期动作映射到 systemd unit 操作。
func applyServiceCommand(command serviceCommand, exePath string, binDir string) error {
	unitName := command.name + ".service"
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	switch command.action {
	case "install":
		content := renderSystemdUnit(command.name, exePath, binDir)
		if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write systemd unit %q: %w", unitPath, err)
		}
		if err := runCommand("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if err := runCommand("systemctl", "enable", unitName); err != nil {
			return err
		}
		fmt.Printf("installed systemd service %q\n", command.name)
		return nil
	case "uninstall":
		_ = runCommand("systemctl", "stop", unitName)
		_ = runCommand("systemctl", "disable", unitName)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove systemd unit %q: %w", unitPath, err)
		}
		if err := runCommand("systemctl", "daemon-reload"); err != nil {
			return err
		}
		fmt.Printf("uninstalled systemd service %q\n", command.name)
		return nil
	case "start":
		return runCommand("systemctl", "start", unitName)
	case "stop":
		return runCommand("systemctl", "stop", unitName)
	case "status":
		return querySystemdServiceStatus(unitName)
	default:
		return fmt.Errorf("unsupported service action %q", command.action)
	}
}

// renderSystemdUnit builds a minimal unit that only passes the service name back to vmm-local and keeps configuration discovery unchanged.
// renderSystemdUnit 用于构建最小 unit，只把服务名传回 vmm-local，并保持配置发现逻辑不变。
func renderSystemdUnit(name string, exePath string, binDir string) string {
	return fmt.Sprintf(`[Unit]
Description=%s local gRPC runtime service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s service run %s
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
`, name, systemdQuote(binDir), systemdQuote(exePath), name)
}

// systemdQuote quotes one systemd command token so paths with spaces still resolve without invoking a shell.
// systemdQuote 用于引用一个 systemd 命令令牌，让包含空格的路径无需 shell 也能正确解析。
func systemdQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// runCommand executes a platform service-manager command and preserves its combined output in any returned error.
// runCommand 用于执行平台服务管理命令，并在返回错误中保留组合输出。
func runCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v failed: %w\n%s", name, args, err, strings.TrimSpace(string(output)))
	}
	if len(output) > 0 {
		fmt.Print(string(output))
	}
	return nil
}

// querySystemdServiceStatus renders installed inactive units as normal status output instead of treating systemctl status's non-zero exit as a command failure.
// querySystemdServiceStatus 用于把已安装但未运行的 unit 渲染为正常状态输出，而不是把 systemctl status 的非零退出码当成命令失败。
func querySystemdServiceStatus(unitName string) error {
	output, err := exec.Command(
		"systemctl",
		"show",
		"--property=LoadState",
		"--property=ActiveState",
		"--property=SubState",
		"--property=UnitFileState",
		unitName,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("query systemd service %q: %w\n%s", unitName, err, strings.TrimSpace(string(output)))
	}

	status := parseSystemdStatusOutput(string(output))
	if status.LoadState == "not-found" || status.LoadState == "" {
		return fmt.Errorf("systemd service %q is not installed", unitName)
	}
	fmt.Printf("%s", fallbackStatusValue(status.ActiveState, "unknown"))
	if strings.TrimSpace(status.SubState) != "" {
		fmt.Printf(" (%s)", status.SubState)
	}
	if strings.TrimSpace(status.UnitFileState) != "" {
		fmt.Printf(" %s", status.UnitFileState)
	}
	fmt.Println()
	return nil
}

// systemdStatus stores the small systemctl show projection needed for stable service status output.
// systemdStatus 用于保存稳定服务状态输出所需的精简 systemctl show 投影。
type systemdStatus struct {
	// LoadState reports whether systemd knows the unit file.
	// LoadState 用于报告 systemd 是否认识该 unit 文件。
	LoadState string

	// ActiveState reports the high-level runtime state such as active or inactive.
	// ActiveState 用于报告 active 或 inactive 等高层运行状态。
	ActiveState string

	// SubState reports the lower-level runtime state such as running or dead.
	// SubState 用于报告 running 或 dead 等更细粒度运行状态。
	SubState string

	// UnitFileState reports whether the unit is enabled, disabled, static, or another install state.
	// UnitFileState 用于报告 unit 是 enabled、disabled、static 或其他安装状态。
	UnitFileState string
}

// parseSystemdStatusOutput parses key-value systemctl show output without relying on property ordering.
// parseSystemdStatusOutput 用于解析 systemctl show 的键值输出，避免依赖属性顺序。
func parseSystemdStatusOutput(output string) systemdStatus {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	values := map[string]string{}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return systemdStatus{
		LoadState:     values["LoadState"],
		ActiveState:   values["ActiveState"],
		SubState:      values["SubState"],
		UnitFileState: values["UnitFileState"],
	}
}

// fallbackStatusValue returns a placeholder only when the platform status field is empty.
// fallbackStatusValue 用于仅在平台状态字段为空时返回占位值。
func fallbackStatusValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
