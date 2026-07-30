// controller_engine.go adapts vldb-controller LanceDB RPCs to the narrow engine handle already used by the VMM vector store.
// controller_engine.go 用于把 vldb-controller LanceDB RPC 适配到 VMM 向量存储已经使用的窄引擎句柄接口。
package vldb_lancedb

import (
	"fmt"
	"strings"
	"time"

	controllerclient "github.com/OpenVulcan/vldb-controller/client-go/controller"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_controller"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"
)

// controllerEngineHandle forwards the existing LanceDB engine contract through one shared controller runtime.
// controllerEngineHandle 通过一个共享 controller 运行时透传现有 LanceDB 引擎契约。
type controllerEngineHandle struct {
	// runtime owns the controller session and LanceDB backend binding.
	// runtime 持有 controller 会话与 LanceDB 后端绑定。
	runtime *vldb_controller.Runtime
}

// NewControllerStore builds the existing vector store on top of a controller-owned LanceDB binding and ensures the table exists.
// NewControllerStore 在 controller 持有的 LanceDB 绑定之上构建现有向量存储，并确保目标表存在。
func NewControllerStore(runtime *vldb_controller.Runtime, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newControllerStore(runtime, timeout, tableName, vectorColumn, dimension, true)
}

// NewControllerStoreWithoutInit builds a controller-backed vector store without creating the table for maintenance flows.
// NewControllerStoreWithoutInit 为维护流程构建不主动建表的 controller 向量存储。
func NewControllerStoreWithoutInit(runtime *vldb_controller.Runtime, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newControllerStore(runtime, timeout, tableName, vectorColumn, dimension, false)
}

// newControllerStore validates the vector contract once and optionally runs the same initialization path as direct FFI mode.
// newControllerStore 统一校验向量契约，并按需执行与直接 FFI 模式相同的初始化流程。
func newControllerStore(runtime *vldb_controller.Runtime, timeout time.Duration, tableName, vectorColumn string, dimension int, ensureTable bool) (*Store, error) {
	if runtime == nil || runtime.Client() == nil {
		return nil, fmt.Errorf("controller runtime is required")
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
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	store := &Store{
		engine:       &controllerEngineHandle{runtime: runtime},
		timeout:      timeout,
		tableName:    resolveVectorTableName(tableName, dimension),
		vectorColumn: strings.TrimSpace(vectorColumn),
		dimension:    dimension,
	}
	if ensureTable {
		ctx, cancel := runtime.RequestContext()
		defer cancel()
		if err := store.init(ctx); err != nil {
			_ = store.Shutdown(ctx)
			return nil, err
		}
	}
	return store, nil
}

// CreateTable forwards one LanceDB schema request through the controller binding.
// CreateTable 通过 controller 绑定透传一次 LanceDB 建表请求。
func (h *controllerEngineHandle) CreateTable(request lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error) {
	columns := make([]controllerclient.LanceDBColumnDef, 0, len(request.Columns))
	for _, column := range request.Columns {
		columnType, err := mapControllerLanceColumnType(column.ColumnType)
		if err != nil {
			return lancedbffi.CreateTableResult{}, err
		}
		columns = append(columns, controllerclient.LanceDBColumnDef{
			Name:       column.Name,
			ColumnType: columnType,
			VectorDim:  column.VectorDim,
			Nullable:   column.Nullable,
		})
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().CreateLanceDBTable(ctx, &controllerclient.LanceDBCreateTableRequest{
		SpaceID:           h.runtime.SpaceID(),
		BindingID:         h.runtime.LanceDBBindingID(),
		TableName:         request.TableName,
		Columns:           columns,
		OverwriteIfExists: request.OverwriteIfExists,
	})
	if err != nil {
		return lancedbffi.CreateTableResult{}, mapControllerLanceError("create lancedb table", err)
	}
	if response == nil {
		return lancedbffi.CreateTableResult{}, fmt.Errorf("controller create lancedb table response is nil")
	}
	return lancedbffi.CreateTableResult{Success: true, Message: response.Message}, nil
}

// VectorUpsertRaw forwards one raw LanceDB upsert without replaying uncertain mutations.
// VectorUpsertRaw 透传一次原始 LanceDB 写入，且不会重放结果不确定的写操作。
func (h *controllerEngineHandle) VectorUpsertRaw(tableName string, format lancedbffi.InputFormat, data []byte, keyColumns []string) (lancedbffi.UpsertResult, error) {
	inputFormat, err := mapControllerLanceInputFormat(format)
	if err != nil {
		return lancedbffi.UpsertResult{}, err
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().UpsertLanceDB(ctx, &controllerclient.LanceDBUpsertRequest{
		SpaceID:     h.runtime.SpaceID(),
		BindingID:   h.runtime.LanceDBBindingID(),
		TableName:   tableName,
		InputFormat: inputFormat,
		Data:        append([]byte(nil), data...),
		KeyColumns:  append([]string(nil), keyColumns...),
	})
	if err != nil {
		return lancedbffi.UpsertResult{}, mapControllerLanceError("upsert lancedb rows", err)
	}
	if response == nil {
		return lancedbffi.UpsertResult{}, fmt.Errorf("controller upsert lancedb response is nil")
	}
	return lancedbffi.UpsertResult{
		Version:      response.Version,
		InputRows:    response.InputRows,
		InsertedRows: response.InsertedRows,
		UpdatedRows:  response.UpdatedRows,
		DeletedRows:  response.DeletedRows,
	}, nil
}

// VectorSearchF32 forwards one vector search and maps the raw payload back to the existing engine result.
// VectorSearchF32 透传一次向量检索，并把原始载荷映射回现有引擎结果。
func (h *controllerEngineHandle) VectorSearchF32(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
	format, err := mapControllerLanceOutputFormat(outputFormat)
	if err != nil {
		return lancedbffi.SearchResult{}, err
	}
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().SearchLanceDB(ctx, &controllerclient.LanceDBSearchRequest{
		SpaceID:      h.runtime.SpaceID(),
		BindingID:    h.runtime.LanceDBBindingID(),
		TableName:    tableName,
		Vector:       append([]float32(nil), vector...),
		Limit:        limit,
		Filter:       filter,
		VectorColumn: vectorColumn,
		OutputFormat: format,
	})
	if err != nil {
		return lancedbffi.SearchResult{}, err
	}
	if response == nil {
		return lancedbffi.SearchResult{}, fmt.Errorf("controller search lancedb response is nil")
	}
	mappedFormat, err := parseControllerLanceOutputFormat(response.Format)
	if err != nil {
		return lancedbffi.SearchResult{}, err
	}
	return lancedbffi.SearchResult{
		Format: mappedFormat,
		Rows:   response.Rows,
		Data:   append([]byte(nil), response.Data...),
	}, nil
}

// Delete forwards one LanceDB predicate deletion through the controller binding.
// Delete 通过 controller 绑定透传一次 LanceDB 条件删除。
func (h *controllerEngineHandle) Delete(request lancedbffi.DeleteRequest) (lancedbffi.DeleteResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().DeleteLanceDB(ctx, &controllerclient.LanceDBDeleteRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.LanceDBBindingID(),
		TableName: request.TableName,
		Condition: request.Condition,
	})
	if err != nil {
		return lancedbffi.DeleteResult{}, mapControllerLanceError("delete lancedb rows", err)
	}
	if response == nil {
		return lancedbffi.DeleteResult{}, fmt.Errorf("controller delete lancedb response is nil")
	}
	return lancedbffi.DeleteResult{
		Success:     true,
		Message:     response.Message,
		Version:     response.Version,
		DeletedRows: response.DeletedRows,
	}, nil
}

// DropTable forwards one destructive LanceDB table deletion through the controller binding.
// DropTable 通过 controller 绑定透传一次破坏性 LanceDB 删表。
func (h *controllerEngineHandle) DropTable(request lancedbffi.DropTableRequest) (lancedbffi.DropTableResult, error) {
	ctx, cancel := h.runtime.RequestContext()
	defer cancel()
	response, err := h.runtime.Client().DropLanceDBTable(ctx, &controllerclient.LanceDBDropTableRequest{
		SpaceID:   h.runtime.SpaceID(),
		BindingID: h.runtime.LanceDBBindingID(),
		TableName: request.TableName,
	})
	if err != nil {
		return lancedbffi.DropTableResult{}, mapControllerLanceError("drop lancedb table", err)
	}
	if response == nil {
		return lancedbffi.DropTableResult{}, fmt.Errorf("controller drop lancedb table response is nil")
	}
	return lancedbffi.DropTableResult{Success: true, Message: response.Message}, nil
}

// Close leaves backend ownership to the shared runtime lifecycle.
// Close 把后端所有权交给共享运行时生命周期管理。
func (h *controllerEngineHandle) Close() error {
	return nil
}

// mapControllerLanceColumnType maps every VMM table column token to one explicit controller SDK enum.
// mapControllerLanceColumnType 把每个 VMM 表列 token 映射成显式 controller SDK 枚举。
func mapControllerLanceColumnType(columnType string) (controllerclient.LanceDBColumnType, error) {
	switch strings.ToLower(strings.TrimSpace(columnType)) {
	case "string":
		return controllerclient.LanceDBColumnTypeString, nil
	case "int64":
		return controllerclient.LanceDBColumnTypeInt64, nil
	case "float64":
		return controllerclient.LanceDBColumnTypeFloat64, nil
	case "bool":
		return controllerclient.LanceDBColumnTypeBool, nil
	case "vector_float32":
		return controllerclient.LanceDBColumnTypeVectorFloat32, nil
	case "float32":
		return controllerclient.LanceDBColumnTypeFloat32, nil
	case "uint64":
		return controllerclient.LanceDBColumnTypeUint64, nil
	case "int32":
		return controllerclient.LanceDBColumnTypeInt32, nil
	case "uint32":
		return controllerclient.LanceDBColumnTypeUint32, nil
	default:
		return controllerclient.LanceDBColumnTypeUnspecified, fmt.Errorf("unsupported lancedb column type: %s", columnType)
	}
}

// mapControllerLanceInputFormat maps the existing FFI input format to the controller SDK enum.
// mapControllerLanceInputFormat 把现有 FFI 输入格式映射成 controller SDK 枚举。
func mapControllerLanceInputFormat(format lancedbffi.InputFormat) (controllerclient.LanceDBInputFormat, error) {
	if format == lancedbffi.InputFormatJSONRows {
		return controllerclient.LanceDBInputFormatJSONRows, nil
	}
	return controllerclient.LanceDBInputFormatUnspecified, fmt.Errorf("unsupported lancedb input format: %d", format)
}

// mapControllerLanceOutputFormat maps the existing FFI output format to the controller SDK enum.
// mapControllerLanceOutputFormat 把现有 FFI 输出格式映射成 controller SDK 枚举。
func mapControllerLanceOutputFormat(format lancedbffi.OutputFormat) (controllerclient.LanceDBOutputFormat, error) {
	if format == lancedbffi.OutputFormatJSONRows {
		return controllerclient.LanceDBOutputFormatJSONRows, nil
	}
	return controllerclient.LanceDBOutputFormatUnspecified, fmt.Errorf("unsupported lancedb output format: %d", format)
}

// parseControllerLanceOutputFormat maps the controller response token back to the existing FFI enum.
// parseControllerLanceOutputFormat 把 controller 响应 token 映射回现有 FFI 枚举。
func parseControllerLanceOutputFormat(format string) (lancedbffi.OutputFormat, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	// The request enum is json_rows while the v0.2.3 server response reports the native wire format token as json.
	// 请求枚举使用 json_rows，而 v0.2.3 服务端响应会把原生线格式 token 返回为 json。
	if normalized == string(controllerclient.LanceDBOutputFormatJSONRows) || normalized == "json" {
		return lancedbffi.OutputFormatJSONRows, nil
	}
	return 0, fmt.Errorf("unsupported controller lancedb output format: %s", format)
}

// mapControllerLanceError preserves the application's explicit uncertain-outcome boundary for non-idempotent mutations.
// mapControllerLanceError 为非幂等写操作保留应用层显式结果不确定边界。
func mapControllerLanceError(operation string, err error) error {
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
