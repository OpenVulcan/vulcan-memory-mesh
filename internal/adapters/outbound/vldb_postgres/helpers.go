// helpers.go centralizes shared PostgreSQL combined-store helpers so dialect routing, schema bootstrap, and query methods can stay small and consistent.
// helpers.go 用于集中 PostgreSQL 组合库共享辅助逻辑，让方言路由、schema 启动和查询方法保持简洁且一致。
package vldb_postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	// bootstrapTimeoutFloor keeps schema bootstrap and index initialization from inheriting an unrealistically small request-time timeout.
	// bootstrapTimeoutFloor 用于避免 schema 启动和索引初始化直接继承过小的请求超时，导致启动阶段不稳定。
	bootstrapTimeoutFloor = 30 * time.Second

	// debugSeedDefaultName keeps the initial hierarchy/user seed deterministic so fresh combined-mode databases still bootstrap the same default path by name.
	// debugSeedDefaultName 用于保持初始层级和用户种子数据在名称层面上的确定性，让全新组合模式数据库始终补出相同的默认路径。
	debugSeedDefaultName = "default"
)

// sqlArgsBuilder keeps positional PostgreSQL placeholders and argument slices aligned while dynamic filters are assembled incrementally.
// sqlArgsBuilder 用于在逐步拼装动态过滤条件时保持 PostgreSQL 位置参数占位符和参数切片严格对齐。
type sqlArgsBuilder struct {
	args []any
}

// Add appends one argument and returns its PostgreSQL placeholder token.
// Add 用于追加一个参数，并返回对应的 PostgreSQL 占位符。
func (b *sqlArgsBuilder) Add(value any) string {
	b.args = append(b.args, value)
	return fmt.Sprintf("$%d", len(b.args))
}

// Args returns the accumulated argument slice in placeholder order.
// Args 用于按占位符顺序返回累计参数切片。
func (b *sqlArgsBuilder) Args() []any {
	return append([]any(nil), b.args...)
}

// qualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema.
// qualifiedTable 用于返回当前配置 schema 下的完整限定表名。
func (s *Store) qualifiedTable(name string) string {
	return quoteIdentifier(s.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// memoryNodesTable returns the fully-qualified durable memory table name used by the combined store.
// memoryNodesTable 用于返回组合库使用的统一长期记忆表的完整限定名称。
func (s *Store) memoryNodesTable() string {
	return s.qualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (s *Store) memoryContextEdgesTable() string {
	return s.qualifiedTable("vmm_memory_context_edges")
}

// usersTable returns the fully-qualified durable user table name.
// usersTable 用于返回长期用户表的完整限定名称。
func (s *Store) usersTable() string {
	return s.qualifiedTable("vmm_users")
}

// teamsTable returns the fully-qualified durable team table name.
// teamsTable 用于返回长期 team 表的完整限定名称。
func (s *Store) teamsTable() string {
	return s.qualifiedTable("vmm_teams")
}

// spacesTable returns the fully-qualified durable space table name.
// spacesTable 用于返回长期 space 表的完整限定名称。
func (s *Store) spacesTable() string {
	return s.qualifiedTable("vmm_spaces")
}

// projectsTable returns the fully-qualified durable project table name.
// projectsTable 用于返回长期 project 表的完整限定名称。
func (s *Store) projectsTable() string {
	return s.qualifiedTable("vmm_projects")
}

// sessionsTable returns the fully-qualified durable session table name.
// sessionsTable 用于返回长期 session 表的完整限定名称。
func (s *Store) sessionsTable() string {
	return s.qualifiedTable("vmm_sessions")
}

// noiseEmbeddingsTable returns the fully-qualified semantic cache table name.
// noiseEmbeddingsTable 用于返回语义缓存表的完整限定名称。
func (s *Store) noiseEmbeddingsTable() string {
	return s.qualifiedTable("vmm_noise_embeddings")
}

// profileNodesTable returns the fully-qualified durable profile-node table name.
// profileNodesTable 用于返回长期画像节点表的完整限定名称。
func (s *Store) profileNodesTable() string {
	return s.qualifiedTable("vmm_profile_nodes")
}

// profileInstructionsTable returns the fully-qualified manual profile-instruction table name.
// profileInstructionsTable 用于返回手工画像指令表的完整限定名称。
func (s *Store) profileInstructionsTable() string {
	return s.qualifiedTable("vmm_profile_instructions")
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (s *Store) turnsTable() string {
	return s.qualifiedTable("vmm_turn_records")
}

// recycleBatchesTable returns the fully-qualified recycle-batch table name used by PostgreSQL retention maintenance.
// recycleBatchesTable 用于返回 PostgreSQL retention 维护使用的回收批次表完整限定名称。
func (s *Store) recycleBatchesTable() string {
	return s.qualifiedTable("vmm_recycle_batches")
}

// recycleJobsTable returns the fully-qualified recycle-job table name used by the cold-turn scan/claim/execute queue.
// recycleJobsTable 用于返回冷 turn 扫描/领取/执行队列使用的回收任务表完整限定名称。
func (s *Store) recycleJobsTable() string {
	return s.qualifiedTable("vmm_recycle_jobs")
}

// vectorGCJobsTable returns the fully-qualified vector-gc job table name used to bridge SQL transactions and async vector deletion.
// vectorGCJobsTable 用于返回向量 GC 任务表的完整限定名称，承接 SQL 事务与异步向量删除之间的衔接。
func (s *Store) vectorGCJobsTable() string {
	return s.qualifiedTable("vmm_vector_gc_jobs")
}

// memoryNodesTrashTable returns the fully-qualified memory-trash table name used by retention maintenance.
// memoryNodesTrashTable 用于返回 retention 维护使用的记忆回收站表完整限定名称。
func (s *Store) memoryNodesTrashTable() string {
	return s.qualifiedTable("vmm_memory_nodes_trash")
}

// memoryContextEdgesTrashTable returns the fully-qualified memory-context-edge trash table name used by retention maintenance.
// memoryContextEdgesTrashTable 用于返回 retention 维护使用的记忆情境边回收站表完整限定名称。
func (s *Store) memoryContextEdgesTrashTable() string {
	return s.qualifiedTable("vmm_memory_context_edges_trash")
}

// turnsTrashTable returns the fully-qualified turn-trash table name used by idle-session retention maintenance.
// turnsTrashTable 用于返回 idle-session retention 维护使用的 turn 回收站表完整限定名称。
func (s *Store) turnsTrashTable() string {
	return s.qualifiedTable("vmm_turn_records_trash")
}

// bootstrapContext derives a startup-oriented timeout so schema bootstrap and dialect index creation are less brittle than regular request-time queries.
// bootstrapContext 用于派生面向启动阶段的超时，让 schema 启动和方言索引创建相比普通请求查询更稳健。
func (s *Store) bootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// maintenanceReadContext derives a maintenance-oriented read timeout so project-memory exports and vector-rebuild fact scans can run longer than ordinary online requests without becoming unbounded.
// maintenanceReadContext 用于派生面向维护读取的超时，让项目记忆导出和向量重建事实扫描可以长于普通在线请求，但又不会变成无界等待。
func (s *Store) maintenanceReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.cfg.MaintenanceReadTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceReadTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// maintenanceWriteContext derives a maintenance-oriented long-transaction timeout so destructive rebuild/import workflows do not inherit startup or request budgets that are too small for large datasets.
// maintenanceWriteContext 用于派生面向维护长事务的超时，让破坏性重建/导入流程不会继承对大数据集来说过小的启动期或请求期预算。
func (s *Store) maintenanceWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.cfg.MaintenanceWriteTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceWriteTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// normalizeUint64List removes zero values and duplicates while keeping a deterministic ascending order for SQL IN/ANY queries.
// normalizeUint64List 用于移除零值和重复项，并保持确定性的升序，供 SQL IN/ANY 查询复用。
func normalizeUint64List(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

// collectTurnAnalysisSupersedeMemoryIDs unions reviewer-approved supersede ids from surviving memory nodes so PostgreSQL persistence only retires memories that still have one accepted replacement.
// collectTurnAnalysisSupersedeMemoryIDs 用于从存活记忆节点中汇总 reviewer 批准的 supersede id，确保 PostgreSQL 持久化只退役那些仍被已接纳新节点替代的旧记忆。
func collectTurnAnalysisSupersedeMemoryIDs(nodes []logicdomain.MemoryNodeCandidate) []uint64 {
	if len(nodes) == 0 {
		return nil
	}
	merged := make([]uint64, 0, len(nodes))
	for _, node := range nodes {
		merged = append(merged, node.SupersedeMemoryIDs...)
	}
	return normalizeUint64List(merged)
}

// normalizeStringList removes blank values and duplicates while keeping a deterministic ascending order for SQL IN/ANY queries.
// normalizeStringList 用于移除空值和重复项，并保持确定性的升序，供 SQL IN/ANY 查询复用。
func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// toInt64List converts numeric identifiers into PostgreSQL-friendly bigint arrays for ANY($n) bindings.
// toInt64List 用于把数字标识转换成 PostgreSQL 友好的 bigint 数组，供 ANY($n) 绑定使用。
func toInt64List(values []uint64) []int64 {
	if len(values) == 0 {
		return nil
	}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		out = append(out, int64(value))
	}
	return out
}

// chooseNonZeroTime preserves a provided timestamp when present, otherwise falls back to the supplied default time.
// chooseNonZeroTime 用于在时间戳已提供时保留原值，否则回退到指定默认时间。
func chooseNonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

// defaultUnifiedMemoryExpiry derives the default retention horizon used when callers omit an explicit expiry for one unified memory row.
// defaultUnifiedMemoryExpiry 用于在调用方省略显式过期时间时，为统一记忆行推导默认保留时长。
func defaultUnifiedMemoryExpiry(scopeLevel int, now time.Time) time.Time {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return now.UTC().Add(15 * 24 * time.Hour)
	case logicdomain.MemoryScopeLevelUser:
		return now.UTC().Add(365 * 24 * time.Hour)
	default:
		return now.UTC().Add(180 * 24 * time.Hour)
	}
}

// normalizeDirectMemoryNodeRecord fills direct-write defaults before the record is persisted into the shared combined-store table.
// normalizeDirectMemoryNodeRecord 用于在记录写入共享组合库表前补齐主动写记忆的默认值。
func normalizeDirectMemoryNodeRecord(session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, now time.Time) logicdomain.MemoryNodeRecord {
	record.TeamID = session.TeamID
	record.SpaceID = session.SpaceID
	record.ProjectID = session.ProjectID
	record.UserID = session.UserID
	if record.OriginSessionID == 0 {
		record.OriginSessionID = session.SessionID
	}
	record.VectorID = strings.TrimSpace(record.VectorID)
	record.Abstract = strings.TrimSpace(record.Abstract)
	record.Details = strings.TrimSpace(record.Details)
	record.DedupeHash = strings.TrimSpace(record.DedupeHash)
	record.CreatedAt = chooseNonZeroTime(record.CreatedAt, now)
	record.UpdatedAt = chooseNonZeroTime(record.UpdatedAt, now)
	record.SupportCount = 0
	record.RebuttalCount = 0
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindGRPCAIWrite
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if !logicdomain.ValidMemoryStatus(record.Status) {
		record.Status = logicdomain.MemoryStatusActive
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	record.LastRecalledAt = chooseNonZeroTime(record.LastRecalledAt, time.Time{})
	record.LastAdoptedAt = chooseNonZeroTime(record.LastAdoptedAt, time.Time{})
	record.LastReinforcedAt = chooseNonZeroTime(record.LastReinforcedAt, time.Time{})
	if record.ReinforcementCount < 0 {
		record.ReinforcementCount = 0
	}
	return record
}

// activeUnexpiredMemoryCondition renders the reusable SQL predicate that keeps recall on active and non-expired durable memory rows.
// activeUnexpiredMemoryCondition 用于渲染可复用 SQL 条件，让召回自动限定在 active 且未过期的长期记忆行上。
func activeUnexpiredMemoryCondition(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	return fmt.Sprintf(`%smemory_status = %d AND (%sexpires_at IS NULL OR %sexpires_at > NOW())`, alias, logicdomain.MemoryStatusActive, alias, alias)
}

// appendScopedMemoryFilter extends one WHERE condition list with the flattened hierarchy and boundary predicates shared by vector and lexical recall.
// appendScopedMemoryFilter 用于把向量与 lexical 召回共享的层级和边界过滤条件追加到 WHERE 条件列表。
func appendScopedMemoryFilter(whereClauses *[]string, args *sqlArgsBuilder, filter logicdomain.SearchFilter, alias string) {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	if filter.TeamID > 0 {
		*whereClauses = append(*whereClauses, alias+"team_id = "+args.Add(int64(filter.TeamID)))
	}
	if filter.SpaceID > 0 {
		*whereClauses = append(*whereClauses, alias+"space_id = "+args.Add(int64(filter.SpaceID)))
	}
	if filter.ProjectID > 0 {
		*whereClauses = append(*whereClauses, alias+"project_id = "+args.Add(int64(filter.ProjectID)))
	}
	if filter.UserID > 0 {
		placeholder := args.Add(int64(filter.UserID))
		*whereClauses = append(*whereClauses, "("+alias+"user_id = 0 OR "+alias+"user_id = "+placeholder+")")
	}
	if filter.SessionID > 0 {
		*whereClauses = append(*whereClauses, alias+"origin_session_id = "+args.Add(int64(filter.SessionID)))
	}
	if filter.BoundarySessionID > 0 {
		sessionPlaceholder := args.Add(int64(filter.BoundarySessionID))
		if filter.ExcludeBoundaryTurn {
			*whereClauses = append(*whereClauses, "("+alias+"origin_session_id <> "+sessionPlaceholder+" OR "+alias+"source_turn_id IS NULL OR "+alias+"source_turn_id = 0)")
			return
		}
		maxTurnPlaceholder := args.Add(int64(filter.BoundaryMaxTurnID))
		*whereClauses = append(*whereClauses, "("+alias+"origin_session_id <> "+sessionPlaceholder+" OR "+alias+"source_turn_id IS NULL OR "+alias+"source_turn_id = 0 OR "+alias+"source_turn_id <= "+maxTurnPlaceholder+")")
	}
}

// encodePGVectorLiteral formats one float32 slice into PostgreSQL pgvector text syntax.
// encodePGVectorLiteral 用于把 float32 切片格式化为 PostgreSQL pgvector 文本字面量。
func encodePGVectorLiteral(values []float32) string {
	if len(values) == 0 {
		return "[]"
	}
	var builder strings.Builder
	builder.WriteByte('[')
	for idx, value := range values {
		if idx > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

// parsePGVectorText restores one pgvector textual result back into a float32 slice and falls back to an empty slice on malformed data.
// parsePGVectorText 用于把 pgvector 文本结果还原成 float32 切片，并在数据损坏时回退为空切片。
func parsePGVectorText(raw string) []float32 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return []float32{}
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if strings.TrimSpace(raw) == "" {
		return []float32{}
	}
	parts := strings.Split(raw, ",")
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return []float32{}
		}
		out = append(out, float32(number))
	}
	return out
}

// decodeStringMap restores one metadata map from JSON while tolerating malformed payloads.
// decodeStringMap 用于从 JSON 还原元数据 map，并容忍损坏的载荷。
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

// nullableTime returns nil when the provided time is zero so SQL writes can persist NULL instead of a fake epoch value.
// nullableTime 用于在时间值为零时返回 nil，让 SQL 写入存储 NULL 而不是伪造的纪元时间。
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

// parseUint64 parses one positive numeric identifier string and reports whether conversion succeeded.
// parseUint64 用于解析正整数标识字符串，并返回转换是否成功。
func parseUint64(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
}

// parseProjectPath enforces the canonical Team/Space/Project path format used by workspace-facing admin flows.
// parseProjectPath 用于强制要求 workspace 管理流使用标准 Team/Space/Project 路径格式。
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

// memoryRecordMetadataFromNode builds the compatibility metadata map expected by workspace migration and vector-schema rebuild flows.
// memoryRecordMetadataFromNode 用于构建 workspace 迁移和向量 schema 重建流程期望的兼容元数据 map。
func memoryRecordMetadataFromNode(record logicdomain.MemoryNodeRecord) map[string]string {
	metadata := map[string]string{
		"category":     strconv.Itoa(record.Category),
		"details":      record.Details,
		"source_kind":  logicdomain.MemorySourceKindLabel(record.SourceKind),
		"scope_level":  logicdomain.MemoryScopeLevelLabel(record.ScopeLevel),
		"priority":     strconv.Itoa(record.Priority),
		"memory_level": strconv.Itoa(record.MemoryLevel),
	}
	if record.SourceTurnID > 0 {
		metadata["turn_id"] = strconv.FormatUint(record.SourceTurnID, 10)
	}
	return metadata
}

// memoryRecordFromNode converts one durable PostgreSQL memory row into the vector-store record used by rebuild, migration, and lifecycle sync paths.
// memoryRecordFromNode 用于把一条 PostgreSQL 长期记忆行转换成重建、迁移和生命周期同步路径使用的向量记录。
func memoryRecordFromNode(record logicdomain.MemoryNodeRecord) logicdomain.MemoryRecord {
	filter := logicdomain.SearchFilter{
		UserID:    record.UserID,
		TeamID:    record.TeamID,
		SpaceID:   record.SpaceID,
		ProjectID: record.ProjectID,
	}
	if record.SourceKind == logicdomain.MemorySourceKindTurnExtract || record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
		filter.SessionID = record.OriginSessionID
	}
	return logicdomain.MemoryRecord{
		ID:           record.VectorID,
		Text:         record.Abstract,
		Vector:       append([]float32(nil), record.Vector...),
		Filter:       filter,
		SourceTurnID: record.SourceTurnID,
		Status:       record.Status,
		ExpiresAt:    record.ExpiresAt,
		Metadata:     memoryRecordMetadataFromNode(record),
		CreatedAt:    record.CreatedAt,
	}
}

// estimateTokenBudget applies the local domestic estimator to one text blob so PostgreSQL turn summaries and extracted payloads keep the same budget heuristic as SQLite mode.
// estimateTokenBudget 用于对文本载荷应用本地估算器，让 PostgreSQL 下的 turn 总结和提炼结果与 SQLite 模式共享同一套预算口径。
func estimateTokenBudget(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return estimator.Estimate(text)
}

// dehydratedTurnPayload mirrors the JSON document stored in PostgreSQL turn rows after one cleaned turn is flattened into a stable analysis unit.
// dehydratedTurnPayload 用于映射写入 PostgreSQL turn 行的 JSON 文档，让一条清洗后的 turn 可以稳定地被后续分析复用。
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

// buildDehydratedTurn converts one cleaned turn into the persisted JSON payload and estimates its token budget before PostgreSQL persistence.
// buildDehydratedTurn 用于把一条清洗后的 turn 转成 PostgreSQL 持久化 JSON 载荷，并预估对应的 token 预算。
func buildDehydratedTurn(turn logicdomain.TurnRecord) (string, int, error) {
	// Preserve the already-sanitized timeline text as-is so PostgreSQL persists exactly the same canonical unit that upstream cleaning produced.
	// 原样保留已经清洗完成的 timeline 文本，确保 PostgreSQL 落库的规范单元与上游清洗结果完全一致。
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
	return string(body), estimateTokenBudget(string(body)), nil
}

// generateConfirmationCode returns one 32-character random hex token used by protected destructive admin flows.
// generateConfirmationCode 用于生成 32 位随机十六进制确认码，服务需要保护的管理删除流程。
func generateConfirmationCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate confirmation code: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// normalizeTurnMemoryNodeRecord fills one turn-extracted memory node with the resolved hierarchy, lifecycle defaults, and stable timestamps before PostgreSQL persistence.
// normalizeTurnMemoryNodeRecord 用于在 PostgreSQL 持久化前，为单轮提炼出的记忆节点补齐解析后的层级、生命周期默认值和稳定时间戳。
func normalizeTurnMemoryNodeRecord(session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, node logicdomain.MemoryNodeCandidate, now time.Time) logicdomain.MemoryNodeRecord {
	record := logicdomain.MemoryNodeRecord{
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.SessionID,
		SourceTurnID:    turn.ID,
		VectorID:        strings.TrimSpace(node.VectorID),
		Vector:          append([]float32(nil), node.Vector...),
		SourceKind:      node.SourceKind,
		ScopeLevel:      node.ScopeLevel,
		Category:        node.Category,
		Abstract:        strings.TrimSpace(node.Abstract),
		Details:         strings.TrimSpace(node.Details),
		Status:          logicdomain.MemoryStatusActive,
		Priority:        node.Priority,
		MemoryLevel:     node.MemoryLevel,
		RefreshWeight:   node.RefreshWeight,
		ExpiresAt:       node.ExpiresAt,
		DedupeHash:      strings.TrimSpace(node.DedupeHash),
		CreatedAt:       now.UTC(),
		UpdatedAt:       now.UTC(),
	}
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindTurnExtract
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	return record
}

// chooseLongerMemoryExpiry preserves whichever expiry lies farther in the future so lifecycle reinforcement never shortens an existing record accidentally.
// chooseLongerMemoryExpiry 用于保留更远的过期时间，避免生命周期强化时意外缩短已有记录的寿命。
func chooseLongerMemoryExpiry(current, candidate time.Time) time.Time {
	if current.IsZero() {
		return candidate.UTC()
	}
	if current.After(candidate) {
		return current.UTC()
	}
	return candidate.UTC()
}

// evolveAdoptedMemoryRecord computes the next lifecycle state for one active memory row after pre-check adopts it for a live request.
// evolveAdoptedMemoryRecord 用于在 pre-check 采纳某条活跃记忆后，计算这条记忆应进入的下一生命周期状态。
func evolveAdoptedMemoryRecord(session logicdomain.SessionRef, row logicdomain.MemoryNodeRecord, adoptedAt time.Time) logicdomain.MemoryNodeRecord {
	updated := row
	updated.LastRecalledAt = adoptedAt.UTC()
	updated.LastAdoptedAt = adoptedAt.UTC()
	updated.LastReinforcedAt = adoptedAt.UTC()
	updated.RecalledCount++
	updated.AdoptedCount++
	updated.ReinforcementCount++
	if updated.RefreshWeight <= 0 {
		updated.RefreshWeight = 1
	}
	updated.RefreshWeight++
	if updated.AdoptedCount >= 2 && updated.MemoryLevel < logicdomain.MemoryLevelPhase {
		updated.MemoryLevel = logicdomain.MemoryLevelPhase
	}
	if row.OriginSessionID > 0 && row.OriginSessionID != session.SessionID {
		updated.CrossSessionAdoptedCount++
	}
	if row.ScopeLevel == logicdomain.MemoryScopeLevelSession && updated.CrossSessionAdoptedCount >= 2 {
		updated.ScopeLevel = logicdomain.MemoryScopeLevelProject
		if updated.MemoryLevel < logicdomain.MemoryLevelStable {
			updated.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if updated.CrossSessionAdoptedCount >= 4 && updated.Priority == logicdomain.MemoryPriorityP0 && updated.MemoryLevel < logicdomain.MemoryLevelPersistent {
		updated.MemoryLevel = logicdomain.MemoryLevelPersistent
	}
	updated.ExpiresAt = chooseLongerMemoryExpiry(updated.ExpiresAt, defaultUnifiedMemoryExpiry(updated.ScopeLevel, adoptedAt))
	updated.UpdatedAt = adoptedAt.UTC()
	return updated
}

// sortedProfileBindingIDs returns one deterministic ascending id slice from a rendered-profile update map so batch updates stay stable in logs and tests.
// sortedProfileBindingIDs 用于从渲染画像更新 map 中返回确定性的升序 id 列表，确保批量更新在日志和测试里保持稳定。
func sortedProfileBindingIDs(values map[uint64]string) []uint64 {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	for id := range values {
		if id == 0 {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	return ids
}
