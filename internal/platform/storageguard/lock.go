// Package storageguard owns operating-system locks shared by native runtime and maintenance writers.
// storageguard 包为原生运行时与维护写入者提供共同的操作系统锁。
package storageguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Lock holds an open lock file; keeping its inode avoids unlink/recreate races between writers.
// Lock 持有打开的锁文件；保留文件节点可避免写入者之间删除重建锁文件的竞态。
type Lock struct {
	mu   sync.Mutex
	file *os.File
}

// CanonicalPath resolves an absolute path through its existing ancestors, including symlinks.
// CanonicalPath 通过现存父目录解析包含符号链接的绝对路径，返回规范路径或解析错误。
func CanonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("storage path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	ancestor := filepath.Clean(absolute)
	var suffix []string
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil {
				return "", fmt.Errorf("resolve storage symlink %q: %w", ancestor, resolveErr)
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect storage path %q: %w", ancestor, err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("storage path has no existing root: %q", absolute)
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}

// Acquire opens and immediately locks path, returning an owned lock or an explicit contention error.
// Acquire 打开并立即锁定指定路径，返回拥有的锁或明确的竞争错误，不等待其他写入者退出。
func Acquire(path string) (*Lock, error) {
	canonical, err := CanonicalPath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		return nil, fmt.Errorf("create storage lock directory: %w", err)
	}
	file, err := os.OpenFile(canonical, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage lock %q: %w", canonical, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("storage is already in use or cannot be locked (%s): %w", canonical, err)
	}
	return &Lock{file: file}, nil
}

// Close releases the OS lock and its descriptor once; the lock file intentionally remains on disk.
// Close 仅释放一次操作系统锁与文件句柄，并有意保留磁盘锁文件以维持同一个锁定对象。
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
