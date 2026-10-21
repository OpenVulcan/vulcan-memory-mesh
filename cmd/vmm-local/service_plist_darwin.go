//go:build darwin

// Typed launchd plist codec used by macOS service installation.
// macOS 服务安装链路使用的强类型 launchd plist 编解码。
package main

import (
	"encoding/xml"
	"fmt"
	"strings"
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
