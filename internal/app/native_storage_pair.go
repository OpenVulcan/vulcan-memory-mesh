// native_storage_pair.go binds the native SQLite and LanceDB locations to one persisted identity.
// native_storage_pair.go 用一个持久化身份把原生 SQLite 与 LanceDB 路径绑定为同一存储组合。
package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// nativeStoragePairFormatVersion identifies the on-disk pair-marker contract.
	// nativeStoragePairFormatVersion 标识磁盘上的存储组合 marker 契约版本。
	nativeStoragePairFormatVersion = 1

	// nativeStoragePairIDBytes fixes the cryptographic identity size used by newly created pairs.
	// nativeStoragePairIDBytes 固定新建存储组合使用的密码学身份长度。
	nativeStoragePairIDBytes = 32

	// nativeStoragePairMarkerMaxBytes bounds marker parsing so a damaged or hostile file cannot consume unbounded memory.
	// nativeStoragePairMarkerMaxBytes 限制 marker 解析大小，避免损坏或恶意文件导致无界内存消耗。
	nativeStoragePairMarkerMaxBytes = 4096

	// nativeSQLitePairMarkerSuffix is the marker suffix next to the native SQLite database file.
	// nativeSQLitePairMarkerSuffix 是原生 SQLite 数据库旁边的 marker 后缀。
	nativeSQLitePairMarkerSuffix = ".pair.json"

	// nativeLancePairMarkerName is the marker name inside the native LanceDB directory.
	// nativeLancePairMarkerName 是原生 LanceDB 目录中的 marker 文件名。
	nativeLancePairMarkerName = ".vmm-pair.json"

	// nativeWriterLockName is the only pre-existing LanceDB entry allowed while initializing a new pair.
	// nativeWriterLockName 是初始化新组合时允许预先存在的唯一 LanceDB 目录项。
	nativeWriterLockName = ".vmm-writer.lock"
)

// nativeStoragePairMarker is the complete, path-independent identity persisted beside both native stores.
// nativeStoragePairMarker 是同时写入两端、与路径无关的完整原生存储组合身份。
type nativeStoragePairMarker struct {
	FormatVersion int    `json:"format_version"`
	PairID        string `json:"pair_id"`
}

// ensureNativeStoragePair verifies or initializes the identity shared by the native SQLite and LanceDB stores.
// ensureNativeStoragePair 校验或初始化原生 SQLite 与 LanceDB 共享的存储组合身份。
//
// The caller must already hold both native writer locks and must call this before opening either backend.
// 调用方必须已经持有两端原生写锁，并且必须在打开任一后端之前调用本函数。
func ensureNativeStoragePair(layout nativeStorageLayout) error {
	if strings.TrimSpace(layout.SQLiteDatabase) == "" || strings.TrimSpace(layout.LanceDBDirectory) == "" {
		return errors.New("native storage pair paths are required")
	}
	sqliteMarkerPath := layout.SQLiteDatabase + nativeSQLitePairMarkerSuffix
	lanceMarkerPath := filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName)

	sqliteMarker, sqlitePresent, err := readNativeStoragePairMarker(sqliteMarkerPath, "SQLite")
	if err != nil {
		return err
	}
	lanceMarker, lancePresent, err := readNativeStoragePairMarker(lanceMarkerPath, "LanceDB")
	if err != nil {
		return err
	}
	if sqlitePresent != lancePresent {
		return fmt.Errorf("native storage pair markers are incomplete: sqlite=%t lancedb=%t", sqlitePresent, lancePresent)
	}
	if sqlitePresent {
		if sqliteMarker.FormatVersion != lanceMarker.FormatVersion {
			return fmt.Errorf("native storage pair marker format versions differ: sqlite=%d lancedb=%d", sqliteMarker.FormatVersion, lanceMarker.FormatVersion)
		}
		if sqliteMarker.PairID != lanceMarker.PairID {
			return errors.New("native storage pair marker identities differ")
		}
		return nil
	}

	if err := requireNativeSQLitePathAbsent(layout.SQLiteDatabase); err != nil {
		return err
	}
	if err := requireNewNativeLanceDirectory(layout.LanceDBDirectory); err != nil {
		return err
	}
	pairMarker, err := newNativeStoragePairMarker()
	if err != nil {
		return err
	}
	// Deliberately create the two files independently. If power or I/O fails between them, the next call sees one marker and refuses takeover rather than guessing a pair.
	// 有意独立创建两个文件；若断电或 I/O 在两次写入之间失败，下次调用会看到单 marker 并拒绝接管，而不是猜测组合关系。
	if err := writeNativeStoragePairMarker(sqliteMarkerPath, pairMarker, "SQLite"); err != nil {
		return fmt.Errorf("initialize native storage pair: %w; remove the unused fresh pair before retrying", err)
	}
	if err := writeNativeStoragePairMarker(lanceMarkerPath, pairMarker, "LanceDB"); err != nil {
		return fmt.Errorf("write LanceDB native storage pair marker after SQLite marker was committed: %w; remove the unused fresh pair before retrying", err)
	}
	return nil
}

// newNativeStoragePairMarker creates one unpredictable path-independent pair identity.
// newNativeStoragePairMarker 创建不可预测且与路径无关的存储组合身份。
func newNativeStoragePairMarker() (nativeStoragePairMarker, error) {
	identity := make([]byte, nativeStoragePairIDBytes)
	if _, err := io.ReadFull(rand.Reader, identity); err != nil {
		return nativeStoragePairMarker{}, fmt.Errorf("generate native storage pair identity: %w", err)
	}
	return nativeStoragePairMarker{
		FormatVersion: nativeStoragePairFormatVersion,
		PairID:        hex.EncodeToString(identity),
	}, nil
}

// readNativeStoragePairMarker reads one marker with an exact field set and bounded input.
// readNativeStoragePairMarker 以精确字段集合和有界输入读取一个 marker。
func readNativeStoragePairMarker(path, backend string) (nativeStoragePairMarker, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nativeStoragePairMarker{}, false, nil
	}
	if err != nil {
		return nativeStoragePairMarker{}, false, fmt.Errorf("inspect %s native storage pair marker %q: %w", backend, path, err)
	}
	if !info.Mode().IsRegular() {
		return nativeStoragePairMarker{}, false, fmt.Errorf("%s native storage pair marker %q is not a regular file", backend, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nativeStoragePairMarker{}, false, fmt.Errorf("%s native storage pair marker %q must be private (0600 or stricter)", backend, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nativeStoragePairMarker{}, false, fmt.Errorf("open %s native storage pair marker %q: %w", backend, path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, nativeStoragePairMarkerMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nativeStoragePairMarker{}, false, fmt.Errorf("read %s native storage pair marker %q: %w", backend, path, readErr)
	}
	if closeErr != nil {
		return nativeStoragePairMarker{}, false, fmt.Errorf("close %s native storage pair marker %q: %w", backend, path, closeErr)
	}
	if len(data) == 0 {
		return nativeStoragePairMarker{}, false, fmt.Errorf("%s native storage pair marker %q is empty", backend, path)
	}
	if len(data) > nativeStoragePairMarkerMaxBytes {
		return nativeStoragePairMarker{}, false, fmt.Errorf("%s native storage pair marker %q is too large", backend, path)
	}
	marker, err := decodeNativeStoragePairMarker(data)
	if err != nil {
		return nativeStoragePairMarker{}, false, fmt.Errorf("parse %s native storage pair marker %q: %w", backend, path, err)
	}
	return marker, true, nil
}

// decodeNativeStoragePairMarker rejects unknown fields, duplicate fields, trailing values, and empty values.
// decodeNativeStoragePairMarker 拒绝未知字段、重复字段、尾随值以及空值。
func decodeNativeStoragePairMarker(data []byte) (nativeStoragePairMarker, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nativeStoragePairMarker{}, fmt.Errorf("decode object start: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nativeStoragePairMarker{}, errors.New("marker must be a JSON object")
	}

	var marker nativeStoragePairMarker
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nativeStoragePairMarker{}, fmt.Errorf("decode field name: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nativeStoragePairMarker{}, errors.New("marker field name is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nativeStoragePairMarker{}, fmt.Errorf("marker field %q is duplicated", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "format_version":
			if err := decoder.Decode(&marker.FormatVersion); err != nil {
				return nativeStoragePairMarker{}, fmt.Errorf("decode format_version: %w", err)
			}
		case "pair_id":
			if err := decoder.Decode(&marker.PairID); err != nil {
				return nativeStoragePairMarker{}, fmt.Errorf("decode pair_id: %w", err)
			}
		default:
			return nativeStoragePairMarker{}, fmt.Errorf("unknown marker field %q", key)
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return nativeStoragePairMarker{}, fmt.Errorf("decode object end: %w", err)
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nativeStoragePairMarker{}, errors.New("marker object is not closed")
	}
	if decoder.More() {
		return nativeStoragePairMarker{}, errors.New("marker contains trailing data")
	}
	if trailing := decoder.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
		if trailing == nil {
			return nativeStoragePairMarker{}, errors.New("marker contains trailing JSON value")
		}
		return nativeStoragePairMarker{}, fmt.Errorf("marker contains trailing data: %w", trailing)
	}
	if len(seen) != 2 {
		return nativeStoragePairMarker{}, errors.New("marker must contain exactly format_version and pair_id")
	}
	if marker.FormatVersion != nativeStoragePairFormatVersion {
		return nativeStoragePairMarker{}, fmt.Errorf("unsupported marker format_version %d", marker.FormatVersion)
	}
	if strings.TrimSpace(marker.PairID) == "" {
		return nativeStoragePairMarker{}, errors.New("marker pair_id is empty")
	}
	return marker, nil
}

// requireNativeSQLitePathAbsent rejects an existing SQLite path before first pair initialization.
// requireNativeSQLitePathAbsent 在首次初始化存储组合前拒绝已经存在的 SQLite 路径。
func requireNativeSQLitePathAbsent(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("native SQLite database %q exists without a pair marker", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect native SQLite database %q: %w", path, err)
	}
	return nil
}

// requireNewNativeLanceDirectory accepts only a missing or empty LanceDB directory with its exact owner lock.
// requireNewNativeLanceDirectory 仅接受不存在或只含精确所有权锁的全新 LanceDB 目录。
func requireNewNativeLanceDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect native LanceDB directory %q: %w", path, err)
	}
	for _, entry := range entries {
		if entry.Name() != nativeWriterLockName {
			return fmt.Errorf("native LanceDB directory %q is not new; unexpected entry %q", path, entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("native LanceDB directory %q has a symlink owner lock", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect native LanceDB owner lock in %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("native LanceDB owner lock in %q is not a regular file", path)
		}
	}
	return nil
}

// writeNativeStoragePairMarker creates one marker without replacing an existing path.
// writeNativeStoragePairMarker 创建单个 marker，绝不替换已经存在的路径。
func writeNativeStoragePairMarker(path string, marker nativeStoragePairMarker, backend string) error {
	encoded, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode %s native storage pair marker: %w", backend, err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s native storage pair marker directory: %w", backend, err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s native storage pair marker %q: %w", backend, path, err)
	}
	writeErr := error(nil)
	if err := file.Chmod(0o600); err != nil {
		writeErr = fmt.Errorf("set %s native storage pair marker permission: %w", backend, err)
	} else if written, err := file.Write(encoded); err != nil {
		writeErr = fmt.Errorf("write %s native storage pair marker: %w", backend, err)
	} else if written != len(encoded) {
		writeErr = fmt.Errorf("write %s native storage pair marker: short write", backend)
	} else if err := file.Sync(); err != nil {
		writeErr = fmt.Errorf("sync %s native storage pair marker: %w", backend, err)
	}
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("close %s native storage pair marker: %w", backend, err)
	}
	if writeErr != nil {
		return writeErr
	}
	return syncNativeParentDirectory(path)
}
