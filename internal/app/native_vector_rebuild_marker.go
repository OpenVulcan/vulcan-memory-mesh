// native_vector_rebuild_marker.go persists the fail-closed marker used while native vectors are being destructively rebuilt.
// native_vector_rebuild_marker.go 用于持久化原生向量破坏性重建期间的失败关闭标记。
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// nativeVectorRebuildIncompleteMarkerSuffix is paired with the authoritative SQLite path and is never shared with FTS or cleanup markers.
	// nativeVectorRebuildIncompleteMarkerSuffix 与权威 SQLite 路径配对，绝不与 FTS 或清理标记共用。
	nativeVectorRebuildIncompleteMarkerSuffix = ".vector-rebuild-incomplete"

	// nativeVectorRebuildMarkerVersion identifies the marker payload so future recovery checks can fail closed.
	// nativeVectorRebuildMarkerVersion 标识标记载荷版本，让未来恢复检查可以失败关闭。
	nativeVectorRebuildMarkerVersion = 1
)

// nativeVectorRebuildMarker records only non-sensitive recovery metadata for one interrupted native vector rebuild.
// nativeVectorRebuildMarker 只记录一次中断原生向量重建所需的无敏感恢复元数据。
type nativeVectorRebuildMarker struct {
	Version   int    `json:"version"`
	StartedAt string `json:"started_at"`
}

// nativeVectorRebuildIncompleteMarkerPath returns the exact marker path paired with one native SQLite database.
// nativeVectorRebuildIncompleteMarkerPath 返回与一份原生 SQLite 数据库配对的精确标记路径。
func nativeVectorRebuildIncompleteMarkerPath(sqlitePath string) string {
	return strings.TrimSpace(sqlitePath) + nativeVectorRebuildIncompleteMarkerSuffix
}

// nativeVectorRebuildMarkerExists reports whether the host-owned marker exists and rejects non-regular replacements.
// nativeVectorRebuildMarkerExists 报告宿主所有标记是否存在，并拒绝非普通文件替换。
func nativeVectorRebuildMarkerExists(sqlitePath string) (bool, error) {
	path := nativeVectorRebuildIncompleteMarkerPath(sqlitePath)
	if strings.TrimSpace(sqlitePath) == "" {
		return false, fmt.Errorf("native vector rebuild marker requires sqlite path")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat native vector rebuild marker %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("native vector rebuild marker %q is not a regular file", path)
	}
	return true, nil
}

// writeNativeVectorRebuildMarker creates the marker before any destructive native reset and leaves an existing marker intact for recovery retries.
// writeNativeVectorRebuildMarker 在原生破坏性重置前创建标记，并在恢复重试时保留已有标记。
func writeNativeVectorRebuildMarker(sqlitePath string) error {
	if strings.TrimSpace(sqlitePath) == "" {
		return fmt.Errorf("native vector rebuild marker requires sqlite path")
	}
	path := nativeVectorRebuildIncompleteMarkerPath(sqlitePath)
	exists, err := nativeVectorRebuildMarkerExists(sqlitePath)
	if err != nil {
		return err
	}
	if exists {
		if err := syncNativeFile(path); err != nil {
			return fmt.Errorf("sync existing native vector rebuild marker: %w", err)
		}
		if err := syncNativeParentDirectory(path); err != nil {
			return fmt.Errorf("sync existing native vector rebuild marker directory: %w", err)
		}
		return nil
	}

	marker := nativeVectorRebuildMarker{
		Version:   nativeVectorRebuildMarkerVersion,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode native vector rebuild marker: %w", err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create native vector rebuild marker directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".vector-rebuild-marker-*")
	if err != nil {
		return fmt.Errorf("create native vector rebuild marker temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure native vector rebuild marker temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write native vector rebuild marker temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync native vector rebuild marker temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close native vector rebuild marker temporary file: %w", err)
	}
	if err := renameNativeAtomicFile(temporaryPath, path); err != nil {
		// Another owner may have published the marker between the existence check and rename; retain that safety state.
		// 另一个所有者可能在检查与改名之间发布了标记；此时保留该安全状态即可。
		exists, statErr := nativeVectorRebuildMarkerExists(sqlitePath)
		if statErr == nil && exists {
			if syncErr := syncNativeFile(path); syncErr != nil {
				return fmt.Errorf("sync concurrently published native vector rebuild marker: %w", syncErr)
			}
			if syncErr := syncNativeParentDirectory(path); syncErr != nil {
				return fmt.Errorf("sync concurrently published native vector rebuild marker directory: %w", syncErr)
			}
			return nil
		}
		return fmt.Errorf("publish native vector rebuild marker: %w", err)
	}
	if err := syncNativeParentDirectory(path); err != nil {
		return fmt.Errorf("sync native vector rebuild marker directory: %w", err)
	}
	return nil
}

// removeNativeVectorRebuildMarker clears the marker only after rebuild verification, schema publication, and embedding identity publication all succeed.
// removeNativeVectorRebuildMarker 仅在重建核验、schema 发布和 embedding identity 发布全部成功后清除标记。
func removeNativeVectorRebuildMarker(sqlitePath string) error {
	if strings.TrimSpace(sqlitePath) == "" {
		return fmt.Errorf("native vector rebuild marker requires sqlite path")
	}
	path := nativeVectorRebuildIncompleteMarkerPath(sqlitePath)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove native vector rebuild marker %q: %w", path, err)
	}
	if err := syncNativeParentDirectory(path); err != nil {
		// Keep startup fail-closed when the directory entry cannot be durably acknowledged as removed.
		// 当目录项无法被可靠确认已删除时保留启动隔离，确保服务不会误把不确定状态当成完成。
		restoreErr := writeNativeVectorRebuildMarker(sqlitePath)
		if restoreErr != nil {
			return fmt.Errorf("sync native vector rebuild marker removal directory: %w; restore marker: %v", err, restoreErr)
		}
		return fmt.Errorf("sync native vector rebuild marker removal directory: %w; marker restored", err)
	}
	return nil
}
