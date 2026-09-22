// store.go implements the LanceDB local-FFI outbound adapter used by vector recall and maintenance flows.
// store.go 用于实现基于 LanceDB 本地 FFI 的出站适配器，承接向量召回与维护流程。
package vldb_lancedb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"
	"github.com/openvulcan/vmm/internal/platform/storagecontract/lance"
)

const (
	// CurrentSchemaVersion tracks the latest LanceDB table layout expected by this runtime.
	// CurrentSchemaVersion 用于标记当前运行时期望的最新 LanceDB 表结构版本。
	CurrentSchemaVersion = 3
)

// Store is the LanceDB local-FFI adapter used by the vector store port.
// Store 用于作为向量存储端口的 LanceDB 本地 FFI 适配器。
type Store struct {
	mu           sync.RWMutex
	lib          interface{ Close() error }
	runtime      interface{ Close() error }
	engine       any
	timeout      time.Duration
	tableName    string
	vectorColumn string
	dimension    int
}

// lancedbEngineHandle narrows the LanceDB FFI engine surface that the adapter depends on so unit tests can replace the engine with one focused in-memory fake.
// lancedbEngineHandle 用于收窄适配器依赖的 LanceDB FFI engine 能力面，这样单测可以把 engine 替换成聚焦的内存 fake。
type lancedbEngineHandle interface {
	lance.Engine
}

// legacyLanceDBEngine preserves the pre-context FFI surface while callers migrate to the neutral contract.
// legacyLanceDBEngine 在调用方迁移到中立契约期间保留旧版无 context 的 FFI 接口。
type legacyLanceDBEngine interface {
	CreateTable(request lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error)
	VectorUpsertRaw(tableName string, format lancedbffi.InputFormat, data []byte, keyColumns []string) (lancedbffi.UpsertResult, error)
	VectorSearchF32(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error)
	Delete(request lancedbffi.DeleteRequest) (lancedbffi.DeleteResult, error)
	DropTable(request lancedbffi.DropTableRequest) (lancedbffi.DropTableResult, error)
	Close() error
}

// legacyEngineAdapter adds the business context parameter around the old dynamic FFI implementation.
// legacyEngineAdapter 为旧动态 FFI 实现补充业务 context 参数。
type legacyEngineAdapter struct {
	legacy legacyLanceDBEngine
}

// CreateTable forwards a legacy create-table call after checking the caller context.
// CreateTable 在检查调用方 context 后转发旧版建表调用。
func (a legacyEngineAdapter) CreateTable(ctx context.Context, request lance.CreateTableRequest) (lance.CreateTableResult, error) {
	if err := checkContext(ctx); err != nil {
		return lance.CreateTableResult{}, err
	}
	legacyRequest := lancedbffi.CreateTableRequest{
		TableName:         request.TableName,
		Columns:           make([]lancedbffi.CreateTableColumn, 0, len(request.Columns)),
		OverwriteIfExists: request.OverwriteIfExists,
	}
	for _, column := range request.Columns {
		legacyRequest.Columns = append(legacyRequest.Columns, lancedbffi.CreateTableColumn{
			Name: column.Name, ColumnType: column.ColumnType, VectorDim: column.VectorDim, Nullable: column.Nullable,
		})
	}
	result, err := a.legacy.CreateTable(legacyRequest)
	return lance.CreateTableResult{Success: result.Success, Message: result.Message}, err
}

// VectorUpsertRaw forwards a legacy upsert call after checking the caller context.
// VectorUpsertRaw 在检查调用方 context 后转发旧版 upsert 调用。
func (a legacyEngineAdapter) VectorUpsertRaw(ctx context.Context, tableName string, format lance.InputFormat, data []byte, keyColumns []string) (lance.UpsertResult, error) {
	if err := checkContext(ctx); err != nil {
		return lance.UpsertResult{}, err
	}
	result, err := a.legacy.VectorUpsertRaw(tableName, lancedbffi.InputFormat(format), data, keyColumns)
	return lance.UpsertResult{Version: result.Version, InputRows: result.InputRows, InsertedRows: result.InsertedRows, UpdatedRows: result.UpdatedRows, DeletedRows: result.DeletedRows}, err
}

// VectorSearchF32 forwards a legacy search call after checking the caller context.
// VectorSearchF32 在检查调用方 context 后转发旧版向量检索调用。
func (a legacyEngineAdapter) VectorSearchF32(ctx context.Context, tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lance.OutputFormat) (lance.SearchResult, error) {
	if err := checkContext(ctx); err != nil {
		return lance.SearchResult{}, err
	}
	result, err := a.legacy.VectorSearchF32(tableName, vector, limit, filter, vectorColumn, lancedbffi.OutputFormat(outputFormat))
	return lance.SearchResult{Format: lance.OutputFormat(result.Format), Rows: result.Rows, Data: append([]byte(nil), result.Data...)}, err
}

// Delete forwards a legacy predicate deletion after checking the caller context.
// Delete 在检查调用方 context 后转发旧版谓词删除调用。
func (a legacyEngineAdapter) Delete(ctx context.Context, request lance.DeleteRequest) (lance.DeleteResult, error) {
	if err := checkContext(ctx); err != nil {
		return lance.DeleteResult{}, err
	}
	result, err := a.legacy.Delete(lancedbffi.DeleteRequest{TableName: request.TableName, Condition: request.Condition})
	return lance.DeleteResult{Success: result.Success, Message: result.Message, Version: result.Version, DeletedRows: result.DeletedRows}, err
}

// DropTable forwards an explicit legacy table drop after checking the caller context.
// DropTable 在检查调用方 context 后转发显式旧版删表调用。
func (a legacyEngineAdapter) DropTable(ctx context.Context, request lance.DropTableRequest) (lance.DropTableResult, error) {
	if err := checkContext(ctx); err != nil {
		return lance.DropTableResult{}, err
	}
	result, err := a.legacy.DropTable(lancedbffi.DropTableRequest{TableName: request.TableName})
	return lance.DropTableResult{Success: result.Success, Message: result.Message}, err
}

// Close releases the legacy engine handle.
// Close 释放旧版引擎句柄。
func (a legacyEngineAdapter) Close() error {
	return a.legacy.Close()
}

// engineForContext returns the neutral engine contract for one store operation.
// engineForContext 为一次存储操作返回中立引擎契约。
func (s *Store) engineForContext(ctx context.Context) (lance.Engine, func(), error) {
	if s == nil {
		return nil, func() {}, fmt.Errorf("lancedb store is not initialized")
	}
	s.mu.RLock()
	release := s.mu.RUnlock
	if s.engine == nil {
		release()
		return nil, func() {}, fmt.Errorf("lancedb store is not initialized")
	}
	if engine, ok := s.engine.(lance.Engine); ok {
		if err := checkContext(ctx); err != nil {
			release()
			return nil, func() {}, err
		}
		return engine, release, nil
	}
	if legacy, ok := s.engine.(legacyLanceDBEngine); ok {
		return legacyEngineAdapter{legacy: legacy}, release, nil
	}
	release()
	return nil, func() {}, fmt.Errorf("unsupported lancedb engine implementation %T", s.engine)
}

// NewStore opens the packaged LanceDB dynamic library and eagerly ensures the configured vector table exists.
// NewStore 用于打开打包后的 LanceDB 动态库，并在启动阶段主动确保目标向量表存在。
func NewStore(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newStore(libraryPath, databaseDir, timeout, tableName, vectorColumn, dimension, true)
}

// NewStoreWithoutInit opens the packaged LanceDB dynamic library without eagerly creating the configured table.
// NewStoreWithoutInit 用于打开打包后的 LanceDB 动态库，但不会提前创建目标表。
func NewStoreWithoutInit(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newStore(libraryPath, databaseDir, timeout, tableName, vectorColumn, dimension, false)
}

// newStore centralizes local LanceDB FFI bootstrapping and lets callers decide whether table initialization should happen eagerly.
// newStore 用于集中处理本地 LanceDB FFI 启动逻辑，并允许调用方决定是否立即初始化目标表。
func newStore(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int, ensureTable bool) (*Store, error) {
	if strings.TrimSpace(libraryPath) == "" {
		return nil, fmt.Errorf("lancedb library path is required")
	}
	if strings.TrimSpace(databaseDir) == "" {
		return nil, fmt.Errorf("lancedb database dir is required")
	}
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("lancedb table_name is required")
	}
	if strings.TrimSpace(vectorColumn) == "" {
		return nil, fmt.Errorf("lancedb vector_column is required")
	}
	if dimension <= 0 {
		return nil, fmt.Errorf("lancedb dimension must be > 0")
	}
	if err := os.MkdirAll(databaseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create lancedb database dir: %w", err)
	}

	lib, err := lancedbffi.Open(strings.TrimSpace(libraryPath))
	if err != nil {
		return nil, err
	}
	runtimeOptions := lib.DefaultRuntimeOptions()
	runtimeOptions.DefaultDBPath = strings.TrimSpace(databaseDir)
	runtimeHandle, err := lib.CreateRuntime(runtimeOptions)
	if err != nil {
		_ = lib.Close()
		return nil, fmt.Errorf("create lancedb runtime: %w", err)
	}
	engine, err := runtimeHandle.OpenDefaultEngine()
	if err != nil {
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, fmt.Errorf("open lancedb default engine: %w", err)
	}

	store := &Store{
		lib:          lib,
		runtime:      runtimeHandle,
		engine:       engine,
		timeout:      timeout,
		tableName:    resolveVectorTableName(tableName, dimension),
		vectorColumn: strings.TrimSpace(vectorColumn),
		dimension:    dimension,
	}
	if ensureTable {
		if err := store.init(context.Background()); err != nil {
			_ = store.Shutdown(context.Background())
			return nil, err
		}
	}
	return store, nil
}

// Upsert writes one memory record into the LanceDB-backed vector table using JSON row ingestion.
// Upsert 用于通过 JSON 行写入方式把一条记忆记录保存到 LanceDB 向量表。
func (s *Store) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("encode memory metadata: %w", err)
	}
	rows := []map[string]any{
		{
			"id":                record.ID,
			"content":           record.Text,
			"team_id":           record.Filter.TeamID,
			"space_id":          record.Filter.SpaceID,
			"project_id":        record.Filter.ProjectID,
			"session_id":        record.Filter.SessionID,
			"user_id":           record.Filter.UserID,
			"source_turn_id":    record.SourceTurnID,
			"memory_status":     normalizedMemoryRecordStatus(record),
			"expires_timestamp": memoryRecordExpiresTimestamp(record),
			"metadata_json":     string(metadataJSON),
			"created_at":        record.CreatedAt.UTC().Format(time.RFC3339Nano),
			s.vectorColumn:      record.Vector,
		},
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal lancedb upsert payload: %w", err)
	}
	if _, err := engine.VectorUpsertRaw(ctx, s.tableName, lance.InputFormatJSONRows, payload, []string{"id"}); err != nil {
		return fmt.Errorf("lancedb vector upsert: %w", err)
	}
	return nil
}

// DeleteByFilter removes all vector rows that match one flattened hierarchy filter.
// DeleteByFilter 用于删除符合某个扁平层级过滤条件的全部向量行。
func (s *Store) DeleteByFilter(ctx context.Context, filter logicdomain.SearchFilter) (uint64, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	condition := buildDeleteCondition(filter)
	if strings.TrimSpace(condition) == "" {
		return 0, fmt.Errorf("lancedb delete filter is empty")
	}
	result, err := engine.Delete(ctx, lance.DeleteRequest{
		TableName: s.tableName,
		Condition: condition,
	})
	if err != nil {
		return 0, fmt.Errorf("lancedb delete: %w", err)
	}
	if !result.Success {
		return 0, fmt.Errorf("lancedb delete: %s", strings.TrimSpace(result.Message))
	}
	return result.DeletedRows, nil
}

// DeleteByIDs removes the specified vector rows precisely by their ids.
// DeleteByIDs 用于按 id 精确删除指定向量行。
func (s *Store) DeleteByIDs(ctx context.Context, ids []string) (uint64, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	condition := buildDeleteIDsCondition(ids)
	if strings.TrimSpace(condition) == "" {
		return 0, nil
	}
	result, err := engine.Delete(ctx, lance.DeleteRequest{
		TableName: s.tableName,
		Condition: condition,
	})
	if err != nil {
		return 0, fmt.Errorf("lancedb delete by ids: %w", err)
	}
	if !result.Success {
		return 0, fmt.Errorf("lancedb delete by ids: %s", strings.TrimSpace(result.Message))
	}
	return result.DeletedRows, nil
}

// Search runs one vector search against the configured table and maps the returned JSON rows back into MemoryHit values.
// Search 用于对配置好的表执行一次向量检索，并把返回的 JSON 行映射回 MemoryHit 结构。
func (s *Store) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if topK <= 0 {
		topK = 10
	}
	filterExpr := buildFilterExpr(filter)
	rows := make([]searchRow, 0)
	result, err := engine.VectorSearchF32(
		ctx,
		s.tableName,
		vector,
		uint32(topK),
		filterExpr,
		s.vectorColumn,
		lance.OutputFormatJSONRows,
	)
	if err != nil {
		return nil, fmt.Errorf("lancedb vector search: %w", err)
	}
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &rows); err != nil {
			return nil, fmt.Errorf("decode lancedb search rows: %w", err)
		}
	}

	hits := make([]logicdomain.MemoryHit, 0, len(rows))
	for _, row := range rows {
		metadata := map[string]string{}
		if strings.TrimSpace(row.MetadataJSON) != "" {
			if err := json.Unmarshal([]byte(row.MetadataJSON), &metadata); err != nil {
				// Keep the hit usable even when metadata decoding fails.
				// 即使元数据解码失败，也保留命中结果可用性。
			}
		}
		// Records with absent metadata are stored as JSON null; decoding null clears the map and must precede origin annotation.
		// 缺省元数据会保存为 JSON null；解码 null 会清空映射，写入来源标记前需还原为空映射。
		if metadata == nil {
			metadata = map[string]string{}
		}
		// Stamp the search-origin at the adapter boundary because every LanceDB hit is produced by the split vector-search path.
		// 在适配器边界写入检索来源，因为每条 LanceDB 命中都来自分离式向量检索路径。
		metadata["origin"] = "vector_search"
		distance := row.Distance
		if distance == 0 {
			distance = row.Score
		}
		hits = append(hits, logicdomain.MemoryHit{
			ID:    row.ID,
			Text:  row.Content,
			Score: distanceToScore(distance),
			Filter: logicdomain.SearchFilter{
				TeamID:              row.TeamID,
				SpaceID:             row.SpaceID,
				ProjectID:           row.ProjectID,
				SessionID:           row.SessionID,
				UserID:              row.UserID,
				BoundarySessionID:   filter.BoundarySessionID,
				BoundaryMaxTurnID:   filter.BoundaryMaxTurnID,
				ExcludeBoundaryTurn: filter.ExcludeBoundaryTurn,
			},
			Metadata: metadata,
		})
	}
	return hits, nil
}

// Count returns the physical row count from a health-capable LanceDB engine.
// Count 返回支持健康检查的 LanceDB 引擎中的物理行数。
func (s *Store) Count(ctx context.Context) (int64, error) {
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	health, ok := engine.(lance.HealthEngine)
	if !ok {
		return 0, fmt.Errorf("lancedb engine does not expose row counting")
	}
	count, err := health.CountRows(ctx, s.tableName, "")
	if err != nil {
		return 0, fmt.Errorf("count lancedb rows: %w", err)
	}
	if count > uint64(1<<63-1) {
		return 0, fmt.Errorf("lancedb row count %d exceeds int64", count)
	}
	return int64(count), nil
}

// CheckHealth verifies the connection, required table, and exact configured schema without creating or replacing anything.
// CheckHealth 校验连接、目标表与精确配置 Schema，不创建也不替换任何对象。
func (s *Store) CheckHealth(ctx context.Context) error {
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	health, ok := engine.(lance.HealthEngine)
	if !ok {
		return fmt.Errorf("lancedb engine does not expose health checks")
	}
	if err := health.CheckHealth(ctx); err != nil {
		return err
	}
	payload, err := health.Schema(ctx, s.tableName)
	if err != nil {
		return fmt.Errorf("read lancedb schema: %w", err)
	}
	var actual struct {
		Columns []lance.CreateTableColumn `json:"columns"`
	}
	if err := json.Unmarshal(payload, &actual); err != nil {
		return fmt.Errorf("decode lancedb schema: %w", err)
	}
	expected := requiredVectorColumns(s.vectorColumn, s.dimension)
	if !reflect.DeepEqual(actual.Columns, expected) {
		return fmt.Errorf("lancedb table %s schema mismatch", s.tableName)
	}
	return nil
}

// Shutdown releases the underlying engine, runtime, and dynamic library handles.
// Shutdown 用于释放底层引擎、运行时与动态库句柄。
func (s *Store) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		engine, ok := s.engine.(interface{ Close() error })
		if !ok {
			return fmt.Errorf("lancedb engine does not support shutdown")
		}
		if err := engine.Close(); err != nil {
			return err
		}
		s.engine = nil
	}
	if s.runtime != nil {
		if err := s.runtime.Close(); err != nil {
			return err
		}
		s.runtime = nil
	}
	if s.lib != nil {
		if err := s.lib.Close(); err != nil {
			return err
		}
		s.lib = nil
	}
	return nil
}

// RecreateTable drops and recreates the current-dimension table used by vector rebuild maintenance flows.
// RecreateTable 用于删除并重建当前维度表，服务向量重建维护流程。
func (s *Store) RecreateTable(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return err
	}
	result, err := engine.DropTable(ctx, lance.DropTableRequest{TableName: s.tableName})
	release()
	if err != nil && !isLanceTableNotFoundMessage(err.Error()) {
		return fmt.Errorf("drop lancedb table %s: %w", s.tableName, err)
	}
	if err == nil && !result.Success && !isLanceTableNotFoundMessage(result.Message) {
		return fmt.Errorf("drop lancedb table %s: %s", s.tableName, strings.TrimSpace(result.Message))
	}

	// Only a confirmed successful drop means this call has already removed the live sidecar table; missing-table responses leave later create failures as ordinary bootstrap errors.
	// 只有明确成功的删表结果才表示本次调用已经移除了线上 sidecar 表；表原本不存在时，后续建表失败仍属于普通启动建表错误。
	droppedExistingTable := err == nil && result.Success
	if err := s.initWithOverwrite(ctx, true); err != nil {
		if droppedExistingTable {
			return lanceTableRecreateOutcomeUncertainError(s.tableName, err)
		}
		return err
	}
	return nil
}

// lanceTableRecreateOutcomeUncertainError reports that a destructive LanceDB recreate crossed the drop boundary before table creation failed.
// lanceTableRecreateOutcomeUncertainError 用于报告 LanceDB 破坏性重建已经越过删表边界，但后续建表失败。
func lanceTableRecreateOutcomeUncertainError(tableName string, err error) error {
	if err == nil {
		return nil
	}
	return logicdomain.OutcomeUncertainError{
		Operation: "recreate lancedb table",
		Message:   fmt.Sprintf("create lancedb table %s after drop: %v", tableName, err),
	}
}

// init ensures the configured vector table exists with the expected schema.
// init 用于确保目标向量表按照预期 schema 存在。
func (s *Store) init(ctx context.Context) error {
	return s.initWithOverwrite(ctx, false)
}

// initWithOverwrite ensures the configured schema and marks destructive replacement as explicit maintenance.
// initWithOverwrite 确保配置 Schema，并把破坏性替换限定在显式维护调用。
func (s *Store) initWithOverwrite(ctx context.Context, overwrite bool) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	result, err := engine.CreateTable(ctx, lance.CreateTableRequest{
		TableName:         s.tableName,
		Columns:           requiredVectorColumns(s.vectorColumn, s.dimension),
		OverwriteIfExists: overwrite,
	})
	if err != nil {
		if isTableAlreadyExistsError(err) {
			return nil
		}
		return fmt.Errorf("ensure lancedb table %s: %w", s.tableName, err)
	}
	if !result.Success && !isTableAlreadyExistsMessage(result.Message) {
		return fmt.Errorf("ensure lancedb table %s: %s", s.tableName, strings.TrimSpace(result.Message))
	}
	return nil
}

// requiredVectorColumns returns the stable physical schema used by both bootstrap and health checks.
// requiredVectorColumns 返回启动与健康检查共同使用的稳定物理 Schema。
func requiredVectorColumns(vectorColumn string, dimension int) []lance.CreateTableColumn {
	return []lance.CreateTableColumn{
		{Name: "id", ColumnType: "string", Nullable: false},
		{Name: "content", ColumnType: "string", Nullable: false},
		{Name: "team_id", ColumnType: "int64", Nullable: false},
		{Name: "space_id", ColumnType: "int64", Nullable: false},
		{Name: "project_id", ColumnType: "int64", Nullable: false},
		{Name: "session_id", ColumnType: "int64", Nullable: false},
		{Name: "user_id", ColumnType: "int64", Nullable: false},
		{Name: "source_turn_id", ColumnType: "int64", Nullable: false},
		{Name: "memory_status", ColumnType: "int64", Nullable: false},
		{Name: "expires_timestamp", ColumnType: "int64", Nullable: false},
		{Name: "metadata_json", ColumnType: "string", Nullable: false},
		{Name: "created_at", ColumnType: "string", Nullable: false},
		{Name: vectorColumn, ColumnType: "vector_float32", VectorDim: uint32(dimension), Nullable: false},
	}
}

// isTableAlreadyExistsError detects create-table errors that simply mean the target table already exists.
// isTableAlreadyExistsError 用于识别“目标表已存在”这类可接受的建表错误。
func isTableAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	return isTableAlreadyExistsMessage(err.Error())
}

// isTableAlreadyExistsMessage normalizes the existing-table message detection used by the LanceDB FFI adapter.
// isTableAlreadyExistsMessage 用于统一检测 LanceDB FFI 适配器中的“表已存在”消息。
func isTableAlreadyExistsMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(normalized, "already exists") || strings.Contains(normalized, "table exists")
}

// isLanceTableNotFoundMessage detects drop-table responses that mean the table is already absent.
// isLanceTableNotFoundMessage 用于识别表示 LanceDB 目标表已经不存在的删表响应。
func isLanceTableNotFoundMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(normalized, "not found") || strings.Contains(normalized, "does not exist")
}

// buildFilterExpr renders one LanceDB-compatible filter expression from the hierarchical search filter.
// buildFilterExpr 用于根据层级检索过滤条件渲染一条兼容 LanceDB 的过滤表达式。
func buildFilterExpr(filter logicdomain.SearchFilter) string {
	nowMs := time.Now().UTC().UnixMilli()
	conditions := []string{
		fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive),
		fmt.Sprintf("(expires_timestamp <= 0 OR expires_timestamp > %d)", nowMs),
	}
	if filter.TeamID > 0 {
		conditions = append(conditions, fmt.Sprintf("team_id = %d", filter.TeamID))
	}
	if filter.SpaceID > 0 {
		conditions = append(conditions, fmt.Sprintf("space_id = %d", filter.SpaceID))
	}
	if filter.ProjectID > 0 {
		conditions = append(conditions, fmt.Sprintf("project_id = %d", filter.ProjectID))
	}
	if filter.UserID > 0 {
		conditions = append(conditions, fmt.Sprintf("(user_id = 0 OR user_id = %d)", filter.UserID))
	}
	if filter.SessionID > 0 {
		conditions = append(conditions, fmt.Sprintf("session_id = %d", filter.SessionID))
	}
	if filter.BoundarySessionID > 0 {
		if filter.ExcludeBoundaryTurn {
			conditions = append(conditions, fmt.Sprintf("(session_id != %d OR source_turn_id = 0)", filter.BoundarySessionID))
		} else {
			conditions = append(conditions, fmt.Sprintf("(session_id != %d OR source_turn_id = 0 OR source_turn_id <= %d)", filter.BoundarySessionID, filter.BoundaryMaxTurnID))
		}
	}
	return strings.Join(conditions, " AND ")
}

// buildDeleteCondition renders one delete filter that targets one flattened hierarchy scope.
// buildDeleteCondition 用于渲染一条删除过滤条件，指向一个扁平层级范围。
func buildDeleteCondition(filter logicdomain.SearchFilter) string {
	conditions := make([]string, 0, 5)
	if filter.TeamID > 0 {
		conditions = append(conditions, fmt.Sprintf("team_id = %d", filter.TeamID))
	}
	if filter.SpaceID > 0 {
		conditions = append(conditions, fmt.Sprintf("space_id = %d", filter.SpaceID))
	}
	if filter.ProjectID > 0 {
		conditions = append(conditions, fmt.Sprintf("project_id = %d", filter.ProjectID))
	}
	if filter.UserID > 0 {
		conditions = append(conditions, fmt.Sprintf("user_id = %d", filter.UserID))
	}
	if filter.SessionID > 0 {
		conditions = append(conditions, fmt.Sprintf("session_id = %d", filter.SessionID))
	}
	return strings.Join(conditions, " AND ")
}

// buildDeleteIDsCondition renders a precise delete condition for one id set.
// buildDeleteIDsCondition 用于为一组 id 渲染精确删除条件。
func buildDeleteIDsCondition(ids []string) string {
	normalized := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		cleaned := strings.TrimSpace(id)
		if cleaned == "" {
			continue
		}
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		normalized = append(normalized, quoteLanceString(cleaned))
	}
	if len(normalized) == 0 {
		return ""
	}
	parts := make([]string, 0, len(normalized))
	for _, id := range normalized {
		parts = append(parts, fmt.Sprintf("id = %s", id))
	}
	return strings.Join(parts, " OR ")
}

// normalizedMemoryRecordStatus returns the sidecar lifecycle status for one memory vector while treating zero-valued legacy records as active.
// normalizedMemoryRecordStatus 用于返回一条记忆向量的旁路生命周期状态，并把零值旧记录视为 active。
func normalizedMemoryRecordStatus(record logicdomain.MemoryRecord) int {
	if logicdomain.ValidMemoryStatus(record.Status) {
		return record.Status
	}
	return logicdomain.MemoryStatusActive
}

// memoryRecordExpiresTimestamp converts one optional durable expiry time into the millisecond value stored in the LanceDB sidecar.
// memoryRecordExpiresTimestamp 用于把可选的长期过期时间转换成 LanceDB 旁路表保存的毫秒值。
func memoryRecordExpiresTimestamp(record logicdomain.MemoryRecord) int64 {
	if record.ExpiresAt.IsZero() {
		return 0
	}
	return record.ExpiresAt.UTC().UnixMilli()
}

// quoteLanceString escapes one string literal for LanceDB filter expressions.
// quoteLanceString 用于为 LanceDB 过滤表达式转义一个字符串字面量。
func quoteLanceString(raw string) string {
	return "'" + strings.ReplaceAll(raw, "'", "''") + "'"
}

// distanceToScore converts the lower-is-better distance metric into the higher-is-better score expected upstream.
// distanceToScore 用于把“越小越好”的距离值转换成上游期望的“越大越好”分数。
func distanceToScore(distance float64) float64 {
	if distance <= 0 {
		return 1
	}
	return 1 / (1 + distance)
}

// resolveVectorTableName appends the embedding dimension suffix to the configured base table name.
// resolveVectorTableName 用于在配置的基础表名后追加 embedding 维度后缀。
func resolveVectorTableName(baseName string, dimension int) string {
	return fmt.Sprintf("%s_%d", strings.TrimSpace(baseName), dimension)
}

// checkContext returns the current context error when the caller has already cancelled the operation.
// checkContext 用于在调用方已取消操作时返回当前 context 错误。
func checkContext(ctx context.Context) error {
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

// searchRow mirrors one JSON rows search hit returned by LanceDB.
// searchRow 用于映射 LanceDB 返回的一条 JSON rows 检索命中。
type searchRow struct {
	ID           string    `json:"id"`
	Content      string    `json:"content"`
	TeamID       uint64    `json:"team_id"`
	SpaceID      uint64    `json:"space_id"`
	ProjectID    uint64    `json:"project_id"`
	SessionID    uint64    `json:"session_id"`
	UserID       uint64    `json:"user_id"`
	SourceTurnID uint64    `json:"source_turn_id"`
	MetadataJSON string    `json:"metadata_json"`
	CreatedAt    time.Time `json:"created_at"`
	Distance     float64   `json:"_distance"`
	Score        float64   `json:"score"`
}

// UnmarshalJSON normalizes numeric/string mixed LanceDB fields into one stable searchRow shape.
// UnmarshalJSON 用于把 LanceDB 数值/字符串混合字段归一化为稳定的 searchRow 结构。
func (r *searchRow) UnmarshalJSON(data []byte) error {
	var raw rawSearchRow
	// Preserve integer tokens because scope identifiers can exceed the exact range of IEEE-754 float64.
	// 保留整数字面量，因为范围标识可能超出 IEEE-754 float64 的精确表示范围。
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	teamID, err := asUint64(raw.TeamID)
	if err != nil {
		return fmt.Errorf("decode team_id: %w", err)
	}
	spaceID, err := asUint64(raw.SpaceID)
	if err != nil {
		return fmt.Errorf("decode space_id: %w", err)
	}
	projectID, err := asUint64(raw.ProjectID)
	if err != nil {
		return fmt.Errorf("decode project_id: %w", err)
	}
	sessionID, err := asUint64(raw.SessionID)
	if err != nil {
		return fmt.Errorf("decode session_id: %w", err)
	}
	userID, err := asUint64(raw.UserID)
	if err != nil {
		return fmt.Errorf("decode user_id: %w", err)
	}
	sourceTurnID, err := asUint64(raw.SourceTurnID)
	if err != nil {
		return fmt.Errorf("decode source_turn_id: %w", err)
	}
	distance, err := asFloat64(raw.Distance)
	if err != nil {
		return fmt.Errorf("decode _distance: %w", err)
	}
	score, err := asFloat64(raw.Score)
	if err != nil {
		return fmt.Errorf("decode score: %w", err)
	}
	createdAt := time.Time{}
	if raw.CreatedAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(asString(raw.CreatedAt)))
		if err != nil {
			return fmt.Errorf("parse lancedb created_at: %w", err)
		}
		createdAt = parsed
	}
	*r = searchRow{
		ID:           strings.TrimSpace(raw.ID),
		Content:      strings.TrimSpace(raw.Content),
		TeamID:       teamID,
		SpaceID:      spaceID,
		ProjectID:    projectID,
		SessionID:    sessionID,
		UserID:       userID,
		SourceTurnID: sourceTurnID,
		MetadataJSON: strings.TrimSpace(raw.MetadataJSON),
		CreatedAt:    createdAt,
		Distance:     distance,
		Score:        score,
	}
	return nil
}

// rawSearchRow stores the heterogeneous JSON field types returned by LanceDB JSON rows search.
// rawSearchRow 用于保存 LanceDB JSON rows 检索返回的异构字段类型。
type rawSearchRow struct {
	ID           string `json:"id"`
	Content      string `json:"content"`
	TeamID       any    `json:"team_id"`
	SpaceID      any    `json:"space_id"`
	ProjectID    any    `json:"project_id"`
	SessionID    any    `json:"session_id"`
	UserID       any    `json:"user_id"`
	SourceTurnID any    `json:"source_turn_id"`
	MetadataJSON string `json:"metadata_json"`
	CreatedAt    any    `json:"created_at"`
	Distance     any    `json:"_distance"`
	Score        any    `json:"score"`
}

// asUint64 converts one JSON scalar into uint64 while preserving clear error messages for malformed LanceDB rows.
// asUint64 用于把一个 JSON 标量转换成 uint64，并在 LanceDB 行数据格式异常时保留清晰错误信息。
func asUint64(value any) (uint64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		if typed < 0 || typed >= 1<<53 || math.Trunc(typed) != typed || math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("invalid numeric uint64 value: %v", typed)
		}
		return uint64(typed), nil
	case json.Number:
		return strconv.ParseUint(typed.String(), 10, 64)
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported uint64 value type %T", value)
	}
}

// asString converts one optional JSON scalar into string.
// asString 用于把一个可选 JSON 标量转换为字符串。
func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

// asFloat64 converts one JSON scalar into float64.
// asFloat64 用于把一个 JSON 标量转换成 float64。
func asFloat64(value any) (float64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return typed, nil
	case json.Number:
		return typed.Float64()
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, nil
		}
		return strconv.ParseFloat(strings.TrimSpace(typed), 64)
	default:
		return 0, fmt.Errorf("unsupported float64 value type %T", value)
	}
}
