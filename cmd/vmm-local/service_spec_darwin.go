//go:build darwin

// launchd service specification ownership, rendering and log directory setup.
// macOS 服务链路的 launchd 配置归属、渲染与日志目录设置。
package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// renderLaunchdPlist builds a daemon plist that persists the service config path and boot-time policy.
// renderLaunchdPlist 用于构建持久化服务配置路径与开机启动策略的守护进程 plist。
func renderLaunchdPlist(label string, exePath string, binDir string, options ...launchdRenderOptions) (string, error) {
	renderOptions := launchdRenderOptions{autoStart: true}
	if len(options) > 0 {
		renderOptions = options[0]
	}
	arguments := launchdProgramArguments(exePath, label, renderOptions.configPath)
	logDir := launchdLogDir(label, binDir, renderOptions.user)
	plist := launchdPlist{
		Version: "1.0",
		Dict: launchdDict{
			Label:             label,
			ProgramArguments:  arguments,
			WorkingDirectory:  binDir,
			UserName:          renderOptions.user,
			RunAtLoad:         renderOptions.autoStart,
			KeepAlive:         renderOptions.autoStart,
			StandardOutPath:   filepath.Join(logDir, "service-stdout.log"),
			StandardErrorPath: filepath.Join(logDir, "service-stderr.log"),
		},
	}
	output, err := xml.MarshalIndent(plist, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render launchd plist: %w", err)
	}
	// Apple's property-list XML contract represents booleans as empty elements; encoding/xml expands them and launchd rejects that form even when plutil accepts it.
	// Apple 的属性列表 XML 契约要求布尔值使用空元素；encoding/xml 会展开标签，launchd 即使在 plutil 接受时仍会拒绝。
	canonicalXML := strings.NewReplacer("<true></true>", "<true/>", "<false></false>", "<false/>").Replace(string(output))
	return xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" + canonicalXML + "\n", nil
}

// launchdProgramArguments returns the exact argv vector persisted in generated plists and checked against loaded jobs.
// launchdProgramArguments 返回生成 plist 持久化并用于核对已加载 job 的精确 argv 向量。
func launchdProgramArguments(exePath, label, configPath string) []string {
	arguments := []string{exePath, "service", "run", label}
	if strings.TrimSpace(configPath) != "" {
		arguments = append(arguments, "-config", configPath)
	}
	return arguments
}

// readLaunchdServiceSpec reads the generated plist and recovers the executable, working directory, config path, and boot policy.
// readLaunchdServiceSpec 用于读取生成的 plist，并恢复可执行文件、工作目录、配置路径和开机策略。
func readLaunchdServiceSpec(plistPath string) (launchdServiceSpec, error) {
	content, err := os.ReadFile(plistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return launchdServiceSpec{}, fmt.Errorf("launchd service plist %q is not installed", plistPath)
		}
		return launchdServiceSpec{}, fmt.Errorf("read launchd plist %q: %w", plistPath, err)
	}
	var plist launchdPlist
	if err := xml.Unmarshal(content, &plist); err != nil {
		return launchdServiceSpec{}, fmt.Errorf("parse launchd plist %q: %w", plistPath, err)
	}
	if plist.Version != "1.0" {
		return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has unsupported version %q", plistPath, plist.Version)
	}
	arguments := plist.Dict.ProgramArguments
	if len(arguments) < 4 || arguments[1] != "service" || arguments[2] != "run" {
		return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has invalid service arguments", plistPath)
	}
	spec := launchdServiceSpec{
		Label:             plist.Dict.Label,
		Executable:        arguments[0],
		UserName:          plist.Dict.UserName,
		BinDir:            plist.Dict.WorkingDirectory,
		AutoStart:         plist.Dict.RunAtLoad,
		KeepAlive:         plist.Dict.KeepAlive,
		StandardOutPath:   plist.Dict.StandardOutPath,
		StandardErrorPath: plist.Dict.StandardErrorPath,
	}
	if spec.Label == "" || spec.Executable == "" || spec.BinDir == "" {
		return launchdServiceSpec{}, fmt.Errorf("launchd plist %q is missing required service fields", plistPath)
	}
	if arguments[3] != spec.Label {
		return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has mismatched service label", plistPath)
	}
	for index := 4; index < len(arguments); index++ {
		if arguments[index] != "-config" || index+1 >= len(arguments) || spec.ConfigPath != "" {
			return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has invalid service arguments", plistPath)
		}
		spec.ConfigPath = arguments[index+1]
		index++
	}
	if spec.ConfigPath != "" {
		if _, err := validateServiceConfigPath(spec.ConfigPath); err != nil {
			return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has invalid config path: %w", plistPath, err)
		}
	}
	if spec.UserName != "" {
		identity, err := resolveServiceUser(spec.UserName)
		if err != nil {
			return launchdServiceSpec{}, fmt.Errorf("launchd plist %q has invalid UserName: %w", plistPath, err)
		}
		spec.UserName = identity.Username
	}
	return spec, nil
}

// verifyLaunchdServiceOwnership confirms the plist is a regular generated VMM daemon before any lifecycle action.
// verifyLaunchdServiceOwnership 在执行生命周期操作前确认 plist 是普通文件且属于生成的 VMM 守护进程。
func verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir string) (launchdServiceSpec, error) {
	info, err := os.Lstat(plistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return launchdServiceSpec{}, fmt.Errorf("launchd service %q is not installed", label)
		}
		return launchdServiceSpec{}, fmt.Errorf("inspect launchd plist %q: %w", plistPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return launchdServiceSpec{}, fmt.Errorf("refusing to operate on non-regular launchd plist %q", plistPath)
	}
	spec, err := readLaunchdServiceSpec(plistPath)
	if err != nil {
		return launchdServiceSpec{}, err
	}
	if err := validateLaunchdServiceSpec(spec, label, exePath, binDir); err != nil {
		return launchdServiceSpec{}, fmt.Errorf("refusing to operate on non-VMM launchd plist %q: %w", plistPath, err)
	}
	return spec, nil
}

// validateLaunchdServiceSpec compares every generated identity field and rejects unknown daemon wiring.
// validateLaunchdServiceSpec 比对所有生成的身份字段，并拒绝未知守护进程 wiring。
func validateLaunchdServiceSpec(spec launchdServiceSpec, label, exePath, binDir string) error {
	if spec.Label != label {
		return fmt.Errorf("label mismatch")
	}
	if filepath.Clean(spec.Executable) != filepath.Clean(exePath) {
		return fmt.Errorf("executable mismatch")
	}
	if filepath.Clean(spec.BinDir) != filepath.Clean(binDir) {
		return fmt.Errorf("working directory mismatch")
	}
	if spec.AutoStart != spec.KeepAlive {
		return fmt.Errorf("RunAtLoad and KeepAlive must match")
	}
	if spec.StandardOutPath != filepath.Join(launchdLogDir(label, binDir, spec.UserName), "service-stdout.log") {
		return fmt.Errorf("stdout path mismatch")
	}
	if spec.StandardErrorPath != filepath.Join(launchdLogDir(label, binDir, spec.UserName), "service-stderr.log") {
		return fmt.Errorf("stderr path mismatch")
	}
	if spec.ConfigPath != "" {
		if _, err := validateServiceConfigPath(spec.ConfigPath); err != nil {
			return fmt.Errorf("config path mismatch: %w", err)
		}
	}
	if spec.UserName != "" {
		identity, err := resolveServiceUser(spec.UserName)
		if err != nil {
			return fmt.Errorf("UserName mismatch: %w", err)
		}
		if identity.Username != spec.UserName {
			return fmt.Errorf("UserName is not canonical")
		}
	}
	return nil
}

// launchdLogDir keeps account-owned logs outside the immutable package tree while retaining the legacy root-service location.
// launchdLogDir 将服务账户拥有的日志置于不可变程序树之外，并保留旧版 root 服务的日志位置。
func launchdLogDir(label string, binDir string, user string) string {
	if user != "" {
		return filepath.Join("/private/var/log/vmmm", label)
	}
	return filepath.Join(filepath.Dir(binDir), "logs")
}

// ensureLaunchdLogDir prepares root-controlled log directories and account-owned files before launchd opens them.
// ensureLaunchdLogDir 在 launchd 打开日志前准备管理员控制的目录和服务账户拥有的文件。
func ensureLaunchdLogDir(spec launchdServiceSpec) error {
	logDir := launchdLogDir(spec.Label, spec.BinDir, spec.UserName)
	if spec.UserName == "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("create legacy launchd log dir %q: %w", logDir, err)
		}
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("prepare launchd service log files requires root")
	}
	identity, err := resolveServiceUser(spec.UserName)
	if err != nil {
		return err
	}
	uid, gid, err := serviceAccountIDs(identity)
	if err != nil {
		return err
	}
	for _, path := range []string{"/", "/private", "/private/var", "/private/var/log", "/private/var/log/vmmm", logDir} {
		if path == "/private/var/log/vmmm" || path == logDir {
			if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create launchd log directory %q: %w", path, err)
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect launchd log directory %q: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("launchd log directory %q must be root-owned, traversable, and protected from other writers", path)
		}
	}
	for _, name := range []string{"service-stdout.log", "service-stderr.log"} {
		if err := ensureLaunchdLogFile(filepath.Join(logDir, name), int(uid), int(gid)); err != nil {
			return err
		}
	}
	return nil
}

// ensureLaunchdLogFile creates a non-link file for the selected account or validates an existing file in the root-owned directory.
// ensureLaunchdLogFile 为选定账户创建非链接文件，或验证管理员目录中的现存文件。
func ensureLaunchdLogFile(path string, uid int, gid int) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		defer file.Close()
		if err := file.Chown(uid, gid); err != nil {
			return fmt.Errorf("assign launchd log file %q to service account: %w", path, err)
		}
		return nil
	}
	if !os.IsExist(err) {
		return fmt.Errorf("create launchd log file %q: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect launchd log file %q: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || stat.Uid != uint32(uid) || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("launchd log file %q must be regular, singly linked, account-owned, and mode 0600", path)
	}
	return nil
}
