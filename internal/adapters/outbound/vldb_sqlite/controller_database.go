// controller_database.go adapts vldb-controller SQLite RPCs to the narrow database handle already used by the VMM relational store.
// controller_database.go 用于把 vldb-controller SQLite RPC 适配到 VMM 关系存储已经使用的窄数据库句柄接口。
package vldb_sqlite

import (
	"fmt"
	"strings"
	"time"

	controllerclient "github.com/OpenVulcan/vldb-controller/client-go/controller"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_controller"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

// controllerDatabaseHandle forwards the existing SQLite adapter handle contract through one shared controller runtime.
// controllerDatabaseHandle 通过一个共享 controller 运行时透传现有 SQLite 适配器句柄契约。
type controllerDatabaseHandle struct {
	// runtime owns the controller session and SQLite backend binding.
	// runtime 持有 controller 会话与 SQLite 后端绑定。
	runtime *vldb_controller.Runtime
}

// NewControllerStore builds the existing relational store on top of a controller-owned SQLite binding.
// NewControllerStore 在 controller 持有的 SQLite 绑定之上构建现有关系存储。
func NewControllerStore(runtime *vldb_controller.Runtime, timeout time.Duration, options ...StoreOptions) (*Store, error) {
	if runtime == nil || runtime.Client() == nil {
		return nil, fmt.Errorf("controller runtime is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	storeOptions := StoreOptions{TokenizerMode: "jieba"}
	if len(options) > 0 {
		storeOptions = options[0]
	}
	tokenizerMode, err := parseSQLiteTokenizerMode(storeOptions.TokenizerMode)
	if err != nil {
		return nil, err
	}
	store := &Store{
		database:      &controllerDatabaseHandle{runtime: runtime},
		timeout:       timeout,
		tokenizerMode: tokenizerMode,
		ftsIndexName:  memoryFTSIndexName,
	}
	initCtx, initCancel := runtime.RequestContext()
	defer initCancel()
	if err := store.init(initCtx); err != nil {
		_ = store.Shutdown(initCtx)
		return nil, err
	}
	return store, nil
}

// ExecuteScript forwards one typed SQLite script without retrying uncertain controller mutations.
// ExecuteScript 透传一次类型化 SQLite 脚本，且不会重试结果不确定的 controller 写操作。
func (h *controllerDatabaseHandle) ExecuteScript(sql string, params []sqliteffi.SQLValue, _ string) (sqliteffi.ExecuteResult, error) {
	mappedParams, err := mapControllerSQLiteValues(params)
	if err != nil {
		return sqliteffi.ExecuteResult{}, err
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().ExecuteSqliteScript(ctx, &controllerclient.SqliteExecuteScriptRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.SQLiteBindingID(),
		SQL:       sql,
		Params:    mappedParams,
	})
	if err != nil {
		return sqliteffi.ExecuteResult{}, mapControllerSQLiteError("execute sqlite script", err)
	}
	if response == nil {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("controller execute sqlite script response is nil")
	}
	return sqliteffi.ExecuteResult{
		Success:         response.Success,
		Message:         response.Message,
		RowsChanged:     response.RowsChanged,
		LastInsertRowID: response.LastInsertRowID,
	}, nil
}

// ExecuteBatch forwards one typed SQLite batch without retrying uncertain controller mutations.
// ExecuteBatch 透传一次类型化 SQLite 批量写入，且不会重试结果不确定的 controller 写操作。
func (h *controllerDatabaseHandle) ExecuteBatch(sql string, items [][]sqliteffi.SQLValue) (sqliteffi.ExecuteResult, error) {
	mappedItems := make([]controllerclient.SqliteExecuteBatchItem, 0, len(items))
	for _, item := range items {
		mappedParams, err := mapControllerSQLiteValues(item)
		if err != nil {
			return sqliteffi.ExecuteResult{}, err
		}
		mappedItems = append(mappedItems, controllerclient.SqliteExecuteBatchItem{Params: mappedParams})
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().ExecuteSqliteBatch(ctx, &controllerclient.SqliteExecuteBatchRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.SQLiteBindingID(),
		SQL:       sql,
		Items:     mappedItems,
	})
	if err != nil {
		return sqliteffi.ExecuteResult{}, mapControllerSQLiteError("execute sqlite batch", err)
	}
	if response == nil {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("controller execute sqlite batch response is nil")
	}
	return sqliteffi.ExecuteResult{
		Success:            response.Success,
		Message:            response.Message,
		RowsChanged:        response.RowsChanged,
		LastInsertRowID:    response.LastInsertRowID,
		StatementsExecuted: response.StatementsExecuted,
	}, nil
}

// QueryJSON forwards one typed SQLite query and returns the same JSON row envelope used by the local FFI handle.
// QueryJSON 透传一次类型化 SQLite 查询，并返回与本地 FFI 句柄相同的 JSON 行封装。
func (h *controllerDatabaseHandle) QueryJSON(sql string, params []sqliteffi.SQLValue, _ string) (sqliteffi.QueryJSONResult, error) {
	mappedParams, err := mapControllerSQLiteValues(params)
	if err != nil {
		return sqliteffi.QueryJSONResult{}, err
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().QuerySqliteJSON(ctx, &controllerclient.SqliteQueryJSONRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.SQLiteBindingID(),
		SQL:       sql,
		Params:    mappedParams,
	})
	if err != nil {
		return sqliteffi.QueryJSONResult{}, err
	}
	if response == nil {
		return sqliteffi.QueryJSONResult{}, fmt.Errorf("controller query sqlite response is nil")
	}
	return sqliteffi.QueryJSONResult{JSONData: response.JSONData, RowCount: response.RowCount}, nil
}

// EnsureFtsIndex forwards built-in FTS index creation through the controller binding.
// EnsureFtsIndex 通过 controller 绑定透传内建 FTS 索引创建。
func (h *controllerDatabaseHandle) EnsureFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().EnsureSqliteFtsIndex(ctx, &controllerclient.SqliteFTSIndexRequest{
		SpaceID:       h.runtime.SpaceID(),
		BindingID:     h.runtime.SQLiteBindingID(),
		IndexName:     indexName,
		TokenizerMode: mapControllerTokenizerMode(mode),
	})
	if err != nil {
		return sqliteffi.EnsureFtsIndexResult{}, err
	}
	if response == nil {
		return sqliteffi.EnsureFtsIndexResult{}, fmt.Errorf("controller ensure sqlite fts response is nil")
	}
	return sqliteffi.EnsureFtsIndexResult{Success: response.Success, TokenizerMode: parseControllerTokenizerMode(response.TokenizerMode)}, nil
}

// RebuildFtsIndex forwards destructive FTS rebuilding and preserves uncertain-outcome semantics.
// RebuildFtsIndex 透传破坏性 FTS 重建，并保留结果不确定语义。
func (h *controllerDatabaseHandle) RebuildFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().RebuildSqliteFtsIndex(ctx, &controllerclient.SqliteFTSIndexRequest{
		SpaceID:       h.runtime.SpaceID(),
		BindingID:     h.runtime.SQLiteBindingID(),
		IndexName:     indexName,
		TokenizerMode: mapControllerTokenizerMode(mode),
	})
	if err != nil {
		return sqliteffi.RebuildFtsIndexResult{}, mapControllerSQLiteError("rebuild sqlite fts index", err)
	}
	if response == nil {
		return sqliteffi.RebuildFtsIndexResult{}, fmt.Errorf("controller rebuild sqlite fts response is nil")
	}
	return sqliteffi.RebuildFtsIndexResult{
		Success:       response.Success,
		TokenizerMode: parseControllerTokenizerMode(response.TokenizerMode),
		ReindexedRows: response.ReindexedRows,
	}, nil
}

// UpsertFtsDocument forwards one FTS document mutation through the controller binding.
// UpsertFtsDocument 通过 controller 绑定透传一条 FTS 文档写入。
func (h *controllerDatabaseHandle) UpsertFtsDocument(indexName string, mode sqliteffi.TokenizerMode, id string, filePath string, title string, content string) (sqliteffi.FtsMutationResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().UpsertSqliteFtsDocument(ctx, &controllerclient.SqliteFTSDocumentRequest{
		SpaceID:       h.runtime.SpaceID(),
		BindingID:     h.runtime.SQLiteBindingID(),
		IndexName:     indexName,
		TokenizerMode: mapControllerTokenizerMode(mode),
		ID:            id,
		FilePath:      filePath,
		Title:         title,
		Content:       content,
	})
	if err != nil {
		return sqliteffi.FtsMutationResult{}, mapControllerSQLiteError("upsert sqlite fts document", err)
	}
	if response == nil {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("controller upsert sqlite fts response is nil")
	}
	return sqliteffi.FtsMutationResult{Success: response.Success, AffectedRows: response.AffectedRows}, nil
}

// DeleteFtsDocument forwards one FTS document deletion through the controller binding.
// DeleteFtsDocument 通过 controller 绑定透传一条 FTS 文档删除。
func (h *controllerDatabaseHandle) DeleteFtsDocument(indexName string, id string) (sqliteffi.FtsMutationResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().DeleteSqliteFtsDocument(ctx, &controllerclient.SqliteFTSDeleteDocumentRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.SQLiteBindingID(),
		IndexName: indexName,
		ID:        id,
	})
	if err != nil {
		return sqliteffi.FtsMutationResult{}, mapControllerSQLiteError("delete sqlite fts document", err)
	}
	if response == nil {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("controller delete sqlite fts response is nil")
	}
	return sqliteffi.FtsMutationResult{Success: response.Success, AffectedRows: response.AffectedRows}, nil
}

// SearchFts forwards one FTS query and maps controller hits back to the existing FFI-compatible result.
// SearchFts 透传一次 FTS 查询，并把 controller 命中映射回现有 FFI 兼容结果。
func (h *controllerDatabaseHandle) SearchFts(indexName string, mode sqliteffi.TokenizerMode, query string, limit uint32, offset uint32) (sqliteffi.SearchResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().SearchSqliteFts(ctx, &controllerclient.SqliteFTSSearchRequest{
		SpaceID:       h.runtime.SpaceID(),
		BindingID:     h.runtime.SQLiteBindingID(),
		IndexName:     indexName,
		TokenizerMode: mapControllerTokenizerMode(mode),
		Query:         query,
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		return sqliteffi.SearchResult{}, err
	}
	if response == nil {
		return sqliteffi.SearchResult{}, fmt.Errorf("controller search sqlite fts response is nil")
	}
	hits := make([]sqliteffi.SearchHit, 0, len(response.Hits))
	for _, hit := range response.Hits {
		hits = append(hits, sqliteffi.SearchHit{
			ID:             hit.ID,
			FilePath:       hit.FilePath,
			Title:          hit.Title,
			TitleHighlight: hit.TitleHighlight,
			ContentSnippet: hit.ContentSnippet,
			Score:          hit.Score,
			Rank:           hit.Rank,
			RawScore:       hit.RawScore,
		})
	}
	return sqliteffi.SearchResult{
		Total:     response.Total,
		Source:    response.Source,
		QueryMode: response.QueryMode,
		Hits:      hits,
	}, nil
}

// Close leaves backend ownership to the shared runtime lifecycle.
// Close 把后端所有权交给共享运行时生命周期管理。
func (h *controllerDatabaseHandle) Close() error {
	return nil
}

// mapControllerSQLiteValues converts FFI-compatible typed values into controller SDK values and rejects unknown enum values.
// mapControllerSQLiteValues 把 FFI 兼容类型值转换成 controller SDK 值，并拒绝未知枚举值。
func mapControllerSQLiteValues(values []sqliteffi.SQLValue) ([]controllerclient.SqliteValue, error) {
	mapped := make([]controllerclient.SqliteValue, 0, len(values))
	for _, value := range values {
		item := controllerclient.SqliteValue{}
		switch value.Kind {
		case sqliteffi.SQLValueNull:
			item.Kind = controllerclient.SqliteValueNull
		case sqliteffi.SQLValueInt64:
			item.Kind = controllerclient.SqliteValueInt64
			item.Int64Value = value.Int64
		case sqliteffi.SQLValueFloat64:
			item.Kind = controllerclient.SqliteValueFloat64
			item.Float64Value = value.Float64
		case sqliteffi.SQLValueString:
			item.Kind = controllerclient.SqliteValueString
			item.StringValue = value.String
		case sqliteffi.SQLValueBytes:
			item.Kind = controllerclient.SqliteValueBytes
			item.BytesValue = append([]byte(nil), value.Bytes...)
		case sqliteffi.SQLValueBool:
			item.Kind = controllerclient.SqliteValueBool
			item.BoolValue = value.Bool
		default:
			return nil, fmt.Errorf("unsupported sqlite ffi value kind: %d", value.Kind)
		}
		mapped = append(mapped, item)
	}
	return mapped, nil
}

// mapControllerTokenizerMode converts the existing FFI tokenizer enum into the controller SDK enum.
// mapControllerTokenizerMode 把现有 FFI 分词枚举转换成 controller SDK 枚举。
func mapControllerTokenizerMode(mode sqliteffi.TokenizerMode) controllerclient.SqliteTokenizerMode {
	if mode == sqliteffi.TokenizerJieba {
		return controllerclient.SqliteTokenizerJieba
	}
	return controllerclient.SqliteTokenizerNone
}

// parseControllerTokenizerMode maps the controller response token back to the existing FFI enum.
// parseControllerTokenizerMode 把 controller 响应 token 映射回现有 FFI 枚举。
func parseControllerTokenizerMode(mode string) sqliteffi.TokenizerMode {
	if strings.EqualFold(strings.TrimSpace(mode), string(controllerclient.SqliteTokenizerJieba)) {
		return sqliteffi.TokenizerJieba
	}
	return sqliteffi.TokenizerNone
}

// mapControllerSQLiteError preserves the application's explicit uncertain-outcome boundary for non-idempotent mutations.
// mapControllerSQLiteError 为非幂等写操作保留应用层显式结果不确定边界。
func mapControllerSQLiteError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if controllerclient.IsMutationOutcomeUncertain(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: operation,
			Message:   err.Error(),
		}
	}
	return err
}
