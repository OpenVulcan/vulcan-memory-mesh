// service_command.go implements the portable service-management command parser for the local VMM executable.
// service_command.go 用于实现本地 VMM 可执行文件的跨平台服务管理命令解析。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// defaultServiceName is the stable service name used when operators do not pass an explicit name.
	// defaultServiceName 是运维未显式传入名称时使用的稳定服务名。
	defaultServiceName = "VulcanMemoryMesh"

	// serviceCommandUsage describes the deliberately small service CLI surface so registration never needs runtime configuration arguments.
	// serviceCommandUsage 用于描述刻意收窄的服务 CLI 范围，确保注册服务时不需要运行时配置参数。
	serviceCommandUsage = "usage: vmm-local service {install|uninstall|start|stop|status|run} [service-name]"
)

// serviceNamePattern restricts service names to a portable subset that can safely become a Windows service name, systemd unit filename, and launchd label.
// serviceNamePattern 用于把服务名限制在可安全用于 Windows 服务名、systemd unit 文件名和 launchd label 的跨平台子集内。
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// serviceCommand carries one parsed service-management action and the normalized service name.
// serviceCommand 用于承载一个已解析的服务管理动作和归一化后的服务名。
type serviceCommand struct {
	// action stores the requested lifecycle operation so platform adapters can map it to native service-manager calls.
	// action 用于保存请求的生命周期操作，让平台适配器可以映射到原生服务管理调用。
	action string

	// name stores the validated service name that is the only optional argument accepted by the service CLI.
	// name 用于保存已校验的服务名，这是服务 CLI 接受的唯一可选参数。
	name string
}

// parseServiceCommand recognizes the service subcommand while leaving normal runtime flags untouched.
// parseServiceCommand 用于识别 service 子命令，同时让普通运行时参数保持原有解析方式。
func parseServiceCommand(args []string) (serviceCommand, bool, error) {
	if len(args) == 0 || args[0] != "service" {
		return serviceCommand{}, false, nil
	}
	if len(args) < 2 {
		return serviceCommand{}, true, fmt.Errorf("%s", serviceCommandUsage)
	}
	if len(args) > 3 {
		return serviceCommand{}, true, fmt.Errorf("%s", serviceCommandUsage)
	}

	action := strings.TrimSpace(strings.ToLower(args[1]))
	if !isServiceAction(action) {
		return serviceCommand{}, true, fmt.Errorf("unsupported service action %q; %s", args[1], serviceCommandUsage)
	}

	name := defaultServiceName
	if len(args) == 3 {
		name = strings.TrimSpace(args[2])
	}
	if err := validateServiceName(name); err != nil {
		return serviceCommand{}, true, err
	}
	return serviceCommand{action: action, name: name}, true, nil
}

// isServiceAction restricts service commands to lifecycle operations that every supported platform can model.
// isServiceAction 用于把服务命令限制在所有受支持平台都能表达的生命周期操作内。
func isServiceAction(action string) bool {
	switch action {
	case "install", "uninstall", "start", "stop", "status", "run":
		return true
	default:
		return false
	}
}

// validateServiceName keeps generated systemd units, launchd labels, and Windows service names path-safe and shell-free.
// validateServiceName 用于确保生成的 systemd unit、launchd label 和 Windows 服务名保持路径安全且不依赖 shell 转义。
func validateServiceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid service name %q: use 1-128 characters from letters, numbers, dot, underscore, or hyphen", name)
	}
	return nil
}

// runServiceCommand executes one service-management command or enters the hidden service runtime path with the packaged binary directory as the working directory.
// runServiceCommand 用于执行一个服务管理命令，或以打包二进制目录作为工作目录进入隐藏的服务运行路径。
func runServiceCommand(command serviceCommand, exePath string) error {
	if command.action == "run" {
		serviceWD := filepath.Dir(exePath)
		if err := os.Chdir(serviceWD); err != nil {
			return fmt.Errorf("change service working directory to %q: %w", serviceWD, err)
		}
		return runServiceRuntime(command.name, func(ctx context.Context, ready func()) error {
			return runRuntime(ctx, exePath, serviceWD, "", ready)
		})
	}
	binDir := filepath.Dir(exePath)
	return applyServiceCommand(command, exePath, binDir)
}
