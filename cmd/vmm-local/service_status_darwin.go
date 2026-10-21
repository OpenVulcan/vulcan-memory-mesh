//go:build darwin

// launchctl output parsing and loaded-service identity checks.
// macOS 服务链路的 launchctl 输出解析与已加载服务身份核对。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// launchdLoadedIdentity captures launchctl's resolved plist and argv for a loaded system-domain job.
// launchdLoadedIdentity 用于保存 system domain 已加载 job 解析出的 plist 路径和 argv。
type launchdLoadedIdentity struct {
	// PlistPath is the launchd source plist selected for the loaded job.
	// PlistPath 用于保存已加载 job 选中的 launchd 源 plist 路径。
	PlistPath string

	// Program is launchd's resolved executable path.
	// Program 用于保存 launchd 解析出的可执行文件路径。
	Program string

	// Arguments is the resolved ProgramArguments vector.
	// Arguments 用于保存解析出的 ProgramArguments 向量。
	Arguments []string
}

// parseLaunchctlPrintIdentity parses the stable path/program/arguments fields emitted by launchctl print.
// parseLaunchctlPrintIdentity 用于解析 launchctl print 输出的 path、program 和 arguments 字段。
func parseLaunchctlPrintIdentity(output string) (launchdLoadedIdentity, error) {
	var identity launchdLoadedIdentity
	argumentsSeen := false
	argumentsClosed := false
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "arguments = {") {
			if argumentsSeen {
				return launchdLoadedIdentity{}, fmt.Errorf("launchctl print returned duplicate arguments")
			}
			argumentsSeen = true
			continue
		}
		if argumentsSeen && !argumentsClosed {
			if trimmed == "}" {
				argumentsClosed = true
				continue
			}
			argument := trimmed
			if strings.HasPrefix(argument, `"`) {
				decoded, err := strconv.Unquote(argument)
				if err != nil {
					return launchdLoadedIdentity{}, fmt.Errorf("decode launchctl argument %q: %w", argument, err)
				}
				argument = decoded
			}
			identity.Arguments = append(identity.Arguments, argument)
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "path":
			if identity.PlistPath != "" {
				return launchdLoadedIdentity{}, fmt.Errorf("launchctl print returned duplicate path")
			}
			identity.PlistPath = value
		case "program":
			if identity.Program != "" {
				return launchdLoadedIdentity{}, fmt.Errorf("launchctl print returned duplicate program")
			}
			identity.Program = value
		}
	}
	if identity.PlistPath == "" || identity.Program == "" || !argumentsSeen || !argumentsClosed || len(identity.Arguments) == 0 {
		return launchdLoadedIdentity{}, fmt.Errorf("launchctl print omitted identity fields")
	}
	return identity, nil
}

// validateLaunchdLoadedIdentity rejects a loaded same-label job unless its resolved plist and argv match this VMM instance.
// validateLaunchdLoadedIdentity 仅在已加载同名 job 的 plist 与 argv 都匹配当前 VMM 实例时放行。
func validateLaunchdLoadedIdentity(identity launchdLoadedIdentity, plistPath string, expectedArguments []string) error {
	if filepath.Clean(identity.PlistPath) != filepath.Clean(plistPath) {
		return fmt.Errorf("loaded plist path mismatch")
	}
	if len(identity.Arguments) != len(expectedArguments) {
		return fmt.Errorf("loaded argument count mismatch")
	}
	for index := range expectedArguments {
		if index == 0 {
			if filepath.Clean(identity.Arguments[index]) != filepath.Clean(expectedArguments[index]) || filepath.Clean(identity.Program) != filepath.Clean(expectedArguments[index]) {
				return fmt.Errorf("loaded executable mismatch")
			}
			continue
		}
		if identity.Arguments[index] != expectedArguments[index] {
			return fmt.Errorf("loaded argument %d mismatch", index)
		}
	}
	return nil
}

// verifyLaunchdLoadedService checks an already loaded system-domain label before lifecycle commands can affect it.
// verifyLaunchdLoadedService 在生命周期命令影响已加载 job 前检查 system domain 中的同名标签。
func verifyLaunchdLoadedService(label, plistPath string, expectedArguments []string) error {
	output, loaded, err := launchdPrint(label)
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	identity, err := parseLaunchctlPrintIdentity(string(output))
	if err != nil {
		return fmt.Errorf("parse loaded launchd service %q: %w", label, err)
	}
	if err := validateLaunchdLoadedIdentity(identity, plistPath, expectedArguments); err != nil {
		return fmt.Errorf("refusing to operate on foreign loaded launchd service %q: %w", label, err)
	}
	return nil
}

// launchdPrint distinguishes the documented not-found response from other launchctl failures before lifecycle decisions.
// launchdPrint 在生命周期决策前区分已知的未找到响应与其他 launchctl 失败。
func launchdPrint(label string) ([]byte, bool, error) {
	output, err := exec.Command("launchctl", "print", "system/"+label).CombinedOutput()
	return classifyLaunchdPrintResult(label, output, err)
}

// classifyLaunchdPrintResult converts one launchctl probe into a loaded state without hiding unexpected failures.
// classifyLaunchdPrintResult 将一次 launchctl 探测转换为加载状态，同时不隐藏意外失败。
func classifyLaunchdPrintResult(label string, output []byte, commandErr error) ([]byte, bool, error) {
	if commandErr == nil {
		if strings.TrimSpace(string(output)) == "" {
			return nil, false, fmt.Errorf("inspect loaded launchd service %q returned empty output", label)
		}
		return output, true, nil
	}
	trimmed := strings.TrimSpace(string(output))
	if isLaunchdServiceNotFoundOutput(trimmed) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("inspect loaded launchd service %q: %w\n%s", label, commandErr, trimmed)
}

// isLaunchdServiceNotFoundOutput recognizes only launchctl's explicit missing-job response.
// isLaunchdServiceNotFoundOutput 仅识别 launchctl 明确表示作业不存在的响应。
func isLaunchdServiceNotFoundOutput(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "could not find service") || strings.Contains(lower, "could not find specified service")
}

// launchdDisabledOverride represents an explicit launchctl enable/disable override for a label.
// launchdDisabledOverride 用于表示标签在 launchctl 中保存的显式 enable/disable 覆盖。
type launchdDisabledOverride struct {
	// Found reports whether print-disabled listed the requested label.
	// Found 用于报告 print-disabled 是否列出了请求的标签。
	Found bool

	// Disabled reports the explicit disabled value when Found is true.
	// Disabled 用于保存 Found 为 true 时的显式禁用值。
	Disabled bool
}

// parseLaunchctlPrintDisabled finds one exact label entry in launchctl print-disabled output.
// parseLaunchctlPrintDisabled 用于从 launchctl print-disabled 输出中查找一个精确标签条目。
func parseLaunchctlPrintDisabled(output string, label string) (launchdDisabledOverride, error) {
	needle := `"` + label + `"`
	var override launchdDisabledOverride
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, needle) {
			continue
		}
		if override.Found {
			return launchdDisabledOverride{}, fmt.Errorf("launchctl print-disabled returned duplicate label %q", label)
		}
		_, value, ok := strings.Cut(trimmed, "=>")
		if !ok {
			return launchdDisabledOverride{}, fmt.Errorf("launchctl print-disabled returned malformed label %q", label)
		}
		switch strings.TrimSpace(value) {
		case "true", "disabled":
			override = launchdDisabledOverride{Found: true, Disabled: true}
		case "false", "enabled":
			override = launchdDisabledOverride{Found: true, Disabled: false}
		default:
			return launchdDisabledOverride{}, fmt.Errorf("launchctl print-disabled returned invalid value for %q", label)
		}
	}
	return override, nil
}

// queryLaunchdDisabledOverride reads the persistent launchctl override used by status and lifecycle actions.
// queryLaunchdDisabledOverride 读取 status 与生命周期操作使用的 launchctl 持久覆盖状态。
func queryLaunchdDisabledOverride(label string) (launchdDisabledOverride, error) {
	output, err := exec.Command("launchctl", "print-disabled", "system").CombinedOutput()
	if err != nil {
		return launchdDisabledOverride{}, fmt.Errorf("query launchctl disabled override for %q: %w\n%s", label, err, strings.TrimSpace(string(output)))
	}
	override, err := parseLaunchctlPrintDisabled(string(output), label)
	if err != nil {
		return launchdDisabledOverride{}, err
	}
	return override, nil
}

// ensureLaunchdLoadable clears a persistent disabled override so manually started jobs can bootstrap; plist RunAtLoad controls boot policy.
// ensureLaunchdLoadable 清除持久化禁用覆盖，使手动服务可以加载；开机策略由 plist 的 RunAtLoad 控制。
func ensureLaunchdLoadable(label string) error {
	return runCommand("launchctl", "enable", "system/"+label)
}

// launchdAutoStartName renders RunAtLoad as a stable key-value value.
// launchdAutoStartName 用于把 RunAtLoad 渲染成稳定的键值文本。
func launchdAutoStartName(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

// queryLaunchdServiceStatus reports unloaded-but-installed daemons as a normal stopped state instead of failing the status command.
// queryLaunchdServiceStatus 用于把已安装但未加载的守护进程报告为正常 stopped 状态，而不是让 status 命令失败。
// launchdRuntimeStatus captures the launchctl fields needed to decide whether a loaded job has a live process.
// launchdRuntimeStatus 用于保存判断已加载 job 是否存在活动进程所需的 launchctl 字段。
type launchdRuntimeStatus struct {
	// NativeState is launchd's reported state value.
	// NativeState 用于保存 launchd 报告的原生状态值。
	NativeState string

	// PID stores the reported process identifier when launchctl includes one.
	// PID 用于保存 launchctl 返回的进程标识符（如果存在）。
	PID int64

	// PIDSeen distinguishes a missing PID from an explicit zero or malformed value.
	// PIDSeen 用于区分缺少 PID 与明确返回零值或非法值的情况。
	PIDSeen bool
}

// parseLaunchctlPrintStatus parses launchctl print output without treating successful lookup as proof of a running process.
// parseLaunchctlPrintStatus 用于解析 launchctl print 输出，避免把查询成功误判为进程正在运行。
func parseLaunchctlPrintStatus(output string) (launchdRuntimeStatus, error) {
	var status launchdRuntimeStatus
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "state":
			if status.NativeState != "" {
				return launchdRuntimeStatus{}, fmt.Errorf("launchctl print returned duplicate state")
			}
			status.NativeState = strings.ToLower(value)
		case "pid":
			if status.PIDSeen {
				return launchdRuntimeStatus{}, fmt.Errorf("launchctl print returned duplicate pid")
			}
			pid, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return launchdRuntimeStatus{}, fmt.Errorf("launchctl print returned invalid pid %q: %w", value, err)
			}
			status.PID = pid
			status.PIDSeen = true
		}
	}
	if status.NativeState == "" {
		return launchdRuntimeStatus{}, fmt.Errorf("launchctl print did not return a state")
	}
	return status, nil
}

// launchdStateName maps launchd state and PID evidence into the manager's cross-platform state vocabulary.
// launchdStateName 将 launchd 状态与 PID 证据映射为管理器跨平台状态词汇。
func launchdStateName(status launchdRuntimeStatus) string {
	switch status.NativeState {
	case "running":
		if status.PIDSeen && status.PID > 0 {
			return "running"
		}
		return "unknown"
	case "starting", "launching", "spawn pending":
		return "start-pending"
	case "stopping", "unloading":
		return "stop-pending"
	case "exited", "waiting", "idle", "loaded", "stopped":
		return "stopped"
	case "crashed", "failed":
		return "failed"
	default:
		return "unknown"
	}
}

// renderLaunchdStatus emits one key-value field per line for strict manager parsing.
// renderLaunchdStatus 按每行一个键值对输出，满足管理器的严格解析契约。
func renderLaunchdStatus(state string, autoStart bool, users ...string) string {
	user := "unknown"
	if len(users) > 0 {
		user = fallbackStatusValue(users[0], "unknown")
	}
	return fmt.Sprintf("state=%s\nauto_start=%s\nuser=%s\n", state, launchdAutoStartName(autoStart), user)
}

// queryLaunchdServiceStatus reports unloaded-but-installed daemons as stopped and verifies loaded daemons using state plus PID.
// queryLaunchdServiceStatus 将已安装但未加载的守护进程报告为 stopped，并使用状态与 PID 验证已加载守护进程。
func queryLaunchdServiceStatus(label string, plistPath string) error {
	if _, err := os.Stat(plistPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("launchd service %q is not installed", label)
		}
		return fmt.Errorf("stat launchd plist %q: %w", plistPath, err)
	}
	spec, err := readLaunchdServiceSpec(plistPath)
	if err != nil {
		return err
	}
	override, err := queryLaunchdDisabledOverride(label)
	if err != nil {
		return err
	}
	state := "stopped"
	output, loaded, err := launchdPrint(label)
	if err != nil {
		return err
	}
	if loaded {
		runtimeStatus, parseErr := parseLaunchctlPrintStatus(string(output))
		if parseErr != nil {
			return fmt.Errorf("parse launchctl status for %q: %w", label, parseErr)
		}
		state = launchdStateName(runtimeStatus)
	}
	autoStart := spec.AutoStart && (!override.Found || !override.Disabled)
	fmt.Print(renderLaunchdStatus(state, autoStart, spec.UserName))
	return nil
}
