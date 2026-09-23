//go:build linux

// service_linux.go registers vmm-local as a systemd service on Linux.
// service_linux.go 用于在 Linux 上把 vmm-local 注册为 systemd 服务。
package main

import (
	"context"
	"errors"
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
		if err := validateSystemdWorkingDirectory(binDir); err != nil {
			return err
		}
		serviceUser := ""
		if command.user != "" {
			identity, err := resolveServiceUser(command.user)
			if err != nil {
				return fmt.Errorf("validate systemd service account: %w", err)
			}
			serviceUser = identity.Username
			if err := validateServiceAccountStorage(exePath, command.configPath, identity); err != nil {
				return err
			}
		}
		if err := verifySystemdInstallTarget(unitName, command.name, exePath, binDir); err != nil {
			return err
		}
		if info, statErr := os.Lstat(unitPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace non-regular systemd unit %q", unitPath)
			}
			if err := verifySystemdUnitOwnership(unitPath, command.name, exePath, binDir); err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect systemd unit %q: %w", unitPath, statErr)
		}
		content := renderSystemdUnitWithUser(command.name, exePath, binDir, command.configPath, serviceUser)
		if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write systemd unit %q: %w", unitPath, err)
		}
		if err := runCommand("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if err := verifySystemdLoadedUnit(unitName, unitPath); err != nil {
			return err
		}
		if err := setSystemdAutoStart(unitName, command.autoStart); err != nil {
			return err
		}
		fmt.Printf("installed systemd service %q auto_start=%s\n", command.name, systemdAutoStartName(command.autoStart))
		return nil
	case "uninstall":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		if err := runCommand("systemctl", "stop", unitName); err != nil {
			return fmt.Errorf("stop systemd service %q before uninstall: %w", command.name, err)
		}
		if err := setSystemdAutoStart(unitName, false); err != nil {
			return err
		}
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove systemd unit %q: %w", unitPath, err)
		}
		if err := runCommand("systemctl", "daemon-reload"); err != nil {
			return err
		}
		fmt.Printf("uninstalled systemd service %q\n", command.name)
		return nil
	case "start":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		return runCommand("systemctl", "start", unitName)
	case "stop":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		return runCommand("systemctl", "stop", unitName)
	case "restart":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		return runCommand("systemctl", "restart", unitName)
	case "enable":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		if err := setSystemdAutoStart(unitName, true); err != nil {
			return err
		}
		fmt.Printf("service %q auto_start=enabled\n", command.name)
		return nil
	case "disable":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		if err := setSystemdAutoStart(unitName, false); err != nil {
			return err
		}
		fmt.Printf("service %q auto_start=disabled\n", command.name)
		return nil
	case "status":
		if err := verifySystemdManagedUnit(unitName, unitPath, command.name, exePath, binDir); err != nil {
			return err
		}
		identity, err := readSystemdUnitIdentity(unitPath, command.name, exePath, binDir)
		if err != nil {
			return err
		}
		return querySystemdServiceStatus(unitName, identity.User)
	default:
		return fmt.Errorf("unsupported service action %q", command.action)
	}
}

// verifySystemdInstallTarget checks systemd's resolved fragment before an /etc unit can shadow another unit directory.
// verifySystemdInstallTarget 用于在写入 /etc unit 前检查 systemd 实际解析的片段，避免覆盖其他目录中的同名 unit。
func verifySystemdInstallTarget(unitName, name, exePath, binDir string) error {
	output, err := exec.Command(
		"systemctl",
		"show",
		"--property=LoadState",
		"--property=FragmentPath",
		"--property=DropInPaths",
		"--property=NeedDaemonReload",
		unitName,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect systemd unit %q before install: %w\n%s", unitName, err, strings.TrimSpace(string(output)))
	}
	status := parseSystemdStatusOutput(string(output))
	if err := validateSystemdInstallLoadState(status); err != nil {
		return err
	}
	if status.LoadState == "not-found" {
		return nil
	}
	if status.DropInPaths != "" || status.NeedDaemonReload != "no" {
		return fmt.Errorf("refusing to replace systemd unit %q with drop-ins or a stale loaded definition", unitName)
	}
	fragmentPath := strings.TrimSpace(status.FragmentPath)
	if fragmentPath == "" || !filepath.IsAbs(fragmentPath) {
		return fmt.Errorf("refusing to install over systemd unit %q with unknown fragment path", unitName)
	}
	info, err := os.Lstat(fragmentPath)
	if err != nil {
		return fmt.Errorf("inspect systemd fragment %q: %w", fragmentPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to replace non-regular systemd fragment %q", fragmentPath)
	}
	content, err := os.ReadFile(fragmentPath)
	if err != nil {
		return fmt.Errorf("read systemd fragment %q: %w", fragmentPath, err)
	}
	if err := validateSystemdUnitIdentity(string(content), name, exePath, binDir); err != nil {
		return fmt.Errorf("refusing to shadow non-VMM systemd unit %q: %w", fragmentPath, err)
	}
	return nil
}

// verifySystemdManagedUnit checks both the disk unit and the definition currently loaded by systemd.
// verifySystemdManagedUnit 同时校验磁盘 unit 与 systemd 当前加载的运行定义。
func verifySystemdManagedUnit(unitName, unitPath, name, exePath, binDir string) error {
	if err := verifySystemdUnitOwnership(unitPath, name, exePath, binDir); err != nil {
		return err
	}
	return verifySystemdLoadedUnit(unitName, unitPath)
}

// verifySystemdLoadedUnit rejects a shadowed fragment, drop-in, or pending daemon reload.
// verifySystemdLoadedUnit 拒绝被覆盖的片段、drop-in 以及待重载的运行定义。
func verifySystemdLoadedUnit(unitName, unitPath string) error {
	output, err := exec.Command(
		"systemctl", "show", "--property=LoadState", "--property=FragmentPath",
		"--property=DropInPaths", "--property=NeedDaemonReload", unitName,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect loaded systemd unit %q: %w\n%s", unitName, err, strings.TrimSpace(string(output)))
	}
	status := parseSystemdStatusOutput(string(output))
	if status.LoadState != "loaded" || filepath.Clean(status.FragmentPath) != filepath.Clean(unitPath) ||
		status.DropInPaths != "" || status.NeedDaemonReload != "no" {
		return fmt.Errorf("systemd unit %q is not the exact managed definition", unitName)
	}
	return nil
}

// validateSystemdInstallLoadState rejects missing or unknown systemd responses before any shadowing decision.
// validateSystemdInstallLoadState 在决定覆盖前拒绝缺失或未知的 systemd 响应。
func validateSystemdInstallLoadState(status systemdStatus) error {
	switch strings.ToLower(strings.TrimSpace(status.LoadState)) {
	case "not-found":
		return nil
	case "loaded", "bad-setting", "error", "masked", "stub", "merged", "generated", "transient":
		return nil
	default:
		return fmt.Errorf("refusing to install over systemd unit with unknown load state %q", status.LoadState)
	}
}

// verifySystemdUnitOwnership accepts only the exact unit shape generated by this VMM executable.
// verifySystemdUnitOwnership 只接受当前 VMM 可执行文件生成的精确 unit 形态。
func verifySystemdUnitOwnership(unitPath, name, exePath, binDir string) error {
	_, err := readSystemdUnitIdentity(unitPath, name, exePath, binDir)
	return err
}

// readSystemdUnitIdentity reads and validates a generated unit, returning its persisted account identity for status output.
// readSystemdUnitIdentity 读取并校验生成的 unit，并返回状态输出所需的持久化账户身份。
func readSystemdUnitIdentity(unitPath, name, exePath, binDir string) (systemdUnitIdentity, error) {
	info, err := os.Lstat(unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return systemdUnitIdentity{}, fmt.Errorf("systemd service %q is not installed", name)
		}
		return systemdUnitIdentity{}, fmt.Errorf("inspect systemd unit %q: %w", unitPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return systemdUnitIdentity{}, fmt.Errorf("refusing to operate on non-regular systemd unit %q", unitPath)
	}
	content, err := os.ReadFile(unitPath)
	if err != nil {
		return systemdUnitIdentity{}, fmt.Errorf("read systemd unit %q: %w", unitPath, err)
	}
	identity, err := parseSystemdUnitIdentity(string(content), name, exePath, binDir)
	if err != nil {
		return systemdUnitIdentity{}, fmt.Errorf("refusing to operate on non-VMM systemd unit %q: %w", unitPath, err)
	}
	return identity, nil
}

// validateSystemdUnitIdentity rejects extra directives and verifies the executable, service name, and optional config argument.
// validateSystemdUnitIdentity 拒绝额外指令，并验证可执行文件、服务名和可选配置参数。
func validateSystemdUnitIdentity(content, name, exePath, binDir string) error {
	_, err := parseSystemdUnitIdentity(content, name, exePath, binDir)
	return err
}

// systemdUnitIdentity carries the validated account persisted in a native unit.
// systemdUnitIdentity 保存原生 unit 中已校验的服务账户。
type systemdUnitIdentity struct {
	// User is the explicit local account name, or empty for a legacy unit without User=.
	// User 是显式本机账户名；旧 unit 没有 User= 时为空。
	User string
}

// parseSystemdUnitIdentity validates the exact generated unit shape and resolves an optional User= account.
// parseSystemdUnitIdentity 校验生成的精确 unit 结构，并解析可选的 User= 账户。
// parseSystemdUnitIdentity validates current units and the exact previously released legacy template.
// parseSystemdUnitIdentity 校验当前 unit 以及历史发布版本的精确旧模板。
func parseSystemdUnitIdentity(content, name, exePath, binDir string) (systemdUnitIdentity, error) {
	identity, currentErr := parseCurrentSystemdUnitIdentity(content, name, exePath, binDir)
	if currentErr == nil {
		return identity, nil
	}
	legacy, legacyErr := parseLegacySystemdUnitIdentity(content, name, exePath, binDir)
	if legacyErr == nil {
		return legacy, nil
	}
	return systemdUnitIdentity{}, currentErr
}

// parseCurrentSystemdUnitIdentity validates the current generated unit shape and resolves an optional User= account.
// parseCurrentSystemdUnitIdentity 校验当前生成的 unit 结构，并解析可选的 User= 账户。
func parseCurrentSystemdUnitIdentity(content, name, exePath, binDir string) (systemdUnitIdentity, error) {
	expectedStatic := map[string]bool{
		"[Unit]": false,
		"Description=" + name + " local gRPC runtime service":      false,
		"After=network-online.target":                              false,
		"Wants=network-online.target":                              false,
		"[Service]":                                                false,
		"Type=simple":                                              false,
		"WorkingDirectory=" + systemdWorkingDirectoryValue(binDir): false,
		"Restart=on-failure":                                       false,
		"RestartSec=5s":                                            false,
		"KillSignal=SIGTERM":                                       false,
		"[Install]":                                                false,
		"WantedBy=multi-user.target":                               false,
	}
	execSeen := false
	userSeen := false
	identity := systemdUnitIdentity{}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ExecStart=") {
			if execSeen {
				return systemdUnitIdentity{}, fmt.Errorf("duplicate ExecStart")
			}
			if err := validateSystemdExecStart(strings.TrimPrefix(line, "ExecStart="), name, exePath); err != nil {
				return systemdUnitIdentity{}, err
			}
			execSeen = true
			continue
		}
		if strings.HasPrefix(line, "User=") {
			if userSeen {
				return systemdUnitIdentity{}, fmt.Errorf("duplicate User")
			}
			account, err := resolveServiceUser(strings.TrimPrefix(line, "User="))
			if err != nil {
				return systemdUnitIdentity{}, fmt.Errorf("invalid User directive: %w", err)
			}
			identity.User = account.Username
			userSeen = true
			continue
		}
		if _, known := expectedStatic[line]; !known {
			return systemdUnitIdentity{}, fmt.Errorf("unexpected directive %q", line)
		}
		if expectedStatic[line] {
			return systemdUnitIdentity{}, fmt.Errorf("duplicate directive %q", line)
		}
		expectedStatic[line] = true
	}
	if !execSeen {
		return systemdUnitIdentity{}, fmt.Errorf("missing ExecStart")
	}
	for line, seen := range expectedStatic {
		if !seen {
			return systemdUnitIdentity{}, fmt.Errorf("missing directive %q", line)
		}
	}
	return identity, nil
}

// validateSystemdExecStart verifies the generated executable command and permits only one validated config argument.
// validateSystemdExecStart 验证生成的可执行命令，并且只允许一个经过校验的配置参数。
// parseLegacySystemdUnitIdentity accepts only the exact unit emitted before explicit config and User= support.
// parseLegacySystemdUnitIdentity 仅接受显式配置参数和 User= 支持加入前发布的精确 unit 模板。
func parseLegacySystemdUnitIdentity(content, name, exePath, binDir string) (systemdUnitIdentity, error) {
	expectedStatic := map[string]bool{
		"[Unit]": false,
		"Description=" + name + " local gRPC runtime service": false,
		"After=network-online.target":                         false,
		"Wants=network-online.target":                         false,
		"[Service]":                                           false,
		"Type=simple":                                         false,
		"WorkingDirectory=" + legacySystemdQuote(binDir):      false,
		"Restart=on-failure":                                  false,
		"RestartSec=5s":                                       false,
		"KillSignal=SIGTERM":                                  false,
		"[Install]":                                           false,
		"WantedBy=multi-user.target":                          false,
	}
	expectedExec := legacySystemdQuote(exePath) + " service run " + name
	execSeen := false
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "ExecStart=") {
			if execSeen {
				return systemdUnitIdentity{}, fmt.Errorf("duplicate ExecStart")
			}
			if strings.TrimPrefix(line, "ExecStart=") != expectedExec {
				return systemdUnitIdentity{}, fmt.Errorf("unexpected legacy ExecStart identity")
			}
			execSeen = true
			continue
		}
		if strings.HasPrefix(line, "User=") {
			return systemdUnitIdentity{}, fmt.Errorf("legacy unit must not contain User")
		}
		if _, known := expectedStatic[line]; !known {
			return systemdUnitIdentity{}, fmt.Errorf("unexpected legacy directive %q", line)
		}
		if expectedStatic[line] {
			return systemdUnitIdentity{}, fmt.Errorf("duplicate legacy directive %q", line)
		}
		expectedStatic[line] = true
	}
	if !execSeen {
		return systemdUnitIdentity{}, fmt.Errorf("missing legacy ExecStart")
	}
	for line, seen := range expectedStatic {
		if !seen {
			return systemdUnitIdentity{}, fmt.Errorf("missing legacy directive %q", line)
		}
	}
	return systemdUnitIdentity{}, nil
}

// legacySystemdQuote reproduces the pre-expansion-escaping token format for strict migration checks.
// legacySystemdQuote 复现旧版本未转义展开字符的令牌格式，仅用于严格迁移检查。
func legacySystemdQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// validateSystemdExecStart verifies the generated executable command and permits only one validated config argument.
// validateSystemdExecStart 楠岃瘉鐢熸垚鐨勫彲鎵ц鍛戒护琛屻€?
func validateSystemdExecStart(value, name, exePath string) error {
	prefix := strings.Join([]string{systemdQuote(exePath), systemdQuote("service"), systemdQuote("run"), systemdQuote(name)}, " ")
	if value == prefix {
		return nil
	}
	configPrefix := prefix + " " + systemdQuote("-config") + " "
	if !strings.HasPrefix(value, configPrefix) {
		return fmt.Errorf("unexpected ExecStart identity")
	}
	configPath, err := unquoteSystemdToken(strings.TrimPrefix(value, configPrefix))
	if err != nil {
		return err
	}
	if _, err := validateServiceConfigPath(configPath); err != nil {
		return fmt.Errorf("invalid persisted config path: %w", err)
	}
	return nil
}

// unquoteSystemdToken decodes the narrow quoting dialect emitted by systemdQuote and rejects trailing tokens.
// unquoteSystemdToken 解码 systemdQuote 生成的有限引用格式，并拒绝尾随参数。
func unquoteSystemdToken(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("persisted config argument is not one quoted token")
	}
	var builder strings.Builder
	for index := 1; index < len(value)-1; index++ {
		switch value[index] {
		case '\\':
			if index+1 >= len(value)-1 || (value[index+1] != '\\' && value[index+1] != '"') {
				return "", fmt.Errorf("persisted config argument has invalid escape")
			}
			index++
			builder.WriteByte(value[index])
		case '%', '$':
			if index+1 >= len(value)-1 || value[index+1] != value[index] {
				return "", fmt.Errorf("persisted config argument has invalid expansion escape")
			}
			index++
			builder.WriteByte(value[index])
		case '"':
			return "", fmt.Errorf("persisted config argument has an unescaped quote")
		default:
			builder.WriteByte(value[index])
		}
	}
	return builder.String(), nil
}

// renderSystemdUnit builds a legacy unit without an explicit service account for direct CLI compatibility.
// renderSystemdUnit 构建不含显式服务账户的旧式 unit，以兼容直接 CLI 调用。
func renderSystemdUnit(name string, exePath string, binDir string, configPaths ...string) string {
	configPath := ""
	if len(configPaths) > 0 {
		configPath = configPaths[0]
	}
	return renderSystemdUnitWithUser(name, exePath, binDir, configPath, "")
}

// renderSystemdUnitWithUser builds a unit with an explicit local account and absolute configuration argument.
// renderSystemdUnitWithUser 构建包含显式本机账户和绝对配置参数的 unit。
func renderSystemdUnitWithUser(name string, exePath string, binDir string, configPath string, user string) string {
	arguments := []string{exePath, "service", "run", name}
	if strings.TrimSpace(configPath) != "" {
		arguments = append(arguments, "-config", configPath)
	}
	quotedArguments := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quotedArguments = append(quotedArguments, systemdQuote(argument))
	}
	userDirective := ""
	if user != "" {
		userDirective = "User=" + user + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=%s local gRPC runtime service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
%sWorkingDirectory=%s
ExecStart=%s
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM

[Install]
WantedBy=multi-user.target
`, name, userDirective, systemdWorkingDirectoryValue(binDir), strings.Join(quotedArguments, " "))
}

// systemdQuote quotes one systemd command token so paths with spaces still resolve without invoking a shell.
// systemdQuote 用于引用一个 systemd 命令令牌，让包含空格的路径无需 shell 也能正确解析。
func systemdQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	// Systemd expands percent specifiers and environment variables in ExecStart, so literal values must use %% and $$.
	// systemd 会在 ExecStart 中展开百分号 specifier 与环境变量，因此字面值必须使用 %% 和 $$。
	escaped = strings.ReplaceAll(escaped, `%`, `%%`)
	escaped = strings.ReplaceAll(escaped, `$`, `$$`)
	return `"` + escaped + `"`
}

// systemdWorkingDirectoryValue keeps the directive value unquoted so spaces remain literal and escapes only systemd percent specifiers.
// systemdWorkingDirectoryValue 保持 directive 值不加引号，让空格保持字面含义，并只转义 systemd 百分号 specifier。
func systemdWorkingDirectoryValue(value string) string {
	return strings.ReplaceAll(value, `%`, `%%`)
}

// validateSystemdWorkingDirectory rejects values that could inject another unit directive.
// validateSystemdWorkingDirectory 拒绝可能注入其他 unit 指令的工作目录值。
func validateSystemdWorkingDirectory(value string) error {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return fmt.Errorf("systemd working directory must be an absolute path")
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("systemd working directory must not contain a newline")
	}
	return nil
}

// setSystemdAutoStart changes only the unit enablement state and keeps manual start available when disabled.
// setSystemdAutoStart 只修改 unit 的启用状态，禁用自启时仍保留手动启动能力。
func setSystemdAutoStart(unitName string, enabled bool) error {
	if enabled {
		return runCommand("systemctl", "enable", unitName)
	}
	state, err := systemdUnitFileState(unitName)
	if err != nil {
		return err
	}
	switch state {
	case "disabled", "static", "not-found":
		return nil
	case "enabled", "enabled-runtime", "linked", "linked-runtime":
		return runCommand("systemctl", "disable", unitName)
	default:
		return fmt.Errorf("systemd unit %q has unsupported enablement state %q", unitName, state)
	}
}

// systemdUnitFileState reads native enablement and preserves command failures.
// systemdUnitFileState 读取原生启用状态，并保留命令执行失败信息。
func systemdUnitFileState(unitName string) (string, error) {
	output, err := exec.Command("systemctl", "is-enabled", unitName).CombinedOutput()
	return classifySystemdUnitFileState(output, err)
}

// classifySystemdUnitFileState accepts only documented states, including nonzero disabled results.
// classifySystemdUnitFileState 仅接受明确状态，包括以非零退出码报告的已禁用状态。
func classifySystemdUnitFileState(output []byte, runErr error) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	state := ""
	for _, line := range lines {
		value := strings.TrimSpace(line)
		if value == "" {
			continue
		}
		if state != "" {
			return "", errors.New("systemctl is-enabled returned multiple output lines")
		}
		state = value
	}
	var exitError *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitError) {
		return "", fmt.Errorf("systemctl is-enabled failed: %w", runErr)
	}
	switch state {
	case "enabled", "enabled-runtime", "linked", "linked-runtime":
		if runErr != nil {
			return "", fmt.Errorf("systemctl is-enabled reported %q with a command failure: %w", state, runErr)
		}
		return state, nil
	case "disabled", "static", "not-found":
		return state, nil
	default:
		return "", fmt.Errorf("systemctl is-enabled returned unknown state %q", state)
	}
}

// systemdAutoStartName renders the registration policy in the same key-value vocabulary used by status output.
// systemdAutoStartName 用于把注册策略渲染成与状态输出一致的键值文本。
func systemdAutoStartName(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
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
func querySystemdServiceStatus(unitName string, user string) error {
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
	status.User = user
	fmt.Print(renderSystemdStatus(status))
	return nil
}

// renderSystemdStatus emits stable key-value fields so the manager can display or parse service state without scraping prose.
// renderSystemdStatus 输出稳定键值字段，让管理器无需抓取自然语言即可展示或解析服务状态。
func renderSystemdStatus(status systemdStatus) string {
	state := systemdStateName(status.ActiveState)
	return fmt.Sprintf(
		"state=%s\nsubstate=%s\nauto_start=%s\nuser=%s\n",
		state,
		fallbackStatusValue(status.SubState, "unknown"),
		fallbackStatusValue(status.UnitFileState, "unknown"),
		fallbackStatusValue(status.User, "unknown"),
	)
}

// systemdStateName maps native systemd activity states to the cross-platform manager vocabulary.
// systemdStateName 将 systemd 原生活动状态映射为跨平台管理器状态词汇。
func systemdStateName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active":
		return "running"
	case "activating", "reloading":
		return "start-pending"
	case "deactivating":
		return "stop-pending"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
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

	// FragmentPath reports the systemd file selected after all unit search paths are resolved.
	// FragmentPath 用于报告 systemd 在解析所有 unit 搜索路径后选中的文件。
	FragmentPath string

	// DropInPaths reports override fragments that may change the managed unit.
	// DropInPaths 报告可能修改受管 unit 的覆盖片段。
	DropInPaths string

	// NeedDaemonReload reports whether systemd is using a stale loaded definition.
	// NeedDaemonReload 报告 systemd 是否仍在使用过期的加载定义。
	NeedDaemonReload string

	// User stores the validated explicit account used for manager-facing status output.
	// User 保存用于管理器状态输出的已校验显式账户。
	User string
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
		LoadState:        values["LoadState"],
		ActiveState:      values["ActiveState"],
		SubState:         values["SubState"],
		UnitFileState:    values["UnitFileState"],
		FragmentPath:     values["FragmentPath"],
		DropInPaths:      values["DropInPaths"],
		NeedDaemonReload: values["NeedDaemonReload"],
	}
}
