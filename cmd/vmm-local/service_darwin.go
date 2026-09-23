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
	"strconv"
	"strings"
	"syscall"
)

// launchdPlist captures the launchd property-list shape needed by the VMM runtime service.
// launchdPlist 用于承载 VMM 运行时服务所需的 launchd 属性列表结构。
type launchdPlist struct {
	// XMLName fixes the root element so launchd receives a standard plist document.
	// XMLName 用于固定根元素，确保 launchd 收到标准 plist 文档。
	XMLName xml.Name `xml:"plist"`

	// Version stores the plist schema version expected by launchd.
	// Version 用于保存 launchd 期望的 plist schema 版本。
	Version string `xml:"version,attr"`

	// Dict stores the generated daemon properties and supports loading them back for enable/disable changes.
	// Dict 用于保存生成的守护进程属性，并支持为 enable/disable 操作重新读取。
	Dict launchdDict `xml:"dict"`
}

// UnmarshalXML accepts only the expected plist root, version attribute, and one dictionary child.
// UnmarshalXML 只接受预期的 plist 根、版本属性和一个 dictionary 子节点。
func (p *launchdPlist) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "plist" {
		return fmt.Errorf("unexpected launchd plist root %q", start.Name.Local)
	}
	for _, attribute := range start.Attr {
		if attribute.Name.Local != "version" {
			return fmt.Errorf("unsupported launchd plist root attribute %q", attribute.Name.Local)
		}
		if p.Version != "" {
			return fmt.Errorf("duplicate launchd plist version attribute")
		}
		p.Version = attribute.Value
	}
	seenDict := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return fmt.Errorf("unexpected launchd plist text %q", string(value))
			}
		case xml.StartElement:
			if value.Name.Local != "dict" || seenDict {
				return fmt.Errorf("unexpected launchd plist element %q", value.Name.Local)
			}
			if err := decoder.DecodeElement(&p.Dict, &value); err != nil {
				return err
			}
			seenDict = true
		case xml.EndElement:
			if value.Name == start.Name {
				if !seenDict {
					return fmt.Errorf("launchd plist is missing dict")
				}
				return nil
			}
			return fmt.Errorf("unexpected launchd plist end element %q", value.Name.Local)
		}
	}
}

// launchdDict stores the generated plist properties in a typed shape so lifecycle changes can preserve the registered config path.
// launchdDict 用类型化结构保存 plist 属性，使生命周期修改可以保留已注册的配置路径。
type launchdDict struct {
	// Label is the stable launchd service label.
	// Label 用于保存稳定的 launchd 服务标签。
	Label string

	// ProgramArguments stores the executable and service runtime arguments.
	// ProgramArguments 用于保存可执行文件及服务运行参数。
	ProgramArguments []string

	// WorkingDirectory stores the packaged binary directory used by the runtime.
	// WorkingDirectory 用于保存运行时使用的打包二进制目录。
	WorkingDirectory string

	// UserName stores the explicit local account launchd must use for the daemon.
	// UserName 保存 launchd 必须用于守护进程的显式本机账户。
	UserName string

	// RunAtLoad controls launchd boot-time activation.
	// RunAtLoad 用于控制 launchd 是否在加载时启动服务。
	RunAtLoad bool

	// KeepAlive keeps a manually loaded runtime supervised after it starts.
	// KeepAlive 用于让手动加载的运行时启动后继续受到监管。
	KeepAlive bool

	// StandardOutPath stores the launchd stdout path.
	// StandardOutPath 用于保存 launchd 标准输出路径。
	StandardOutPath string

	// StandardErrorPath stores the launchd stderr path.
	// StandardErrorPath 用于保存 launchd 标准错误路径。
	StandardErrorPath string
}

// launchdRenderOptions carries the optional values used when rendering or rewriting a daemon plist.
// launchdRenderOptions 用于承载渲染或重写守护进程 plist 时的可选值。
type launchdRenderOptions struct {
	// configPath is the absolute configuration root or YAML file passed to service run.
	// configPath 用于保存传给 service run 的绝对配置根目录或 YAML 文件。
	configPath string

	// autoStart determines the generated RunAtLoad value.
	// autoStart 用于决定生成的 RunAtLoad 值。
	autoStart bool

	// user stores the explicit local account launchd must use for the daemon.
	// user 保存 launchd 必须用于守护进程的显式本机账户。
	user string
}

// launchdServiceSpec is the minimal persisted service definition needed by enable and disable.
// launchdServiceSpec 用于保存 enable 和 disable 所需的最小持久化服务定义。
type launchdServiceSpec struct {
	// Label identifies the launchd job.
	// Label 用于标识 launchd job。
	Label string

	// Executable identifies the packaged VMM binary.
	// Executable 用于标识打包的 VMM 可执行文件。
	Executable string

	// UserName identifies the explicitly selected local account for launchd execution.
	// UserName 用于标识 launchd 执行时显式选定的本机账户。
	UserName string

	// BinDir identifies the runtime working directory.
	// BinDir 用于标识运行时工作目录。
	BinDir string

	// ConfigPath identifies the persisted absolute configuration path.
	// ConfigPath 用于标识已持久化的绝对配置路径。
	ConfigPath string

	// AutoStart stores the current RunAtLoad policy.
	// AutoStart 用于保存当前 RunAtLoad 策略。
	AutoStart bool

	// KeepAlive stores the launchd supervision policy and must match the generated boot policy.
	// KeepAlive 用于保存 launchd 守护策略，并且必须与生成的开机策略一致。
	KeepAlive bool

	// StandardOutPath stores the generated stdout path used to identify the VMM daemon.
	// StandardOutPath 用于保存识别 VMM 守护进程的标准输出路径。
	StandardOutPath string

	// StandardErrorPath stores the generated stderr path used to identify the VMM daemon.
	// StandardErrorPath 用于保存识别 VMM 守护进程的标准错误路径。
	StandardErrorPath string
}

// MarshalXML writes the known launchd keys in deterministic order for stable review and rewriting.
// MarshalXML 按确定顺序写出已知 launchd 键，保证结果便于审核和重写。
func (d launchdDict) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	if start.Name.Local == "" {
		start.Name.Local = "dict"
	}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	if err := encodeLaunchdString(encoder, "Label", d.Label); err != nil {
		return err
	}
	if err := encodeLaunchdStringArray(encoder, "ProgramArguments", d.ProgramArguments); err != nil {
		return err
	}
	if err := encodeLaunchdString(encoder, "WorkingDirectory", d.WorkingDirectory); err != nil {
		return err
	}
	if d.UserName != "" {
		if err := encodeLaunchdString(encoder, "UserName", d.UserName); err != nil {
			return err
		}
	}
	if err := encodeLaunchdBool(encoder, "RunAtLoad", d.RunAtLoad); err != nil {
		return err
	}
	if err := encodeLaunchdBool(encoder, "KeepAlive", d.KeepAlive); err != nil {
		return err
	}
	if err := encodeLaunchdString(encoder, "StandardOutPath", d.StandardOutPath); err != nil {
		return err
	}
	if err := encodeLaunchdString(encoder, "StandardErrorPath", d.StandardErrorPath); err != nil {
		return err
	}
	return encoder.EncodeToken(xml.EndElement{Name: start.Name})
}

// UnmarshalXML reads generated launchd key/value pairs so enable and disable retain the original command line.
// UnmarshalXML 读取生成的 launchd 键值对，使 enable 和 disable 保留原始命令行。
func (d *launchdDict) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	seen := make(map[string]bool)
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.EndElement:
			if value.Name == start.Name {
				return nil
			}
		case xml.StartElement:
			if value.Name.Local != "key" {
				return fmt.Errorf("unexpected launchd plist element %q", value.Name.Local)
			}
			var property string
			if err := decoder.DecodeElement(&property, &value); err != nil {
				return err
			}
			if seen[property] {
				return fmt.Errorf("duplicate launchd plist key %q", property)
			}
			seen[property] = true
			valueStart, err := nextLaunchdStartElement(decoder)
			if err != nil {
				return err
			}
			switch property {
			case "Label":
				if err := decoder.DecodeElement(&d.Label, &valueStart); err != nil {
					return err
				}
			case "ProgramArguments":
				arguments, err := decodeLaunchdStringArray(decoder, valueStart)
				if err != nil {
					return err
				}
				d.ProgramArguments = arguments
			case "WorkingDirectory":
				if err := decoder.DecodeElement(&d.WorkingDirectory, &valueStart); err != nil {
					return err
				}
			case "UserName":
				if err := decoder.DecodeElement(&d.UserName, &valueStart); err != nil {
					return err
				}
			case "RunAtLoad":
				d.RunAtLoad, err = decodeLaunchdBool(decoder, valueStart)
				if err != nil {
					return err
				}
			case "KeepAlive":
				d.KeepAlive, err = decodeLaunchdBool(decoder, valueStart)
				if err != nil {
					return err
				}
			case "StandardOutPath":
				if err := decoder.DecodeElement(&d.StandardOutPath, &valueStart); err != nil {
					return err
				}
			case "StandardErrorPath":
				if err := decoder.DecodeElement(&d.StandardErrorPath, &valueStart); err != nil {
					return err
				}
			default:
				if err := decoder.Skip(); err != nil {
					return err
				}
				return fmt.Errorf("unsupported launchd plist key %q", property)
			}
		}
	}
}

// decodeLaunchdStringArray reads exactly the string children of a plist array and rejects foreign value types.
// decodeLaunchdStringArray 只读取 plist 数组中的 string 子元素，并拒绝其他值类型。
func decodeLaunchdStringArray(decoder *xml.Decoder, start xml.StartElement) ([]string, error) {
	if start.Name.Local != "array" || len(start.Attr) != 0 {
		return nil, fmt.Errorf("launchd ProgramArguments is not a plain array")
	}
	var arguments []string
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch value := token.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return nil, fmt.Errorf("launchd ProgramArguments contains unexpected text")
			}
		case xml.StartElement:
			if value.Name.Local != "string" || len(value.Attr) != 0 {
				return nil, fmt.Errorf("launchd ProgramArguments contains an unsupported element")
			}
			var argument string
			if err := decoder.DecodeElement(&argument, &value); err != nil {
				return nil, err
			}
			arguments = append(arguments, argument)
		case xml.EndElement:
			if value.Name != start.Name {
				return nil, fmt.Errorf("launchd ProgramArguments has an unexpected closing element")
			}
			return arguments, nil
		default:
			return nil, fmt.Errorf("launchd ProgramArguments contains an unsupported token")
		}
	}
}

// nextLaunchdStartElement skips formatting whitespace and returns the next plist value element.
// nextLaunchdStartElement 用于跳过格式空白并返回下一个 plist 值元素。
func nextLaunchdStartElement(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			return value, nil
		case xml.CharData:
			if strings.TrimSpace(string(value)) == "" {
				continue
			}
			return xml.StartElement{}, fmt.Errorf("unexpected launchd plist text %q", string(value))
		default:
			return xml.StartElement{}, fmt.Errorf("missing launchd plist value")
		}
	}
}

// decodeLaunchdBool decodes launchd's empty true/false element representation.
// decodeLaunchdBool 用于解析 launchd 使用的空 true/false 元素表示。
func decodeLaunchdBool(decoder *xml.Decoder, start xml.StartElement) (bool, error) {
	if start.Name.Local != "true" && start.Name.Local != "false" {
		return false, fmt.Errorf("expected launchd boolean, got %q", start.Name.Local)
	}
	if err := decoder.Skip(); err != nil {
		return false, err
	}
	return start.Name.Local == "true", nil
}

// encodeLaunchdString emits one plist key and string value pair.
// encodeLaunchdString 用于写出一个 plist 键与字符串值对。
func encodeLaunchdString(encoder *xml.Encoder, name string, value string) error {
	if err := encoder.EncodeElement(name, xml.StartElement{Name: xml.Name{Local: "key"}}); err != nil {
		return err
	}
	return encoder.EncodeElement(value, xml.StartElement{Name: xml.Name{Local: "string"}})
}

// encodeLaunchdStringArray emits one plist key and ordered string array pair.
// encodeLaunchdStringArray 用于写出一个 plist 键与有序字符串数组值对。
func encodeLaunchdStringArray(encoder *xml.Encoder, name string, values []string) error {
	if err := encoder.EncodeElement(name, xml.StartElement{Name: xml.Name{Local: "key"}}); err != nil {
		return err
	}
	arrayStart := xml.StartElement{Name: xml.Name{Local: "array"}}
	if err := encoder.EncodeToken(arrayStart); err != nil {
		return err
	}
	for _, value := range values {
		if err := encoder.EncodeElement(value, xml.StartElement{Name: xml.Name{Local: "string"}}); err != nil {
			return err
		}
	}
	return encoder.EncodeToken(xml.EndElement{Name: arrayStart.Name})
}

// encodeLaunchdBool emits one plist key and empty true/false element pair.
// encodeLaunchdBool 用于写出一个 plist 键与空 true/false 元素值对。
func encodeLaunchdBool(encoder *xml.Encoder, name string, value bool) error {
	if err := encoder.EncodeElement(name, xml.StartElement{Name: xml.Name{Local: "key"}}); err != nil {
		return err
	}
	local := "false"
	if value {
		local = "true"
	}
	start := xml.StartElement{Name: xml.Name{Local: local}}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	return encoder.EncodeToken(xml.EndElement{Name: start.Name})
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
