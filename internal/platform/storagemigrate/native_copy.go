// native_copy.go copies a consistent legacy SQLite snapshot into an initialized native SQLite database.
// native_copy.go 用于把一致性的旧 SQLite 快照复制到已经初始化的原生 SQLite 数据库。
package storagemigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	// nativeSQLiteSchemaVersion is the only legacy relational schema that this offline copier accepts.
	// nativeSQLiteSchemaVersion 是离线复制器唯一接受的旧关系 schema 版本。
	nativeSQLiteSchemaVersion = 22

	// legacyMemoryFTSName is the only legacy FTS5 virtual table that belongs to the old SQLite store.
	// legacyMemoryFTSName 是旧 SQLite 存储唯一拥有的 FTS5 虚拟表名称。
	legacyMemoryFTSName = "vmm_memory_nodes_fts"
)

var (
	// nativeCopyBusinessTables is the audited allowlist copied as durable relational facts.
	// nativeCopyBusinessTables 是经过审计、会被复制为长期关系事实的白名单。
	nativeCopyBusinessTables = []string{
		"vmm_noise_embeddings",
		"vmm_users",
		"vmm_teams",
		"vmm_spaces",
		"vmm_projects",
		"vmm_sessions",
		"vmm_turn_records",
		"vmm_turn_analysis_failures",
		"vmm_memory_nodes",
		"vmm_memory_context_edges",
		"vmm_recycle_batches",
		"vmm_recycle_jobs",
		"vmm_memory_nodes_trash",
		"vmm_memory_context_edges_trash",
		"vmm_turn_records_trash",
		"vmm_vector_gc_jobs",
		"vmm_profile_nodes",
		"vmm_profile_instructions",
		"vmm_management_session_states",
		"vmm_management_previews",
		"vmm_management_operations",
		"vmm_management_recycle_batches",
		"vmm_sessions_trash",
		"vmm_profile_nodes_trash",
	}

	// nativeCopyTableSet makes allowlist checks independent from the order used for the copy plan.
	// nativeCopyTableSet 让白名单检查不依赖复制计划的排列顺序。
	nativeCopyTableSet = makeNativeCopyTableSet()

	// legacyFTSShadowNames enumerates the FTS5 shadow tables produced by the legacy memory index.
	// legacyFTSShadowNames 列出旧记忆索引创建的 FTS5 shadow 表。
	legacyFTSShadowNames = map[string]struct{}{
		legacyMemoryFTSName:              {},
		legacyMemoryFTSName + "_config":  {},
		legacyMemoryFTSName + "_content": {},
		legacyMemoryFTSName + "_data":    {},
		legacyMemoryFTSName + "_docsize": {},
		legacyMemoryFTSName + "_idx":     {},
	}

	// legacyEngineMetadataNames enumerates metadata owned by the old SQLite engine rather than VMM business storage.
	// legacyEngineMetadataNames 列出由旧 SQLite 引擎拥有、而非 VMM 业务存储拥有的元数据表。
	legacyEngineMetadataNames = map[string]struct{}{
		"_vulcan_dict": {},
	}
)

// NativeCopyTableReport describes the count and type-sensitive digest of one copied table.
// NativeCopyTableReport 描述一张已复制表的行数和类型敏感摘要。
type NativeCopyTableReport struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}

// NativeCopyReport summarizes one complete legacy-to-native SQLite copy.
// NativeCopyReport 汇总一次完整的旧库到原生 SQLite 复制。
type NativeCopyReport struct {
	SourceSchemaVersion int                     `json:"source_schema_version"`
	Tables              []NativeCopyTableReport `json:"tables"`
	TotalRows           int64                   `json:"total_rows"`
}

// sqliteTableColumn contains the schema facts needed to preserve column order, declared type, nullability, default, and primary-key order.
// sqliteTableColumn 保存复制所需的列顺序、声明类型、可空性、默认值和主键顺序事实。
type sqliteTableColumn struct {
	CID        int
	Name       string
	Type       string
	NotNull    int
	Default    sql.NullString
	PrimaryKey int
}

// sqliteTableObject identifies one user-visible SQLite table returned by sqlite_master.
// sqliteTableObject 标识 sqlite_master 返回的一张用户可见 SQLite 表。
type sqliteTableObject struct {
	Name string
	Type string
}

// CopyNativeSnapshot copies a read-only immutable legacy snapshot into an empty initialized native database in one transaction.
// CopyNativeSnapshot 将只读 immutable 旧库快照在一个事务中复制到已初始化且业务表为空的原生数据库。
func CopyNativeSnapshot(ctx context.Context, snapshotSQLitePath, targetNativeSQLitePath string) (NativeCopyReport, error) {
	if ctx == nil {
		return NativeCopyReport{}, errors.New("native snapshot copy context is nil")
	}
	sourcePath, targetPath, err := validateCopyPaths(snapshotSQLitePath, targetNativeSQLitePath)
	if err != nil {
		return NativeCopyReport{}, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("stat legacy sqlite snapshot %q: %w", sourcePath, err)
	}
	if targetInfo, err := os.Stat(targetPath); err != nil {
		return NativeCopyReport{}, fmt.Errorf("stat initialized native sqlite database %q: %w", targetPath, err)
	} else if targetInfo.IsDir() {
		return NativeCopyReport{}, fmt.Errorf("initialized native sqlite database path %q is a directory", targetPath)
	} else if os.SameFile(sourceInfo, targetInfo) {
		return NativeCopyReport{}, errors.New("legacy snapshot and native target refer to the same file")
	}

	source, err := sql.Open("sqlite", immutableSQLiteDSN(sourcePath))
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("open immutable legacy sqlite snapshot: %w", err)
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(1)
	if err := source.PingContext(ctx); err != nil {
		return NativeCopyReport{}, fmt.Errorf("ping immutable legacy sqlite snapshot: %w", err)
	}
	if _, err := source.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return NativeCopyReport{}, fmt.Errorf("enable legacy sqlite query_only mode: %w", err)
	}

	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("open initialized native sqlite database: %w", err)
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	target.SetMaxIdleConns(1)
	if err := target.PingContext(ctx); err != nil {
		return NativeCopyReport{}, fmt.Errorf("ping initialized native sqlite database: %w", err)
	}
	if err := enableTargetForeignKeys(ctx, target); err != nil {
		return NativeCopyReport{}, err
	}

	sourceObjects, err := listSQLiteTables(ctx, source)
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("list legacy sqlite tables: %w", err)
	}
	if err := validateLegacyObjects(ctx, source, sourceObjects); err != nil {
		return NativeCopyReport{}, err
	}
	targetObjects, err := listSQLiteTables(ctx, target)
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("list native sqlite tables: %w", err)
	}
	if err := validateNativeObjects(targetObjects); err != nil {
		return NativeCopyReport{}, err
	}
	sourceSchemas, err := validateTableSchemas(ctx, source, target, sourceObjects, targetObjects)
	if err != nil {
		return NativeCopyReport{}, err
	}
	if err := validateTargetBusinessTablesEmpty(ctx, target); err != nil {
		return NativeCopyReport{}, err
	}
	if err := validateSQLiteSequence(ctx, target, targetObjects, "native target"); err != nil {
		return NativeCopyReport{}, err
	}

	sourceSchemaVersion, err := readRequiredSchemaVersion(ctx, source)
	if err != nil {
		return NativeCopyReport{}, err
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("begin native sqlite copy transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `DELETE FROM "vmm_schema_versions"`); err != nil {
		return NativeCopyReport{}, fmt.Errorf("clear native schema versions before copy: %w", err)
	}

	reports := make([]NativeCopyTableReport, 0, len(nativeCopyBusinessTables)+1)
	var totalRows int64
	for _, tableName := range nativeCopyBusinessTables {
		report, err := copyTable(ctx, source, tx, tableName, sourceSchemas[tableName])
		if err != nil {
			return NativeCopyReport{}, fmt.Errorf("copy table %s: %w", tableName, err)
		}
		reports = append(reports, report)
		totalRows += report.Rows
	}
	schemaReport, err := copyTable(ctx, source, tx, "vmm_schema_versions", sourceSchemas["vmm_schema_versions"])
	if err != nil {
		return NativeCopyReport{}, fmt.Errorf("copy table vmm_schema_versions: %w", err)
	}
	reports = append(reports, schemaReport)
	totalRows += schemaReport.Rows

	if err := verifyCopiedTables(ctx, tx, reports, sourceSchemas); err != nil {
		return NativeCopyReport{}, err
	}
	if err := checkTargetIntegrity(ctx, tx); err != nil {
		return NativeCopyReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return NativeCopyReport{}, fmt.Errorf("commit native sqlite snapshot copy: %w", err)
	}
	committed = true
	return NativeCopyReport{SourceSchemaVersion: sourceSchemaVersion, Tables: reports, TotalRows: totalRows}, nil
}

// makeNativeCopyTableSet builds the durable table allowlist once at package initialization.
// makeNativeCopyTableSet 在包初始化时建立长期表白名单。
func makeNativeCopyTableSet() map[string]struct{} {
	set := make(map[string]struct{}, len(nativeCopyBusinessTables)+1)
	for _, tableName := range nativeCopyBusinessTables {
		set[tableName] = struct{}{}
	}
	set["vmm_schema_versions"] = struct{}{}
	return set
}

// validateCopyPaths normalizes both filesystem paths and rejects copying a file onto itself.
// validateCopyPaths 规范化两个文件系统路径，并拒绝把文件复制到自身。
func validateCopyPaths(snapshotSQLitePath, targetNativeSQLitePath string) (string, string, error) {
	if strings.TrimSpace(snapshotSQLitePath) == "" || strings.TrimSpace(targetNativeSQLitePath) == "" {
		return "", "", errors.New("legacy snapshot and native target paths are required")
	}
	sourcePath, err := filepath.Abs(filepath.Clean(snapshotSQLitePath))
	if err != nil {
		return "", "", fmt.Errorf("resolve legacy sqlite snapshot path: %w", err)
	}
	targetPath, err := filepath.Abs(filepath.Clean(targetNativeSQLitePath))
	if err != nil {
		return "", "", fmt.Errorf("resolve native sqlite target path: %w", err)
	}
	if strings.EqualFold(sourcePath, targetPath) {
		return "", "", errors.New("legacy snapshot and native target must be different files")
	}
	return sourcePath, targetPath, nil
}

// immutableSQLiteDSN creates the verified read-only immutable SQLite URI used for snapshot reads.
// immutableSQLiteDSN 创建已验证的只读 immutable SQLite URI，用于读取快照。
func immutableSQLiteDSN(path string) string {
	slashPath := filepath.ToSlash(path)
	// A Windows drive path needs one URI slash before the drive letter so the result is file:///D:/...; POSIX absolute paths already have it.
	// Windows 驱动器路径需要在盘符前补一个 URI 斜杠以生成 file:///D:/...，POSIX 绝对路径本身已经带有该斜杠。
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	uri := url.URL{Scheme: "file", Path: slashPath}
	uri.RawQuery = url.Values{"immutable": []string{"1"}, "mode": []string{"ro"}}.Encode()
	return uri.String()
}

// enableTargetForeignKeys enables and verifies foreign-key enforcement on the dedicated target connection.
// enableTargetForeignKeys 在专用目标连接上启用并验证外键约束。
func enableTargetForeignKeys(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable native target foreign keys: %w", err)
	}
	var enabled int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return fmt.Errorf("read native target foreign-key mode: %w", err)
	}
	if enabled != 1 {
		return errors.New("native target foreign-key enforcement is unavailable")
	}
	return nil
}

// listSQLiteTables reads all persistent tables and views, including internal sequence and FTS shadow tables.
// listSQLiteTables 读取全部持久化表和视图，包括内部 sequence 表和 FTS shadow 表。
func listSQLiteTables(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]sqliteTableObject, error) {
	rows, err := db.QueryContext(ctx, "SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make(map[string]sqliteTableObject)
	for rows.Next() {
		var object sqliteTableObject
		if err := rows.Scan(&object.Name, &object.Type); err != nil {
			return nil, err
		}
		objects[object.Name] = object
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return objects, nil
}

// validateLegacyObjects rejects every old user table outside the audited business and exact FTS allowlists.
// validateLegacyObjects 拒绝审计业务表和精确 FTS 白名单之外的全部旧用户表。
func validateLegacyObjects(ctx context.Context, db *sql.DB, objects map[string]sqliteTableObject) error {
	if err := validateRequiredTables(objects, "legacy snapshot"); err != nil {
		return err
	}
	for tableName := range objects {
		if _, allowed := nativeCopyTableSet[tableName]; allowed {
			continue
		}
		if isSQLiteInternalTable(tableName) {
			continue
		}
		if isLegacyFTSShadow(tableName) {
			if objects[tableName].Type != "table" {
				return fmt.Errorf("legacy FTS object %q has unexpected type %q", tableName, objects[tableName].Type)
			}
			continue
		}
		if isLegacyEngineMetadata(tableName) {
			if objects[tableName].Type != "table" {
				return fmt.Errorf("legacy engine metadata %q has unexpected type %q", tableName, objects[tableName].Type)
			}
			continue
		}
		return fmt.Errorf("legacy snapshot contains unknown user table %q", tableName)
	}
	return validateSQLiteSequence(ctx, db, objects, "legacy snapshot")
}

// validateNativeObjects verifies that a pre-initialized target contains the expected tables and only known native auxiliaries.
// validateNativeObjects 验证预初始化目标包含所需表，并且只有已知原生辅助表。
func validateNativeObjects(objects map[string]sqliteTableObject) error {
	if err := validateRequiredTables(objects, "native target"); err != nil {
		return err
	}
	for tableName := range objects {
		if _, allowed := nativeCopyTableSet[tableName]; allowed {
			continue
		}
		if isSQLiteInternalTable(tableName) {
			continue
		}
		if isNativeAuxiliaryTable(tableName) {
			if objects[tableName].Type != "table" {
				return fmt.Errorf("native auxiliary object %q has unexpected type %q", tableName, objects[tableName].Type)
			}
			continue
		}
		return fmt.Errorf("native target contains unknown user table %q", tableName)
	}
	return nil
}

// validateRequiredTables confirms every audited table exists as a persistent table.
// validateRequiredTables 确认审计清单中的每张表都作为持久化表存在。
func validateRequiredTables(objects map[string]sqliteTableObject, databaseName string) error {
	for tableName := range nativeCopyTableSet {
		object, ok := objects[tableName]
		if !ok || object.Type != "table" {
			return fmt.Errorf("%s is missing required table %q", databaseName, tableName)
		}
	}
	return nil
}

// isSQLiteInternalTable identifies SQLite-owned objects that are not application data.
// isSQLiteInternalTable 识别不属于应用数据的 SQLite 自有对象。
func isSQLiteInternalTable(tableName string) bool {
	return strings.HasPrefix(tableName, "sqlite_")
}

// isLegacyFTSShadow recognizes only the old memory FTS virtual table and its standard five FTS5 shadows.
// isLegacyFTSShadow 仅识别旧记忆 FTS 虚拟表及标准五个 FTS5 shadow 表。
func isLegacyFTSShadow(tableName string) bool {
	_, ok := legacyFTSShadowNames[tableName]
	return ok
}

// isLegacyEngineMetadata recognizes the old Jieba dictionary table that native GSE must not import.
// isLegacyEngineMetadata 识别旧 Jieba 词典表，原生 GSE 不应导入该表。
func isLegacyEngineMetadata(tableName string) bool {
	_, ok := legacyEngineMetadataNames[tableName]
	return ok
}

// isNativeAuxiliaryTable recognizes the marker and durable native FTS bookkeeping tables already owned by the target.
// isNativeAuxiliaryTable 识别目标已经拥有的原生 marker 与 FTS 持久化辅助表。
func isNativeAuxiliaryTable(tableName string) bool {
	if tableName == "vmm_native_storage_marker" || tableName == "vmm_native_fts_metadata" || tableName == "vmm_native_fts_sync_queue" {
		return true
	}
	const generationPrefix = "vmm_native_fts_vmm_memory_nodes_fts_g"
	remainder, ok := strings.CutPrefix(tableName, generationPrefix)
	if !ok || remainder == "" {
		return false
	}
	digitCount := 0
	for digitCount < len(remainder) && remainder[digitCount] >= '0' && remainder[digitCount] <= '9' {
		digitCount++
	}
	if digitCount == 0 {
		return false
	}
	suffix := remainder[digitCount:]
	if suffix == "" {
		return true
	}
	_, isFTSShadow := legacyFTSShadowNames[legacyMemoryFTSName+suffix]
	return isFTSShadow
}

// validateSQLiteSequence rejects sequence state because the audited schema has no AUTOINCREMENT table to migrate.
// validateSQLiteSequence 拒绝 sequence 状态，因为当前审计 schema 没有需要迁移的 AUTOINCREMENT 表。
func validateSQLiteSequence(ctx context.Context, db *sql.DB, objects map[string]sqliteTableObject, databaseName string) error {
	if _, exists := objects["sqlite_sequence"]; !exists {
		return nil
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "sqlite_sequence"`).Scan(&count); err != nil {
		return fmt.Errorf("count %s sqlite_sequence: %w", databaseName, err)
	}
	if count != 0 {
		return fmt.Errorf("%s sqlite_sequence is non-empty (%d rows); migration requires an explicit sequence policy", databaseName, count)
	}
	return nil
}

// readTableColumns returns PRAGMA table_info in SQLite's declared column and primary-key order.
// readTableColumns 按 SQLite 声明列顺序和主键顺序返回 PRAGMA table_info 结果。
func readTableColumns(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tableName string) ([]sqliteTableColumn, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(tableName)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]sqliteTableColumn, 0)
	for rows.Next() {
		var column sqliteTableColumn
		if err := rows.Scan(&column.CID, &column.Name, &column.Type, &column.NotNull, &column.Default, &column.PrimaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %q has no columns", tableName)
	}
	return columns, nil
}

// validateTableSchemas compares source and target column declarations before any target mutation begins and returns source-order schemas for copying.
// validateTableSchemas 在修改目标之前比较源和目标的列声明，并返回供复制使用的源列顺序 schema。
func validateTableSchemas(ctx context.Context, source, target *sql.DB, sourceObjects, targetObjects map[string]sqliteTableObject) (map[string][]sqliteTableColumn, error) {
	sourceSchemas := make(map[string][]sqliteTableColumn, len(nativeCopyTableSet))
	for tableName := range nativeCopyTableSet {
		if _, ok := sourceObjects[tableName]; !ok || targetObjects[tableName].Type != "table" {
			return nil, fmt.Errorf("cannot compare schema for table %q", tableName)
		}
		sourceColumns, err := readTableColumns(ctx, source, tableName)
		if err != nil {
			return nil, fmt.Errorf("read legacy schema for %s: %w", tableName, err)
		}
		targetColumns, err := readTableColumns(ctx, target, tableName)
		if err != nil {
			return nil, fmt.Errorf("read native schema for %s: %w", tableName, err)
		}
		if err := compareTableColumns(tableName, sourceColumns, targetColumns); err != nil {
			return nil, err
		}
		sourceSchemas[tableName] = sourceColumns
	}
	return sourceSchemas, nil
}

// compareTableColumns enforces exact column membership, declarations, defaults, nullability, and primary-key positions while allowing historical ALTER TABLE order drift.
// compareTableColumns 强制校验列集合、声明类型、默认值、可空性和主键位置一致，同时允许历史 ALTER TABLE 导致的列顺序差异。
func compareTableColumns(tableName string, source, target []sqliteTableColumn) error {
	if len(source) != len(target) {
		return fmt.Errorf("schema mismatch for %s: source has %d columns, target has %d", tableName, len(source), len(target))
	}
	targetByName := make(map[string]sqliteTableColumn, len(target))
	for _, column := range target {
		if _, duplicate := targetByName[column.Name]; duplicate {
			return fmt.Errorf("schema mismatch for %s: duplicate target column %q", tableName, column.Name)
		}
		targetByName[column.Name] = column
	}
	for _, left := range source {
		right, ok := targetByName[left.Name]
		if !ok {
			return fmt.Errorf("schema mismatch for %s: target is missing column %q", tableName, left.Name)
		}
		if normalizeSQLiteType(left.Type) != normalizeSQLiteType(right.Type) || left.NotNull != right.NotNull || left.Default.Valid != right.Default.Valid || left.Default.String != right.Default.String || left.PrimaryKey != right.PrimaryKey {
			return fmt.Errorf("schema mismatch for %s column %q: source=%s target=%s", tableName, left.Name, describeSQLiteColumn(left), describeSQLiteColumn(right))
		}
	}
	primaryKeys := 0
	for _, column := range source {
		if column.PrimaryKey > 0 {
			primaryKeys++
		}
	}
	if primaryKeys == 0 {
		return fmt.Errorf("schema mismatch for %s: table has no primary key for stable copy ordering", tableName)
	}
	return nil
}

// normalizeSQLiteType compares declared SQLite types without treating harmless whitespace and case differences as schema changes.
// normalizeSQLiteType 比较 SQLite 声明类型时忽略无意义的空白和大小写差异。
func normalizeSQLiteType(typeName string) string {
	return strings.ToUpper(strings.Join(strings.Fields(typeName), " "))
}

// describeSQLiteColumn renders schema details into deterministic migration errors.
// describeSQLiteColumn 将 schema 细节渲染为确定性的迁移错误信息。
func describeSQLiteColumn(column sqliteTableColumn) string {
	defaultValue := "<null>"
	if column.Default.Valid {
		defaultValue = column.Default.String
	}
	return fmt.Sprintf("cid=%d name=%q type=%q notnull=%d default=%q pk=%d", column.CID, column.Name, normalizeSQLiteType(column.Type), column.NotNull, defaultValue, column.PrimaryKey)
}

// validateTargetBusinessTablesEmpty guarantees that this operation never overwrites pre-existing native facts.
// validateTargetBusinessTablesEmpty 确保本操作不会覆盖目标原生库已有事实。
func validateTargetBusinessTablesEmpty(ctx context.Context, target *sql.DB) error {
	for _, tableName := range nativeCopyBusinessTables {
		var count int64
		query := "SELECT COUNT(*) FROM " + quoteSQLiteIdentifier(tableName)
		if err := target.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return fmt.Errorf("count native target table %s: %w", tableName, err)
		}
		if count != 0 {
			return fmt.Errorf("native target table %s is not empty (%d rows)", tableName, count)
		}
	}
	return nil
}

// readRequiredSchemaVersion enforces the exact legacy SQLite schema version before copying any data.
// readRequiredSchemaVersion 在复制数据前强制要求旧库使用准确的 SQLite schema 版本。
func readRequiredSchemaVersion(ctx context.Context, source *sql.DB) (int, error) {
	var version int
	err := source.QueryRowContext(ctx, `SELECT schema_version FROM "vmm_schema_versions" WHERE component = 'sqlite' LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("legacy snapshot has no sqlite schema version row")
	}
	if err != nil {
		return 0, fmt.Errorf("read legacy sqlite schema version: %w", err)
	}
	if version != nativeSQLiteSchemaVersion {
		return 0, fmt.Errorf("legacy sqlite schema version %d is unsupported; required %d", version, nativeSQLiteSchemaVersion)
	}
	return version, nil
}

// copyTable streams one source table into the target transaction while hashing every original SQLite value.
// copyTable 在目标事务中流式复制一张源表，并对每个原始 SQLite 值计算摘要。
func copyTable(ctx context.Context, source *sql.DB, tx *sql.Tx, tableName string, columns []sqliteTableColumn) (NativeCopyTableReport, error) {
	if len(columns) == 0 {
		return NativeCopyTableReport{}, fmt.Errorf("table %s has no columns", tableName)
	}
	primaryKeys := primaryKeyColumns(columns)
	if len(primaryKeys) == 0 {
		return NativeCopyTableReport{}, fmt.Errorf("table %s has no primary key", tableName)
	}
	columnSQL := quotedColumnList(columns)
	selectSQL := "SELECT " + columnSQL + " FROM " + quoteSQLiteIdentifier(tableName) + " ORDER BY " + quotedPrimaryKeyList(primaryKeys)
	rows, err := source.QueryContext(ctx, selectSQL)
	if err != nil {
		return NativeCopyTableReport{}, fmt.Errorf("query source rows: %w", err)
	}
	defer rows.Close()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	insertSQL := "INSERT INTO " + quoteSQLiteIdentifier(tableName) + " (" + columnSQL + ") VALUES (" + placeholders + ")"
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return NativeCopyTableReport{}, fmt.Errorf("prepare target insert: %w", err)
	}
	defer stmt.Close()
	hasher := sha256.New()
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return NativeCopyTableReport{}, fmt.Errorf("scan source row: %w", err)
		}
		for index, value := range values {
			values[index], err = normalizeSQLiteValue(value)
			if err != nil {
				return NativeCopyTableReport{}, fmt.Errorf("normalize %s column %s: %w", tableName, columns[index].Name, err)
			}
		}
		if err := hashSQLiteRow(hasher, values); err != nil {
			return NativeCopyTableReport{}, fmt.Errorf("hash source row: %w", err)
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return NativeCopyTableReport{}, fmt.Errorf("insert target row: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return NativeCopyTableReport{}, fmt.Errorf("iterate source rows: %w", err)
	}
	return NativeCopyTableReport{Name: tableName, Rows: count, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

// verifyCopiedTables compares target counts and type-sensitive digests with source-derived reports before commit.
// verifyCopiedTables 在提交前比较目标行数和类型敏感摘要与源数据报告。
func verifyCopiedTables(ctx context.Context, tx *sql.Tx, reports []NativeCopyTableReport, sourceSchemas map[string][]sqliteTableColumn) error {
	for _, report := range reports {
		targetColumns, err := readTableColumns(ctx, tx, report.Name)
		if err != nil {
			return fmt.Errorf("read target schema while verifying %s: %w", report.Name, err)
		}
		if _, ok := sourceSchemas[report.Name]; !ok {
			return fmt.Errorf("source schema disappeared for %s", report.Name)
		}
		if err := compareTableColumns(report.Name, sourceSchemas[report.Name], targetColumns); err != nil {
			return err
		}
		actual, err := hashTable(ctx, tx, report.Name, sourceSchemas[report.Name])
		if err != nil {
			return fmt.Errorf("hash target table %s: %w", report.Name, err)
		}
		if actual.Rows != report.Rows || actual.SHA256 != report.SHA256 {
			return fmt.Errorf("copied table %s verification mismatch: source rows/hash=%d/%s target=%d/%s", report.Name, report.Rows, report.SHA256, actual.Rows, actual.SHA256)
		}
	}
	return nil
}

// hashTable computes the same stable primary-key-ordered digest used while streaming the source table.
// hashTable 使用与源流式复制相同的稳定主键排序摘要算法。
func hashTable(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, tableName string, columns []sqliteTableColumn) (NativeCopyTableReport, error) {
	primaryKeys := primaryKeyColumns(columns)
	if len(primaryKeys) == 0 {
		return NativeCopyTableReport{}, fmt.Errorf("table %s has no primary key", tableName)
	}
	query := "SELECT " + quotedColumnList(columns) + " FROM " + quoteSQLiteIdentifier(tableName) + " ORDER BY " + quotedPrimaryKeyList(primaryKeys)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return NativeCopyTableReport{}, err
	}
	defer rows.Close()
	hasher := sha256.New()
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return NativeCopyTableReport{}, err
		}
		for index, value := range values {
			values[index], err = normalizeSQLiteValue(value)
			if err != nil {
				return NativeCopyTableReport{}, fmt.Errorf("normalize target column %s: %w", columns[index].Name, err)
			}
		}
		if err := hashSQLiteRow(hasher, values); err != nil {
			return NativeCopyTableReport{}, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return NativeCopyTableReport{}, err
	}
	return NativeCopyTableReport{Name: tableName, Rows: count, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

// primaryKeyColumns returns primary-key columns in SQLite's declared composite-key order.
// primaryKeyColumns 按 SQLite 声明的复合主键顺序返回主键列。
func primaryKeyColumns(columns []sqliteTableColumn) []sqliteTableColumn {
	keys := make([]sqliteTableColumn, 0)
	for _, column := range columns {
		if column.PrimaryKey > 0 {
			keys = append(keys, column)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].PrimaryKey < keys[right].PrimaryKey
	})
	return keys
}

// quotedColumnList renders validated schema columns for SELECT and INSERT statements.
// quotedColumnList 将已验证 schema 列渲染为 SELECT 和 INSERT 语句片段。
func quotedColumnList(columns []sqliteTableColumn) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = quoteSQLiteIdentifier(column.Name)
	}
	return strings.Join(quoted, ", ")
}

// quotedPrimaryKeyList renders a deterministic ascending order over all primary-key columns.
// quotedPrimaryKeyList 将全部主键列渲染为确定性的升序排序片段。
func quotedPrimaryKeyList(columns []sqliteTableColumn) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = quoteSQLiteIdentifier(column.Name) + " ASC"
	}
	return strings.Join(quoted, ", ")
}

// quoteSQLiteIdentifier quotes an identifier that originated from the audited schema.
// quoteSQLiteIdentifier 为来自审计 schema 的标识符加引号。
func quoteSQLiteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// normalizeSQLiteValue keeps only the driver value kinds that preserve SQLite data without JSON coercion.
// normalizeSQLiteValue 只保留无需 JSON 强制转换即可保真 SQLite 数据的驱动值类型。
func normalizeSQLiteValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, int64, float64, string, bool:
		return typed, nil
	case []byte:
		copyValue := make([]byte, len(typed))
		copy(copyValue, typed)
		return copyValue, nil
	default:
		return nil, fmt.Errorf("unsupported SQLite driver value type %T", value)
	}
}

// hashSQLiteRow writes unambiguous type tags and lengths so NULL, text, blobs, integers, and floats cannot collide.
// hashSQLiteRow 写入无歧义的类型标签和长度，避免 NULL、文本、二进制、整数和浮点数发生摘要碰撞。
func hashSQLiteRow(hasher hash.Hash, values []any) error {
	writeHashByte(hasher, 0xff)
	writeHashUint64(hasher, uint64(len(values)))
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			writeHashByte(hasher, 0)
		case int64:
			writeHashByte(hasher, 1)
			writeHashUint64(hasher, uint64(typed))
		case float64:
			writeHashByte(hasher, 2)
			writeHashUint64(hasher, math.Float64bits(typed))
		case string:
			writeHashByte(hasher, 3)
			writeHashBytes(hasher, []byte(typed))
		case []byte:
			writeHashByte(hasher, 4)
			writeHashBytes(hasher, typed)
		case bool:
			writeHashByte(hasher, 5)
			if typed {
				writeHashByte(hasher, 1)
			} else {
				writeHashByte(hasher, 0)
			}
		default:
			return fmt.Errorf("unsupported SQLite hash value type %T", value)
		}
	}
	return nil
}

// writeHashByte appends one fixed-width hash byte.
// writeHashByte 向摘要追加一个定宽字节。
func writeHashByte(hasher hash.Hash, value byte) {
	_, _ = hasher.Write([]byte{value})
}

// writeHashUint64 appends one big-endian integer to the digest.
// writeHashUint64 向摘要追加一个大端整数。
func writeHashUint64(hasher hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = hasher.Write(encoded[:])
}

// writeHashBytes appends a length-delimited byte sequence to the digest.
// writeHashBytes 向摘要追加一个带长度的字节序列。
func writeHashBytes(hasher hash.Hash, value []byte) {
	writeHashUint64(hasher, uint64(len(value)))
	_, _ = hasher.Write(value)
}

// checkTargetIntegrity runs foreign-key and quick checks inside the copy transaction before commit.
// checkTargetIntegrity 在复制事务内提交前执行外键检查和 quick_check。
func checkTargetIntegrity(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run native target foreign_key_check: %w", err)
	}
	if rows.Next() {
		values := make([]any, 4)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if scanErr := rows.Scan(destinations...); scanErr != nil {
			_ = rows.Close()
			return fmt.Errorf("scan native target foreign_key_check: %w", scanErr)
		}
		_ = rows.Close()
		return fmt.Errorf("native target foreign_key_check reported table=%v row=%v parent=%v fk=%v", values[0], values[1], values[2], values[3])
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate native target foreign_key_check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close native target foreign_key_check: %w", err)
	}
	var quickCheck string
	if err := tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil {
		return fmt.Errorf("run native target quick_check: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(quickCheck), "ok") {
		return fmt.Errorf("native target quick_check returned %q", quickCheck)
	}
	return nil
}
