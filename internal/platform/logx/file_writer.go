// file_writer.go implements the local runtime log file writer that mirrors stdout logs into day/hour-partitioned files.
// file_writer.go 用于实现本地运行时日志文件写入器，把 stdout 日志同步镜像到按天/小时分区的文件中。
package logx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HourlyFileWriter writes logs into `root/YYYYMMDD/YYYYMMDDHH.log` and rotates automatically when the local hour changes.
// HourlyFileWriter 用于把日志写入 `root/YYYYMMDD/YYYYMMDDHH.log`，并在本地小时变化时自动滚动。
type HourlyFileWriter struct {
	rootDir       string
	filePrefix    string
	now           func() time.Time
	mu            sync.Mutex
	currentBucket string
	currentPath   string
	file          *os.File
}

// NewHourlyFileWriter creates one hourly-partitioned runtime log writer and eagerly opens the current bucket so startup can fail fast when the target log path is invalid or not writable.
// NewHourlyFileWriter 用于创建一个按小时分区的运行时日志写入器，并在启动时立即打开当前桶；这样当目标日志路径无效或不可写时，应用可以尽早失败。
func NewHourlyFileWriter(rootDir string) (*HourlyFileWriter, error) {
	return newHourlyFileWriterWithPrefix(rootDir, "", time.Now)
}

// NewHourlyPrefixedFileWriter creates one hourly-partitioned runtime log writer that keeps the same day/hour directory layout while prepending one stable file-name prefix such as `LLM-`.
// NewHourlyPrefixedFileWriter 用于创建一个带稳定文件名前缀（例如 `LLM-`）的按小时分区日志写入器，同时保持与主日志相同的按天/小时目录结构。
func NewHourlyPrefixedFileWriter(rootDir string, prefix string) (*HourlyFileWriter, error) {
	return newHourlyFileWriterWithPrefix(rootDir, prefix, time.Now)
}

// newHourlyFileWriter builds one hourly-partitioned writer with an injectable clock so tests can verify directory layout and hour rollover deterministically.
// newHourlyFileWriter 用于创建一个支持注入时钟的按小时分区写入器，方便测试稳定验证目录布局和整点滚动行为。
func newHourlyFileWriter(rootDir string, now func() time.Time) (*HourlyFileWriter, error) {
	return newHourlyFileWriterWithPrefix(rootDir, "", now)
}

// newHourlyFileWriterWithPrefix builds one hourly-partitioned writer with an injectable clock and optional file-name prefix so tests can deterministically validate both normal and prefixed log streams.
// newHourlyFileWriterWithPrefix 用于创建一个支持注入时钟和可选文件名前缀的按小时分区写入器，方便测试稳定验证普通日志与带前缀日志流。
func newHourlyFileWriterWithPrefix(rootDir string, prefix string, now func() time.Time) (*HourlyFileWriter, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil, fmt.Errorf("log root dir is empty")
	}
	if now == nil {
		now = time.Now
	}
	writer := &HourlyFileWriter{
		rootDir:    filepath.Clean(rootDir),
		filePrefix: strings.TrimSpace(prefix),
		now:        now,
	}
	if err := writer.rotateLocked(now()); err != nil {
		return nil, err
	}
	return writer, nil
}

// Write appends one already-formatted log block into the current hourly file and rotates the file first when the local hour changed since the previous write.
// Write 用于把一条已经格式化完成的日志块追加到当前小时文件；如果本地小时自上次写入后已经变化，会先完成滚动。
func (w *HourlyFileWriter) Write(p []byte) (int, error) {
	if w == nil {
		return 0, fmt.Errorf("hourly file writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rotateLocked(w.now()); err != nil {
		return 0, err
	}
	if w.file == nil {
		return 0, fmt.Errorf("log file is not open")
	}
	return w.file.Write(p)
}

// Shutdown closes the active file handle during application shutdown so the final buffered bytes are flushed before the process exits.
// Shutdown 用于在应用关闭时关闭当前文件句柄，确保进程退出前把最后缓冲的字节刷新到磁盘。
func (w *HourlyFileWriter) Shutdown(_ context.Context) error {
	return w.Close()
}

// Close closes the currently opened hourly file, if any.
// Close 用于关闭当前已打开的小时日志文件（如果存在）。
func (w *HourlyFileWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeLocked()
}

// rotateLocked ensures the writer is bound to the current local-hour file before a write happens, creating the day directory on demand.
// rotateLocked 用于在写入发生前确保写入器绑定到当前本地小时文件，并按需创建当天目录。
func (w *HourlyFileWriter) rotateLocked(now time.Time) error {
	now = now.Local()
	bucket := now.Format("2006010215")
	if w.file != nil && w.currentBucket == bucket {
		return nil
	}

	dirPath := filepath.Join(w.rootDir, now.Format("20060102"))
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	filePath := filepath.Join(dirPath, w.filePrefix+bucket+".log")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	if err := w.closeLocked(); err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	w.currentBucket = bucket
	w.currentPath = filePath
	return nil
}

// closeLocked releases the currently opened file handle while the caller already holds the mutex.
// closeLocked 用于在调用方已经持有互斥锁时释放当前打开的文件句柄。
func (w *HourlyFileWriter) closeLocked() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.currentBucket = ""
	return err
}
