// fts.go implements native-only FTS5 tables, GSE lexical surfaces, metadata, and recovery queue processing.
// fts.go 用于实现原生专用 FTS5 表、GSE 词法表面、元数据和恢复队列处理。
package native_sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-ego/gse"
	storagecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	// nativeFTSMetadataTable stores the active generation and tokenizer fingerprints for every logical native index.
	// nativeFTSMetadataTable 保存每个原生逻辑索引的活动代次与分词指纹。
	nativeFTSMetadataTable = "vmm_native_fts_metadata"

	// nativeFTSQueueTable records fact-table revisions whose derived FTS row is not yet confirmed synchronized.
	// nativeFTSQueueTable 记录事实表修订与派生 FTS 行尚未确认同步的待处理项。
	nativeFTSQueueTable = "vmm_native_fts_sync_queue"

	// nativeFTSIndexVersion identifies the native FTS table shape and query contract.
	// nativeFTSIndexVersion 标识原生 FTS 表结构和查询契约版本。
	nativeFTSIndexVersion = 1

	// nativeFTSNormalizationVersion identifies the whitespace normalization used in indexed and queried text.
	// nativeFTSNormalizationVersion 标识索引文本与查询文本使用的空白归一化版本。
	nativeFTSNormalizationVersion = "normalize-whitespace-v1"

	// nativeFTSAlgorithmVersion identifies the application-side GSE token expansion strategy.
	// nativeFTSAlgorithmVersion 标识应用侧 GSE token 扩展策略。
	nativeFTSAlgorithmVersion = "gse-pretokenize-v1"

	// nativeFTSDefaultLogicalIndex is the logical name used by the memory store.
	// nativeFTSDefaultLogicalIndex 是 memory store 使用的逻辑索引名。
	nativeFTSDefaultLogicalIndex = "vmm_memory_nodes_fts"

	// nativeFTSRebuildBatchSize bounds relation rows held while one rebuild transaction remains open.
	// nativeFTSRebuildBatchSize 限制重建事务中暂存的关系行数量。
	nativeFTSRebuildBatchSize = 256

	// nativeFTSQueueBatchSize bounds pending queue rows held during one recovery transaction.
	// nativeFTSQueueBatchSize 限制恢复事务中暂存的待处理队列行数量。
	nativeFTSQueueBatchSize = 256
)

// nativeFTSMetadata mirrors one durable native FTS metadata row.
// nativeFTSMetadata 映射一条持久化原生 FTS 元数据记录。
type nativeFTSMetadata struct {
	LogicalName           string
	ActiveTable           string
	IndexVersion          int
	TokenizerMode         int
	TokenizerAlgorithm    string
	GSEVersion            string
	DictionaryFingerprint string
	NormalizationVersion  string
	Generation            int64
	State                 string
}

// nativeFTSQueueItem mirrors one pending fact-to-index synchronization record.
// nativeFTSQueueItem 映射一条事实到索引的待同步记录。
type nativeFTSQueueItem struct {
	MemoryID  uint64
	Revision  int64
	Operation string
}

// nativeMemoryFTSDocument carries raw relation fields before lexical projection.
// nativeMemoryFTSDocument 在词法投影前携带关系表原始字段。
type nativeMemoryFTSDocument struct {
	ID               uint64
	VectorID         string
	Abstract         string
	Details          string
	UpdatedTimestamp int64
}

// EnsureFtsIndex creates or repairs the native FTS index and recovers pending derived rows before returning.
// EnsureFtsIndex 创建或修复原生 FTS 索引，并在返回前恢复待处理派生行。
func (d *Database) EnsureFtsIndex(ctx context.Context, indexName string, mode storagecontract.TokenizerMode) (storagecontract.EnsureFtsIndexResult, error) {
	logicalName, err := validateLogicalIndexName(indexName)
	if err != nil {
		return storagecontract.EnsureFtsIndexResult{}, err
	}
	if err := validateNativeTokenizerMode(mode); err != nil {
		return storagecontract.EnsureFtsIndexResult{}, err
	}
	var result storagecontract.EnsureFtsIndexResult
	err = d.withMaintenanceOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		metadata, err := d.ensureFTSIndexLocked(ctx, conn, logicalName, mode)
		if err != nil {
			return err
		}
		result = storagecontract.EnsureFtsIndexResult{Success: true, TokenizerMode: storagecontract.TokenizerMode(metadata.TokenizerMode)}
		return nil
	})
	if err != nil {
		return storagecontract.EnsureFtsIndexResult{}, err
	}
	return result, nil
}

// RebuildFtsIndex rebuilds a new generation from relation facts and atomically switches the active table.
// RebuildFtsIndex 从关系事实重建新代次，并原子切换活动表。
func (d *Database) RebuildFtsIndex(ctx context.Context, indexName string, mode storagecontract.TokenizerMode) (storagecontract.RebuildFtsIndexResult, error) {
	logicalName, err := validateLogicalIndexName(indexName)
	if err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	if err := validateNativeTokenizerMode(mode); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	var result storagecontract.RebuildFtsIndexResult
	err = d.withMaintenanceOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		result, err = d.rebuildFTSIndexLocked(ctx, conn, logicalName, mode)
		return err
	})
	if err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	return result, nil
}

// UpsertFtsDocument writes one pre-tokenized native FTS row and conditionally clears its current queue revision.
// UpsertFtsDocument 写入一条原生预分词 FTS 行，并按当前修订条件清理其队列记录。
func (d *Database) UpsertFtsDocument(ctx context.Context, indexName string, mode storagecontract.TokenizerMode, id string, _filePath string, _title string, _content string) (storagecontract.FtsMutationResult, error) {
	logicalName, err := validateLogicalIndexName(indexName)
	if err != nil {
		return storagecontract.FtsMutationResult{}, err
	}
	if err := validateNativeTokenizerMode(mode); err != nil {
		return storagecontract.FtsMutationResult{}, err
	}
	if strings.TrimSpace(id) == "" {
		return storagecontract.FtsMutationResult{}, errors.New("native sqlite fts document id is required")
	}
	var result storagecontract.FtsMutationResult
	err = d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		metadata, err := d.ensureFTSIndexLocked(ctx, conn, logicalName, mode)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin native fts upsert: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		physical := quoteIdentifier(metadata.ActiveTable)
		filePath, title, content := _filePath, _title, _content
		memoryID, memoryIDErr := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
		authoritativeMemory := logicalName == nativeFTSDefaultLogicalIndex
		currentMemoryExists := false
		var currentRevision int64
		var currentDocument nativeMemoryFTSDocument
		if authoritativeMemory {
			if memoryIDErr != nil {
				return fmt.Errorf("native memory fts document id must be an integer: %q", id)
			}
			currentDocument, currentMemoryExists, err = loadCurrentMemoryFTSDocumentTx(ctx, tx, memoryID)
			if err != nil {
				return err
			}
			if currentMemoryExists {
				filePath, title, content = currentDocument.VectorID, currentDocument.Abstract, currentDocument.Details
				currentRevision = currentDocument.UpdatedTimestamp
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+physical+" WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete previous native fts document: %w", err)
		}
		if !authoritativeMemory || currentMemoryExists {
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+physical+" (id, file_path, title, content) VALUES (?, ?, ?, ?)", id, filePath, d.indexText(mode, title), d.indexText(mode, content)); err != nil {
				return fmt.Errorf("insert native fts document: %w", err)
			}
		}
		if authoritativeMemory && currentMemoryExists {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM vmm_native_fts_sync_queue
WHERE memory_id = ?
  AND operation = 'upsert'
  AND revision = ?
`, memoryID, currentRevision); err != nil {
				return fmt.Errorf("clear native fts upsert queue: %w", err)
			}
		} else if authoritativeMemory {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM vmm_native_fts_sync_queue
WHERE memory_id = ? AND operation = 'delete'
`, memoryID); err != nil {
				return fmt.Errorf("clear native fts delete queue after stale upsert: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nativeSQLiteCommitError("commit native fts upsert", err)
		}
		committed = true
		result = storagecontract.FtsMutationResult{Success: true, AffectedRows: 1}
		if authoritativeMemory && !currentMemoryExists {
			result.AffectedRows = 0
		}
		return nil
	})
	if err != nil {
		return storagecontract.FtsMutationResult{}, err
	}
	return result, nil
}

// DeleteFtsDocument removes one native FTS row and clears only a delete queue with no surviving fact row.
// DeleteFtsDocument 删除一条原生 FTS 行，并仅在事实行不存在时清理删除队列。
func (d *Database) DeleteFtsDocument(ctx context.Context, indexName string, id string) (storagecontract.FtsMutationResult, error) {
	logicalName, err := validateLogicalIndexName(indexName)
	if err != nil {
		return storagecontract.FtsMutationResult{}, err
	}
	if strings.TrimSpace(id) == "" {
		return storagecontract.FtsMutationResult{}, errors.New("native sqlite fts document id is required")
	}
	var result storagecontract.FtsMutationResult
	err = d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if err := ensureFTSAuxiliarySchema(ctx, conn); err != nil {
			return err
		}
		mode := storagecontract.TokenizerGSE
		if existingMetadata, exists, err := loadFTSMetadata(ctx, conn, logicalName); err != nil {
			return err
		} else if exists {
			mode = storagecontract.TokenizerMode(existingMetadata.TokenizerMode)
		}
		if err := validateNativeTokenizerMode(mode); err != nil {
			return err
		}
		metadata, err := d.ensureFTSIndexLocked(ctx, conn, logicalName, mode)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin native fts delete: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		physical := quoteIdentifier(metadata.ActiveTable)
		memoryID, memoryIDErr := strconv.ParseUint(strings.TrimSpace(id), 10, 64)
		authoritativeMemory := logicalName == nativeFTSDefaultLogicalIndex
		currentMemoryExists := false
		var currentDocument nativeMemoryFTSDocument
		if authoritativeMemory {
			if memoryIDErr != nil {
				return fmt.Errorf("native memory fts document id must be an integer: %q", id)
			}
			currentDocument, currentMemoryExists, err = loadCurrentMemoryFTSDocumentTx(ctx, tx, memoryID)
			if err != nil {
				return err
			}
		}
		deleteResult, err := tx.ExecContext(ctx, "DELETE FROM "+physical+" WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete native fts document: %w", err)
		}
		if authoritativeMemory && currentMemoryExists {
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+physical+" (id, file_path, title, content) VALUES (?, ?, ?, ?)", id, currentDocument.VectorID, d.indexText(mode, currentDocument.Abstract), d.indexText(mode, currentDocument.Details)); err != nil {
				return fmt.Errorf("restore current native fts document after stale delete: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
DELETE FROM vmm_native_fts_sync_queue
WHERE memory_id = ? AND operation = 'upsert' AND revision = ?
`, memoryID, currentDocument.UpdatedTimestamp); err != nil {
				return fmt.Errorf("clear native fts upsert queue after stale delete: %w", err)
			}
		} else if authoritativeMemory {
			if _, err := tx.ExecContext(ctx, `
DELETE FROM vmm_native_fts_sync_queue
WHERE memory_id = ?
  AND operation = 'delete'
`, memoryID); err != nil {
				return fmt.Errorf("clear native fts delete queue: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nativeSQLiteCommitError("commit native fts delete", err)
		}
		committed = true
		changed, _ := deleteResult.RowsAffected()
		result = storagecontract.FtsMutationResult{Success: true, AffectedRows: uint64(maxInt64(changed, 0))}
		return nil
	})
	if err != nil {
		return storagecontract.FtsMutationResult{}, err
	}
	return result, nil
}

// loadCurrentMemoryFTSDocumentTx reads the authoritative relation row inside the mutation transaction.
// loadCurrentMemoryFTSDocumentTx 在变更事务内读取权威关系行。
func loadCurrentMemoryFTSDocumentTx(ctx context.Context, tx *sql.Tx, memoryID uint64) (nativeMemoryFTSDocument, bool, error) {
	var document nativeMemoryFTSDocument
	err := tx.QueryRowContext(ctx, `
SELECT id, vector_id, abstract, details, updated_timestamp
FROM vmm_memory_nodes WHERE id = ?`, memoryID).Scan(&document.ID, &document.VectorID, &document.Abstract, &document.Details, &document.UpdatedTimestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeMemoryFTSDocument{}, false, nil
	}
	if err != nil {
		return nativeMemoryFTSDocument{}, false, fmt.Errorf("read current native memory fact %d: %w", memoryID, err)
	}
	return document, true, nil
}

// SearchFts executes safe quoted MATCH syntax and returns higher-is-better BM25 scores.
// SearchFts 执行安全转义的 MATCH 语法，并返回“越大越好”的 BM25 分数。
func (d *Database) SearchFts(ctx context.Context, indexName string, mode storagecontract.TokenizerMode, query string, limit uint32, offset uint32) (storagecontract.SearchResult, error) {
	logicalName, err := validateLogicalIndexName(indexName)
	if err != nil {
		return storagecontract.SearchResult{}, err
	}
	if err := validateNativeTokenizerMode(mode); err != nil {
		return storagecontract.SearchResult{}, err
	}
	expression := d.matchExpression(mode, query)
	if expression == "" {
		return storagecontract.SearchResult{Source: "sqlite_fts", QueryMode: "fts", Hits: []storagecontract.SearchHit{}}, nil
	}
	if limit == 0 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	var result storagecontract.SearchResult
	err = d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		metadata, err := d.ensureFTSIndexLocked(ctx, conn, logicalName, mode)
		if err != nil {
			return err
		}
		physical := quoteIdentifier(metadata.ActiveTable)
		var total uint64
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+physical+" WHERE "+physical+" MATCH ?", expression).Scan(&total); err != nil {
			return fmt.Errorf("count native fts hits: %w", err)
		}
		rows, err := conn.QueryContext(ctx, fmt.Sprintf(`
SELECT id,
       file_path,
       title,
       highlight(%s, 2, '<mark>', '</mark>'),
       snippet(%s, 3, '<mark>', '</mark>', '...', 12),
       bm25(%s, 2.0, 1.0)
FROM %s
WHERE %s MATCH ?
ORDER BY 6 ASC, file_path ASC, id ASC
LIMIT ? OFFSET ?
`, physical, physical, physical, physical, physical), expression, int64(limit), int64(offset))
		if err != nil {
			return fmt.Errorf("query native fts hits: %w", err)
		}
		defer rows.Close()
		hits := make([]storagecontract.SearchHit, 0, limit)
		rank := uint64(offset) + 1
		for rows.Next() {
			var id, filePath string
			var title, titleHighlight, contentSnippet sql.NullString
			var rawScore float64
			if err := rows.Scan(&id, &filePath, &title, &titleHighlight, &contentSnippet, &rawScore); err != nil {
				return fmt.Errorf("scan native fts hit: %w", err)
			}
			if math.IsNaN(rawScore) || math.IsInf(rawScore, 0) {
				rawScore = 0
			}
			hits = append(hits, storagecontract.SearchHit{
				ID:             id,
				FilePath:       filePath,
				Title:          title.String,
				TitleHighlight: titleHighlight.String,
				ContentSnippet: contentSnippet.String,
				Score:          -rawScore,
				Rank:           rank,
				RawScore:       rawScore,
			})
			rank++
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate native fts hits: %w", err)
		}
		result = storagecontract.SearchResult{Total: total, Source: "sqlite_fts", QueryMode: "fts", Hits: hits}
		return nil
	})
	if err != nil {
		return storagecontract.SearchResult{}, err
	}
	return result, nil
}

// ensureFTSIndexLocked creates auxiliary tables, validates metadata, and recovers queued changes on one connection.
// ensureFTSIndexLocked 在单个连接上创建辅助表、校验元数据并恢复队列变更。
func (d *Database) ensureFTSIndexLocked(ctx context.Context, conn *sql.Conn, logicalName string, mode storagecontract.TokenizerMode) (nativeFTSMetadata, error) {
	if err := ensureFTSAuxiliarySchema(ctx, conn); err != nil {
		return nativeFTSMetadata{}, err
	}
	metadata, exists, err := loadFTSMetadata(ctx, conn, logicalName)
	if err != nil {
		return nativeFTSMetadata{}, err
	}
	if !exists {
		if _, err := d.rebuildFTSIndexLocked(ctx, conn, logicalName, mode); err != nil {
			return nativeFTSMetadata{}, err
		}
		metadata, _, err = loadFTSMetadata(ctx, conn, logicalName)
		if err != nil {
			return nativeFTSMetadata{}, err
		}
	} else if !nativeFTSMetadataMatches(metadata, mode) || !nativeFTSTableExists(ctx, conn, metadata.ActiveTable) || metadata.State != "ready" {
		var rebuildResult storagecontract.RebuildFtsIndexResult
		rebuildResult, err = d.rebuildFTSIndexLocked(ctx, conn, logicalName, mode)
		if err != nil {
			return nativeFTSMetadata{}, err
		}
		metadata, _, err = loadFTSMetadata(ctx, conn, logicalName)
		if err != nil {
			return nativeFTSMetadata{}, err
		}
		if !rebuildResult.Success {
			return nativeFTSMetadata{}, errors.New("native fts rebuild reported unsuccessful result")
		}
	}
	if err := recoverNativeFTSQueue(ctx, conn, metadata, mode, d); err != nil {
		return nativeFTSMetadata{}, err
	}
	return metadata, nil
}

// rebuildFTSIndexLocked materializes a new generation and switches metadata only after all rows are validated.
// rebuildFTSIndexLocked 先物化新代次并校验全部行，随后才切换元数据活动指针。
func (d *Database) rebuildFTSIndexLocked(ctx context.Context, conn *sql.Conn, logicalName string, mode storagecontract.TokenizerMode) (storagecontract.RebuildFtsIndexResult, error) {
	if err := ensureFTSAuxiliarySchema(ctx, conn); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	metadata, exists, err := loadFTSMetadata(ctx, conn, logicalName)
	if err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	oldTable := ""
	generation := int64(0)
	if exists {
		oldTable = metadata.ActiveTable
		generation = metadata.Generation
	}
	if generation < 0 {
		generation = 0
	}
	generation++
	newTable := nativeFTSPhysicalTable(logicalName, generation)
	newMetadata := nativeFTSMetadata{LogicalName: logicalName, ActiveTable: newTable, IndexVersion: nativeFTSIndexVersion, TokenizerMode: int(mode), TokenizerAlgorithm: nativeFTSAlgorithmForMode(mode), GSEVersion: gse.Version, DictionaryFingerprint: gseDictionaryFingerprint(), NormalizationVersion: nativeFTSNormalizationVersion, Generation: generation, State: "ready"}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("begin native fts rebuild: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteIdentifier(newTable)); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("clear native fts generation: %w", err)
	}
	if err := createNativeFTSTableTx(ctx, tx, newTable); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	insertStmt, err := tx.PrepareContext(ctx, "INSERT INTO "+quoteIdentifier(newTable)+" (id, file_path, title, content) VALUES (?, ?, ?, ?)")
	if err != nil {
		return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("prepare native fts rebuild insert: %w", err)
	}
	var indexedRows int64
	var lastID uint64
	hasCursor := false
	for {
		query := `SELECT id, vector_id, abstract, details FROM vmm_memory_nodes ORDER BY id ASC LIMIT ?`
		args := []any{nativeFTSRebuildBatchSize}
		if hasCursor {
			query = `SELECT id, vector_id, abstract, details FROM vmm_memory_nodes WHERE id > ? ORDER BY id ASC LIMIT ?`
			args = []any{lastID, nativeFTSRebuildBatchSize}
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			_ = insertStmt.Close()
			return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("read memory facts for native fts rebuild: %w", err)
		}
		documents := make([]nativeMemoryFTSDocument, 0, nativeFTSRebuildBatchSize)
		for rows.Next() {
			var document nativeMemoryFTSDocument
			if err := rows.Scan(&document.ID, &document.VectorID, &document.Abstract, &document.Details); err != nil {
				_ = rows.Close()
				_ = insertStmt.Close()
				return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("scan memory fact for native fts rebuild: %w", err)
			}
			documents = append(documents, document)
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if rowsErr != nil {
			_ = insertStmt.Close()
			return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("iterate memory facts for native fts rebuild: %w", rowsErr)
		}
		if closeErr != nil {
			_ = insertStmt.Close()
			return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("close memory facts for native fts rebuild: %w", closeErr)
		}
		if len(documents) == 0 {
			break
		}
		for _, document := range documents {
			if _, err := insertStmt.ExecContext(ctx, strconv.FormatUint(document.ID, 10), document.VectorID, d.indexText(mode, document.Abstract), d.indexText(mode, document.Details)); err != nil {
				_ = insertStmt.Close()
				return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("insert memory fact into native fts: %w", err)
			}
			indexedRows++
			lastID = document.ID
			hasCursor = true
		}
		if len(documents) < nativeFTSRebuildBatchSize {
			break
		}
	}
	if err := insertStmt.Close(); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("close native fts rebuild insert: %w", err)
	}
	if oldTable != "" && oldTable != newTable {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteIdentifier(oldTable)); err != nil {
			return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("drop previous native fts generation: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdentifier(nativeFTSQueueTable)); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, fmt.Errorf("clear native fts sync queue after rebuild: %w", err)
	}
	if err := upsertFTSMetadataTx(ctx, tx, newMetadata); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return storagecontract.RebuildFtsIndexResult{}, nativeSQLiteCommitError("commit native fts rebuild", err)
	}
	committed = true
	return storagecontract.RebuildFtsIndexResult{Success: true, TokenizerMode: mode, ReindexedRows: uint64(maxInt64(indexedRows, 0))}, nil
}

// ensureFTSAuxiliarySchema installs the metadata, queue, and fact-table triggers required for recovery.
// ensureFTSAuxiliarySchema 安装恢复所需的元数据表、队列表和事实表触发器。
func ensureFTSAuxiliarySchema(ctx context.Context, conn *sql.Conn) error {
	statement := `
CREATE TABLE IF NOT EXISTS vmm_native_fts_metadata (
  logical_name TEXT PRIMARY KEY,
  active_table TEXT NOT NULL,
  index_version INTEGER NOT NULL,
  tokenizer_mode INTEGER NOT NULL,
  tokenizer_algorithm TEXT NOT NULL,
  gse_version TEXT NOT NULL,
  dictionary_fingerprint TEXT NOT NULL,
  normalization_version TEXT NOT NULL,
  generation INTEGER NOT NULL,
  state TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS vmm_native_fts_sync_queue (
  memory_id BIGINT PRIMARY KEY,
  revision BIGINT NOT NULL,
  operation TEXT NOT NULL CHECK(operation IN ('upsert', 'delete')),
  queued_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_native_fts_sync_queue_pending ON vmm_native_fts_sync_queue(operation, revision, memory_id);
CREATE TRIGGER IF NOT EXISTS trg_vmm_native_fts_memory_insert
AFTER INSERT ON vmm_memory_nodes
BEGIN
  INSERT INTO vmm_native_fts_sync_queue(memory_id, revision, operation, queued_at)
  VALUES (NEW.id, NEW.updated_timestamp, 'upsert', CAST(strftime('%s','now') AS INTEGER) * 1000)
  ON CONFLICT(memory_id) DO UPDATE SET revision = excluded.revision, operation = excluded.operation, queued_at = excluded.queued_at;
END;
CREATE TRIGGER IF NOT EXISTS trg_vmm_native_fts_memory_update
AFTER UPDATE OF vector_id, abstract, details, updated_timestamp ON vmm_memory_nodes
BEGIN
  INSERT INTO vmm_native_fts_sync_queue(memory_id, revision, operation, queued_at)
  VALUES (NEW.id, NEW.updated_timestamp, 'upsert', CAST(strftime('%s','now') AS INTEGER) * 1000)
  ON CONFLICT(memory_id) DO UPDATE SET revision = excluded.revision, operation = excluded.operation, queued_at = excluded.queued_at;
END;
CREATE TRIGGER IF NOT EXISTS trg_vmm_native_fts_memory_delete
AFTER DELETE ON vmm_memory_nodes
BEGIN
  INSERT INTO vmm_native_fts_sync_queue(memory_id, revision, operation, queued_at)
  VALUES (OLD.id, OLD.updated_timestamp, 'delete', CAST(strftime('%s','now') AS INTEGER) * 1000)
  ON CONFLICT(memory_id) DO UPDATE SET revision = excluded.revision, operation = excluded.operation, queued_at = excluded.queued_at;
END;
`
	if _, err := conn.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("ensure native fts auxiliary schema: %w", err)
	}
	return nil
}

// recoverNativeFTSQueue applies pending fact revisions in one transaction and clears only matching revisions.
// recoverNativeFTSQueue 在一个事务中应用事实修订，并仅清理版本匹配的队列记录。
func recoverNativeFTSQueue(ctx context.Context, conn *sql.Conn, metadata nativeFTSMetadata, mode storagecontract.TokenizerMode, database *Database) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin native fts queue recovery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	physical := quoteIdentifier(metadata.ActiveTable)
	var lastMemoryID uint64
	hasCursor := false
	for {
		query := `SELECT memory_id, revision, operation FROM vmm_native_fts_sync_queue ORDER BY memory_id ASC LIMIT ?`
		args := []any{nativeFTSQueueBatchSize}
		if hasCursor {
			query = `SELECT memory_id, revision, operation FROM vmm_native_fts_sync_queue WHERE memory_id > ? ORDER BY memory_id ASC LIMIT ?`
			args = []any{lastMemoryID, nativeFTSQueueBatchSize}
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("read native fts sync queue: %w", err)
		}
		queue := make([]nativeFTSQueueItem, 0, nativeFTSQueueBatchSize)
		for rows.Next() {
			var item nativeFTSQueueItem
			if err := rows.Scan(&item.MemoryID, &item.Revision, &item.Operation); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan native fts sync queue: %w", err)
			}
			queue = append(queue, item)
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("iterate native fts sync queue: %w", rowsErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close native fts sync queue: %w", closeErr)
		}
		if len(queue) == 0 {
			break
		}
		for _, item := range queue {
			if item.Operation == "upsert" {
				document, exists, loadErr := loadCurrentMemoryFTSDocumentTx(ctx, tx, item.MemoryID)
				if loadErr != nil {
					return loadErr
				}
				if !exists {
					if _, err := tx.ExecContext(ctx, "DELETE FROM "+physical+" WHERE id = ?", strconv.FormatUint(item.MemoryID, 10)); err != nil {
						return fmt.Errorf("remove missing native fts document %d: %w", item.MemoryID, err)
					}
				} else {
					if _, err := tx.ExecContext(ctx, "DELETE FROM "+physical+" WHERE id = ?", strconv.FormatUint(document.ID, 10)); err != nil {
						return fmt.Errorf("replace pending native fts document %d: %w", item.MemoryID, err)
					}
					if _, err := tx.ExecContext(ctx, "INSERT INTO "+physical+" (id, file_path, title, content) VALUES (?, ?, ?, ?)", strconv.FormatUint(document.ID, 10), document.VectorID, database.indexText(mode, document.Abstract), database.indexText(mode, document.Details)); err != nil {
						return fmt.Errorf("write pending native fts document %d: %w", item.MemoryID, err)
					}
				}
			} else if item.Operation == "delete" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+physical+" WHERE id = ?", strconv.FormatUint(item.MemoryID, 10)); err != nil {
					return fmt.Errorf("delete pending native fts document %d: %w", item.MemoryID, err)
				}
			} else {
				return fmt.Errorf("unsupported native fts queue operation %q", item.Operation)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM vmm_native_fts_sync_queue WHERE memory_id = ? AND revision = ? AND operation = ?", item.MemoryID, item.Revision, item.Operation); err != nil {
				return fmt.Errorf("clear native fts queue item %d: %w", item.MemoryID, err)
			}
			lastMemoryID = item.MemoryID
			hasCursor = true
		}
		if len(queue) < nativeFTSQueueBatchSize {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return nativeSQLiteCommitError("commit native fts queue recovery", err)
	}
	committed = true
	return nil
}

// checkFTSHealthLocked verifies at least one ready metadata row and an executable active FTS table.
// checkFTSHealthLocked 校验至少存在一条 ready 元数据记录和可执行的活动 FTS 表。
func (d *Database) checkFTSHealthLocked(ctx context.Context, conn *sql.Conn) error {
	var metadata nativeFTSMetadata
	err := conn.QueryRowContext(ctx, `
SELECT logical_name, active_table, index_version, tokenizer_mode, tokenizer_algorithm,
       gse_version, dictionary_fingerprint, normalization_version, generation, state
FROM vmm_native_fts_metadata
ORDER BY logical_name ASC
LIMIT 1`).Scan(&metadata.LogicalName, &metadata.ActiveTable, &metadata.IndexVersion, &metadata.TokenizerMode, &metadata.TokenizerAlgorithm, &metadata.GSEVersion, &metadata.DictionaryFingerprint, &metadata.NormalizationVersion, &metadata.Generation, &metadata.State)
	if err != nil {
		return fmt.Errorf("read native fts health metadata: %w", err)
	}
	if metadata.State != "ready" || !nativeFTSTableExists(ctx, conn, metadata.ActiveTable) {
		return fmt.Errorf("native fts index %q is not ready", metadata.LogicalName)
	}
	var probe int
	if err := conn.QueryRowContext(ctx, "SELECT 1 FROM "+quoteIdentifier(metadata.ActiveTable)+" LIMIT 1").Scan(&probe); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("probe native fts index %q: %w", metadata.LogicalName, err)
	}
	return nil
}

// loadFTSMetadata loads one logical index's durable metadata.
// loadFTSMetadata 加载一个逻辑索引的持久化元数据。
func loadFTSMetadata(ctx context.Context, conn *sql.Conn, logicalName string) (nativeFTSMetadata, bool, error) {
	var metadata nativeFTSMetadata
	err := conn.QueryRowContext(ctx, `
SELECT logical_name, active_table, index_version, tokenizer_mode, tokenizer_algorithm,
       gse_version, dictionary_fingerprint, normalization_version, generation, state
FROM vmm_native_fts_metadata WHERE logical_name = ?`, logicalName).Scan(&metadata.LogicalName, &metadata.ActiveTable, &metadata.IndexVersion, &metadata.TokenizerMode, &metadata.TokenizerAlgorithm, &metadata.GSEVersion, &metadata.DictionaryFingerprint, &metadata.NormalizationVersion, &metadata.Generation, &metadata.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeFTSMetadata{}, false, nil
	}
	if err != nil {
		return nativeFTSMetadata{}, false, fmt.Errorf("load native fts metadata %q: %w", logicalName, err)
	}
	return metadata, true, nil
}

// insertFTSMetadata inserts the first generation metadata row.
// insertFTSMetadata 插入首个代次的元数据记录。
func insertFTSMetadata(ctx context.Context, conn *sql.Conn, metadata nativeFTSMetadata) error {
	_, err := conn.ExecContext(ctx, `
INSERT INTO vmm_native_fts_metadata (
  logical_name, active_table, index_version, tokenizer_mode, tokenizer_algorithm,
  gse_version, dictionary_fingerprint, normalization_version, generation, state, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, metadata.LogicalName, metadata.ActiveTable, metadata.IndexVersion, metadata.TokenizerMode, metadata.TokenizerAlgorithm, metadata.GSEVersion, metadata.DictionaryFingerprint, metadata.NormalizationVersion, metadata.Generation, metadata.State, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert native fts metadata: %w", err)
	}
	return nil
}

// upsertFTSMetadataTx publishes one validated generation inside its rebuild transaction.
// upsertFTSMetadataTx 在重建事务中发布一代已验证的元数据。
func upsertFTSMetadataTx(ctx context.Context, tx *sql.Tx, metadata nativeFTSMetadata) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO vmm_native_fts_metadata (
  logical_name, active_table, index_version, tokenizer_mode, tokenizer_algorithm,
  gse_version, dictionary_fingerprint, normalization_version, generation, state, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(logical_name) DO UPDATE SET
  active_table = excluded.active_table,
  index_version = excluded.index_version,
  tokenizer_mode = excluded.tokenizer_mode,
  tokenizer_algorithm = excluded.tokenizer_algorithm,
  gse_version = excluded.gse_version,
  dictionary_fingerprint = excluded.dictionary_fingerprint,
  normalization_version = excluded.normalization_version,
  generation = excluded.generation,
  state = excluded.state,
  updated_at = excluded.updated_at`, metadata.LogicalName, metadata.ActiveTable, metadata.IndexVersion, metadata.TokenizerMode, metadata.TokenizerAlgorithm, metadata.GSEVersion, metadata.DictionaryFingerprint, metadata.NormalizationVersion, metadata.Generation, metadata.State, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("publish native fts metadata: %w", err)
	}
	return nil
}

// createNativeFTSTable creates one unicode61 FTS5 table that stores only derived lexical text and identifiers.
// createNativeFTSTable 创建一张仅保存派生词法文本与标识符的 unicode61 FTS5 表。
func createNativeFTSTable(ctx context.Context, conn *sql.Conn, tableName string) error {
	if _, err := conn.ExecContext(ctx, createNativeFTSSQL(tableName)); err != nil {
		return fmt.Errorf("create native fts table %q: %w", tableName, err)
	}
	return nil
}

// createNativeFTSTableTx creates one generation table inside the rebuild transaction.
// createNativeFTSTableTx 在重建事务中创建一张代次表。
func createNativeFTSTableTx(ctx context.Context, tx *sql.Tx, tableName string) error {
	if _, err := tx.ExecContext(ctx, createNativeFTSSQL(tableName)); err != nil {
		return fmt.Errorf("create native fts generation %q: %w", tableName, err)
	}
	return nil
}

// createNativeFTSSQL renders only an internally validated table identifier into the FTS DDL.
// createNativeFTSSQL 只把内部已校验的表标识符渲染进 FTS DDL。
func createNativeFTSSQL(tableName string) string {
	return fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(
  id UNINDEXED,
  file_path UNINDEXED,
  title,
  content,
  tokenize = 'unicode61 remove_diacritics 2'
)`, quoteIdentifier(tableName))
}

// nativeFTSTableExists checks the SQLite catalog for one active-generation table.
// nativeFTSTableExists 检查 SQLite 目录中是否存在一张活动代次表。
func nativeFTSTableExists(ctx context.Context, conn *sql.Conn, tableName string) bool {
	var count int
	err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tableName).Scan(&count)
	return err == nil && count > 0
}

// nativeFTSMetadataMatches identifies mode or algorithm drift that requires derived-only rebuilding.
// nativeFTSMetadataMatches 判断分词模式或算法漂移，并要求仅重建派生索引。
func nativeFTSMetadataMatches(metadata nativeFTSMetadata, mode storagecontract.TokenizerMode) bool {
	return metadata.IndexVersion == nativeFTSIndexVersion && metadata.TokenizerMode == int(mode) && metadata.TokenizerAlgorithm == nativeFTSAlgorithmForMode(mode) && metadata.GSEVersion == gse.Version && metadata.DictionaryFingerprint == gseDictionaryFingerprint() && metadata.NormalizationVersion == nativeFTSNormalizationVersion
}

// nativeFTSPhysicalTable derives an internal generation table name from a validated logical index name.
// nativeFTSPhysicalTable 根据已校验逻辑索引名推导内部代次表名。
func nativeFTSPhysicalTable(logicalName string, generation int64) string {
	return fmt.Sprintf("vmm_native_fts_%s_g%d", logicalName, generation)
}

// nativeFTSAlgorithmForMode returns the explicit algorithm marker for each native mode.
// nativeFTSAlgorithmForMode 返回每种原生模式的明确算法标记。
func nativeFTSAlgorithmForMode(mode storagecontract.TokenizerMode) string {
	if mode == storagecontract.TokenizerNone {
		return "unicode61-v1"
	}
	return nativeFTSAlgorithmVersion
}

// gseDictionaryFingerprint produces a stable fingerprint for the embedded dictionary profile.
// gseDictionaryFingerprint 为内嵌词典配置生成稳定指纹。
func gseDictionaryFingerprint() string {
	digest := sha256.Sum256([]byte("gse-embedded-zh:" + gse.Version))
	return hex.EncodeToString(digest[:])
}

// validateLogicalIndexName rejects dynamic identifiers that cannot be safely quoted into DDL.
// validateLogicalIndexName 拒绝不能安全用于 DDL 的动态标识符。
func validateLogicalIndexName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("native sqlite fts index name is required")
	}
	for index, r := range value {
		if index == 0 {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return "", fmt.Errorf("native sqlite fts index name must start with [A-Za-z_]: %q", value)
			}
			continue
		}
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", fmt.Errorf("native sqlite fts index name only supports [A-Za-z0-9_]: %q", value)
		}
	}
	return value, nil
}

// validateNativeTokenizerMode keeps legacy Jieba ABI values out of the native GSE index path.
// validateNativeTokenizerMode 防止旧 Jieba ABI 值进入原生 GSE 索引路径。
func validateNativeTokenizerMode(mode storagecontract.TokenizerMode) error {
	if mode != storagecontract.TokenizerNone && mode != storagecontract.TokenizerGSE {
		return fmt.Errorf("unsupported native sqlite tokenizer mode %d; use gse or unicode61", mode)
	}
	return nil
}

// indexText applies the selected native lexical projection while preserving raw relation values elsewhere.
// indexText 应用所选原生词法投影，同时保持关系表中的原文不变。
func (d *Database) indexText(mode storagecontract.TokenizerMode, raw string) string {
	if mode == storagecontract.TokenizerGSE {
		return d.tokenizer.BuildSQLiteFTSIndexText(raw)
	}
	return textutil.DisabledLexicalTokenizer().BuildSQLiteFTSIndexText(raw)
}

// matchExpression creates a safe quoted MATCH expression and never returns a full-table fallback.
// matchExpression 构造安全转义的 MATCH 表达式，绝不回退为全表匹配。
func (d *Database) matchExpression(mode storagecontract.TokenizerMode, query string) string {
	if mode == storagecontract.TokenizerGSE {
		return d.tokenizer.BuildSQLiteFTSMatchExpression(query)
	}
	return textutil.DisabledLexicalTokenizer().BuildSQLiteFTSMatchExpression(query)
}

// maxInt64 converts negative driver counts to zero for unsigned result fields.
// maxInt64 将负驱动计数转换为无符号结果字段所需的零值。
func maxInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}
