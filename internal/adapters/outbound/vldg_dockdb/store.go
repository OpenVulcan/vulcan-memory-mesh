// store.go implements the DuckDB-gateway outbound adapter used by archive, relational, and noise-cache flows.
// store.go 用于实现基于 DuckDB 网关的出站适配器，承接归档、关系存储和噪声缓存流程。
package vldg_dockdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldg_dockdb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	currentSchemaVersion = 1
	versionTableSQL      = `
CREATE TABLE IF NOT EXISTS vmm_version (
  singleton_id INTEGER PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  updated_at VARCHAR NOT NULL
);
`
	schemaV1SQL = `
CREATE TABLE IF NOT EXISTS vmm_memories (
  id VARCHAR PRIMARY KEY,
  session_id VARCHAR NOT NULL,
  content TEXT NOT NULL,
  created_at VARCHAR NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_memories_session_id ON vmm_memories(session_id);

CREATE TABLE IF NOT EXISTS vmm_noise_embeddings (
  scope VARCHAR NOT NULL,
  language VARCHAR NOT NULL,
  category_name VARCHAR NOT NULL,
  phrase TEXT NOT NULL,
  model VARCHAR NOT NULL,
  dimension INTEGER NOT NULL,
  rules_hash VARCHAR NOT NULL,
  vector_json TEXT NOT NULL,
  updated_at VARCHAR NOT NULL,
  PRIMARY KEY (scope, language, category_name, phrase, model, dimension, rules_hash)
);
CREATE INDEX IF NOT EXISTS idx_vmm_noise_embeddings_lookup
  ON vmm_noise_embeddings(scope, language, model, dimension, rules_hash);

CREATE TABLE IF NOT EXISTS vmm_chat_sessions (
  session_id VARCHAR PRIMARY KEY,
  user_id VARCHAR NOT NULL,
  team_id VARCHAR NOT NULL,
  project_id VARCHAR NOT NULL,
  space_id VARCHAR NOT NULL,
  last_updated_at VARCHAR NOT NULL,
  created_at VARCHAR NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_chat_sessions_scope_updated
  ON vmm_chat_sessions(user_id, team_id, project_id, space_id, last_updated_at);

CREATE TABLE IF NOT EXISTS vmm_chat_logs (
  session_id VARCHAR NOT NULL,
  turn_index INTEGER NOT NULL,
  user_id VARCHAR NOT NULL,
  team_id VARCHAR NOT NULL,
  project_id VARCHAR NOT NULL,
  space_id VARCHAR NOT NULL,
  user_message TEXT NOT NULL,
  assistant_reply TEXT NOT NULL,
  created_at VARCHAR NOT NULL,
  updated_at VARCHAR NOT NULL,
  PRIMARY KEY (session_id, turn_index)
);
CREATE INDEX IF NOT EXISTS idx_vmm_chat_logs_session_turn
  ON vmm_chat_logs(session_id, turn_index);
`
)

// Store is the shared DuckDB-gateway adapter used by the local runtime for durable SQL-backed data.
// Store 用于作为本地运行时的共享 DuckDB 网关适配器，承载长期 SQL 数据。
type Store struct {
	conn    *grpc.ClientConn
	client  duckdbv1.DuckDbServiceClient
	timeout time.Duration
	writeMu sync.Mutex
}

// NewStore dials the DuckDB gateway and ensures the required VMM schema exists before traffic starts.
// NewStore 用于连接 DuckDB 网关，并在开始提供流量前确保 VMM 所需表结构已经存在。
func NewStore(address string, timeout time.Duration) (*Store, error) {
	// Resolve a predictable timeout first so local startup does not hang forever on a dead gateway.
	// 先解析稳定的超时值，避免本地启动时因网关失联而无限卡住。
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("dockdb address is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// Dial the local gRPC gateway eagerly so configuration errors surface during startup.
	// 以阻塞方式连接本地 gRPC 网关，让配置错误在启动阶段就暴露出来。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		strings.TrimSpace(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial dockdb gateway: %w", err)
	}

	store := &Store{
		conn:    conn,
		client:  duckdbv1.NewDuckDbServiceClient(conn),
		timeout: timeout,
	}
	if err := store.init(context.Background()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return store, nil
}

// SaveMemory persists one scrubbed chat message through the DuckDB gateway without storing raw PII.
// SaveMemory 用于通过 DuckDB 网关持久化一条已脱敏聊天消息，不写入原始敏感信息。
func (s *Store) SaveMemory(ctx context.Context, record logicdomain.ArchivedMemory) error {
	// Insert one archive row with parameterized SQL so the local gateway keeps the operation injection-safe.
	// 用参数化 SQL 写入归档记录，确保本地网关侧保持安全的入库方式。
	return s.exec(ctx,
		`INSERT INTO vmm_memories (id, session_id, content, created_at) VALUES (?, ?, ?, ?)`,
		record.ID,
		record.SessionID,
		record.Content,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
}

// LoadNoiseEmbeddingCache reads one semantic prototype bundle identified by language, model, dimension, and rules hash.
// LoadNoiseEmbeddingCache 用于读取由语言、模型、维度和规则指纹唯一标识的一组语义原型缓存。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	// Query the exact cache fingerprint so stale vectors miss cleanly when rules or models change.
	// 按完整缓存指纹查询，确保规则或模型变化时旧向量会干净失效。
	rows, err := queryJSONRows[noiseEmbeddingCacheRow](ctx, s, `
SELECT
  scope,
  language,
  category_name,
  phrase,
  model,
  dimension,
  rules_hash,
  vector_json,
  updated_at
FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
ORDER BY category_name, phrase
`, query.Scope, query.Language, query.Model, query.Dimension, query.RulesHash)
	if err != nil {
		return nil, fmt.Errorf("load noise embedding cache: %w", err)
	}

	// Decode the cached JSON vectors into the domain entries expected by the processor layer.
	// 将缓存里的 JSON 向量解码成处理器层需要的领域结构。
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(rows))
	for _, row := range rows {
		var vector []float32
		if err := json.Unmarshal([]byte(row.VectorJSON), &vector); err != nil {
			return nil, fmt.Errorf("decode noise cache vector: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse noise cache updated_at: %w", err)
		}
		entries = append(entries, logicdomain.NoiseEmbeddingCacheEntry{
			Scope:        row.Scope,
			Language:     row.Language,
			CategoryName: row.CategoryName,
			Phrase:       row.Phrase,
			Model:        row.Model,
			Dimension:    row.Dimension,
			RulesHash:    row.RulesHash,
			Vector:       vector,
			UpdatedAt:    updatedAt,
		})
	}
	return entries, nil
}

// ReplaceNoiseEmbeddingCache refreshes one scope/language cache bundle so startup-time semantic vectors can be reused later.
// ReplaceNoiseEmbeddingCache 用于刷新某个作用域和语言下的缓存包，供启动阶段后续直接复用语义向量。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	// Serialize replacement writes so concurrent startups never interleave delete/insert sequences.
	// 序列化缓存替换写入，避免并发启动时把删除和插入交错在一起。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, `DELETE FROM vmm_noise_embeddings WHERE scope = ? AND language = ?`, query.Scope, query.Language); err != nil {
		return fmt.Errorf("clear stale noise embedding cache: %w", err)
	}
	for _, entry := range entries {
		vectorJSON, err := json.Marshal(entry.Vector)
		if err != nil {
			return fmt.Errorf("encode noise embedding cache vector: %w", err)
		}
		updatedAt := entry.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if err := s.exec(ctx, `
INSERT INTO vmm_noise_embeddings (
  scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, entry.Scope, entry.Language, entry.CategoryName, entry.Phrase, entry.Model, entry.Dimension, entry.RulesHash, string(vectorJSON), updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert noise embedding cache: %w", err)
		}
	}
	return nil
}

// UpsertChatLogs appends normalized user-assistant turns into the DuckDB-backed relational history store.
// UpsertChatLogs 用于把标准化用户-助手轮次追加写入基于 DuckDB 的关系历史库。
func (s *Store) UpsertChatLogs(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.NormalizedTurn) error {
	// Append turns under a serialized write lock so session-local turn indexes remain monotonic.
	// 在串行写锁下追加轮次，确保同一会话的 turn_index 保持单调递增。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	maxTurnIndex, err := s.loadMaxTurnIndex(ctx, session.SessionID)
	if err != nil {
		return fmt.Errorf("load current max turn index: %w", err)
	}
	for idx, turn := range turns {
		persistedIndex := maxTurnIndex + idx + 1
		createdAt := turn.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		if err := s.exec(ctx, `
INSERT INTO vmm_chat_logs (
  session_id, turn_index, user_id, team_id, project_id, space_id, user_message, assistant_reply, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, session.SessionID, persistedIndex, session.UserID, session.TeamID, session.ProjectID, session.SpaceID, turn.UserMessage, turn.AssistantReply, createdAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert chat log turn %d: %w", persistedIndex, err)
		}
	}
	return nil
}

// RefreshSession updates or creates one session row so post-action can keep lightweight session metadata fresh.
// RefreshSession 用于更新或创建会话主记录，让 post-action 可以持续维护轻量会话元数据。
func (s *Store) RefreshSession(ctx context.Context, session logicdomain.SessionRef) error {
	// Replace the session row under the same lock so scope fields and updated_at stay self-consistent.
	// 在同一把锁下替换会话记录，确保作用域字段和 updated_at 保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt, err := s.loadSessionCreatedAt(ctx, session.SessionID)
	if err != nil {
		return fmt.Errorf("load existing session created_at: %w", err)
	}
	if createdAt == "" {
		createdAt = now
	}
	if err := s.exec(ctx, `DELETE FROM vmm_chat_sessions WHERE session_id = ?`, session.SessionID); err != nil {
		return fmt.Errorf("delete stale session row: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_chat_sessions (
  session_id, user_id, team_id, project_id, space_id, last_updated_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
`, session.SessionID, session.UserID, session.TeamID, session.ProjectID, session.SpaceID, now, createdAt); err != nil {
		return fmt.Errorf("insert refreshed session row: %w", err)
	}
	return nil
}

// Shutdown closes the gRPC client connection used by the DuckDB gateway adapter.
// Shutdown 用于关闭 DuckDB 网关适配器使用的 gRPC 连接。
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

// init creates the VMM tables on the gateway side before the local runtime begins serving traffic.
// init 用于在本地运行时开始提供服务前，让网关侧先创建 VMM 所需的数据表。
func (s *Store) init(ctx context.Context) error {
	// Bootstrap the schema version table first so later boots can apply only missing migrations.
	// 先初始化版本表，让后续启动只需要补齐缺失迁移，而不是每次重跑整套建表脚本。
	if err := s.exec(ctx, versionTableSQL); err != nil {
		return fmt.Errorf("init dockdb version table: %w", err)
	}

	// Load the current schema version and apply forward-only migrations until the runtime catches up.
	// 读取当前 schema 版本，并按顺序执行前向迁移直到追平当前运行时版本。
	version, err := s.loadSchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("load dockdb schema version: %w", err)
	}
	for next := version + 1; next <= currentSchemaVersion; next++ {
		if err := s.applySchemaMigration(ctx, next); err != nil {
			return fmt.Errorf("apply dockdb schema migration v%d: %w", next, err)
		}
		if err := s.saveSchemaVersion(ctx, next); err != nil {
			return fmt.Errorf("persist dockdb schema version v%d: %w", next, err)
		}
	}
	return nil
}

// loadSchemaVersion reads the current VMM schema version row from DockDB and falls back to zero for fresh installs.
// loadSchemaVersion 用于读取 DockDB 中当前 VMM schema 版本；对于首次安装则回退为零。
func (s *Store) loadSchemaVersion(ctx context.Context) (int, error) {
	rows, err := queryJSONRows[schemaVersionRow](ctx, s, `
SELECT schema_version
FROM vmm_version
WHERE singleton_id = 1
LIMIT 1
`)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].SchemaVersion, nil
}

// applySchemaMigration executes one forward-only DockDB schema migration identified by its target version.
// applySchemaMigration 用于执行一条按目标版本编号区分的 DockDB 前向 schema 迁移。
func (s *Store) applySchemaMigration(ctx context.Context, version int) error {
	switch version {
	case 1:
		return s.exec(ctx, schemaV1SQL)
	default:
		return fmt.Errorf("unsupported dockdb schema version: %d", version)
	}
}

// saveSchemaVersion replaces the singleton schema version row after one migration finishes successfully.
// saveSchemaVersion 用于在单条迁移成功后替换单例 schema 版本记录。
func (s *Store) saveSchemaVersion(ctx context.Context, version int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.exec(ctx, `DELETE FROM vmm_version WHERE singleton_id = 1`); err != nil {
		return err
	}
	return s.exec(ctx, `
INSERT INTO vmm_version (singleton_id, schema_version, updated_at)
VALUES (1, ?, ?)
`, version, time.Now().UTC().Format(time.RFC3339Nano))
}

// loadMaxTurnIndex reads the current highest turn index for one session so new normalized turns can append safely.
// loadMaxTurnIndex 用于读取某个会话当前最大的 turn_index，便于新轮次安全追加。
func (s *Store) loadMaxTurnIndex(ctx context.Context, sessionID string) (int, error) {
	rows, err := queryJSONRows[maxTurnIndexRow](ctx, s, `
SELECT COALESCE(MAX(turn_index), 0) AS max_turn_index
FROM vmm_chat_logs
WHERE session_id = ?
`, sessionID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].MaxTurnIndex, nil
}

// loadSessionCreatedAt keeps the original session creation time stable across later refresh operations.
// loadSessionCreatedAt 用于在后续刷新操作中保留最初的会话创建时间。
func (s *Store) loadSessionCreatedAt(ctx context.Context, sessionID string) (string, error) {
	rows, err := queryJSONRows[sessionCreatedAtRow](ctx, s, `
SELECT created_at
FROM vmm_chat_sessions
WHERE session_id = ?
LIMIT 1
`, sessionID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].CreatedAt, nil
}

// exec sends one parameterized or script-style SQL command to the DuckDB gateway with the adapter timeout applied.
// exec 用于把一条参数化或脚本式 SQL 命令发送到 DuckDB 网关，并统一套用适配器超时。
func (s *Store) exec(ctx context.Context, sql string, params ...any) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("dockdb store is not initialized")
	}
	req := &duckdbv1.ExecuteRequest{Sql: strings.TrimSpace(sql)}
	if len(params) > 0 {
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal duckdb params: %w", err)
		}
		req.ParamsJson = string(paramsJSON)
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.ExecuteScript(callCtx, req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// queryJSONRows executes one SELECT-style query and decodes the returned JSON rows into the requested Go shape.
// queryJSONRows 用于执行一条 SELECT 查询，并把返回的 JSON 行解码到请求的 Go 结构中。
func queryJSONRows[T any](ctx context.Context, s *Store, sql string, params ...any) ([]T, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("dockdb store is not initialized")
	}
	req := &duckdbv1.QueryRequest{Sql: strings.TrimSpace(sql)}
	if len(params) > 0 {
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal duckdb params: %w", err)
		}
		req.ParamsJson = string(paramsJSON)
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.QueryJson(callCtx, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(resp.JsonData) == "" {
		return []T{}, nil
	}
	var rows []T
	if err := json.Unmarshal([]byte(resp.JsonData), &rows); err != nil {
		return nil, fmt.Errorf("decode duckdb json rows: %w", err)
	}
	return rows, nil
}

// noiseEmbeddingCacheRow mirrors one cache row returned by the DuckDB gateway JSON query interface.
// noiseEmbeddingCacheRow 用于映射 DuckDB 网关 JSON 查询返回的一条噪声缓存记录。
type noiseEmbeddingCacheRow struct {
	Scope        string `json:"scope"`
	Language     string `json:"language"`
	CategoryName string `json:"category_name"`
	Phrase       string `json:"phrase"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	RulesHash    string `json:"rules_hash"`
	VectorJSON   string `json:"vector_json"`
	UpdatedAt    string `json:"updated_at"`
}

// maxTurnIndexRow captures the single aggregate row used to append turn indexes safely.
// maxTurnIndexRow 用于承接安全追加 turn_index 时使用的聚合查询结果。
type maxTurnIndexRow struct {
	MaxTurnIndex int `json:"max_turn_index"`
}

// sessionCreatedAtRow captures the stored creation timestamp for one existing session row.
// sessionCreatedAtRow 用于承接某个已存在会话记录的创建时间查询结果。
type sessionCreatedAtRow struct {
	CreatedAt string `json:"created_at"`
}

// schemaVersionRow captures the singleton schema version row returned during migration bootstrap.
// schemaVersionRow 用于承接迁移初始化阶段返回的单例 schema 版本记录。
type schemaVersionRow struct {
	SchemaVersion int `json:"schema_version"`
}
