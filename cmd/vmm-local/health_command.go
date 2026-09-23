// health_command.go implements the local, read-only runtime health probe used by the installer and operator TUI.
// health_command.go 用于实现安装器与运维 TUI 使用的本地只读运行时健康探测。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	// healthCommandUsage documents the stable probe command consumed by the standalone manager.
	// healthCommandUsage 用于记录独立管理器消费的稳定健康探测命令入口。
	healthCommandUsage = "usage: vmm-local health --config <absolute-directory-or-yaml> [--json]"

	// healthProbeTimeout bounds both connection establishment and the Healthz RPC so a stopped service cannot block the TUI.
	// healthProbeTimeout 同时限制连接建立与 Healthz RPC，避免停止中的服务阻塞 TUI。
	healthProbeTimeout = 2 * time.Second

	// healthClassOK identifies a successful Healthz response.
	// healthClassOK 表示 Healthz 成功返回健康状态。
	healthClassOK = "ok"

	// healthClassConfiguration identifies a rejected or unloadable configuration before any network operation.
	// healthClassConfiguration 表示在执行任何网络操作前拒绝或无法加载配置。
	healthClassConfiguration = "configuration_invalid"

	// healthClassUnreachable identifies a loopback endpoint that could not be connected.
	// healthClassUnreachable 表示允许的回环地址无法建立连接。
	healthClassUnreachable = "unreachable"

	// healthClassStorage identifies a connected runtime whose Healthz dependency check failed.
	// healthClassStorage 表示已连接运行时但其 Healthz 依赖检查失败。
	healthClassStorage = "storage_unavailable"

	// healthClassRuntime identifies a connected runtime that returned an unexpected health response or RPC failure.
	// healthClassRuntime 表示已连接运行时返回了非预期健康响应或 RPC 失败。
	healthClassRuntime = "runtime_error"
)

// healthCommand carries one parsed read-only health action and its output mode.
// healthCommand 保存一个已解析的只读健康动作及其输出模式。
type healthCommand struct {
	configPath string
	jsonOutput bool
}

// healthResult is the intentionally narrow machine-readable health contract; it never carries endpoint, trace, or error text.
// healthResult 是有意收窄的机器可读健康契约，不携带 endpoint、trace 或原始错误文本。
type healthResult struct {
	Status      string `json:"status"`
	Class       string `json:"class"`
	Error       string `json:"error,omitempty"`
	ElapsedMsec int64  `json:"elapsed_ms"`
}

// parseHealthCommand recognizes health flags without changing the foreground runtime parser.
// parseHealthCommand 用于识别 health 参数，同时保持前台运行时解析器不变。
func parseHealthCommand(args []string) (healthCommand, bool, error) {
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "health" {
		return healthCommand{}, false, nil
	}

	var command healthCommand
	configSeen := false
	jsonSeen := false
	for index := 1; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json" || arg == "-json":
			if jsonSeen {
				return healthCommand{}, true, fmt.Errorf("duplicate --json flag; %s", healthCommandUsage)
			}
			jsonSeen = true
			command.jsonOutput = true
		case arg == "--config" || arg == "-config":
			if configSeen {
				return healthCommand{}, true, fmt.Errorf("duplicate --config flag; %s", healthCommandUsage)
			}
			if index+1 >= len(args) {
				return healthCommand{}, true, fmt.Errorf("--config requires an absolute path; %s", healthCommandUsage)
			}
			index++
			command.configPath = args[index]
			configSeen = true
		case strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-config="):
			if configSeen {
				return healthCommand{}, true, fmt.Errorf("duplicate --config flag; %s", healthCommandUsage)
			}
			command.configPath = strings.TrimPrefix(strings.TrimPrefix(arg, "--config="), "-config=")
			configSeen = true
		case strings.HasPrefix(arg, "-"):
			return healthCommand{}, true, fmt.Errorf("unknown health flag %q; %s", arg, healthCommandUsage)
		default:
			return healthCommand{}, true, fmt.Errorf("unexpected positional argument %q; %s", arg, healthCommandUsage)
		}
	}

	if !configSeen || strings.TrimSpace(command.configPath) == "" {
		return healthCommand{}, true, fmt.Errorf("health requires --config; %s", healthCommandUsage)
	}
	if hasHealthControlCharacter(command.configPath) {
		return healthCommand{}, true, fmt.Errorf("--config contains control characters; %s", healthCommandUsage)
	}
	if !filepath.IsAbs(command.configPath) {
		return healthCommand{}, true, fmt.Errorf("--config must be absolute; %s", healthCommandUsage)
	}
	command.configPath = filepath.Clean(command.configPath)
	return command, true, nil
}

// hasHealthControlCharacter rejects invisible path characters that could change terminal or service-manager output.
// hasHealthControlCharacter 拒绝可能改变终端或服务管理器输出的不可见路径字符。
func hasHealthControlCharacter(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

// runHealthCommand resolves the real layered configuration and probes the configured loopback gRPC endpoint.
// runHealthCommand 解析真实分层配置，并探测配置中的回环 gRPC endpoint。
func runHealthCommand(command healthCommand, exePath, cwd string, output, errorOutput io.Writer) int {
	result := probeHealth(exePath, cwd, command.configPath)
	if command.jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(errorOutput, "write health result: %v\n", err)
			return 1
		}
	} else {
		writeTextHealthResult(output, result)
	}
	if result.Class != healthClassOK {
		return 1
	}
	return 0
}

// probeHealth performs bounded configuration loading, endpoint policy validation, dialing, and Healthz invocation.
// probeHealth 按有界时限完成配置加载、endpoint 策略校验、连接建立和 Healthz 调用。
func probeHealth(exePath, cwd, configPath string) healthResult {
	started := time.Now()
	finish := func(result healthResult) healthResult {
		result.ElapsedMsec = time.Since(started).Milliseconds()
		return result
	}

	// Reuse the production layout and loader so the probe observes the same base, bundled, user, and environment layers as startup.
	// 复用生产布局与加载器，确保探测与启动观察相同的 base、打包、用户和环境配置层。
	layout, err := config.ResolvePromptLayout(exePath, cwd, configPath)
	if err != nil {
		return finish(healthConfigurationResult("configuration_layout"))
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return finish(healthConfigurationResult(healthConfigErrorCode(err)))
	}

	address, err := validateHealthAddress(cfg.GRPC.ListenAddr)
	if err != nil {
		return finish(healthConfigurationResult("grpc_address_invalid"))
	}

	// Keep connection and RPC work inside one short deadline; the result never exposes the underlying transport error.
	// 将连接和 RPC 工作限制在一个短截止时间内，结果不会暴露底层传输错误。
	ctx, cancel := context.WithTimeout(context.Background(), healthProbeTimeout)
	defer cancel()
	conn, err := grpc.DialContext(ctx, address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return finish(healthResult{Status: "error", Class: healthClassUnreachable, Error: "grpc_unreachable"})
	}
	defer conn.Close()

	response, err := vmmv1.NewVMMServiceClient(conn).Healthz(ctx, &emptypb.Empty{})
	if err != nil {
		// VMM's Healthz maps dependency failures to Unavailable; deadline is also a bounded dependency failure after dialing succeeded.
		// VMM 的 Healthz 会把依赖失败映射为 Unavailable；连接成功后的 deadline 也属于有界依赖失败。
		if code := status.Code(err); code == codes.Unavailable || code == codes.DeadlineExceeded {
			return finish(healthResult{Status: "error", Class: healthClassStorage, Error: "storage_health_failed"})
		}
		return finish(healthResult{Status: "error", Class: healthClassRuntime, Error: "health_rpc_failed"})
	}
	if response == nil || strings.ToLower(strings.TrimSpace(response.GetStatus())) != healthClassOK {
		return finish(healthResult{Status: "error", Class: healthClassRuntime, Error: "health_status_invalid"})
	}
	return finish(healthResult{Status: "ok", Class: healthClassOK})
}

// healthConfigurationResult returns a redacted configuration failure without embedding filesystem or secret-bearing loader text.
// healthConfigurationResult 返回脱敏的配置失败，不嵌入文件系统文本或可能含密钥的加载器错误。
func healthConfigurationResult(code string) healthResult {
	return healthResult{Status: "error", Class: healthClassConfiguration, Error: code}
}

// healthConfigErrorCode maps trusted loader stages to stable, non-sensitive result codes.
// healthConfigErrorCode 将可信加载阶段映射为稳定且不敏感的结果码。
func healthConfigErrorCode(err error) string {
	if err == nil {
		return "configuration_invalid"
	}
	if stage, ok := config.ConfigLoadErrorStage(err); ok {
		switch stage {
		case config.ConfigLoadStageValidation:
			return "configuration_validation"
		case config.ConfigLoadStageRead:
			return "configuration_read"
		case config.ConfigLoadStageParse:
			return "configuration_parse"
		case config.ConfigLoadStageEnvironment:
			return "configuration_environment"
		}
	}
	return "configuration_invalid"
}

// validateHealthAddress accepts only explicitly loopback TCP addresses and rejects wildcard or public targets.
// validateHealthAddress 只接受明确的回环 TCP 地址，并拒绝通配符或公网目标。
func validateHealthAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" || hasHealthControlCharacter(address) {
		return "", errors.New("empty or unsafe gRPC address")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("gRPC address must be host:port")
	}
	if port == "" {
		return "", errors.New("gRPC address must include a port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return "", errors.New("gRPC address has an invalid port")
	}
	// A wildcard listener is safe to probe through its local loopback alias because the probe never dials a remote interface.
	// 通配监听通过本机回环别名进行探测是安全的，因为探测永远不会连接远程接口。
	switch host {
	case "", "0.0.0.0":
		return net.JoinHostPort("127.0.0.1", port), nil
	case "::":
		return net.JoinHostPort("::1", port), nil
	}

	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return address, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("gRPC address host is not loopback")
	}
	if ip.IsLoopback() || (ip.To4() != nil && ip.To4().IsLoopback()) {
		return address, nil
	}
	return "", errors.New("gRPC address host is not loopback")
}

// writeTextHealthResult renders a compact human-readable result for operators who omit --json.
// writeTextHealthResult 为未指定 --json 的操作者渲染紧凑的人类可读结果。
func writeTextHealthResult(output io.Writer, result healthResult) {
	if result.Error == "" {
		fmt.Fprintf(output, "status=%s class=%s elapsed_ms=%d\n", result.Status, result.Class, result.ElapsedMsec)
		return
	}
	fmt.Fprintf(output, "status=%s class=%s error=%s elapsed_ms=%d\n", result.Status, result.Class, result.Error, result.ElapsedMsec)
}
