// helpers.go centralizes shared PostgreSQL combined-store helpers so dialect routing, schema bootstrap, and query methods can stay small and consistent.
// helpers.go 用于集中 PostgreSQL 组合库共享辅助逻辑，让方言路由、schema 启动和查询方法保持简洁且一致。
package vldb_postgres

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
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
	return storageutil.NormalizeUint64List(merged)
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
