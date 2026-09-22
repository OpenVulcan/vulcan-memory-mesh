// native_storage_owner.go coordinates pair ownership and native health checks; legacy migration shares its path locks.
// native_storage_owner.go 协调数据库组合所有权与原生健康检查，旧库迁移共用其路径锁。
package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/storageguard"
)

// nativeStorageOwner owns path locks until all database handles have been shut down by the app.
// nativeStorageOwner 持有路径锁，直到应用关闭全部数据库句柄后才释放。
type nativeStorageOwner struct {
	mu        sync.RWMutex
	locks     []*storageguard.Lock
	checks    []appports.HealthChecker
	resources []appports.Shutdowner
	closed    bool
}

// acquireNativeStorageOwner locks both resources in a stable order and unwinds partial acquisition.
// acquireNativeStorageOwner 按稳定顺序锁定两个资源，部分获取失败时释放已有锁。
func acquireNativeStorageOwner(layout nativeStorageLayout) (*nativeStorageOwner, error) {
	paths := []string{layout.SQLiteDatabase + ".vmm-writer.lock", filepath.Join(layout.LanceDBDirectory, ".vmm-writer.lock")}
	sort.Strings(paths)
	owner := &nativeStorageOwner{}
	for _, path := range paths {
		lock, err := storageguard.Acquire(path)
		if err != nil {
			_ = owner.Shutdown(context.Background())
			return nil, err
		}
		owner.locks = append(owner.locks, lock)
	}
	return owner, nil
}

// CheckHealth verifies each configured database while excluding concurrent ownership release.
// CheckHealth 检查每个已配置数据库，同时排除所有权释放竞态，返回首个健康错误。
func (o *nativeStorageOwner) CheckHealth(ctx context.Context) error {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return fmt.Errorf("native storage is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(o.checks) != 2 {
		return fmt.Errorf("native storage health checks are not initialized")
	}
	for _, check := range o.checks {
		if err := check.CheckHealth(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown releases ownership after database shutdown, even when the caller's deadline has expired.
// Shutdown 在数据库关闭后释放所有权，即使调用方超时也执行释放，返回收集到的系统错误。
func (o *nativeStorageOwner) Shutdown(_ context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	// Retain all locks until every resource confirms closure, even if an earlier app shutdown timed out.
	// 即使应用先前关闭超时，也须确认全部资源已关闭后才释放锁。
	for len(o.resources) > 0 {
		i := len(o.resources) - 1
		if err := o.resources[i].Shutdown(context.Background()); err != nil {
			// A failed child close must retain its parent runtime as well as the path locks for a later retry.
			// 子资源关闭失败时同时保留父运行时和路径锁，允许后续重试。
			return fmt.Errorf("database resources could not close; ownership remains locked: %w", err)
		}
		o.resources = o.resources[:i]
	}
	var closeErrors []error
	for i := len(o.locks) - 1; i >= 0; i-- {
		closeErrors = append(closeErrors, o.locks[i].Close())
	}
	o.locks = nil
	o.closed = true
	return errors.Join(closeErrors...)
}
