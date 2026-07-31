// main.go implements the local VMM executable entrypoint.
// main.go 用于实现本地 VMM 可执行入口。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// main runs the local runtime CLI and exits with the status returned by argument parsing or application startup.
// main 用于运行本地运行时 CLI，并以参数解析或应用启动返回的状态码退出。
func main() {
	if code := runMain(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// runMain dispatches service-management commands before falling back to the normal foreground runtime.
// runMain 用于在进入普通前台运行时之前，优先分发服务管理命令。
func runMain(args []string) int {
	// Resolve runtime paths from the executable location and current workspace.
	// 根据可执行文件位置和当前工作区解析运行时路径。
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve executable path: %v\n", err)
		return 1
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get working directory: %v\n", err)
		return 1
	}

	serviceCommand, ok, err := parseServiceCommand(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if ok {
		if err := runServiceCommand(serviceCommand, exePath); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}

	runtimeArgs, err := parseRuntimeArguments(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if runtimeArgs.ManagedConfigPath != "" {
		if err := runManagedRuntime(context.Background(), runtimeArgs.ManagedConfigPath); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}
	if err := runRuntime(context.Background(), exePath, wd, runtimeArgs.ConfigPath, nil); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return 0
}

// runtimeArguments captures the two mutually exclusive foreground runtime entrypoints.
// runtimeArguments 用于保存两个互斥的前台运行时入口。
type runtimeArguments struct {
	ConfigPath        string
	ManagedConfigPath string
}

// parseRuntimeArguments parses only foreground runtime flags and rejects any attempt to mix standalone and managed configuration.
// parseRuntimeArguments 仅解析前台运行时参数，并拒绝混用独立配置与托管配置。
func parseRuntimeArguments(args []string) (runtimeArguments, error) {
	fs := flag.NewFlagSet("vmm-local", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "user override root (~/.vmm by default); explicit config files must use .yaml or .yml")
	managedConfigPath := fs.String("vulcan-managed-config", "", "absolute path to one strict Vulcan Code managed-runtime manifest")
	if err := fs.Parse(args); err != nil {
		return runtimeArguments{}, err
	}
	if fs.NArg() > 0 {
		return runtimeArguments{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	if *cfgPath != "" && *managedConfigPath != "" {
		return runtimeArguments{}, fmt.Errorf("-config and -vulcan-managed-config are mutually exclusive")
	}
	return runtimeArguments{
		ConfigPath:        *cfgPath,
		ManagedConfigPath: *managedConfigPath,
	}, nil
}

// runRuntime loads configuration, composes the application, and runs the local gRPC runtime until shutdown.
// runRuntime 用于加载配置、装配应用并运行本地 gRPC 运行时直到关闭。
func runRuntime(ctx context.Context, exePath string, wd string, cfgPath string, ready func()) error {
	// Build the prompt/config layout before any application dependency is created.
	// 在创建任何应用依赖之前先构建提示词与配置布局。
	layout, err := config.ResolvePromptLayout(exePath, wd, cfgPath)
	if err != nil {
		return fmt.Errorf("resolve prompt layout: %w", err)
	}
	fmt.Printf("[vmm-boot] SystemDir: %s\n", layout.SystemDir)
	fmt.Printf("[vmm-boot] UserDir: %s\n", layout.UserDir)
	for idx, path := range layout.ConfigPaths() {
		fmt.Printf("[vmm-boot] ConfigChain[%d]: %s\n", idx, path)
	}

	// Load the merged configuration layers before composing the normal runtime.
	// 先加载合并后的配置层，再装配正常运行时。
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Load prompt assets only for the normal runtime path because one-shot maintenance actions now live in the standalone vmm-migrate binary.
	// 仅在正常运行路径加载提示词资产，因为一次性维护动作已经迁移到独立的 vmm-migrate 二进制。
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, cfg.Prompts.PromptLanguage)
	if err != nil {
		return err
	}

	// Compose the application and start the gRPC service.
	// 完成应用装配并启动 gRPC 服务。
	application, err := app.NewLocal(cfg, prompts, layout)
	if err != nil {
		return fmt.Errorf("build app: %w", err)
	}
	if err := application.RunWithReady(ctx, ready); err != nil {
		return fmt.Errorf("run app: %w", err)
	}
	return nil
}

// managedRuntimeStatus is the authenticated-parent handshake written after the child listener is bound.
// managedRuntimeStatus 用于表示子进程监听成功后写出的已鉴别父进程握手。
type managedRuntimeStatus struct {
	ContractVersion          int    `json:"contract_version"`
	InstanceID               string `json:"instance_id"`
	Generation               uint64 `json:"generation"`
	ProcessID                int    `json:"process_id"`
	ParentProcessID          int    `json:"parent_process_id"`
	ManifestDigest           string `json:"manifest_digest"`
	GRPCListenAddr           string `json:"grpc_listen_addr"`
	GRPCProtoVersion         int    `json:"grpc_proto_version"`
	InferenceProtocolVersion int    `json:"inference_protocol_version"`
}

// runManagedRuntime loads the single manifest, builds managed adapters, and reports the resolved listener address atomically.
// runManagedRuntime 用于加载单一清单、构建托管适配器，并原子回报最终监听地址。
func runManagedRuntime(ctx context.Context, managedConfigPath string) error {
	bundle, err := config.LoadVulcanManagedConfig(managedConfigPath)
	if err != nil {
		return fmt.Errorf("load Vulcan managed config: %w", err)
	}
	releaseLock, err := acquireManagedRuntimeLock(bundle)
	if err != nil {
		return err
	}
	defer releaseLock()
	prompts, err := config.NewPromptManager(
		bundle.Layout.SystemDir,
		bundle.Layout.UserDir,
		bundle.Config.Prompts.PromptLanguage,
	)
	if err != nil {
		return fmt.Errorf("load managed prompt assets: %w", err)
	}
	application, err := app.NewManaged(bundle, prompts)
	if err != nil {
		return fmt.Errorf("build managed app: %w", err)
	}
	managedContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchManagedLifecycle(
		managedContext,
		cancel,
		bundle.Manifest.Parent.ProcessID,
		bundle.Manifest.Parent.StartedAtUnixMS,
		bundle.Manifest.Runtime.Inference.DiscoveryFile,
		bundle.Manifest.Runtime.ShutdownFile,
	)
	return application.RunWithReadyAddress(managedContext, func(address string) error {
		status := managedRuntimeStatus{
			ContractVersion:          config.ManagedContractVersion,
			InstanceID:               bundle.Manifest.InstanceID,
			Generation:               bundle.Manifest.Generation,
			ProcessID:                os.Getpid(),
			ParentProcessID:          bundle.Manifest.Parent.ProcessID,
			ManifestDigest:           bundle.Manifest.Digest,
			GRPCListenAddr:           address,
			GRPCProtoVersion:         1,
			InferenceProtocolVersion: 2,
		}
		return writeManagedStatus(bundle.Manifest.Runtime.StatusFile, status)
	})
}

// managedRuntimeLock records the exact parent and child identities that own one managed data root.
// managedRuntimeLock 用于记录拥有一个托管数据根的精确父子进程身份。
type managedRuntimeLock struct {
	InstanceID      string `json:"instance_id"`
	ParentProcessID int    `json:"parent_process_id"`
	ProcessID       int    `json:"process_id"`
}

// acquireManagedRuntimeLock prevents two live managed generations from binding the same stable data root.
// acquireManagedRuntimeLock 用于防止两个存活托管代次绑定同一个稳定数据根。
func acquireManagedRuntimeLock(bundle config.ManagedRuntimeBundle) (func(), error) {
	if err := os.MkdirAll(bundle.DataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create managed data root: %w", err)
	}
	path := filepath.Join(bundle.DataRoot, "managed-runtime.lock")
	if body, err := os.ReadFile(path); err == nil {
		var existing managedRuntimeLock
		if json.Unmarshal(body, &existing) != nil {
			return nil, errors.New("managed runtime lock is malformed; refusing destructive recovery")
		}
		if managedParentAlive(existing.ProcessID) {
			return nil, fmt.Errorf(
				"managed data root is already owned by live parent %d and child %d",
				existing.ParentProcessID,
				existing.ProcessID,
			)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale managed runtime lock: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect managed runtime lock: %w", err)
	}

	owner := managedRuntimeLock{
		InstanceID:      bundle.Manifest.InstanceID,
		ParentProcessID: bundle.Manifest.Parent.ProcessID,
		ProcessID:       os.Getpid(),
	}
	body, err := json.Marshal(owner)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create managed runtime lock: %w", err)
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write managed runtime lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("flush managed runtime lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close managed runtime lock: %w", err)
	}
	return func() {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return
		}
		var current managedRuntimeLock
		if json.Unmarshal(body, &current) == nil &&
			current.InstanceID == owner.InstanceID &&
			current.ProcessID == owner.ProcessID {
			_ = os.Remove(path)
		}
	}, nil
}

// watchManagedLifecycle cancels the child runtime when the exact parent disappears or the host publishes the shutdown marker.
// watchManagedLifecycle 会在精确父进程消失或宿主发布关闭标记时取消子运行时。
func watchManagedLifecycle(
	ctx context.Context,
	cancel context.CancelFunc,
	parentProcessID int,
	parentStartedAtUnixMS int64,
	discoveryFile string,
	shutdownFile string,
) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(shutdownFile); err == nil {
				cancel()
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				cancel()
				return
			}
			if !managedParentAlive(parentProcessID) {
				cancel()
				return
			}
			matches, err := managedHostIdentityMatches(
				discoveryFile,
				parentProcessID,
				parentStartedAtUnixMS,
			)
			if err == nil && !matches {
				cancel()
				return
			}
		}
	}
}

// managedHostIdentity is the non-secret identity subset of Vulcan Code inference discovery.
// managedHostIdentity 是 Vulcan Code 推理发现文件中不含密钥的身份子集。
type managedHostIdentity struct {
	HostProcessID   int    `json:"process_id"`
	StartedAtUnixMS string `json:"started_at_unix_ms"`
}

// managedHostIdentityMatches verifies that a live reused PID still belongs to the owning Vulcan Code generation.
// managedHostIdentityMatches 用于确认仍存活或被复用的 PID 继续属于持有该运行时的 Vulcan Code 代次。
func managedHostIdentityMatches(
	discoveryFile string,
	parentProcessID int,
	parentStartedAtUnixMS int64,
) (bool, error) {
	body, err := os.ReadFile(discoveryFile)
	if err != nil {
		return false, err
	}
	var identity managedHostIdentity
	if err := json.Unmarshal(body, &identity); err != nil {
		return false, err
	}
	startedAtUnixMS, err := strconv.ParseInt(identity.StartedAtUnixMS, 10, 64)
	if err != nil {
		return false, err
	}
	return identity.HostProcessID == parentProcessID &&
		startedAtUnixMS == parentStartedAtUnixMS, nil
}

// writeManagedStatus serializes one ready handshake through a same-directory temporary file and atomic replacement.
// writeManagedStatus 用于通过同目录临时文件与原子替换序列化一份就绪握手。
func writeManagedStatus(path string, status managedRuntimeStatus) error {
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create managed status directory: %w", err)
	}
	tempFile, err := os.CreateTemp(directory, ".managed-status-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if err := tempFile.Chmod(0o600); err != nil {
		_ = tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(body); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
