// storage_snapshot.go creates isolated, consistency-preserving SQLite snapshots for offline native migration.
// storage_snapshot.go 为离线原生迁移创建隔离且保持一致性的 SQLite 快照。
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	controllerclient "github.com/OpenVulcan/vldb-controller/client-go/controller"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_controller"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

const (
	// defaultLegacySnapshotTimeout bounds an offline snapshot when neither caller nor config supplies a useful deadline.
	// defaultLegacySnapshotTimeout 用于在调用方和配置都没有有效期限时限制离线快照的最长执行时间。
	defaultLegacySnapshotTimeout = 5 * time.Minute
	// snapshotShutdownTimeout bounds cleanup of a controller session after snapshot execution.
	// snapshotShutdownTimeout 用于限制快照执行后 controller 会话清理的最长时间。
	snapshotShutdownTimeout = 10 * time.Second
)

// legacySQLiteSnapshotPaths contains the canonical source and destination paths validated before any backend is opened.
// legacySQLiteSnapshotPaths 保存打开后端前已经校验过的源库与目标快照规范路径。
type legacySQLiteSnapshotPaths struct {
	source      string
	destination string
}

// legacySnapshotDatabase is the minimal source handle contract needed by the snapshot session.
// legacySnapshotDatabase 是快照会话所需的最小源数据库句柄契约。
type legacySnapshotDatabase interface {
	ExecuteScript(context.Context, string, []sqliteffi.SQLValue, string) (sqliteffi.ExecuteResult, error)
	Close() error
}

// legacySnapshotRuntime is the minimal runtime cleanup contract retained after the source is opened.
// legacySnapshotRuntime 是打开源库后保留的最小运行时清理契约。
type legacySnapshotRuntime interface {
	Close() error
}

// legacySnapshotLibrary is the minimal dynamic-library cleanup contract retained after runtime creation.
// legacySnapshotLibrary 是创建运行时后保留的最小动态库清理契约。
type legacySnapshotLibrary interface {
	Close() error
}

// ExportLegacySQLiteSnapshot creates one new SQLite file with SQLite's single-call VACUUM INTO operation.
// ExportLegacySQLiteSnapshot 使用 SQLite 单次 VACUUM INTO 操作为旧 SQLite 创建一个新的快照文件。
//
// The source remains owned by its configured backend: split mode opens the legacy FFI directly, while controller mode
// uses a controller maintenance session with exclusive-space checking and sends exactly one script request.
// 源库始终由其配置的后端负责：split 模式直接打开旧 FFI，controller 模式通过带空间独占检查的维护会话，并且只发送一次脚本请求。
// Parameters: ctx supplies cancellation, cfg selects the legacy storage mode, promptLayout resolves the real source, and destination names a new file.
// 参数：ctx 提供取消信号，cfg 选择旧存储模式，promptLayout 解析真实源库，destination 指定新文件。
// Returns an error when validation, the single backend operation, or bounded cleanup fails; uncertain output is retained.
// 返回：校验、单次后端操作或有界清理失败时返回错误；结果不确定时保留目标产物。
func ExportLegacySQLiteSnapshot(ctx context.Context, cfg config.Config, promptLayout config.PromptLayout, destination string) (err error) {
	session, err := AcquireLegacySQLiteSnapshot(ctx, cfg, promptLayout)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, session.Shutdown(context.Background()))
	}()
	if err := session.Export(ctx, destination); err != nil {
		return fmt.Errorf("export legacy SQLite snapshot: %w", err)
	}
	return nil
}

// LegacySQLiteSnapshotSession keeps the source backend open while an offline migration copies the resulting snapshot.
// LegacySQLiteSnapshotSession 在离线迁移复制快照期间持续持有源后端。
type LegacySQLiteSnapshotSession struct {
	mu                sync.Mutex
	cfg               config.Config
	mode              string
	source            string
	library           legacySnapshotLibrary
	runtimeHandle     legacySnapshotRuntime
	database          legacySnapshotDatabase
	controllerRuntime *vldb_controller.Runtime
	closed            bool
}

// AcquireLegacySQLiteSnapshot opens the configured legacy source and holds its backend ownership until Shutdown.
// AcquireLegacySQLiteSnapshot 打开配置指定的旧源库，并持续持有后端所有权直到 Shutdown。
// Parameters: ctx bounds startup, cfg supplies the configured backend, and promptLayout resolves its source layout.
// 参数：ctx 限制启动时间，cfg 提供后端配置，promptLayout 解析源库布局。
// Returns a session that keeps the source owner until Shutdown, or an error before any migration write.
// 返回：持有源所有者直到 Shutdown 的会话；若失败则在迁移写入前返回错误。
func AcquireLegacySQLiteSnapshot(ctx context.Context, cfg config.Config, promptLayout config.PromptLayout) (*LegacySQLiteSnapshotSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := cfg.StorageMode()
	if mode != "split" && mode != "controller" {
		return nil, fmt.Errorf("legacy SQLite snapshot requires storage.mode=split or controller, got %q", mode)
	}
	layout, err := resolveLocalStorageLayoutForConfig(cfg, promptLayout)
	if err != nil {
		return nil, fmt.Errorf("resolve legacy SQLite snapshot layout: %w", err)
	}
	sourcePaths, err := validateLegacySQLiteSourcePath(layout)
	if err != nil {
		return nil, err
	}
	session := &LegacySQLiteSnapshotSession{cfg: cfg, mode: mode, source: sourcePaths.source}
	if err := checkSnapshotContext(ctx); err != nil {
		return nil, err
	}
	switch mode {
	case "split":
		library, err := sqliteffi.Open(layout.SQLiteLibrary)
		if err != nil {
			return nil, fmt.Errorf("open legacy SQLite library: %w", err)
		}
		session.library = library
		runtimeHandle, err := library.CreateRuntime()
		if err != nil {
			_ = session.Shutdown(context.Background())
			return nil, fmt.Errorf("create legacy SQLite runtime: %w", err)
		}
		session.runtimeHandle = runtimeHandle
		database, err := runtimeHandle.OpenDatabase(sourcePaths.source)
		if err != nil {
			_ = session.Shutdown(context.Background())
			return nil, fmt.Errorf("open legacy SQLite source: %w", err)
		}
		session.database = database
	case "controller":
		startupCtx, cancelStartup := controllerSnapshotStartupContext(ctx, cfg)
		controllerRuntime, err := newLegacySnapshotController(startupCtx, cfg, layout)
		cancelStartup()
		if err != nil {
			return nil, fmt.Errorf("open controller maintenance session: %w", err)
		}
		session.controllerRuntime = controllerRuntime
	}
	if err := checkSnapshotContext(ctx); err != nil {
		_ = session.Shutdown(context.Background())
		return nil, err
	}
	return session, nil
}

// Export executes one typed VACUUM INTO request while retaining the source backend owner for later migration steps.
// Export 在保留源后端所有者的同时执行一次带类型参数的 VACUUM INTO 请求。
// Parameters: ctx bounds the one VACUUM INTO request and destination must be a new file in a private directory.
// 参数：ctx 限制一次 VACUUM INTO 请求，destination 必须是私有目录中的新文件。
// Returns an error without deleting a possibly created destination, so uncertain writes remain diagnosable.
// 返回：失败时不删除可能已创建的目标，保留不确定写入以便诊断。
func (session *LegacySQLiteSnapshotSession) Export(ctx context.Context, destination string) (err error) {
	if session == nil {
		return errors.New("legacy SQLite snapshot session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return errors.New("legacy SQLite snapshot session is closed")
	}
	mode := session.mode
	source := session.source
	database := session.database
	controllerRuntime := session.controllerRuntime
	paths, err := validateLegacySQLiteSnapshotDestination(source, destination)
	if err != nil {
		return err
	}
	operationCtx, cancel := legacySnapshotContext(ctx, session.cfg)
	defer cancel()
	if err := checkSnapshotContext(operationCtx); err != nil {
		return err
	}
	switch mode {
	case "split":
		if database == nil {
			return errors.New("legacy SQLite database handle is nil")
		}
		result, executeErr := database.ExecuteScript(operationCtx, "VACUUM INTO ?", []sqliteffi.SQLValue{{
			Kind:   sqliteffi.SQLValueString,
			String: paths.destination,
		}}, "")
		if executeErr != nil {
			return fmt.Errorf("execute legacy SQLite VACUUM INTO: %w", executeErr)
		}
		if !result.Success {
			return fmt.Errorf("legacy SQLite VACUUM INTO failed: %s", strings.TrimSpace(result.Message))
		}
	case "controller":
		if controllerRuntime == nil {
			return errors.New("controller maintenance runtime is nil")
		}
		client := controllerRuntime.Client()
		if client == nil {
			return errors.New("controller maintenance client is nil")
		}
		response, executeErr := client.ExecuteSqliteScript(operationCtx, &controllerclient.SqliteExecuteScriptRequest{
			SpaceID:   controllerRuntime.SpaceID(),
			BindingID: controllerRuntime.SQLiteBindingID(),
			SQL:       "VACUUM INTO ?",
			Params: []controllerclient.SqliteValue{{
				Kind:        controllerclient.SqliteValueString,
				StringValue: paths.destination,
			}},
		})
		if executeErr != nil {
			return fmt.Errorf("execute controller SQLite VACUUM INTO: %w", executeErr)
		}
		if response == nil {
			return errors.New("controller SQLite VACUUM INTO returned no response")
		}
		if !response.Success {
			return fmt.Errorf("controller SQLite VACUUM INTO failed: %s", strings.TrimSpace(response.Message))
		}
	default:
		return fmt.Errorf("unsupported legacy SQLite snapshot mode %q", mode)
	}
	if err := checkSnapshotContext(operationCtx); err != nil {
		return err
	}
	if err := verifyCreatedSnapshot(paths.destination); err != nil {
		return fmt.Errorf("verify legacy SQLite snapshot: %w", err)
	}
	return nil
}

// Shutdown releases the held source backend in reverse ownership order and is safe to call repeatedly.
// Shutdown 按所有权逆序释放持有的源后端，并且可以重复调用。
// Parameters: the context argument is accepted for the shutdowner contract; cleanup uses an independent bounded background context.
// 参数：context 参数用于满足 shutdowner 契约；清理使用独立且有界的后台上下文。
// Returns any cleanup error while retaining the remaining ownership for retry; closed is set only after all resources succeed.
// 返回：清理失败时保留剩余所有权以便重试；全部资源成功后才设置 closed。
func (session *LegacySQLiteSnapshotSession) Shutdown(_ context.Context) error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return nil
	}
	database := session.database
	runtimeHandle := session.runtimeHandle
	library := session.library
	controllerRuntime := session.controllerRuntime
	if controllerRuntime != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), snapshotShutdownTimeout)
		controllerErr := controllerRuntime.Shutdown(shutdownCtx)
		cancel()
		if controllerErr != nil {
			return controllerErr
		}
		session.controllerRuntime = nil
	}
	if database != nil {
		if closeErr := database.Close(); closeErr != nil {
			return closeErr
		}
		session.database = nil
	}
	if runtimeHandle != nil {
		if closeErr := runtimeHandle.Close(); closeErr != nil {
			return closeErr
		}
		session.runtimeHandle = nil
	}
	if library != nil {
		if closeErr := library.Close(); closeErr != nil {
			return closeErr
		}
		session.library = nil
	}
	session.closed = true
	return nil
}

// newLegacySnapshotController reuses the runtime's authoritative controller config mapping while forcing maintenance exclusivity.
// newLegacySnapshotController 复用运行时权威的 controller 配置映射，同时强制启用维护独占。
func newLegacySnapshotController(ctx context.Context, cfg config.Config, layout localStorageLayout) (*vldb_controller.Runtime, error) {
	return vldb_controller.New(ctx, controllerRuntimeConfigForLayout(cfg, layout, true))
}

// legacySnapshotContext creates one bounded operation context using the fixed migration limit and the configured SQLite timeout.
// legacySnapshotContext 使用固定迁移时限与配置的 SQLite 超时创建有界操作上下文。
func legacySnapshotContext(parent context.Context, cfg config.Config) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := defaultLegacySnapshotTimeout
	if cfg.SQLite.Timeout.Duration > timeout {
		timeout = cfg.SQLite.Timeout.Duration
	}
	return context.WithTimeout(parent, timeout)
}

// controllerSnapshotStartupContext gives controller connection and startup enough time without removing the caller's outer deadline.
// controllerSnapshotStartupContext 在不移除调用方外层期限的前提下，为 controller 连接与启动提供足够时间。
func controllerSnapshotStartupContext(parent context.Context, cfg config.Config) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := cfg.Controller.StartupTimeout.Duration + cfg.Controller.ConnectTimeout.Duration + 10*time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

// checkSnapshotContext makes cancellation explicit before and after the synchronous legacy ABI operation.
// checkSnapshotContext 在同步旧 ABI 操作前后显式检查取消状态。
func checkSnapshotContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// validateLegacySQLiteSourcePath checks source identity before any backend is opened and keeps the destination policy separate.
// validateLegacySQLiteSourcePath 在打开任何后端前校验源数据库身份，并将目标策略独立处理。
func validateLegacySQLiteSourcePath(layout localStorageLayout) (legacySQLiteSnapshotPaths, error) {
	rawSource := strings.TrimSpace(layout.SQLiteDatabase)
	if rawSource == "" {
		return legacySQLiteSnapshotPaths{}, errors.New("legacy SQLite source path is empty")
	}
	source, err := filepath.Abs(filepath.Clean(rawSource))
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("resolve legacy SQLite source path: %w", err)
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("stat legacy SQLite source %q: %w", source, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite source %q is not a regular file", source)
	}

	return legacySQLiteSnapshotPaths{source: source}, nil
}

// validateLegacySQLiteSnapshotDestination validates a new private destination without changing the source.
// validateLegacySQLiteSnapshotDestination 在校验新的私有目标路径时不修改源数据库。
func validateLegacySQLiteSnapshotDestination(source, destination string) (legacySQLiteSnapshotPaths, error) {
	rawDestination := strings.TrimSpace(destination)
	if rawDestination == "" {
		return legacySQLiteSnapshotPaths{}, errors.New("legacy SQLite snapshot destination is empty")
	}
	destination, err := filepath.Abs(filepath.Clean(rawDestination))
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("resolve legacy SQLite snapshot destination: %w", err)
	}
	if destination == "." || filepath.Base(destination) == "." || filepath.Base(destination) == string(filepath.Separator) {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite snapshot destination %q is not a file path", rawDestination)
	}
	parent := filepath.Dir(destination)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("stat legacy SQLite snapshot destination root %q: %w", parent, err)
	}
	if !parentInfo.IsDir() {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite snapshot destination root %q is not a directory", parent)
	}
	parentEntry, err := os.Lstat(parent)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("inspect legacy SQLite snapshot destination root %q: %w", parent, err)
	}
	if parentEntry.Mode()&os.ModeSymlink != 0 {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite snapshot destination root %q must not be a symlink", parent)
	}
	// Unix mode bits are an enforceable private-root check; Windows inherits the creator ACL and does not expose it through FileMode.Perm.
	// Unix 使用可检查的权限位验证私有根目录；Windows 继承创建者 ACL，FileMode.Perm 不提供等价信息。
	if runtime.GOOS != "windows" && parentInfo.Mode().Perm()&0o077 != 0 {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite snapshot destination root %q must be private mode 0700", parent)
	}
	if _, err := os.Lstat(destination); err == nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("legacy SQLite snapshot destination %q already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("check legacy SQLite snapshot destination %q: %w", destination, err)
	}

	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("resolve legacy SQLite source identity: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, fmt.Errorf("resolve legacy SQLite snapshot destination root identity: %w", err)
	}
	canonicalDestination := filepath.Join(canonicalParent, filepath.Base(destination))
	if samePath(canonicalSource, canonicalDestination) {
		return legacySQLiteSnapshotPaths{}, errors.New("legacy SQLite snapshot destination aliases the source database")
	}
	canonicalSourceParent := filepath.Dir(canonicalSource)
	if samePath(canonicalSourceParent, canonicalParent) || isWithinDirectory(canonicalSourceParent, canonicalParent) || isWithinDirectory(canonicalParent, canonicalSourceParent) {
		return legacySQLiteSnapshotPaths{}, errors.New("legacy SQLite snapshot destination root overlaps the source database directory")
	}
	return legacySQLiteSnapshotPaths{source: source, destination: destination}, nil
}

// validateLegacySQLiteSnapshotPaths validates both source and destination for callers that do not hold a source owner.
// validateLegacySQLiteSnapshotPaths 为未持有源所有者的调用方同时校验源路径与目标路径。
func validateLegacySQLiteSnapshotPaths(layout localStorageLayout, destination string) (legacySQLiteSnapshotPaths, error) {
	sourcePaths, err := validateLegacySQLiteSourcePath(layout)
	if err != nil {
		return legacySQLiteSnapshotPaths{}, err
	}
	return validateLegacySQLiteSnapshotDestination(sourcePaths.source, destination)
}

// verifyCreatedSnapshot confirms VACUUM INTO produced a new regular non-empty file before reporting success.
// verifyCreatedSnapshot 在报告成功前确认 VACUUM INTO 已创建新的非空普通文件。
func verifyCreatedSnapshot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot path %q is not a regular file", path)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("snapshot path %q is empty", path)
	}
	return nil
}

// isWithinDirectory reports whether path is lexically contained by an existing directory after absolute normalization.
// isWithinDirectory 判断路径经过绝对化后是否处于指定目录之内。
func isWithinDirectory(path, root string) bool {
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(pathAbs); resolveErr == nil {
		pathAbs = filepath.Clean(resolved)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(rootAbs); resolveErr == nil {
		rootAbs = filepath.Clean(resolved)
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == ".." {
		return false
	}
	return relative != "" && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// samePath compares canonical paths using the host's case rules so Windows aliases cannot bypass source protection.
// samePath 按当前主机大小写规则比较规范路径，防止 Windows 路径别名绕过源库保护。
func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}
