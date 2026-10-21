//go:build darwin

// macOS launchd service lifecycle operations for the local CLI.
// 本地命令行的 macOS launchd 服务生命周期操作。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runServiceRuntime runs the same foreground runtime because launchd supervises ordinary long-running processes directly.
// runServiceRuntime 用于运行同一套前台运行时，因为 launchd 可以直接监管普通长驻进程。
func runServiceRuntime(_ string, run func(context.Context, func()) error) error {
	return run(context.Background(), nil)
}

// applyServiceCommand maps portable service lifecycle actions onto launchd daemon operations.
// applyServiceCommand 用于把跨平台服务生命周期动作映射到 launchd 守护进程操作。
func applyServiceCommand(command serviceCommand, exePath string, binDir string) error {
	plistPath := filepath.Join("/Library/LaunchDaemons", command.name+".plist")
	label := command.name
	if command.action == "status" || command.action == "uninstall" {
		absent, err := launchdServiceAbsent(label, plistPath)
		if err != nil {
			return err
		}
		if absent {
			fmt.Print("state=not-installed\nauto_start=false\n")
			return nil
		}
	}
	switch command.action {
	case "install":
		serviceUser := ""
		if command.user != "" {
			identity, err := resolveServiceUser(command.user)
			if err != nil {
				return fmt.Errorf("validate launchd service account: %w", err)
			}
			serviceUser = identity.Username
			if err := validateServiceAccountStorage(exePath, command.configPath, identity); err != nil {
				return err
			}
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(exePath, label, command.configPath)); err != nil {
			return err
		}
		if info, statErr := os.Lstat(plistPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace non-regular launchd plist %q", plistPath)
			}
			if _, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir); err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect launchd plist %q: %w", plistPath, statErr)
		}
		if err := ensureLaunchdLogDir(launchdServiceSpec{Label: label, BinDir: binDir, UserName: serviceUser}); err != nil {
			return err
		}
		content, err := renderLaunchdPlist(label, exePath, binDir, launchdRenderOptions{
			configPath: command.configPath,
			autoStart:  command.autoStart,
			user:       serviceUser,
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write launchd plist %q: %w", plistPath, err)
		}
		if err := ensureLaunchdLoadable(label); err != nil {
			return err
		}
		fmt.Printf("installed launchd service %q auto_start=%s\n", command.name, launchdAutoStartName(command.autoStart))
		return nil
	case "uninstall":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		_, loaded, err := launchdPrint(label)
		if err != nil {
			return err
		}
		if loaded {
			if err := runCommand("launchctl", "bootout", "system", plistPath); err != nil {
				return err
			}
		}
		if err := os.Remove(plistPath); err != nil {
			return fmt.Errorf("remove launchd plist %q: %w", plistPath, err)
		}
		fmt.Printf("uninstalled launchd service %q\n", command.name)
		return nil
	case "start":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		return startLaunchdService(spec, plistPath)
	case "stop":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		_, loaded, err := launchdPrint(label)
		if err != nil {
			return err
		}
		if !loaded {
			fmt.Printf("stopped launchd service %q\n", command.name)
			return nil
		}
		return runCommand("launchctl", "bootout", "system", plistPath)
	case "restart":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		return startLaunchdService(spec, plistPath)
	case "enable", "disable":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		spec.AutoStart = command.action == "enable"
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		content, err := renderLaunchdPlist(spec.Label, spec.Executable, spec.BinDir, launchdRenderOptions{
			configPath: spec.ConfigPath,
			autoStart:  spec.AutoStart,
			user:       spec.UserName,
		})
		if err != nil {
			return err
		}
		_, loaded, err := launchdPrint(spec.Label)
		if err != nil {
			return err
		}
		if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write launchd plist %q: %w", plistPath, err)
		}
		if err := ensureLaunchdLoadable(spec.Label); err != nil {
			return err
		}
		if loaded {
			if err := runCommand("launchctl", "bootout", "system", plistPath); err != nil {
				return err
			}
			if err := ensureLaunchdLogDir(spec); err != nil {
				return err
			}
			if err := runCommand("launchctl", "bootstrap", "system", plistPath); err != nil {
				return err
			}
			if err := kickstartLaunchdService(spec.Label); err != nil {
				return err
			}
		}
		fmt.Printf("service %q auto_start=%s\n", command.name, launchdAutoStartName(spec.AutoStart))
		return nil
	case "status":
		spec, err := verifyLaunchdServiceOwnership(plistPath, label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := verifyLaunchdLoadedService(label, plistPath, launchdProgramArguments(spec.Executable, label, spec.ConfigPath)); err != nil {
			return err
		}
		return queryLaunchdServiceStatus(label, plistPath)
	default:
		return fmt.Errorf("unsupported service action %q", command.action)
	}
}

// launchdServiceAbsent confirms there is neither a managed plist nor a loaded system service before permitting a reinstall.
// launchdServiceAbsent 在允许重装前核对受管属性列表与已加载系统服务均不存在；权限及查询错误不会当成不存在。
func launchdServiceAbsent(label, plistPath string) (bool, error) {
	if _, err := os.Lstat(plistPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect launchd plist %q: %w", plistPath, err)
	}
	_, loaded, err := launchdPrint(label)
	if err != nil {
		return false, err
	}
	return !loaded, nil
}

// startLaunchdService bootstraps an unloaded job or restarts an already loaded one.
// startLaunchdService 用于加载未加载的 job，或重启已经加载的 job。
func startLaunchdService(spec launchdServiceSpec, plistPath string) error {
	label := spec.Label
	if _, err := os.Stat(plistPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("launchd service %q is not installed", label)
		}
		return fmt.Errorf("stat launchd plist %q: %w", plistPath, err)
	}
	if err := ensureLaunchdLogDir(spec); err != nil {
		return err
	}
	_, loaded, err := launchdPrint(label)
	if err != nil {
		return err
	}
	if !loaded {
		if err := runCommand("launchctl", "bootstrap", "system", plistPath); err != nil {
			return err
		}
		if err := kickstartLaunchdService(label); err != nil {
			return err
		}
		fmt.Printf("started launchd service %q\n", label)
		return nil
	}
	return kickstartLaunchdService(label)
}

// kickstartLaunchdService explicitly starts a loaded job so manual mode works even when RunAtLoad is false.
// kickstartLaunchdService 用于显式启动已加载的 job，确保 RunAtLoad 为 false 时手动模式仍可启动。
func kickstartLaunchdService(label string) error {
	return runCommand("launchctl", "kickstart", "-k", "system/"+label)
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
