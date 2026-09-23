// service_command.go implements the portable service-management command parser for the local VMM executable.
// service_command.go 用于实现本地 VMM 可执行文件的跨平台服务管理命令解析。
package main

import (
	"context"
	"fmt"
	"os"
	osuser "os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// defaultServiceName is the stable service name used when operators do not pass an explicit name.
	// defaultServiceName 是运维未显式传入名称时使用的稳定服务名。
	defaultServiceName = "VulcanMemoryMesh"

	// serviceCommandUsage describes the service CLI, including the persisted configuration and auto-start options.
	// serviceCommandUsage 用于描述服务 CLI，包括持久化配置根和自启动选项。
	serviceCommandUsage = "usage: vmm-local service {install|uninstall|start|stop|restart|enable|disable|status|run} [service-name] [-config <absolute-dir-or-yaml>] [-user <local-account>] [-auto-start=true|false]"
)

// serviceNamePattern restricts service names to a portable subset that can safely become a Windows service name, systemd unit filename, and launchd label.
// serviceNamePattern 用于把服务名限制在可安全用于 Windows 服务名、systemd unit 文件名和 launchd label 的跨平台子集内。
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// serviceUserPattern limits persisted native service identities to account names that are safe in systemd, launchd, and line-oriented status output.
// serviceUserPattern 将持久化的原生服务身份限制为可安全用于 systemd、launchd 和逐行状态输出的账户名。
var serviceUserPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// serviceCommand carries one parsed service-management action and the persisted service settings.
// serviceCommand 用于承载一个已解析的服务管理动作以及需要持久化的服务设置。
type serviceCommand struct {
	// action stores the requested lifecycle operation so platform adapters can map it to native service-manager calls.
	// action 用于保存请求的生命周期操作，让平台适配器可以映射到原生服务管理调用。
	action string

	// name stores the validated service name used by the native service manager.
	// name 用于保存原生服务管理器使用的已校验服务名。
	name string

	// configPath stores the absolute configuration root or YAML file passed to the service process.
	// configPath 用于保存传给服务进程的绝对配置根目录或 YAML 文件路径。
	configPath string

	// autoStart controls whether the installed service is enabled at system boot.
	// autoStart 用于控制已安装服务是否在系统启动时自动启用。
	autoStart bool

	// user stores the explicitly selected local account for Unix service execution.
	// user 保存 Linux/macOS 服务显式选定的本机账户。
	user string
}

// serviceUserIdentity is the validated local account identity persisted by Unix service adapters.
// serviceUserIdentity 是 Unix 服务适配器持久化前校验过的本机账户身份。
type serviceUserIdentity struct {
	// Username is the canonical local account name returned by the operating system account database.
	// Username 是操作系统账户数据库返回的规范本机账户名。
	Username string

	// UID is the numeric user identifier validated while resolving the account.
	// UID 是解析账户时校验过的数字用户标识。
	UID string

	// GID is the numeric primary group identifier validated while resolving the account.
	// GID 是解析账户时校验过的数字主组标识。
	GID string
}

// parseServiceCommand recognizes the service subcommand while leaving normal runtime flags untouched; install without -config keeps the legacy account-home behavior for compatibility.
// parseServiceCommand 用于识别 service 子命令，同时让普通运行时参数保持原有解析方式；不带 -config 的 install 为兼容性保留旧的服务账户 HOME 行为。
func parseServiceCommand(args []string) (serviceCommand, bool, error) {
	if len(args) == 0 || args[0] != "service" {
		return serviceCommand{}, false, nil
	}
	if len(args) < 2 {
		return serviceCommand{}, true, fmt.Errorf("%s", serviceCommandUsage)
	}

	action := strings.TrimSpace(strings.ToLower(args[1]))
	if !isServiceAction(action) {
		return serviceCommand{}, true, fmt.Errorf("unsupported service action %q; %s", args[1], serviceCommandUsage)
	}

	command := serviceCommand{
		action:    action,
		name:      defaultServiceName,
		autoStart: true,
	}
	seenName := false
	seenConfig := false
	seenAutoStart := false
	seenUser := false
	for index := 2; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			flagName, flagValue, hasValue := splitServiceFlag(arg)
			switch flagName {
			case "config":
				if action != "install" && action != "run" {
					return serviceCommand{}, true, fmt.Errorf("service action %q does not accept -config; %s", action, serviceCommandUsage)
				}
				if seenConfig {
					return serviceCommand{}, true, fmt.Errorf("duplicate -config flag; %s", serviceCommandUsage)
				}
				if !hasValue {
					if index+1 >= len(args) {
						return serviceCommand{}, true, fmt.Errorf("-config requires an absolute directory or YAML file; %s", serviceCommandUsage)
					}
					index++
					flagValue = args[index]
				}
				configPath, err := validateServiceConfigPath(flagValue)
				if err != nil {
					return serviceCommand{}, true, err
				}
				command.configPath = configPath
				seenConfig = true
			case "auto-start":
				if action != "install" {
					return serviceCommand{}, true, fmt.Errorf("service action %q does not accept -auto-start; %s", action, serviceCommandUsage)
				}
				if seenAutoStart {
					return serviceCommand{}, true, fmt.Errorf("duplicate -auto-start flag; %s", serviceCommandUsage)
				}
				if !hasValue {
					if index+1 >= len(args) {
						return serviceCommand{}, true, fmt.Errorf("-auto-start requires true or false; %s", serviceCommandUsage)
					}
					index++
					flagValue = args[index]
				}
				parsed, err := parseStrictBool(flagValue)
				if err != nil {
					return serviceCommand{}, true, err
				}
				command.autoStart = parsed
				seenAutoStart = true
			case "user":
				if action != "install" {
					return serviceCommand{}, true, fmt.Errorf("service action %q does not accept -user; %s", action, serviceCommandUsage)
				}
				if seenUser {
					return serviceCommand{}, true, fmt.Errorf("duplicate -user flag; %s", serviceCommandUsage)
				}
				if !hasValue {
					if index+1 >= len(args) {
						return serviceCommand{}, true, fmt.Errorf("-user requires a local account name; %s", serviceCommandUsage)
					}
					index++
					flagValue = args[index]
				}
				identity, err := resolveServiceUser(flagValue)
				if err != nil {
					return serviceCommand{}, true, err
				}
				command.user = identity.Username
				seenUser = true
			default:
				return serviceCommand{}, true, fmt.Errorf("unknown service flag %q; %s", arg, serviceCommandUsage)
			}
			continue
		}

		if seenName {
			return serviceCommand{}, true, fmt.Errorf("duplicate service name %q; %s", arg, serviceCommandUsage)
		}
		command.name = strings.TrimSpace(arg)
		seenName = true
	}
	if err := validateServiceName(command.name); err != nil {
		return serviceCommand{}, true, err
	}
	if seenUser && !seenConfig {
		return serviceCommand{}, true, fmt.Errorf("-user requires -config so the service account cannot fall back to HOME; %s", serviceCommandUsage)
	}
	return command, true, nil
}

// resolveServiceUser resolves one explicit local account and rejects aliases, malformed identities, and non-numeric ids.
// resolveServiceUser 解析一个显式本机账户，并拒绝别名、格式错误的身份以及非数字标识。
func resolveServiceUser(value string) (serviceUserIdentity, error) {
	if value == "" || strings.TrimSpace(value) != value || !serviceUserPattern.MatchString(value) {
		return serviceUserIdentity{}, fmt.Errorf("invalid -user value %q: use the exact local account name", value)
	}
	account, err := osuser.Lookup(value)
	if err != nil {
		return serviceUserIdentity{}, fmt.Errorf("lookup local service account %q: %w", value, err)
	}
	if account.Username != value {
		return serviceUserIdentity{}, fmt.Errorf("local service account %q resolved to non-canonical name %q", value, account.Username)
	}
	if _, err := strconv.ParseUint(account.Uid, 10, 64); err != nil {
		return serviceUserIdentity{}, fmt.Errorf("local service account %q has invalid uid %q: %w", value, account.Uid, err)
	}
	if _, err := strconv.ParseUint(account.Gid, 10, 64); err != nil {
		return serviceUserIdentity{}, fmt.Errorf("local service account %q has invalid gid %q: %w", value, account.Gid, err)
	}
	return serviceUserIdentity{Username: account.Username, UID: account.Uid, GID: account.Gid}, nil
}

// fallbackStatusValue returns a stable placeholder when a native status field is absent.
// fallbackStatusValue 在原生状态字段缺失时返回稳定的占位值。
func fallbackStatusValue(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// isServiceAction restricts service commands to lifecycle operations that every supported platform can model.
// isServiceAction 用于把服务命令限制在所有受支持平台都能表达的生命周期操作内。
func isServiceAction(action string) bool {
	switch action {
	case "install", "uninstall", "start", "stop", "restart", "enable", "disable", "status", "run":
		return true
	default:
		return false
	}
}

// splitServiceFlag separates a supported short or long service flag from its optional equals value.
// splitServiceFlag 用于拆分受支持的短格式或长格式服务参数及其可选等号值。
func splitServiceFlag(arg string) (string, string, bool) {
	trimmed := ""
	switch {
	case strings.HasPrefix(arg, "--"):
		trimmed = strings.TrimPrefix(arg, "--")
	case strings.HasPrefix(arg, "-"):
		trimmed = strings.TrimPrefix(arg, "-")
	default:
		return "", "", false
	}
	name, value, hasValue := strings.Cut(trimmed, "=")
	return strings.TrimSpace(name), value, hasValue
}

// parseStrictBool accepts only the explicit boolean spellings used by service registration scripts.
// parseStrictBool 只接受服务注册脚本使用的明确布尔拼写。
func parseStrictBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid -auto-start value %q: use true or false", value)
	}
}

// validateServiceConfigPath requires a platform-absolute directory or YAML file so a service never resolves configuration through its account home directory.
// validateServiceConfigPath 要求平台绝对目录或 YAML 文件，避免服务通过服务账户 HOME 解析配置。
func validateServiceConfigPath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("-config must not be empty; %s", serviceCommandUsage)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("-config path %q must be absolute", path)
	}
	if strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("-config path %q contains a control character", path)
	}
	cleaned := filepath.Clean(path)
	info, err := os.Stat(cleaned)
	if err == nil {
		if info.IsDir() {
			return cleaned, nil
		}
		if err := validateServiceYAMLPath(cleaned); err != nil {
			return "", err
		}
		return cleaned, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect -config path %q: %w", cleaned, err)
	}
	// A not-yet-created path is accepted as a directory only when it has no ambiguous file suffix.
	// 尚未创建的路径只有在没有含糊文件后缀时才按目录处理。
	if ext := strings.ToLower(filepath.Ext(cleaned)); ext == ".yaml" || ext == ".yml" {
		return cleaned, nil
	}
	if ext := strings.ToLower(filepath.Ext(cleaned)); ext != "" {
		return "", fmt.Errorf("-config path %q must be an existing directory or a .yaml/.yml file", cleaned)
	}
	return cleaned, nil
}

// validateServiceYAMLPath rejects non-YAML files before they are persisted into native service definitions.
// validateServiceYAMLPath 在把路径写入原生服务定义前拒绝非 YAML 文件。
func validateServiceYAMLPath(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yaml" && ext != ".yml" {
		return fmt.Errorf("-config file %q must use .yaml or .yml", path)
	}
	return nil
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
			return runRuntime(ctx, exePath, serviceWD, command.configPath, ready)
		})
	}
	binDir := filepath.Dir(exePath)
	return applyServiceCommand(command, exePath, binDir)
}
