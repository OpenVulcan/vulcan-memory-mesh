// store.go implements the LanceDB-gateway outbound adapter used by vector recall and admin cleanup flows.
// store.go 用于实现基于 LanceDB 网关的出站适配器，承接向量召回和管理清理流程。
package vldb_lancedb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Store is the LanceDB-gateway adapter used by the vector store port.
// Store 用于作为向量存储端口的 LanceDB 网关适配器。
type Store struct {
	conn         *grpc.ClientConn
	client       lancedbv1.LanceDbServiceClient
	timeout      time.Duration
	tableName    string
	vectorColumn string
	dimension    int
}

// NewStore dials the LanceDB gateway and ensures the configured vector table exists before serving traffic.
// NewStore 用于连接 LanceDB 网关，并在开始提供服务前确保配置指定的向量表已经存在。
func NewStore(address string, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	// Validate the minimum table configuration first so startup errors stay easy to interpret.
	// 先校验最小表配置，保证启动错误保持易于理解。
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("lancedb address is required")
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

	// Dial the local gateway eagerly so configuration drift is caught during application startup.
	// 以阻塞方式连接本地网关，让配置漂移在应用启动时就能被发现。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		strings.TrimSpace(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial lancedb gateway: %w", err)
	}

	store := &Store{
		conn:         conn,
		client:       lancedbv1.NewLanceDbServiceClient(conn),
		timeout:      timeout,
		tableName:    resolveVectorTableName(tableName, dimension),
		vectorColumn: strings.TrimSpace(vectorColumn),
		dimension:    dimension,
	}
	if err := store.init(context.Background()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return store, nil
}

// Upsert writes one memory record into the LanceDB-backed vector table using JSON row ingestion.
// Upsert 用于通过 JSON 行写入方式把一条记忆记录保存到 LanceDB 向量表。
func (s *Store) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error {
	// Convert the domain record into one JSON row so the gateway can upsert it by id.
	// 将领域记录转换成一条 JSON 行，交给网关按 id 执行 upsert。
	if s == nil || s.client == nil {
		return fmt.Errorf("lancedb store is not initialized")
	}
	metadataJSON, err := json.Marshal(record.Metadata)
	if err != nil {
		return fmt.Errorf("encode memory metadata: %w", err)
	}
	rows := []map[string]any{
		{
			"id":            record.ID,
			"content":       record.Text,
			"team_id":       record.Filter.TeamID,
			"space_id":      record.Filter.SpaceID,
			"project_id":    record.Filter.ProjectID,
			"session_id":    record.Filter.SessionID,
			"user_id":       record.Filter.UserID,
			"metadata_json": string(metadataJSON),
			"created_at":    record.CreatedAt.UTC().Format(time.RFC3339Nano),
			s.vectorColumn:  record.Vector,
		},
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal lancedb upsert payload: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.VectorUpsert(callCtx, &lancedbv1.UpsertRequest{
		TableName:   s.tableName,
		InputFormat: lancedbv1.InputFormat_INPUT_FORMAT_JSON_ROWS,
		Data:        payload,
		KeyColumns:  []string{"id"},
	})
	if err != nil {
		return fmt.Errorf("lancedb vector upsert: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("lancedb vector upsert: %s", resp.Message)
	}
	return nil
}

// DeleteByFilter removes all vector rows that match one flattened hierarchy filter.
// DeleteByFilter 用于删除符合某个扁平层级过滤条件的全部向量行。
func (s *Store) DeleteByFilter(ctx context.Context, filter logicdomain.SearchFilter) (uint64, error) {
	if s == nil || s.client == nil {
		return 0, fmt.Errorf("lancedb store is not initialized")
	}
	condition := buildDeleteCondition(filter)
	if strings.TrimSpace(condition) == "" {
		return 0, fmt.Errorf("lancedb delete filter is empty")
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.Delete(callCtx, &lancedbv1.DeleteRequest{
		TableName: s.tableName,
		Condition: condition,
	})
	if err != nil {
		return 0, fmt.Errorf("lancedb delete: %w", err)
	}
	if !resp.GetSuccess() {
		return 0, fmt.Errorf("lancedb delete: %s", resp.GetMessage())
	}
	return resp.GetDeletedRows(), nil
}

// Search runs one vector search against the configured table and maps the returned JSON rows back into MemoryHit values.
// Search 用于对配置好的表执行一次向量检索，并把返回的 JSON 行映射回 MemoryHit 结构。
func (s *Store) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	// Build a filter expression compatible with the gateway service while keeping the port contract unchanged.
	// 在保持端口契约不变的前提下，构造一条兼容网关服务的过滤表达式。
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("lancedb store is not initialized")
	}
	if topK <= 0 {
		topK = 10
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.VectorSearch(callCtx, &lancedbv1.SearchRequest{
		TableName:    s.tableName,
		Vector:       vector,
		Limit:        uint32(topK),
		Filter:       buildFilterExpr(filter),
		VectorColumn: s.vectorColumn,
		OutputFormat: lancedbv1.OutputFormat_OUTPUT_FORMAT_JSON_ROWS,
	})
	if err != nil {
		return nil, fmt.Errorf("lancedb vector search: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("lancedb vector search: %s", resp.Message)
	}

	// Decode the gateway JSON output and translate the distance metric into the higher-is-better score expected upstream.
	// 解码网关 JSON 输出，并把距离值转换成上游期望的“越高越好”分数。
	rows := make([]searchRow, 0)
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &rows); err != nil {
			return nil, fmt.Errorf("decode lancedb search rows: %w", err)
		}
	}
	hits := make([]logicdomain.MemoryHit, 0, len(rows))
	for _, row := range rows {
		metadata := map[string]string{}
		if strings.TrimSpace(row.MetadataJSON) != "" {
			_ = json.Unmarshal([]byte(row.MetadataJSON), &metadata)
		}
		distance := row.Distance
		if distance == 0 {
			distance = row.Score
		}
		hits = append(hits, logicdomain.MemoryHit{
			ID:    row.ID,
			Text:  row.Content,
			Score: distanceToScore(distance),
			Filter: logicdomain.SearchFilter{
				TeamID:    row.TeamID,
				SpaceID:   row.SpaceID,
				ProjectID: row.ProjectID,
				SessionID: row.SessionID,
				UserID:    row.UserID,
			},
			Metadata: metadata,
		})
	}
	return hits, nil
}

// Shutdown closes the gRPC client connection used by the LanceDB gateway adapter.
// Shutdown 用于关闭 LanceDB 网关适配器使用的 gRPC 连接。
func (s *Store) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// init creates the configured vector table lazily so seed-memory can upsert without extra setup steps.
// init 用于惰性创建配置指定的向量表，让 seed-memory 无需额外建表步骤即可写入。
func (s *Store) init(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.CreateTable(callCtx, &lancedbv1.CreateTableRequest{
		TableName:         s.tableName,
		OverwriteIfExists: false,
		Columns: []*lancedbv1.ColumnDef{
			{Name: "id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_STRING, Nullable: false},
			{Name: "content", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_STRING, Nullable: false},
			{Name: "team_id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_UINT64, Nullable: false},
			{Name: "space_id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_UINT64, Nullable: false},
			{Name: "project_id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_UINT64, Nullable: false},
			{Name: "session_id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_UINT64, Nullable: false},
			{Name: "user_id", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_UINT64, Nullable: false},
			{Name: "metadata_json", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_STRING, Nullable: false},
			{Name: "created_at", ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_STRING, Nullable: false},
			{Name: s.vectorColumn, ColumnType: lancedbv1.ColumnType_COLUMN_TYPE_VECTOR_FLOAT32, VectorDim: uint32(s.dimension), Nullable: false},
		},
	})
	if err != nil {
		// Treat idempotent table-exists failures as success so repeated boots can reuse the same vector table.
		// 将“表已存在”视为幂等成功，保证重复启动时可以直接复用同一张向量表。
		if isTableAlreadyExistsError(err) {
			return nil
		}
		return fmt.Errorf("create lancedb table: %w", err)
	}
	if !resp.Success {
		// Some gateway builds report existing tables through the response body instead of a transport error.
		// 某些网关实现会通过响应体而不是传输层错误来报告“表已存在”。
		if isTableAlreadyExistsMessage(resp.GetMessage()) {
			return nil
		}
		return fmt.Errorf("create lancedb table: %s", resp.Message)
	}
	return nil
}

// isTableAlreadyExistsError classifies gateway transport errors that mean the configured table is already present.
// isTableAlreadyExistsError 用于识别那些实际表示“目标表已经存在”的网关传输层错误。
func isTableAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.AlreadyExists {
		return true
	}
	return isTableAlreadyExistsMessage(err.Error())
}

// isTableAlreadyExistsMessage matches the gateway messages used when CreateTable is retried on an existing table.
// isTableAlreadyExistsMessage 用于匹配网关在重复创建已有表时返回的典型消息。
func isTableAlreadyExistsMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "already exists")
}

// buildFilterExpr converts one search filter into the simple SQL-like predicate syntax accepted by the gateway.
// buildFilterExpr 用于把检索过滤条件转换成网关接受的简易 SQL 风格谓词表达式。
func buildFilterExpr(filter logicdomain.SearchFilter) string {
	parts := make([]string, 0, 5)
	if filter.TeamID > 0 {
		parts = append(parts, fmt.Sprintf("team_id = %d", filter.TeamID))
	}
	if filter.SpaceID > 0 {
		parts = append(parts, fmt.Sprintf("space_id = %d", filter.SpaceID))
	}
	if filter.ProjectID > 0 {
		parts = append(parts, fmt.Sprintf("project_id = %d", filter.ProjectID))
	}
	if filter.SessionID > 0 {
		parts = append(parts, fmt.Sprintf("session_id = %d", filter.SessionID))
	}
	if filter.UserID > 0 {
		parts = append(parts, fmt.Sprintf("(user_id = 0 OR user_id = %d)", filter.UserID))
	}
	return strings.Join(parts, " AND ")
}

// buildDeleteCondition converts a hierarchy filter into the stricter predicate used by destructive vector cleanup flows.
// buildDeleteCondition 用于把层级过滤条件转换成更严格的删除谓词，服务向量清理流程。
func buildDeleteCondition(filter logicdomain.SearchFilter) string {
	parts := make([]string, 0, 5)
	if filter.TeamID > 0 {
		parts = append(parts, fmt.Sprintf("team_id = %d", filter.TeamID))
	}
	if filter.SpaceID > 0 {
		parts = append(parts, fmt.Sprintf("space_id = %d", filter.SpaceID))
	}
	if filter.ProjectID > 0 {
		parts = append(parts, fmt.Sprintf("project_id = %d", filter.ProjectID))
	}
	if filter.SessionID > 0 {
		parts = append(parts, fmt.Sprintf("session_id = %d", filter.SessionID))
	}
	if filter.UserID > 0 {
		parts = append(parts, fmt.Sprintf("user_id = %d", filter.UserID))
	}
	return strings.Join(parts, " AND ")
}

// distanceToScore turns the LanceDB nearest-neighbor distance into a stable higher-is-better score.
// distanceToScore 用于把 LanceDB 最近邻距离转换成稳定的“越高越好”分数。
func distanceToScore(distance float64) float64 {
	if distance <= 0 {
		return 1
	}
	return 1 / (1 + math.Max(distance, 0))
}

// resolveVectorTableName derives the concrete LanceDB table name from the logical base name and embedding dimension.
// resolveVectorTableName 用于根据逻辑基础表名和 embedding 维度推导实际的 LanceDB 表名。
func resolveVectorTableName(baseName string, dimension int) string {
	trimmedBase := strings.TrimSpace(baseName)
	return fmt.Sprintf("%s_%d", trimmedBase, dimension)
}

// searchRow mirrors the JSON search row emitted by the gateway in JSON output mode.
// searchRow 用于映射网关在 JSON 输出模式下返回的一行检索结果。
type searchRow struct {
	ID           string  `json:"id"`
	Content      string  `json:"content"`
	TeamID       uint64  `json:"team_id"`
	SpaceID      uint64  `json:"space_id"`
	ProjectID    uint64  `json:"project_id"`
	SessionID    uint64  `json:"session_id"`
	UserID       uint64  `json:"user_id"`
	MetadataJSON string  `json:"metadata_json"`
	Distance     float64 `json:"_distance"`
	Score        float64 `json:"distance"`
}

// UnmarshalJSON keeps search-row distance parsing tolerant to either numeric or string JSON values.
// UnmarshalJSON 用于兼容检索结果里的距离字段既可能是数值也可能是字符串的情况。
func (r *searchRow) UnmarshalJSON(data []byte) error {
	type rawSearchRow struct {
		ID           string         `json:"id"`
		Text         string         `json:"text"`
		UserID       string         `json:"user_id"`
		ProjectID    string         `json:"project_id"`
		SpaceID      string         `json:"space_id"`
		MetadataJSON string         `json:"metadata_json"`
		Distance     any            `json:"_distance"`
		AltDistance  any            `json:"distance"`
		Extra        map[string]any `json:"-"`
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.ID = asString(raw["id"])
	r.Content = asString(raw["content"])
	r.TeamID = asUint64(raw["team_id"])
	r.SpaceID = asUint64(raw["space_id"])
	r.ProjectID = asUint64(raw["project_id"])
	r.SessionID = asUint64(raw["session_id"])
	r.UserID = asUint64(raw["user_id"])
	r.MetadataJSON = asString(raw["metadata_json"])
	r.Distance = asFloat64(raw["_distance"])
	r.Score = asFloat64(raw["distance"])
	return nil
}

// asUint64 converts one generic JSON field into uint64 while tolerating float and string encodings from the gateway.
// asUint64 用于把通用 JSON 字段转换成 uint64，并兼容网关返回的浮点或字符串编码。
func asUint64(value any) uint64 {
	switch typed := value.(type) {
	case float64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case float32:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case int:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case int64:
		if typed < 0 {
			return 0
		}
		return uint64(typed)
	case uint64:
		return typed
	case string:
		number, _ := strconv.ParseUint(strings.TrimSpace(typed), 10, 64)
		return number
	default:
		return 0
	}
}

// asString converts one generic JSON field into a string without panicking on null or unexpected shapes.
// asString 用于把通用 JSON 字段安全转换成字符串，避免在 null 或意外结构上发生 panic。
func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

// asFloat64 converts one generic JSON field into a float64 while tolerating integer and string values.
// asFloat64 用于把通用 JSON 字段转换成 float64，同时兼容整数和字符串值。
func asFloat64(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint32:
		return float64(typed)
	case uint64:
		return float64(typed)
	case json.Number:
		number, _ := typed.Float64()
		return number
	case string:
		number, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number
	default:
		return 0
	}
}
