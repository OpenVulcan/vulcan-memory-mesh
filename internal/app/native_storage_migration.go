// native_storage_migration.go coordinates offline copies into a new native database pair.
// native_storage_migration.go 在应用层编排离线迁移，将完整事实数据复制到全新的原生数据库组合。
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/storageguard"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
	"gopkg.in/yaml.v3"
)

// nativeMigrationReport records verified copy results without persisting provider credentials.
// nativeMigrationReport 记录已验证的复制结果，不保存提供商凭据。
type nativeMigrationReport struct {
	SourceMode       string `json:"source_mode"`
	SourceModel      string `json:"configured_source_model"`
	Dimension        int    `json:"dimension"`
	SQLite           any    `json:"sqlite"`
	Vectors          int64  `json:"vectors"`
	RestorableTrash  int64  `json:"restorable_trash_rows"`
	DuplicateVectors int64  `json:"duplicate_vector_rows"`
	SkippedInactive  int64  `json:"inactive_rows_without_vectors"`
	CompletedAt      string `json:"completed_at"`
}

// MigrateLegacyStorageToNative copies one stopped legacy installation without changing its configuration or data.
// MigrateLegacyStorageToNative 复制已停止的旧安装数据，不修改其配置或业务数据；失败保留带隔离标记的目标。
func MigrateLegacyStorageToNative(ctx context.Context, cfg config.Config, promptLayout config.PromptLayout, output string) (result error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.StorageMode() != "split" && !cfg.UsesController() {
		return fmt.Errorf("native migration requires split or controller source mode")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("native output directory is required")
	}
	output, err := storageguard.CanonicalPath(output)
	if err != nil {
		return err
	}
	sourceLayout, err := ResolveLocalStorageLayoutForConfig(cfg, promptLayout)
	if err != nil {
		return err
	}
	sourceRoot, err := storageguard.CanonicalPath(sourceLayout.DatabaseDir)
	if err != nil {
		return err
	}
	if nativePathContains(output, sourceRoot) || nativePathContains(sourceRoot, output) {
		return fmt.Errorf("native migration output must not overlap the legacy database directory")
	}
	// Serialize competing migrations before checking the destination, then retain only newly created artifacts.
	// 在检查目标前串行化并发迁移，后续仅写入新建产物。
	migrationLock, err := storageguard.Acquire(output + ".migration.lock")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, migrationLock.Close()) }()
	entries, err := os.ReadDir(output)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("native migration output must be empty: %s", output)
	}
	// Keep the same source path locks used by both legacy runtime modes for the entire migration window.
	// 在整个迁移窗口持有两个旧运行模式共同遵守的源路径锁。
	sourceOwner, err := acquireLegacyStorageOwner(sourceLayout)
	if err != nil {
		return fmt.Errorf("lock legacy migration source: %w", err)
	}
	defer func() { result = errors.Join(result, sourceOwner.Shutdown(context.Background())) }()
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	nativeCfg := cfg
	nativeCfg.Storage.Mode = "native"
	nativeCfg.SQLite.Native.Path = filepath.Join(output, "sqlite.db")
	nativeCfg.LanceDB.Native.Path = filepath.Join(output, "lancedb")
	nativeLayout, err := resolveNativeStorageLayout(nativeCfg, promptLayout)
	if err != nil {
		return err
	}
	incomplete := nativeLayout.SQLiteDatabase + ".migration-incomplete"
	if err := writeNativeMigrationFile(incomplete, []byte("Migration has not passed all checks. Do not serve this database.\n")); err != nil {
		return err
	}
	owner, err := acquireNativeStorageOwner(nativeLayout)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, owner.Shutdown(context.Background())) }()
	if err := ensureNativeStoragePair(nativeLayout); err != nil {
		return fmt.Errorf("initialize native migration pair: %w", err)
	}

	// Materialize one consistent snapshot before any target business rows are imported.
	// 在导入任何目标业务行前生成单一一致性快照。
	snapshotPath := filepath.Join(output, "source-snapshot.db")
	sourceSession, err := AcquireLegacySQLiteSnapshot(ctx, cfg, promptLayout)
	if err != nil {
		return fmt.Errorf("acquire legacy snapshot source: %w", err)
	}
	sourceOwner.resources = append(sourceOwner.resources, sourceSession)
	if err := sourceSession.Export(ctx, snapshotPath); err != nil {
		return fmt.Errorf("snapshot legacy SQLite: %w", err)
	}
	bootstrap, err := vldb_sqlite.NewNativeStore(nativeLayout.SQLiteDatabase, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{TokenizerMode: nativeCfg.SQLite.Native.Tokenizer, SkipDebugSeed: true})
	if err != nil {
		return err
	}
	owner.resources = append(owner.resources, bootstrap)
	if err := bootstrap.Shutdown(context.Background()); err != nil {
		return err
	}
	copyReport, err := storagemigrate.CopyNativeSnapshot(ctx, snapshotPath, nativeLayout.SQLiteDatabase)
	if err != nil {
		return fmt.Errorf("copy SQLite snapshot: %w", err)
	}
	relational, err := vldb_sqlite.NewNativeStore(nativeLayout.SQLiteDatabase, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{TokenizerMode: nativeCfg.SQLite.Native.Tokenizer, SkipDebugSeed: true})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, relational.Shutdown(context.Background())) }()
	owner.resources = append(owner.resources, relational)
	if err := relational.RebuildFTSIndex(ctx); err != nil {
		return fmt.Errorf("rebuild migrated full-text index: %w", err)
	}
	vector, err := vldb_lancedb.NewNativeStore(nativeLayout.LanceDBLibrary, nativeLayout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, vector.Shutdown(context.Background())) }()
	owner.resources = append(owner.resources, vector)
	report := nativeMigrationReport{SourceMode: cfg.StorageMode(), SourceModel: cfg.Embedding.Model, Dimension: cfg.Embedding.Dimension, SQLite: copyReport}
	migrationTime := time.Now().UTC()
	// A restorable recycle batch keeps its sidecar vector even while the corresponding live fact is absent.
	// 可恢复回收批次在活动事实行不存在时仍保留旁路向量，恢复流程只恢复关系行。
	seenVectors := make(map[string][sha256.Size]byte)
	replayVector := func(record logicdomain.MemoryRecord) error {
		fresh, err := registerNativeMigrationVector(record, cfg.Embedding.Dimension, seenVectors)
		if err != nil {
			return err
		}
		if !fresh {
			report.DuplicateVectors++
			return nil
		}
		if err := vector.Upsert(ctx, record); err != nil {
			return err
		}
		report.Vectors++
		return nil
	}
	if err := relational.WalkNativeMigrationMemories(ctx, func(record logicdomain.MemoryRecord) error {
		if len(record.Vector) == 0 && (record.Status != logicdomain.MemoryStatusActive || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(migrationTime))) {
			report.SkippedInactive++
			return nil
		}
		return replayVector(record)
	}); err != nil {
		return fmt.Errorf("replay durable vectors: %w", err)
	}
	if err := relational.WalkNativeMigrationRestorableTrash(ctx, migrationTime, func(record logicdomain.MemoryRecord) error {
		report.RestorableTrash++
		return replayVector(record)
	}); err != nil {
		return fmt.Errorf("replay restorable recycle vectors: %w", err)
	}
	count, err := vector.Count(ctx)
	if err != nil {
		return err
	}
	if count != report.Vectors {
		return fmt.Errorf("migrated vector count %d differs from expected %d", count, report.Vectors)
	}
	if err := ensureNativeVectorSchemaVersion(ctx, relational); err != nil {
		return err
	}
	if err := relational.CheckHealth(ctx); err != nil {
		return err
	}
	if err := vector.CheckHealth(ctx); err != nil {
		return err
	}
	// Close durable resources before publishing completion so close failures leave the quarantine marker intact.
	// 发布完成状态前关闭持久资源，关闭失败时保留隔离标记。
	if err := errors.Join(vector.Shutdown(context.Background()), relational.Shutdown(context.Background())); err != nil {
		return err
	}
	if err := sourceSession.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("close legacy snapshot source before publishing migration: %w", err)
	}
	if err := updateNativeEmbeddingIdentity(nativeCfg); err != nil {
		return fmt.Errorf("record migrated embedding identity: %w", err)
	}
	report.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeNativeMigrationArtifacts(output, nativeCfg, nativeLayout, report); err != nil {
		return err
	}
	if err := os.Remove(snapshotPath); err != nil {
		return fmt.Errorf("remove migration-owned snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(incomplete); err != nil {
		return err
	}
	if err := syncNativeParentDirectory(incomplete); err != nil {
		// An unacknowledged completion must remain quarantined even when the in-memory directory view already lost the marker.
		// 完成状态未得到持久确认时，即使当前目录视图已删除标记，也必须恢复隔离。
		restoreErr := writeNativeMigrationFile(incomplete, []byte("Migration completion could not be durably published. Do not serve this database.\n"))
		return errors.Join(fmt.Errorf("sync native migration completion: %w", err), restoreErr)
	}
	return nil
}

// registerNativeMigrationVector deduplicates identical vector payloads while rejecting ambiguous IDs shared by live and restorable rows.
// registerNativeMigrationVector 对完全一致的向量载荷去重，拒绝活动行和可恢复回收行之间存在歧义的标识。
func registerNativeMigrationVector(record logicdomain.MemoryRecord, dimension int, seen map[string][sha256.Size]byte) (bool, error) {
	if err := validateNativeMigrationVector(record, dimension); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("encode migration vector identity %q: %w", record.ID, err)
	}
	fingerprint := sha256.Sum256(encoded)
	key := strings.TrimSpace(record.ID)
	if existing, found := seen[key]; found {
		if existing != fingerprint {
			return false, fmt.Errorf("vector id %q has conflicting durable payloads across live or restorable rows", record.ID)
		}
		return false, nil
	}
	seen[key] = fingerprint
	return true, nil
}

// validateNativeMigrationVector rejects incomplete or incompatible vectors instead of generating replacements.
// validateNativeMigrationVector 拒绝不完整或不兼容向量，不生成替代向量。
func validateNativeMigrationVector(record logicdomain.MemoryRecord, dimension int) error {
	if strings.TrimSpace(record.ID) == "" || len(record.Vector) != dimension || dimension <= 0 {
		return fmt.Errorf("memory vector %q has dimension %d, expected %d", record.ID, len(record.Vector), dimension)
	}
	for _, value := range record.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("memory vector %q contains non-finite values", record.ID)
		}
	}
	return nil
}

// writeNativeMigrationArtifacts writes a credential-free report and explicit storage override for operator review.
// writeNativeMigrationArtifacts 写入不含凭据的报告与明确存储覆盖配置，供操作者审核后切换。
func writeNativeMigrationArtifacts(output string, cfg config.Config, layout nativeStorageLayout, report nativeMigrationReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := writeNativeMigrationFile(filepath.Join(output, "migration-report.json"), append(encoded, '\n')); err != nil {
		return err
	}
	override := map[string]any{
		"storage":   map[string]any{"mode": "native"},
		"sqlite":    map[string]any{"native": map[string]any{"path": layout.SQLiteDatabase, "tokenizer": cfg.SQLite.Native.Tokenizer}},
		"lancedb":   map[string]any{"table_name": cfg.LanceDB.TableName, "vector_column": cfg.LanceDB.VectorColumn, "native": map[string]any{"path": layout.LanceDBDirectory, "library_path": layout.LanceDBLibrary}},
		"embedding": map[string]any{"dimension": cfg.Embedding.Dimension},
	}
	encoded, err = yaml.Marshal(override)
	if err != nil {
		return err
	}
	header := []byte("# 配置片段，不能直接作为 -config 参数；合并到原配置覆盖根中的原文件。\n# 保留原来源的全部 embedding 提供商、模型以及 prompts/pii_rules/noise_rules 覆盖目录。\n# 内存行不包含模型身份；配置中的来源模型由 migration-report.json 记录。\n")
	return writeNativeMigrationFile(filepath.Join(output, "native-storage-override.fragment.yaml"), append(header, encoded...))
}

// writeNativeMigrationFile durably creates one migration-owned file; existing files are never replaced.
// writeNativeMigrationFile 持久创建单个迁移专属文件，绝不覆盖已有文件；内容和目录项均确认后才返回成功。
func writeNativeMigrationFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(content)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return syncNativeParentDirectory(path)
}
