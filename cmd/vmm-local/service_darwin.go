//go:build darwin

// service_darwin.go registers vmm-local as a launchd daemon on macOS.
// service_darwin.go 用于在 macOS 上把 vmm-local 注册为 launchd 守护进程。
package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// launchdPlist captures the small launchd property-list shape needed by the VMM runtime service.
// launchdPlist 用于承载 VMM 运行时服务所需的精简 launchd 属性列表结构。
type launchdPlist struct {
	// XMLName fixes the root element so launchd receives a standard plist document.
	// XMLName 用于固定根元素，确保 launchd 收到标准 plist 文档。
	XMLName xml.Name `xml:"plist"`

	// Version stores the plist schema version expected by launchd.
	// Version 用于保存 launchd 期望的 plist schema 版本。
	Version string `xml:"version,attr"`

	// Dict stores the ordered launchd daemon keys.
	// Dict 用于保存有序的 launchd 守护进程配置键。
	Dict dict `xml:"dict"`
}

// dict models ordered launchd key/value pairs because plist XML is order-sensitive for human review.
// dict 用于按顺序建模 launchd 键值对，因为 plist XML 的顺序更便于人工审阅。
type dict struct {
	// Items preserves alternating key/value nodes so the generated plist remains readable and deterministic.
	// Items 用于保留交替排列的键值节点，使生成的 plist 保持可读且确定。
	Items []any `xml:",any"`
}

// key models a plist key element.
// key 用于建模 plist key 元素。
type key struct {
	// XMLName fixes the element name to key for plist serialization.
	// XMLName 用于把元素名固定为 key 以完成 plist 序列化。
	XMLName xml.Name `xml:"key"`

	// Value stores the launchd property key text.
	// Value 用于保存 launchd 属性键文本。
	Value string `xml:",chardata"`
}

// stringValue models a plist string element.
// stringValue 用于建模 plist string 元素。
type stringValue struct {
	// XMLName fixes the element name to string for plist serialization.
	// XMLName 用于把元素名固定为 string 以完成 plist 序列化。
	XMLName xml.Name `xml:"string"`

	// Value stores one string value in the launchd plist.
	// Value 用于保存 launchd plist 中的一个字符串值。
	Value string `xml:",chardata"`
}

// boolValue models a plist true or false element.
// boolValue 用于建模 plist true 或 false 元素。
type boolValue struct {
	// XMLName carries either true or false because plist booleans are represented as empty named elements.
	// XMLName 用于承载 true 或 false，因为 plist 布尔值使用空的命名元素表示。
	XMLName xml.Name
}

// arrayValue models a plist array element.
// arrayValue 用于建模 plist array 元素。
type arrayValue struct {
	// XMLName fixes the element name to array for plist serialization.
	// XMLName 用于把元素名固定为 array 以完成 plist 序列化。
	XMLName xml.Name `xml:"array"`

	// Values stores the ordered program arguments passed to vmm-local.
	// Values 用于保存传递给 vmm-local 的有序程序参数。
	Values []stringValue `xml:"string"`
}

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
	switch command.action {
	case "install":
		if err := ensureLaunchdLogDir(binDir); err != nil {
			return err
		}
		content, err := renderLaunchdPlist(label, exePath, binDir)
		if err != nil {
			return err
		}
		if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write launchd plist %q: %w", plistPath, err)
		}
		fmt.Printf("installed launchd service %q\n", command.name)
		return nil
	case "uninstall":
		_ = runCommand("launchctl", "bootout", "system", plistPath)
		if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove launchd plist %q: %w", plistPath, err)
		}
		fmt.Printf("uninstalled launchd service %q\n", command.name)
		return nil
	case "start":
		if err := ensureLaunchdLogDir(binDir); err != nil {
			return err
		}
		if !commandSucceeds("launchctl", "print", "system/"+label) {
			if err := runCommand("launchctl", "bootstrap", "system", plistPath); err != nil {
				return err
			}
			fmt.Printf("started launchd service %q\n", command.name)
			return nil
		}
		return runCommand("launchctl", "kickstart", "-k", "system/"+label)
	case "stop":
		return runCommand("launchctl", "bootout", "system", plistPath)
	case "status":
		return queryLaunchdServiceStatus(label, plistPath)
	default:
		return fmt.Errorf("unsupported service action %q", command.action)
	}
}

// renderLaunchdPlist builds a daemon plist that passes only the service name to the hidden service runtime path.
// renderLaunchdPlist 用于构建守护进程 plist，只把服务名传给隐藏的服务运行路径。
func renderLaunchdPlist(label string, exePath string, binDir string) (string, error) {
	logDir := launchdLogDir(binDir)
	plist := launchdPlist{
		Version: "1.0",
		Dict: dict{Items: []any{
			key{Value: "Label"}, stringValue{Value: label},
			key{Value: "ProgramArguments"}, arrayValue{Values: []stringValue{
				{Value: exePath},
				{Value: "service"},
				{Value: "run"},
				{Value: label},
			}},
			key{Value: "WorkingDirectory"}, stringValue{Value: binDir},
			key{Value: "RunAtLoad"}, boolValue{XMLName: xml.Name{Local: "true"}},
			key{Value: "KeepAlive"}, boolValue{XMLName: xml.Name{Local: "true"}},
			key{Value: "StandardOutPath"}, stringValue{Value: filepath.Join(logDir, "service-stdout.log")},
			key{Value: "StandardErrorPath"}, stringValue{Value: filepath.Join(logDir, "service-stderr.log")},
		}},
	}
	output, err := xml.MarshalIndent(plist, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render launchd plist: %w", err)
	}
	return xml.Header + "<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" + string(output) + "\n", nil
}

// launchdLogDir returns the packaged stdout/stderr directory that launchd must open before the VMM process starts.
// launchdLogDir 用于返回 launchd 在 VMM 进程启动前必须打开的打包 stdout/stderr 目录。
func launchdLogDir(binDir string) string {
	return filepath.Join(filepath.Dir(binDir), "logs")
}

// ensureLaunchdLogDir creates the launchd stdout/stderr directory before plist loading so launchd never fails before VMM can initialize its own logs.
// ensureLaunchdLogDir 用于在加载 plist 前创建 launchd stdout/stderr 目录，避免 launchd 在 VMM 初始化自身日志前失败。
func ensureLaunchdLogDir(binDir string) error {
	logDir := launchdLogDir(binDir)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create launchd log dir %q: %w", logDir, err)
	}
	return nil
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

// commandSucceeds reports whether a platform service-manager probe exits successfully without printing expected negative lookup output.
// commandSucceeds 用于判断平台服务管理器探测命令是否成功退出，同时避免打印预期内的未命中输出。
func commandSucceeds(name string, args ...string) bool {
	command := exec.Command(name, args...)
	return command.Run() == nil
}

// queryLaunchdServiceStatus reports unloaded-but-installed daemons as a normal stopped state instead of failing the status command.
// queryLaunchdServiceStatus 用于把已安装但未加载的守护进程报告为正常 stopped 状态，而不是让 status 命令失败。
func queryLaunchdServiceStatus(label string, plistPath string) error {
	if _, err := os.Stat(plistPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("launchd service %q is not installed", label)
		}
		return fmt.Errorf("stat launchd plist %q: %w", plistPath, err)
	}

	output, err := exec.Command("launchctl", "print", "system/"+label).CombinedOutput()
	if err != nil {
		fmt.Println("unloaded")
		return nil
	}
	if len(output) > 0 {
		fmt.Print(string(output))
		return nil
	}
	fmt.Println("loaded")
	return nil
}
