// runtime.go owns the shared vldb-controller session and the SQLite/LanceDB backend bindings used by one VMM process.
// runtime.go 用于持有单个 VMM 进程共享的 vldb-controller 会话，以及 SQLite/LanceDB 后端绑定。
package vldb_controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	controllerclient "github.com/OpenVulcan/vldb-controller/client-go/controller"
)

const (
	// sqliteConnectionPoolSize matches the vldb-sqlite v0.1.6 database default.
	// sqliteConnectionPoolSize 与 vldb-sqlite v0.1.6 的数据库默认值保持一致。
	sqliteConnectionPoolSize uint32 = 8
	// sqliteBusyTimeout matches the vldb-sqlite v0.1.6 database default.
	// sqliteBusyTimeout 与 vldb-sqlite v0.1.6 的数据库默认值保持一致。
	sqliteBusyTimeout = 5 * time.Second
	// sqliteWALAutocheckpointPages matches the vldb-sqlite v0.1.6 pragma default.
	// sqliteWALAutocheckpointPages 与 vldb-sqlite v0.1.6 的 pragma 默认值保持一致。
	sqliteWALAutocheckpointPages uint32 = 1000
	// sqliteCacheSizeKiB matches the vldb-sqlite v0.1.6 pragma default.
	// sqliteCacheSizeKiB 与 vldb-sqlite v0.1.6 的 pragma 默认值保持一致。
	sqliteCacheSizeKiB int64 = 65_536
	// sqliteMmapSizeBytes matches the vldb-sqlite v0.1.6 pragma default.
	// sqliteMmapSizeBytes 与 vldb-sqlite v0.1.6 的 pragma 默认值保持一致。
	sqliteMmapSizeBytes uint64 = 268_435_456
	// lanceMaxUpsertPayload matches the vldb-lancedb v0.1.5 engine default.
	// lanceMaxUpsertPayload 与 vldb-lancedb v0.1.5 的引擎默认值保持一致。
	lanceMaxUpsertPayload uint64 = 50 * 1024 * 1024
	// lanceMaxSearchLimit matches the vldb-lancedb v0.1.5 engine default.
	// lanceMaxSearchLimit 与 vldb-lancedb v0.1.5 的引擎默认值保持一致。
	lanceMaxSearchLimit uint64 = 10_000
	// lanceMaxConcurrentRequests matches the vldb-lancedb v0.1.5 engine default.
	// lanceMaxConcurrentRequests 与 vldb-lancedb v0.1.5 的引擎默认值保持一致。
	lanceMaxConcurrentRequests uint64 = 500
)

// Config contains the fully resolved controller lifecycle and physical database paths for one VMM runtime.
// Config 包含单个 VMM 运行时已经解析完成的 controller 生命周期参数与物理数据库路径。
type Config struct {
	// Endpoint is the loopback controller gRPC endpoint.
	// Endpoint 是 controller 的 loopback gRPC 端点。
	Endpoint string
	// AutoSpawn controls whether the SDK may launch the controller executable.
	// AutoSpawn 控制 SDK 是否允许启动 controller 可执行文件。
	AutoSpawn bool
	// Executable is the controller executable path used by automatic startup.
	// Executable 是自动启动时使用的 controller 可执行文件路径。
	Executable string
	// ProcessMode selects managed or service controller lifecycle behavior.
	// ProcessMode 选择 managed 或 service controller 生命周期行为。
	ProcessMode string
	// MinimumUptime is passed to an automatically spawned managed controller.
	// MinimumUptime 会传递给自动启动的 managed controller。
	MinimumUptime time.Duration
	// IdleTimeout is passed to an automatically spawned managed controller.
	// IdleTimeout 会传递给自动启动的 managed controller。
	IdleTimeout time.Duration
	// LeaseTTL controls the registered VMM client lease.
	// LeaseTTL 控制已注册 VMM 客户端的租约时长。
	LeaseTTL time.Duration
	// ConnectTimeout bounds each controller connection attempt.
	// ConnectTimeout 限制每次 controller 连接尝试。
	ConnectTimeout time.Duration
	// StartupTimeout bounds automatic controller startup.
	// StartupTimeout 限制自动启动 controller 的总时长。
	StartupTimeout time.Duration
	// StartupRetryInterval controls controller readiness polling.
	// StartupRetryInterval 控制 controller 就绪探测间隔。
	StartupRetryInterval time.Duration
	// LeaseRenewInterval controls background lease renewal.
	// LeaseRenewInterval 控制后台租约续期间隔。
	LeaseRenewInterval time.Duration
	// RequestTimeout bounds individual database proxy operations.
	// RequestTimeout 限制单次数据库代理操作。
	RequestTimeout time.Duration
	// RequireExclusiveSpace rejects startup when another live controller client is already attached to the target space.
	// RequireExclusiveSpace 在已有其他存活 controller 客户端附着目标空间时拒绝启动。
	RequireExclusiveSpace bool
	// SpaceID is the stable VMM installation-level controller space identifier.
	// SpaceID 是 VMM 安装级稳定 controller 空间标识。
	SpaceID string
	// SpaceLabel is the diagnostic controller space label.
	// SpaceLabel 是 controller 空间的诊断标签。
	SpaceLabel string
	// SpaceRoot is the canonical physical database root for the controller space.
	// SpaceRoot 是 controller 空间对应的规范化物理数据库根目录。
	SpaceRoot string
	// SQLiteDatabase is the canonical SQLite database file path.
	// SQLiteDatabase 是规范化 SQLite 数据库文件路径。
	SQLiteDatabase string
	// LanceDBDirectory is the canonical LanceDB database directory.
	// LanceDBDirectory 是规范化 LanceDB 数据库目录。
	LanceDBDirectory string
}

// Runtime owns one controller client session and one process-scoped binding pair.
// Runtime 持有一个 controller 客户端会话与一对进程级后端绑定。
type Runtime struct {
	// mu protects lifecycle flags and one-time shutdown.
	// mu 保护生命周期状态与单次关闭流程。
	mu sync.Mutex
	// client is the shared Go-native controller SDK client.
	// client 是共享的 Go 原生 controller SDK 客户端。
	client *controllerclient.Client
	// requestTimeout bounds adapter proxy calls.
	// requestTimeout 限制适配器代理调用。
	requestTimeout time.Duration
	// spaceID identifies the attached installation-level controller space.
	// spaceID 标识已附着的安装级 controller 空间。
	spaceID string
	// sqliteBindingID identifies the process-scoped SQLite binding.
	// sqliteBindingID 标识进程级 SQLite 绑定。
	sqliteBindingID string
	// lanceDBBindingID identifies the process-scoped LanceDB binding.
	// lanceDBBindingID 标识进程级 LanceDB 绑定。
	lanceDBBindingID string
	// sqliteEnabled records whether shutdown must release the SQLite binding.
	// sqliteEnabled 记录关闭时是否需要释放 SQLite 绑定。
	sqliteEnabled bool
	// lanceDBEnabled records whether shutdown must release the LanceDB binding.
	// lanceDBEnabled 记录关闭时是否需要释放 LanceDB 绑定。
	lanceDBEnabled bool
	// spaceAttached records whether shutdown must detach the controller space.
	// spaceAttached 记录关闭时是否需要分离 controller 空间。
	spaceAttached bool
	// closed prevents duplicate backend and session release.
	// closed 防止重复释放后端与会话。
	closed bool
}

// New connects to the controller, attaches one canonical root space, and enables both database backends.
// New 连接 controller、附着一个规范化根空间，并启用两种数据库后端。
func New(ctx context.Context, cfg Config) (*Runtime, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return nil, err
	}
	processMode := controllerclient.ProcessMode(strings.ToLower(strings.TrimSpace(normalized.ProcessMode)))
	client := controllerclient.New(controllerclient.Config{
		Endpoint:             normalized.Endpoint,
		AutoSpawn:            normalized.AutoSpawn,
		SpawnExecutable:      normalized.Executable,
		SpawnProcessMode:     processMode,
		MinimumUptime:        normalized.MinimumUptime,
		IdleTimeout:          normalized.IdleTimeout,
		DefaultLeaseTTL:      normalized.LeaseTTL,
		ConnectTimeout:       normalized.ConnectTimeout,
		StartupTimeout:       normalized.StartupTimeout,
		StartupRetryInterval: normalized.StartupRetryInterval,
		LeaseRenewInterval:   normalized.LeaseRenewInterval,
	}, controllerclient.ClientRegistration{
		ClientName:  "VulcanMemoryMesh",
		HostKind:    "vmm-local",
		ProcessID:   uint32(os.Getpid()),
		ProcessName: filepath.Base(os.Args[0]),
		LeaseTTL:    normalized.LeaseTTL,
	})
	runtime := &Runtime{
		client:           client,
		requestTimeout:   normalized.RequestTimeout,
		spaceID:          normalized.SpaceID,
		sqliteBindingID:  "vmm-sqlite-" + instanceID,
		lanceDBBindingID: "vmm-lancedb-" + instanceID,
	}

	// Register the session and stable installation space before enabling process-scoped database bindings.
	// 在启用进程级数据库绑定前，先注册会话与稳定的安装级空间。
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect vldb-controller: %w", err)
	}
	if _, err := client.AttachSpace(ctx, controllerclient.SpaceRegistration{
		SpaceID:    normalized.SpaceID,
		SpaceLabel: normalized.SpaceLabel,
		SpaceKind:  controllerclient.SpaceKindRoot,
		SpaceRoot:  normalized.SpaceRoot,
	}); err != nil {
		_ = client.Shutdown(context.Background())
		return nil, fmt.Errorf("attach vldb-controller space: %w", err)
	}
	runtime.spaceAttached = true

	// Destructive maintenance sessions must not share the physical database root with a live VMM session.
	// 破坏性维护会话不得与仍存活的 VMM 会话共享同一物理数据库根。
	if normalized.RequireExclusiveSpace {
		spaces, err := client.ListSpaces(ctx)
		if err != nil {
			_ = runtime.Shutdown(context.Background())
			return nil, fmt.Errorf("verify exclusive vldb-controller space: %w", err)
		}
		for _, space := range spaces {
			if space == nil || space.SpaceID != normalized.SpaceID {
				continue
			}
			if space.AttachedClients > 1 {
				_ = runtime.Shutdown(context.Background())
				return nil, fmt.Errorf("controller space %q is already used by %d active clients; stop VMM before maintenance", normalized.SpaceID, space.AttachedClients-1)
			}
			break
		}
	}

	// Send every SQLite hardening option explicitly because protobuf boolean zero values do not inherit vldb-sqlite defaults.
	// 显式发送全部 SQLite 加固选项，因为 protobuf 布尔零值不会继承 vldb-sqlite 默认值。
	if err := client.EnableSqlite(ctx, &controllerclient.SqliteEnableRequest{
		SpaceID:                normalized.SpaceID,
		BindingID:              runtime.sqliteBindingID,
		DBPath:                 normalized.SQLiteDatabase,
		ConnectionPoolSize:     sqliteConnectionPoolSize,
		BusyTimeoutMs:          uint64(sqliteBusyTimeout / time.Millisecond),
		JournalMode:            "WAL",
		Synchronous:            "NORMAL",
		ForeignKeys:            true,
		TempStore:              "MEMORY",
		WalAutocheckpointPages: sqliteWALAutocheckpointPages,
		CacheSizeKib:           sqliteCacheSizeKiB,
		MmapSizeBytes:          sqliteMmapSizeBytes,
		EnforceDBFileLock:      true,
		ReadOnly:               false,
		AllowURIFilenames:      false,
		TrustedSchema:          false,
		Defensive:              true,
	}); err != nil {
		_ = runtime.Shutdown(context.Background())
		return nil, fmt.Errorf("enable vldb-controller sqlite backend: %w", err)
	}
	runtime.sqliteEnabled = true

	// Enable LanceDB through the same controller session so one process owns both physical database handles.
	// 通过同一个 controller 会话启用 LanceDB，使一个进程统一持有两种物理数据库句柄。
	if err := client.EnableLanceDB(ctx, &controllerclient.LanceDBEnableRequest{
		SpaceID:                   normalized.SpaceID,
		BindingID:                 runtime.lanceDBBindingID,
		DefaultDBPath:             normalized.LanceDBDirectory,
		ReadConsistencyIntervalMs: 0,
		MaxUpsertPayload:          lanceMaxUpsertPayload,
		MaxSearchLimit:            lanceMaxSearchLimit,
		MaxConcurrentRequests:     lanceMaxConcurrentRequests,
	}); err != nil {
		_ = runtime.Shutdown(context.Background())
		return nil, fmt.Errorf("enable vldb-controller lancedb backend: %w", err)
	}
	runtime.lanceDBEnabled = true
	return runtime, nil
}

// Client returns the shared SDK client for the SQLite and LanceDB transport bridges.
// Client 返回供 SQLite 与 LanceDB 传输桥接层共享的 SDK 客户端。
func (r *Runtime) Client() *controllerclient.Client {
	if r == nil {
		return nil
	}
	return r.client
}

// SpaceID returns the attached controller space identifier.
// SpaceID 返回已附着的 controller 空间标识。
func (r *Runtime) SpaceID() string {
	if r == nil {
		return ""
	}
	return r.spaceID
}

// SQLiteBindingID returns the process-scoped SQLite binding identifier.
// SQLiteBindingID 返回进程级 SQLite 绑定标识。
func (r *Runtime) SQLiteBindingID() string {
	if r == nil {
		return ""
	}
	return r.sqliteBindingID
}

// LanceDBBindingID returns the process-scoped LanceDB binding identifier.
// LanceDBBindingID 返回进程级 LanceDB 绑定标识。
func (r *Runtime) LanceDBBindingID() string {
	if r == nil {
		return ""
	}
	return r.lanceDBBindingID
}

// RequestContext creates one bounded background context for an adapter operation whose legacy handle interface has no context parameter.
// RequestContext 为旧句柄接口中没有 context 参数的适配器操作创建一个有界后台上下文。
func (r *Runtime) RequestContext() (context.Context, context.CancelFunc) {
	timeout := 5 * time.Second
	if r != nil && r.requestTimeout > 0 {
		timeout = r.requestTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// CheckHealth verifies that the controller session is reachable and that both database backends remain enabled on the attached space.
// CheckHealth 用于校验 controller 会话可达，并确认已附着空间中的两种数据库后端仍处于启用状态。
func (r *Runtime) CheckHealth(ctx context.Context) error {
	if r == nil {
		return errors.New("controller runtime is nil")
	}
	r.mu.Lock()
	closed := r.closed
	client := r.client
	spaceID := r.spaceID
	r.mu.Unlock()
	if closed {
		return errors.New("controller runtime is closed")
	}
	if client == nil {
		return errors.New("controller client is nil")
	}

	// Query controller-owned state through the recoverable read path so health checks also exercise session recovery without replaying writes.
	// 通过可恢复的读取路径查询 controller 持有的状态，使健康检查同时覆盖会话恢复且不会重放写请求。
	if _, err := client.GetStatus(ctx); err != nil {
		return fmt.Errorf("get controller status: %w", err)
	}
	spaces, err := client.ListSpaces(ctx)
	if err != nil {
		return fmt.Errorf("list controller spaces: %w", err)
	}
	for _, space := range spaces {
		if space == nil || space.SpaceID != spaceID {
			continue
		}
		if space.SQLite == nil || !space.SQLite.Enabled {
			return fmt.Errorf("controller sqlite backend is not enabled for space %q", spaceID)
		}
		if space.LanceDB == nil || !space.LanceDB.Enabled {
			return fmt.Errorf("controller lancedb backend is not enabled for space %q", spaceID)
		}
		return nil
	}
	return fmt.Errorf("controller space %q is not attached", spaceID)
}

// Shutdown releases LanceDB, SQLite, the space attachment, and the client session in dependency order.
// Shutdown 按依赖顺序释放 LanceDB、SQLite、空间附着与客户端会话。
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	client := r.client
	spaceID := r.spaceID
	sqliteBindingID := r.sqliteBindingID
	lanceDBBindingID := r.lanceDBBindingID
	sqliteEnabled := r.sqliteEnabled
	lanceDBEnabled := r.lanceDBEnabled
	spaceAttached := r.spaceAttached
	r.mu.Unlock()

	if client == nil {
		return nil
	}
	var shutdownErrors []error
	if lanceDBEnabled {
		if _, err := client.DisableLanceDB(ctx, spaceID, lanceDBBindingID); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("disable lancedb binding: %w", err))
		}
	}
	if sqliteEnabled {
		if _, err := client.DisableSqlite(ctx, spaceID, sqliteBindingID); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("disable sqlite binding: %w", err))
		}
	}
	if spaceAttached {
		if _, err := client.DetachSpace(ctx, spaceID); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("detach controller space: %w", err))
		}
	}
	if err := client.Shutdown(ctx); err != nil {
		shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown controller client: %w", err))
	}
	return errors.Join(shutdownErrors...)
}

// normalizeConfig creates database directories and canonicalizes every path before it is registered with the controller.
// normalizeConfig 在向 controller 注册前创建数据库目录并规范化全部路径。
func normalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.SpaceRoot) == "" {
		return Config{}, errors.New("controller space root is required")
	}
	if strings.TrimSpace(cfg.SQLiteDatabase) == "" {
		return Config{}, errors.New("controller sqlite database path is required")
	}
	if strings.TrimSpace(cfg.LanceDBDirectory) == "" {
		return Config{}, errors.New("controller lancedb database directory is required")
	}
	if err := os.MkdirAll(cfg.SpaceRoot, 0o755); err != nil {
		return Config{}, fmt.Errorf("create controller space root: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SQLiteDatabase), 0o755); err != nil {
		return Config{}, fmt.Errorf("create controller sqlite directory: %w", err)
	}
	if err := os.MkdirAll(cfg.LanceDBDirectory, 0o755); err != nil {
		return Config{}, fmt.Errorf("create controller lancedb directory: %w", err)
	}
	spaceRoot, err := canonicalDirectory(cfg.SpaceRoot)
	if err != nil {
		return Config{}, fmt.Errorf("canonicalize controller space root: %w", err)
	}
	sqliteParent, err := canonicalDirectory(filepath.Dir(cfg.SQLiteDatabase))
	if err != nil {
		return Config{}, fmt.Errorf("canonicalize controller sqlite directory: %w", err)
	}
	lanceDBDirectory, err := canonicalDirectory(cfg.LanceDBDirectory)
	if err != nil {
		return Config{}, fmt.Errorf("canonicalize controller lancedb directory: %w", err)
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Executable = strings.TrimSpace(cfg.Executable)
	cfg.ProcessMode = strings.ToLower(strings.TrimSpace(cfg.ProcessMode))
	cfg.SpaceID = strings.TrimSpace(cfg.SpaceID)
	cfg.SpaceLabel = strings.TrimSpace(cfg.SpaceLabel)
	cfg.SpaceRoot = spaceRoot
	cfg.SQLiteDatabase = filepath.Join(sqliteParent, filepath.Base(filepath.Clean(cfg.SQLiteDatabase)))
	cfg.LanceDBDirectory = lanceDBDirectory
	return cfg, nil
}

// canonicalDirectory returns an absolute symlink-resolved directory path so equivalent spellings cannot create duplicate controller resources.
// canonicalDirectory 返回绝对且已解析符号链接的目录路径，避免等价路径拼写创建重复 controller 资源。
func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// newInstanceID returns a random process-scoped suffix that avoids stale lease binding collisions after a crash.
// newInstanceID 返回随机进程级后缀，避免崩溃后与旧租约绑定发生冲突。
func newInstanceID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate controller binding instance id: %w", err)
	}
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(raw[:])), nil
}
