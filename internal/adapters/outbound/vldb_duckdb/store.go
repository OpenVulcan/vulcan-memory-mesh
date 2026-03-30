// store.go implements the DuckDB-gateway outbound adapter used as the durable SQL source of truth.
// store.go 用于实现 DuckDB 网关适配器，并把它作为长期 SQL 事实来源。
package vldb_duckdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_duckdb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// currentSchemaVersion tracks the newest DuckDB schema version understood by this runtime.
	// currentSchemaVersion 用于标记当前运行时理解的最新 DuckDB 表结构版本。
	currentSchemaVersion = 3

	// versionSingletonID pins the schema-version row to one deterministic singleton record.
	// versionSingletonID 用于把 schema 版本记录固定到一条确定性的单例行。
	versionSingletonID = 1
)

const resetManagedSchemaSQL = `
DROP TABLE IF EXISTS vmm_turn_records;
DROP TABLE IF EXISTS vmm_chat_messages;
DROP TABLE IF EXISTS vmm_memory_entries;
DROP TABLE IF EXISTS vmm_sessions;
DROP TABLE IF EXISTS vmm_projects;
DROP TABLE IF EXISTS vmm_spaces;
DROP TABLE IF EXISTS vmm_teams;
DROP TABLE IF EXISTS vmm_users;
DROP TABLE IF EXISTS vmm_noise_embeddings;
`

const currentSchemaSQL = `
CREATE TABLE IF NOT EXISTS vmm_noise_embeddings (
  scope TEXT NOT NULL,
  language TEXT NOT NULL,
  category_name TEXT NOT NULL,
  phrase TEXT NOT NULL,
  model TEXT NOT NULL,
  dimension INTEGER NOT NULL,
  rules_hash TEXT NOT NULL,
  vector_json TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (scope, language, category_name, phrase, model, dimension, rules_hash)
);

CREATE TABLE IF NOT EXISTS vmm_users (
  id BIGINT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  delete_confirm_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_teams (
  id BIGINT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_spaces (
  id BIGINT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(team_id, name),
  FOREIGN KEY(team_id) REFERENCES vmm_teams(id)
);

CREATE TABLE IF NOT EXISTS vmm_projects (
  id BIGINT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(space_id, name),
  FOREIGN KEY(team_id) REFERENCES vmm_teams(id),
  FOREIGN KEY(space_id) REFERENCES vmm_spaces(id)
);

CREATE TABLE IF NOT EXISTS vmm_sessions (
  id BIGINT PRIMARY KEY,
  session_key TEXT NOT NULL,
  user_id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  turn_count INTEGER NOT NULL DEFAULT 0,
  last_summarized_id BIGINT NOT NULL DEFAULT 0,
  summarize_content TEXT NOT NULL DEFAULT '',
  summarize_budget INTEGER NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  UNIQUE(project_id, session_key),
  FOREIGN KEY(user_id) REFERENCES vmm_users(id),
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_scope ON vmm_sessions(user_id, team_id, space_id, project_id, updated_timestamp);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_project_session ON vmm_sessions(project_id, session_key);

CREATE TABLE IF NOT EXISTS vmm_turn_records (
  id BIGINT PRIMARY KEY,
  session_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  dehydrated_content JSON NOT NULL,
  dehydrated_budget INTEGER NOT NULL DEFAULT 0,
  extracted_status TINYINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES vmm_sessions(id),
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_session ON vmm_turn_records(session_id, id);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_project_status ON vmm_turn_records(project_id, extracted_status, id);

CREATE TABLE IF NOT EXISTS vmm_memory_entries (
  id TEXT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  session_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  content TEXT NOT NULL,
  vector_json TEXT NOT NULL DEFAULT '[]',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY(user_id) REFERENCES vmm_users(id),
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id),
  FOREIGN KEY(session_id) REFERENCES vmm_sessions(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_entries_scope ON vmm_memory_entries(user_id, team_id, space_id, project_id, session_id, updated_at);
`

// Store is the DuckDB-gateway adapter used for hierarchy metadata, session/turn persistence, and cache storage.
// Store 用于作为 DuckDB 网关适配器，承接层级元数据、session/turn 持久化以及缓存存储。
type Store struct {
	conn    *grpc.ClientConn
	client  duckdbv1.DuckDbServiceClient
	timeout time.Duration
	writeMu sync.Mutex
}

// NewStore dials the DuckDB gateway and ensures the local schema is initialized before serving traffic.
// NewStore 用于连接 DuckDB 网关，并在对外提供服务前确保本地表结构已经初始化。
func NewStore(address string, timeout time.Duration) (*Store, error) {
	// Validate and normalize the gateway endpoint first so startup failures remain easy to diagnose.
	// 先校验并规范化网关地址，确保启动失败原因保持易于诊断。
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("duckdb address is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// Dial the gateway eagerly so configuration drift is caught during application boot.
	// 以阻塞方式建立网关连接，让配置漂移在应用启动阶段就暴露出来。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		strings.TrimSpace(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial duckdb gateway: %w", err)
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

// Shutdown closes the gRPC client connection used by the DuckDB gateway adapter.
// Shutdown 用于关闭 DuckDB 网关适配器所使用的 gRPC 客户端连接。
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

// init ensures the debugging-stage DuckDB schema always matches the current post-action baseline, resetting managed tables when needed.
// init 用于确保调试阶段 DuckDB schema 始终匹配当前 post-action 基线，并在需要时重置受管表。
func (s *Store) init(ctx context.Context) error {
	// Bootstrap the version table first so startup can decide whether the managed schema needs a full reset.
	// 先引导版本表，让启动流程可以判断当前受管 schema 是否需要整库重置。
	if err := s.exec(ctx, `CREATE TABLE IF NOT EXISTS vmm_version (singleton_id INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL, updated_at TEXT NOT NULL DEFAULT '')`); err != nil {
		return fmt.Errorf("bootstrap schema version table: %w", err)
	}
	rows, err := queryRows[versionRow](s, ctx, `SELECT schema_version FROM vmm_version WHERE singleton_id = 1 LIMIT 1`)
	if err != nil {
		return fmt.Errorf("query schema version: %w", err)
	}
	if len(rows) == 0 || rows[0].SchemaVersion != currentSchemaVersion {
		return s.resetCurrentSchema(ctx)
	}
	return nil
}

// resetCurrentSchema clears the managed DuckDB tables and recreates the current baseline because old debug data is disposable.
// resetCurrentSchema 用于清空受管 DuckDB 表并重建当前基线，因为调试阶段的旧数据可以直接丢弃。
func (s *Store) resetCurrentSchema(ctx context.Context) error {
	// Serialize destructive schema rewrites so concurrent startups never interleave drop/create scripts.
	// 串行化破坏性 schema 重写，避免并发启动时交错执行删表和建表脚本。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, resetManagedSchemaSQL); err != nil {
		return fmt.Errorf("reset managed duckdb schema: %w", err)
	}
	if err := s.exec(ctx, currentSchemaSQL); err != nil {
		return fmt.Errorf("apply current duckdb schema: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_version`); err != nil {
		return fmt.Errorf("clear duckdb schema version row: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_version (singleton_id, schema_version, updated_at) VALUES (?, ?, ?)`, versionSingletonID, currentSchemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("persist duckdb schema version row: %w", err)
	}
	return nil
}

// exec sends one SQL script to the DuckDB gateway with optional JSON parameters.
// exec 用于把一段 SQL 脚本和可选 JSON 参数发送给 DuckDB 网关。
func (s *Store) exec(ctx context.Context, sql string, params ...any) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("duckdb store is not initialized")
	}
	payload, err := marshalParams(params)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.ExecuteScript(callCtx, &duckdbv1.ExecuteRequest{
		Sql:        strings.TrimSpace(sql),
		ParamsJson: payload,
	})
	if err != nil {
		return fmt.Errorf("duckdb execute: %w", err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("duckdb execute: %s", strings.TrimSpace(resp.GetMessage()))
	}
	return nil
}

// queryRows decodes one JSON query response into a typed slice so higher-level methods can stay small and explicit.
// queryRows 用于把 JSON 查询结果解码为强类型切片，让上层方法保持小而明确。
func queryRows[T any](s *Store, ctx context.Context, sql string, params ...any) ([]T, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("duckdb store is not initialized")
	}
	payload, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	resp, err := s.client.QueryJson(callCtx, &duckdbv1.QueryRequest{
		Sql:        strings.TrimSpace(sql),
		ParamsJson: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("duckdb query: %w", err)
	}
	rows := make([]T, 0)
	body := strings.TrimSpace(resp.GetJsonData())
	if body == "" {
		return rows, nil
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		return nil, fmt.Errorf("decode duckdb query rows: %w", err)
	}
	return rows, nil
}

// marshalParams encodes positional SQL parameters into the JSON form expected by the gateway.
// marshalParams 用于把位置参数编码成网关期望的 JSON 形式。
func marshalParams(params []any) (string, error) {
	if len(params) == 0 {
		return "", nil
	}
	body, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal duckdb params: %w", err)
	}
	return string(body), nil
}

// versionRow mirrors the tiny schema-version query payload returned by the gateway.
// versionRow 用于映射网关返回的 schema 版本查询结果。
type versionRow struct {
	SchemaVersion int `json:"schema_version"`
}

// idRow mirrors one MAX(id) style query used to allocate deterministic numeric ids.
// idRow 用于映射 MAX(id) 这类查询结果，以便分配确定性的数字 ID。
type idRow struct {
	NextID uint64 `json:"next_id"`
}

// nextNumericID allocates the next numeric identifier from one table under the adapter write lock.
// nextNumericID 用于在适配器写锁保护下，从某张表里分配下一个数字 ID。
func (s *Store) nextNumericID(ctx context.Context, table string) (uint64, error) {
	rows, err := queryRows[idRow](s, ctx, fmt.Sprintf(`SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM %s`, table))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || rows[0].NextID == 0 {
		return 1, nil
	}
	return rows[0].NextID, nil
}

// generateConfirmationCode returns one 32-character random hex token used by protected destructive flows.
// generateConfirmationCode 用于生成 32 位随机十六进制确认码，服务需要保护的破坏性操作。
func generateConfirmationCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate confirmation code: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// LoadNoiseEmbeddingCache returns one persisted semantic prototype bundle keyed by scope, language, model, dimension, and rules hash.
// LoadNoiseEmbeddingCache 用于返回按作用域、语言、模型、维度和规则哈希定位的一组语义原型缓存。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	rows, err := queryRows[noiseEmbeddingRow](s, ctx, `
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
ORDER BY category_name ASC, phrase ASC
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash))
	if err != nil {
		return nil, fmt.Errorf("load noise embedding cache: %w", err)
	}
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(rows))
	for _, row := range rows {
		updatedAt, _ := time.Parse(time.RFC3339Nano, row.UpdatedAt)
		entries = append(entries, logicdomain.NoiseEmbeddingCacheEntry{
			Scope:        row.Scope,
			Language:     row.Language,
			CategoryName: row.CategoryName,
			Phrase:       row.Phrase,
			Model:        row.Model,
			Dimension:    row.Dimension,
			RulesHash:    row.RulesHash,
			Vector:       decodeFloat32Slice(row.VectorJSON),
			UpdatedAt:    updatedAt,
		})
	}
	return entries, nil
}

// ReplaceNoiseEmbeddingCache rewrites one semantic prototype bundle atomically by deleting the old rows and inserting the new set.
// ReplaceNoiseEmbeddingCache 用于通过“先删后插”原子重写一组语义原型缓存。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, `
DELETE FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash)); err != nil {
		return fmt.Errorf("clear noise embedding cache: %w", err)
	}
	for _, entry := range entries {
		vectorJSON, err := json.Marshal(entry.Vector)
		if err != nil {
			return fmt.Errorf("encode noise embedding cache vector: %w", err)
		}
		if err := s.exec(ctx, `
INSERT INTO vmm_noise_embeddings (
  scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, strings.TrimSpace(entry.Scope), strings.TrimSpace(entry.Language), strings.TrimSpace(entry.CategoryName), strings.TrimSpace(entry.Phrase), strings.TrimSpace(entry.Model), entry.Dimension, strings.TrimSpace(entry.RulesHash), string(vectorJSON), entry.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert noise embedding cache row: %w", err)
		}
	}
	return nil
}

// ResolveRequestScope validates numeric user/project identifiers, resolves hierarchy names, and auto-creates the session row when needed.
// ResolveRequestScope 用于校验数字 user/project 标识、解析层级名称，并在需要时自动创建 session 记录。
func (s *Store) ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	// Reject incomplete numeric identifiers here so PreCheck/PostAction never enter business code with unresolved scopes.
	// 在这里拒绝不完整的数字标识，确保 PreCheck/PostAction 不会带着未解析的范围进入业务代码。
	if strings.TrimSpace(sessionKey) == "" {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if userID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if projectID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}

	user, err := s.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	project, err := s.loadProjectByID(ctx, projectID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	session, err := s.ensureSession(ctx, strings.TrimSpace(sessionKey), user.ID, project)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	return logicdomain.SessionRef{
		SessionID:        session.ID,
		SessionKey:       session.SessionKey,
		UserID:           user.ID,
		TeamID:           project.TeamID,
		SpaceID:          project.SpaceID,
		ProjectID:        project.ID,
		TurnCount:        session.TurnCount,
		LastSummarizedID: session.LastSummarizedID,
		SummarizeContent: session.SummarizeContent,
		SummarizeBudget:  session.SummarizeBudget,
		CreatedAt:        session.CreatedAt,
		UpdatedAt:        session.UpdatedAt,
		UserName:         user.Name,
		TeamName:         project.TeamName,
		SpaceName:        project.SpaceName,
		ProjectName:      project.Name,
	}, nil
}

// AppendTurnRecord persists one cleaned turn into the resolved session and increments the session turn counter in the same write path.
// AppendTurnRecord 用于把一条清洗后的 turn 写入已解析的 session，并在同一路径里递增会话 turn 计数。
func (s *Store) AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) error {
	if strings.TrimSpace(turn.UserContent) == "" && strings.TrimSpace(turn.AssistantContent) == "" && len(turn.Timeline) == 0 {
		return nil
	}
	if session.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}

	// Serialize turn writes so session counters and turn rows stay consistent inside one DuckDB transaction boundary.
	// 串行化 turn 写入，确保 session 计数和 turn 行在同一个 DuckDB 写入边界内保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_turn_records")
	if err != nil {
		return fmt.Errorf("allocate turn record id: %w", err)
	}
	dehydratedContent, dehydratedBudget, err := buildDehydratedTurn(turn)
	if err != nil {
		return err
	}
	nowMs := time.Now().UTC().UnixMilli()
	createdMs := nowMs
	if !turn.CreatedAt.IsZero() {
		createdMs = turn.CreatedAt.UTC().UnixMilli()
	}
	if err := s.exec(ctx, buildTurnInsertSQL(nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs)); err != nil {
		return fmt.Errorf("insert turn record: %w", err)
	}
	if err := s.exec(ctx, buildSessionTurnUpdateSQL(session.SessionID, dehydratedBudget, nowMs)); err != nil {
		return fmt.Errorf("update session turn counters: %w", err)
	}
	return nil
}

// loadUserByID resolves one durable user row by numeric id and converts absence into a stable domain not-found error.
// loadUserByID 用于按数字 ID 解析长期用户记录，并把不存在情况转换成稳定的领域 not-found 错误。
func (s *Store) loadUserByID(ctx context.Context, userID uint64) (logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE id = ?
LIMIT 1
`, userID)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("query user: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user_id %d does not exist", userID)}
	}
	return rows[0].toDomain(), nil
}

// loadProjectByID resolves one project together with its parent team and space names.
// loadProjectByID 用于解析单个项目，以及它对应的 team 和 space 名称。
func (s *Store) loadProjectByID(ctx context.Context, projectID uint64) (logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.id = ?
LIMIT 1
`, projectID)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("query project: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project_id %d does not exist", projectID)}
	}
	return rows[0].toDomain(), nil
}

// ensureSession loads one existing session inside the target project or creates it under the resolved hierarchy when it does not exist yet.
// ensureSession 用于在目标项目内按外部 session_key 读取 session；如果还不存在，则在已解析层级下创建一条新 session。
func (s *Store) ensureSession(ctx context.Context, sessionKey string, userID uint64, project logicdomain.ProjectRecord) (logicdomain.SessionRecord, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count, last_summarized_id, summarize_content, summarize_budget, created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE project_id = ? AND session_key = ?
LIMIT 1
`, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("query session: %w", err)
	}
	if len(rows) > 0 {
		// Reuse the project-scoped session row directly even when the upstream plugin has switched user ids during debugging.
		// 在调试阶段，即使上游插件手动切换了 user_id，也直接复用同一 project 下的 session 行。
		return rows[0].toDomain(), nil
	}

	// Auto-create the session row only after user and project are both confirmed to exist.
	// 只有在 user 和 project 都确认存在之后，才自动创建 session 行。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nextID, err := s.nextNumericID(ctx, "vmm_sessions")
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("allocate session id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	if err := s.exec(ctx, `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id, turn_count, last_summarized_id, summarize_content, summarize_budget, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 0, 0, '', 0, ?, ?)
`, nextID, sessionKey, userID, project.TeamID, project.SpaceID, project.ID, nowMs, nowMs); err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("insert session: %w", err)
	}
	return logicdomain.SessionRecord{
		ID:               nextID,
		SessionKey:       sessionKey,
		UserID:           userID,
		TeamID:           project.TeamID,
		SpaceID:          project.SpaceID,
		ProjectID:        project.ID,
		TurnCount:        0,
		LastSummarizedID: 0,
		SummarizeContent: "",
		SummarizeBudget:  0,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// ListProjects returns all projects together with their display path components, ordered for deterministic UX.
// ListProjects 用于返回全部项目及其展示路径组成部分，并按稳定顺序输出。
func (s *Store) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
ORDER BY t.name ASC, sp.name ASC, p.name ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]logicdomain.ProjectRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// ResolveProjectRef resolves either a numeric project id or a canonical Team/Space/Project path.
// ResolveProjectRef 用于解析数字 project id，或标准的 Team/Space/Project 路径。
func (s *Store) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return logicdomain.ProjectRecord{}, logicdomain.ValidationError{Field: "project_ref", Message: "is required"}
	}
	if projectID, ok := parseUint64(projectRef); ok {
		return s.loadProjectByID(ctx, projectID)
	}
	teamName, spaceName, projectName, err := parseProjectPath(projectRef)
	if err != nil {
		return logicdomain.ProjectRecord{}, err
	}
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE t.name = ? AND sp.name = ? AND p.name = ?
LIMIT 1
`, teamName, spaceName, projectName)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("resolve project path: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project path %q does not exist", projectRef)}
	}
	return rows[0].toDomain(), nil
}

// ListProjectMemories returns memory rows under one project so migrations can rebuild vector rows from durable SQL data.
// ListProjectMemories 用于返回某个项目下的记忆条目，让迁移流程可以从长期 SQL 数据重建向量行。
func (s *Store) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	rows, err := queryRows[memoryEntryRow](s, ctx, `
SELECT id, team_id, space_id, project_id, session_id, user_id, content, vector_json, metadata_json, created_at
FROM vmm_memory_entries
WHERE project_id = ?
ORDER BY updated_at ASC, id ASC
`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project memories: %w", err)
	}
	out := make([]logicdomain.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// EnsureProjectPath resolves or creates a Team/Space/Project path according to the confirm flag rules required by the admin RPCs.
// EnsureProjectPath 用于按管理 RPC 约定的确认规则，解析或创建一个 Team/Space/Project 路径。
func (s *Store) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	teamName, spaceName, projectName, err := parseProjectPath(projectPath)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if existing, err := s.ResolveProjectRef(ctx, projectPath); err == nil {
		return logicdomain.ProjectMutationResult{
			Project: existing,
			Message: fmt.Sprintf("project %s already exists", existing.Path()),
			Exists:  true,
		}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.ProjectMutationResult{}, err
	}

	team, teamExists, err := s.lookupTeamByName(ctx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err := s.lookupSpaceByName(ctx, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      buildProjectConfirmMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	createdTeam := false
	createdSpace := false
	createdProject := false
	if !teamExists {
		team, err = s.insertTeam(ctx, teamName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdTeam = true
	}
	if !spaceExists {
		space, err = s.insertSpace(ctx, team.ID, spaceName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdSpace = true
	}
	project, err := s.insertProject(ctx, team, space, projectName, now)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	createdProject = true
	return logicdomain.ProjectMutationResult{
		Project:        project,
		Message:        fmt.Sprintf("project %s created", project.Path()),
		CreatedTeam:    createdTeam,
		CreatedSpace:   createdSpace,
		CreatedProject: createdProject,
	}, nil
}

// DeleteProjectPath deletes one resolved project plus its sessions, turn records, and SQL-backed memories when confirmation is explicit.
// DeleteProjectPath 用于在确认删除时，删除一个已解析项目及其 sessions、turn 记录和 SQL 侧记忆。
func (s *Store) DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	project, err := s.ResolveProjectRef(ctx, projectPath)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if !confirmDelete {
		return logicdomain.ProjectDeleteResult{
			Project:      project,
			Message:      fmt.Sprintf("confirm delete project %s", project.Path()),
			NeedsConfirm: true,
		}, nil
	}

	sessions, messages, memories, err := s.countProjectRows(ctx, project.ID)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.exec(ctx, `DELETE FROM vmm_turn_records WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project turn records: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_sessions WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project sessions: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_memory_entries WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project memories: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_projects WHERE id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project row: %w", err)
	}
	return logicdomain.ProjectDeleteResult{
		Project:         project,
		Message:         fmt.Sprintf("project %s deleted", project.Path()),
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
	}, nil
}

// MigrateProjectPath moves SQL-backed sessions/turn records/memories from one project scope onto another when explicitly confirmed.
// MigrateProjectPath 用于在显式确认后，把 SQL 侧的 sessions/turn 记录/memories 从源项目范围迁移到目标项目范围。
func (s *Store) MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	source, err := s.ResolveProjectRef(ctx, sourcePath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err := s.ResolveProjectRef(ctx, targetPath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	if source.ID == target.ID {
		return logicdomain.ProjectMigrationResult{}, logicdomain.ConflictError{Resource: "project", Message: "source and target project are the same"}
	}
	if !confirm {
		return logicdomain.ProjectMigrationResult{
			Source:       source,
			Target:       target,
			Message:      fmt.Sprintf("confirm migrate project %s -> %s", source.Path(), target.Path()),
			NeedsConfirm: true,
		}, nil
	}

	sessions, messages, memories, err := s.countProjectRows(ctx, source.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nowMs := time.Now().UTC().UnixMilli()
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_sessions
SET team_id = %d, space_id = %d, project_id = %d, updated_timestamp = %d
WHERE project_id = %d
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate sessions: %w", err)
	}
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_turn_records
SET project_id = %d, updated_timestamp = %d
WHERE project_id = %d
`, target.ID, nowMs, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate turn records: %w", err)
	}
	if err := s.exec(ctx, `
UPDATE vmm_memory_entries
SET team_id = ?, space_id = ?, project_id = ?, updated_at = ?
WHERE project_id = ?
`, target.TeamID, target.SpaceID, target.ID, time.Now().UTC().Format(time.RFC3339Nano), source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate memories: %w", err)
	}
	return logicdomain.ProjectMigrationResult{
		Source:           source,
		Target:           target,
		Message:          fmt.Sprintf("migrated project %s -> %s", source.Path(), target.Path()),
		MigratedSessions: sessions,
		MigratedMessages: messages,
		MigratedMemories: memories,
	}, nil
}

// ResolveUserRef resolves either a numeric user id or a unique user name.
// ResolveUserRef 用于解析数字 user id，或唯一用户名。
func (s *Store) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	userRef = strings.TrimSpace(userRef)
	if userRef == "" {
		return logicdomain.UserRecord{}, logicdomain.ValidationError{Field: "user_ref", Message: "is required"}
	}
	if userID, ok := parseUint64(userRef); ok {
		return s.loadUserByID(ctx, userID)
	}
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE name = ?
LIMIT 1
`, userRef)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("resolve user by name: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist", userRef)}
	}
	return rows[0].toDomain(), nil
}

// EnsureUserName resolves one user by name or creates it when confirmation is explicit.
// EnsureUserName 用于按名称解析用户，或在确认创建时补建该用户。
func (s *Store) EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return logicdomain.UserResolveResult{}, logicdomain.ValidationError{Field: "user_name", Message: "is required"}
	}
	if user, err := s.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s already exists", user.Name), Exists: true}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	if !confirmCreate {
		return logicdomain.UserResolveResult{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist; use confirm_create=1 to create it", userName)}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	nextID, err := s.nextNumericID(ctx, "vmm_users")
	if err != nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("allocate user id: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_users (id, name, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', ?, ?)
`, nextID, userName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("insert user: %w", err)
	}
	return logicdomain.UserResolveResult{
		User:    logicdomain.UserRecord{ID: nextID, Name: userName, CreatedAt: now, UpdatedAt: now},
		Message: fmt.Sprintf("user %s created", userName),
		Created: true,
	}, nil
}

// ListUsers returns all durable users ordered by id for deterministic management output.
// ListUsers 用于按 id 稳定输出所有长期用户。
func (s *Store) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, delete_confirm_code, created_at, updated_at
FROM vmm_users
ORDER BY id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]logicdomain.UserRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// DeleteUserRef executes the protected two-phase user deletion flow and removes the user plus all SQL-side dependent data.
// DeleteUserRef 用于执行受保护的双阶段用户删除流程，并删除该用户及其所有 SQL 侧关联数据。
func (s *Store) DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	user, err := s.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if strings.TrimSpace(confirmationCode) == "" || strings.TrimSpace(confirmationCode) != strings.TrimSpace(user.DeleteConfirmCode) {
		code, err := generateConfirmationCode()
		if err != nil {
			return logicdomain.UserDeleteResult{}, err
		}
		if err := s.exec(ctx, `UPDATE vmm_users SET delete_confirm_code = ?, updated_at = ? WHERE id = ?`, code, time.Now().UTC().Format(time.RFC3339Nano), user.ID); err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("persist user delete confirmation code: %w", err)
		}
		user.DeleteConfirmCode = code
		return logicdomain.UserDeleteResult{
			User:                 user,
			Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", user.Name),
			RequiresConfirmation: true,
			ConfirmationCode:     code,
		}, nil
	}

	sessions, messages, memories, err := s.countUserRows(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.exec(ctx, `DELETE FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, user.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user turn records: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_sessions WHERE user_id = ?`, user.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user sessions: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_memory_entries WHERE user_id = ?`, user.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user memories: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_users WHERE id = ?`, user.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user row: %w", err)
	}
	return logicdomain.UserDeleteResult{
		User:            user,
		Message:         fmt.Sprintf("user %s deleted", user.Name),
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
	}, nil
}

// lookupTeamByName resolves one team name and reports whether it already exists.
// lookupTeamByName 用于解析单个 team 名称，并返回它是否已经存在。
func (s *Store) lookupTeamByName(ctx context.Context, teamName string) (logicdomain.TeamRecord, bool, error) {
	rows, err := queryRows[teamRow](s, ctx, `
SELECT id, name, created_at, updated_at
FROM vmm_teams
WHERE name = ?
LIMIT 1
`, teamName)
	if err != nil {
		return logicdomain.TeamRecord{}, false, fmt.Errorf("lookup team: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.TeamRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// lookupSpaceByName resolves one space under the given team and reports whether it already exists.
// lookupSpaceByName 用于在给定 team 下解析单个 space，并返回它是否已经存在。
func (s *Store) lookupSpaceByName(ctx context.Context, teamID uint64, spaceName string, teamExists bool) (logicdomain.SpaceRecord, bool, error) {
	if !teamExists {
		return logicdomain.SpaceRecord{}, false, nil
	}
	rows, err := queryRows[spaceRow](s, ctx, `
SELECT id, team_id, name, created_at, updated_at
FROM vmm_spaces
WHERE team_id = ? AND name = ?
LIMIT 1
`, teamID, spaceName)
	if err != nil {
		return logicdomain.SpaceRecord{}, false, fmt.Errorf("lookup space: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SpaceRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// insertTeam creates one missing team node under the write lock.
// insertTeam 用于在写锁保护下创建缺失的 team 节点。
func (s *Store) insertTeam(ctx context.Context, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_teams")
	if err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("allocate team id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_teams (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, nextID, teamName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("insert team: %w", err)
	}
	return logicdomain.TeamRecord{ID: nextID, Name: teamName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertSpace creates one missing space node under an existing team.
// insertSpace 用于在已存在的 team 下创建缺失的 space 节点。
func (s *Store) insertSpace(ctx context.Context, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_spaces")
	if err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("allocate space id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_spaces (id, team_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, nextID, teamID, spaceName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("insert space: %w", err)
	}
	return logicdomain.SpaceRecord{ID: nextID, TeamID: teamID, Name: spaceName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertProject creates one missing project node under the resolved Team/Space hierarchy.
// insertProject 用于在已解析的 Team/Space 层级下创建缺失的 project 节点。
func (s *Store) insertProject(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_projects")
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("allocate project id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_projects (id, team_id, space_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, nextID, team.ID, space.ID, projectName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("insert project: %w", err)
	}
	return logicdomain.ProjectRecord{
		ID:        nextID,
		TeamID:    team.ID,
		SpaceID:   space.ID,
		TeamName:  team.Name,
		SpaceName: space.Name,
		Name:      projectName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// countProjectRows returns project-scoped row counts so delete and migrate operations can report meaningful summaries.
// countProjectRows 用于返回项目范围内的行计数，让删除和迁移操作能够输出有意义的结果摘要。
func (s *Store) countProjectRows(ctx context.Context, projectID uint64) (int, int, int, error) {
	sessionRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project sessions: %w", err)
	}
	messageRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project turn records: %w", err)
	}
	memoryRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_memory_entries WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project memories: %w", err)
	}
	return sessionRows[0].Count, messageRows[0].Count, memoryRows[0].Count, nil
}

// countUserRows returns user-scoped row counts so protected user deletion can explain what will be removed.
// countUserRows 用于返回用户范围内的行计数，让受保护的用户删除能够说明将要删除的内容。
func (s *Store) countUserRows(ctx context.Context, userID uint64) (int, int, int, error) {
	sessionRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user sessions: %w", err)
	}
	messageRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user turn records: %w", err)
	}
	memoryRows, err := queryRows[countRow](s, ctx, `SELECT COUNT(*) AS count FROM vmm_memory_entries WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user memories: %w", err)
	}
	return sessionRows[0].Count, messageRows[0].Count, memoryRows[0].Count, nil
}

// buildProjectConfirmMessage generates the stable confirmation text returned when missing Team/Space nodes require explicit confirmation.
// buildProjectConfirmMessage 用于生成稳定的确认提示文本，说明缺失 Team/Space 节点需要显式确认。
func buildProjectConfirmMessage(teamName, spaceName, projectName string, teamExists, spaceExists bool) string {
	missing := make([]string, 0, 2)
	if !teamExists {
		missing = append(missing, "team")
	}
	if !spaceExists {
		missing = append(missing, "space")
	}
	return fmt.Sprintf("path %s/%s/%s is incomplete; missing %s, use confirm_create=1 to create them", teamName, spaceName, projectName, strings.Join(missing, ", "))
}

// parseProjectPath enforces the canonical Team/Space/Project path format required by the admin RPCs.
// parseProjectPath 用于强制要求管理 RPC 使用标准的 Team/Space/Project 路径格式。
func parseProjectPath(path string) (string, string, string, error) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	if len(parts) != 3 {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	teamName := strings.TrimSpace(parts[0])
	spaceName := strings.TrimSpace(parts[1])
	projectName := strings.TrimSpace(parts[2])
	if teamName == "" || spaceName == "" || projectName == "" {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	return teamName, spaceName, projectName, nil
}

// parseUint64 parses one numeric identifier string and reports whether the conversion succeeded.
// parseUint64 用于解析数字标识字符串，并返回转换是否成功。
func parseUint64(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
}

// decodeFloat32Slice restores one vector payload from JSON and falls back to an empty slice on malformed data.
// decodeFloat32Slice 用于从 JSON 还原向量载荷，并在数据损坏时退回空切片。
func decodeFloat32Slice(raw string) []float32 {
	if strings.TrimSpace(raw) == "" {
		return []float32{}
	}
	var out []float32
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []float32{}
	}
	return out
}

// decodeStringMap restores one metadata map from JSON and falls back to an empty map on malformed data.
// decodeStringMap 用于从 JSON 还原元数据 map，并在数据损坏时退回空 map。
func decodeStringMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// dehydratedTurnPayload mirrors the JSON document persisted into vmm_turn_records after one cleaned turn is flattened into a stable analysis unit.
// dehydratedTurnPayload 用于映射写入 vmm_turn_records 的 JSON 文档，此时一条清洗后的 turn 已经被压平成稳定分析单元。
type dehydratedTurnPayload struct {
	User      string                       `json:"user"`
	Timeline  []dehydratedTurnTimelineItem `json:"timeline"`
	Assistant string                       `json:"assistant"`
}

// dehydratedTurnTimelineItem stores one middle timeline node inside the dehydrated turn JSON payload.
// dehydratedTurnTimelineItem 用于保存脱水 turn JSON 载荷中的一条中间 timeline 节点。
type dehydratedTurnTimelineItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// buildDehydratedTurn converts one cleaned turn into the persisted JSON payload and estimates its token budget.
// buildDehydratedTurn 用于把一条清洗后的 turn 转换成持久化 JSON 载荷，并估算对应的 token 预算。
func buildDehydratedTurn(turn logicdomain.TurnRecord) (string, int, error) {
	// Preserve the storage-ready timeline exactly as the upstream sanitizer produced it because media/noise filtering already happened earlier.
	// 原样保留上游清洗后的 timeline 文本，因为媒体与噪声过滤已经在更前面的阶段完成。
	timeline := make([]dehydratedTurnTimelineItem, 0, len(turn.Timeline))
	for _, item := range turn.Timeline {
		timeline = append(timeline, dehydratedTurnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	payload := dehydratedTurnPayload{
		User:      strings.TrimSpace(turn.UserContent),
		Timeline:  timeline,
		Assistant: strings.TrimSpace(turn.AssistantContent),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("marshal dehydrated turn: %w", err)
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return string(body), estimator.Estimate(string(body)), nil
}

// buildTurnInsertSQL renders one raw INSERT statement so the gateway avoids the buggy optional-pointer update path seen during debug runs.
// buildTurnInsertSQL 用于渲染原始 INSERT 语句，让网关绕开调试阶段已出现过的 optional-pointer 更新故障路径。
func buildTurnInsertSQL(id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) string {
	return fmt.Sprintf(`
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, created_timestamp, updated_timestamp
) VALUES (%d, %d, %d, CAST(%s AS JSON), %d, 0, %d, %d)
`, id, sessionID, projectID, sqlStringLiteral(dehydratedContent), dehydratedBudget, createdMs, updatedMs)
}

// buildSessionTurnUpdateSQL renders one raw UPDATE that increments the turn counter and the unsummarized token budget after a turn row is inserted successfully.
// buildSessionTurnUpdateSQL 用于渲染原始 UPDATE，在 turn 行成功插入后同步递增 turn 计数和未总结 token 预算。
func buildSessionTurnUpdateSQL(sessionID uint64, addedBudget int, updatedMs int64) string {
	return fmt.Sprintf(`
UPDATE vmm_sessions
SET turn_count = turn_count + 1, summarize_budget = summarize_budget + %d, updated_timestamp = %d
WHERE id = %d
`, addedBudget, updatedMs, sessionID)
}

// sqlStringLiteral escapes one string into a single-quoted SQL literal for debug-stage raw statement rendering.
// sqlStringLiteral 用于把字符串转成单引号 SQL 字面量，服务调试阶段的原始语句渲染。
func sqlStringLiteral(raw string) string {
	return "'" + strings.ReplaceAll(raw, "'", "''") + "'"
}

// unixMilliToTime converts one millisecond unix timestamp back into UTC time and tolerates zero values.
// unixMilliToTime 用于把毫秒 unix 时间戳转回 UTC time，并容忍零值。
func unixMilliToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

type noiseEmbeddingRow struct {
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

type userRow struct {
	ID                uint64 `json:"id"`
	Name              string `json:"name"`
	DeleteConfirmCode string `json:"delete_confirm_code"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (r userRow) toDomain() logicdomain.UserRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.UserRecord{
		ID:                r.ID,
		Name:              r.Name,
		DeleteConfirmCode: r.DeleteConfirmCode,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
}

type teamRow struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r teamRow) toDomain() logicdomain.TeamRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.TeamRecord{ID: r.ID, Name: r.Name, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type spaceRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r spaceRow) toDomain() logicdomain.SpaceRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.SpaceRecord{ID: r.ID, TeamID: r.TeamID, Name: r.Name, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type projectJoinRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	SpaceID   uint64 `json:"space_id"`
	Name      string `json:"name"`
	TeamName  string `json:"team_name"`
	SpaceName string `json:"space_name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r projectJoinRow) toDomain() logicdomain.ProjectRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.ProjectRecord{
		ID:        r.ID,
		TeamID:    r.TeamID,
		SpaceID:   r.SpaceID,
		TeamName:  r.TeamName,
		SpaceName: r.SpaceName,
		Name:      r.Name,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

type sessionRow struct {
	ID               uint64 `json:"id"`
	SessionKey       string `json:"session_key"`
	UserID           uint64 `json:"user_id"`
	TeamID           uint64 `json:"team_id"`
	SpaceID          uint64 `json:"space_id"`
	ProjectID        uint64 `json:"project_id"`
	TurnCount        int    `json:"turn_count"`
	LastSummarizedID uint64 `json:"last_summarized_id"`
	SummarizeContent string `json:"summarize_content"`
	SummarizeBudget  int    `json:"summarize_budget"`
	CreatedTimestamp int64  `json:"created_timestamp"`
	UpdatedTimestamp int64  `json:"updated_timestamp"`
}

func (r sessionRow) toDomain() logicdomain.SessionRecord {
	return logicdomain.SessionRecord{
		ID:               r.ID,
		SessionKey:       r.SessionKey,
		UserID:           r.UserID,
		TeamID:           r.TeamID,
		SpaceID:          r.SpaceID,
		ProjectID:        r.ProjectID,
		TurnCount:        r.TurnCount,
		LastSummarizedID: r.LastSummarizedID,
		SummarizeContent: r.SummarizeContent,
		SummarizeBudget:  r.SummarizeBudget,
		CreatedAt:        unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:        unixMilliToTime(r.UpdatedTimestamp),
	}
}

type memoryEntryRow struct {
	ID           string `json:"id"`
	TeamID       uint64 `json:"team_id"`
	SpaceID      uint64 `json:"space_id"`
	ProjectID    uint64 `json:"project_id"`
	SessionID    uint64 `json:"session_id"`
	UserID       uint64 `json:"user_id"`
	Content      string `json:"content"`
	VectorJSON   string `json:"vector_json"`
	MetadataJSON string `json:"metadata_json"`
	CreatedAt    string `json:"created_at"`
}

func (r memoryEntryRow) toDomain() logicdomain.MemoryRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	return logicdomain.MemoryRecord{
		ID:     r.ID,
		Text:   r.Content,
		Vector: decodeFloat32Slice(r.VectorJSON),
		Filter: logicdomain.SearchFilter{
			UserID:    r.UserID,
			TeamID:    r.TeamID,
			SpaceID:   r.SpaceID,
			ProjectID: r.ProjectID,
			SessionID: r.SessionID,
		},
		Metadata:  decodeStringMap(r.MetadataJSON),
		CreatedAt: createdAt,
	}
}

type countRow struct {
	Count int `json:"count"`
}
