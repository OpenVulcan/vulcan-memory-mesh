// dialect_test.go verifies the PostgreSQL combined-store helper logic that does not require a live database connection.
// dialect_test.go 用于验证无需真实数据库连接即可覆盖的 PostgreSQL 组合库辅助逻辑。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestParadeDBDialectBuildLexicalSearchSQL verifies the ParadeDB flavor emits @@@ plus pdb.parse and preserves scoped filters.
// TestParadeDBDialectBuildLexicalSearchSQL 用于验证 ParadeDB flavor 会发出 @@@ 与 pdb.parse，并保留作用域过滤条件。
func TestParadeDBDialectBuildLexicalSearchSQL(t *testing.T) {
	store := newTestStore(Config{
		Schema:                  "memory",
		Flavor:                  "paradedb",
		BM25IndexName:           "vmm_memory_nodes_bm25_idx",
		TRGMSimilarityThreshold: 0.2,
		EmbeddingDimension:      3,
	})
	sqlText, args := paradeDBDialect{}.BuildLexicalSearchSQL(&store.repos.memory, "架构 决策", 8, logicdomain.SearchFilter{
		TeamID:            10,
		SpaceID:           20,
		ProjectID:         30,
		UserID:            40,
		BoundarySessionID: 50,
		BoundaryMaxTurnID: 60,
	})
	requiredFragments := []string{
		"@@@ pdb.parse",
		"pdb.score(m.id)",
		"m.team_id = $3",
		"m.space_id = $4",
		"m.project_id = $5",
		"(m.user_id = 0 OR m.user_id = $6)",
		"m.origin_session_id <> $7",
		"m.source_turn_id <= $8",
		`"memory"."vmm_memory_nodes"`,
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("BuildLexicalSearchSQL missing fragment %q in SQL:\n%s", fragment, sqlText)
		}
	}
	if len(args) != 8 {
		t.Fatalf("BuildLexicalSearchSQL arg count = %d, want 8", len(args))
	}
}

// TestStandardDialectBuildLexicalSearchSQL verifies the standard flavor emits pg_trgm-friendly ILIKE and similarity clauses instead of tsvector search.
// TestStandardDialectBuildLexicalSearchSQL 用于验证 standard flavor 会发出适合 pg_trgm 的 ILIKE 与 similarity 条件，而不是 tsvector 搜索。
func TestStandardDialectBuildLexicalSearchSQL(t *testing.T) {
	store := newTestStore(Config{
		Schema:                  "public",
		Flavor:                  "standard",
		TRGMSimilarityThreshold: 0.23,
		EmbeddingDimension:      3,
	})
	sqlText, args := standardDialect{}.BuildLexicalSearchSQL(&store.repos.memory, "中文检索", 6, logicdomain.SearchFilter{
		ProjectID: 1,
		UserID:    2,
	})
	requiredFragments := []string{
		"ILIKE",
		"similarity(m.abstract",
		"similarity(m.details",
		"m.project_id = $4",
		"(m.user_id = 0 OR m.user_id = $5)",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("BuildLexicalSearchSQL missing fragment %q in SQL:\n%s", fragment, sqlText)
		}
	}
	if strings.Contains(sqlText, "to_tsvector") || strings.Contains(sqlText, "@@") {
		t.Fatalf("BuildLexicalSearchSQL unexpectedly fell back to tsvector syntax:\n%s", sqlText)
	}
	if len(args) != 5 {
		t.Fatalf("BuildLexicalSearchSQL arg count = %d, want 5", len(args))
	}
}

// TestParadeDBDialectBuildHybridSearchSQL verifies the ParadeDB flavor can emit one SQL-level fusion query instead of requiring application-side vector/lexical fan-out.
// TestParadeDBDialectBuildHybridSearchSQL 用于验证 ParadeDB flavor 可以直接生成 SQL 层融合查询，而不是继续依赖应用层拆成向量与 lexical 两段查询。
func TestParadeDBDialectBuildHybridSearchSQL(t *testing.T) {
	store := newTestStore(Config{
		Schema:             "memory",
		Flavor:             "paradedb",
		BM25IndexName:      "vmm_memory_nodes_bm25_idx",
		EmbeddingDimension: 3,
	})
	sqlText, args := paradeDBDialect{}.BuildHybridSearchSQL(&store.repos.memory, "混合 检索", []float32{0.1, 0.2, 0.3}, 8, logicdomain.SearchFilter{
		ProjectID:         30,
		UserID:            40,
		BoundarySessionID: 50,
		BoundaryMaxTurnID: 60,
	}, 60)
	requiredFragments := []string{
		"WITH vector_candidates AS",
		"FULL OUTER JOIN lexical_candidates",
		"m.embedding <=> $1::vector",
		"@@@ pdb.parse($2, lenient => true)",
		"pdb.score(m.id)",
		"'hybrid_rrf'",
		"m.embedding::text AS embedding_text",
		"1.0 / ($4::double precision + v.vector_rank::double precision)",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("BuildHybridSearchSQL missing fragment %q in SQL:\n%s", fragment, sqlText)
		}
	}
	if len(args) == 0 {
		t.Fatalf("BuildHybridSearchSQL args should not be empty")
	}
}

// TestStandardDialectBuildHybridSearchSQL verifies the standard flavor fuses pgvector and trigram candidates in SQL without falling back to tsvector syntax.
// TestStandardDialectBuildHybridSearchSQL 用于验证 standard flavor 会在 SQL 层融合 pgvector 与 trigram 候选，而不会回退到 tsvector 语法。
func TestStandardDialectBuildHybridSearchSQL(t *testing.T) {
	store := newTestStore(Config{
		Schema:                  "public",
		Flavor:                  "standard",
		TRGMSimilarityThreshold: 0.23,
		EmbeddingDimension:      3,
	})
	sqlText, args := standardDialect{}.BuildHybridSearchSQL(&store.repos.memory, "中文检索", []float32{0.1, 0.2, 0.3}, 6, logicdomain.SearchFilter{
		ProjectID: 1,
		UserID:    2,
	}, 60)
	requiredFragments := []string{
		"WITH vector_candidates AS",
		"FULL OUTER JOIN lexical_candidates",
		"m.embedding <=> $1::vector",
		"similarity(m.abstract, $2)",
		"similarity(m.details, $2)",
		"'hybrid_rrf'",
		"m.embedding::text AS embedding_text",
		"1.0 / ($5::double precision + l.lexical_rank::double precision)",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(sqlText, fragment) {
			t.Fatalf("BuildHybridSearchSQL missing fragment %q in SQL:\n%s", fragment, sqlText)
		}
	}
	if strings.Contains(sqlText, "to_tsvector") || strings.Contains(sqlText, "@@ to_tsquery") {
		t.Fatalf("BuildHybridSearchSQL unexpectedly fell back to tsvector syntax:\n%s", sqlText)
	}
	if len(args) == 0 {
		t.Fatalf("BuildHybridSearchSQL args should not be empty")
	}
}

// TestPostgresMemoryHitFromRecordPreservesOrigin verifies scanned memory rows keep the adapter-stamped retrieval channel when converted into shared recall hits.
// TestPostgresMemoryHitFromRecordPreservesOrigin 用于验证已扫描的记忆行转换成共享召回命中时，会保留适配器写入的检索通道。
func TestPostgresMemoryHitFromRecordPreservesOrigin(t *testing.T) {
	hit := postgresMemoryHitFromRecord(logicdomain.MemoryNodeRecord{
		ID:              701,
		VectorID:        " vec-701 ",
		OriginSessionID: 51,
		SourceTurnID:    5101,
		UserID:          7,
		ProjectID:       9,
		Abstract:        "普通 pgvector 检索命中。",
	}, 0.87, " vector_search ")

	if hit.ID != "vec-701" || hit.Metadata["origin"] != "vector_search" {
		t.Fatalf("unexpected postgres memory hit mapping: %+v", hit)
	}
	if hit.Filter.SessionID != 51 || hit.Metadata["turn_id"] != "5101" {
		t.Fatalf("unexpected postgres memory hit scope metadata: %+v", hit)
	}
}

// TestPostgresCommonIndexStatementsExposeStableNames verifies shared bootstrap indexes carry one diagnostic name per DDL statement.
// TestPostgresCommonIndexStatementsExposeStableNames 用于验证共享启动索引的每条 DDL 都携带一个诊断名称。
func TestPostgresCommonIndexStatementsExposeStableNames(t *testing.T) {
	repo := &maintenanceRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	statements := postgresCommonIndexStatements(repo)
	if len(statements) != 28 {
		t.Fatalf("common index statement count = %d, want 28", len(statements))
	}
	for _, statement := range statements {
		if strings.TrimSpace(statement.name) == "" {
			t.Fatalf("common index statement has empty diagnostic name: %+v", statement)
		}
		if !strings.Contains(statement.sql, statement.name) {
			t.Fatalf("common index statement %q does not include its index name in SQL: %s", statement.name, statement.sql)
		}
	}
}

// TestStandardTrigramIndexStatementsExposeStableNames verifies standard lexical indexes carry precise names for startup error reporting.
// TestStandardTrigramIndexStatementsExposeStableNames 用于验证 standard lexical 索引会携带精确名称以服务启动错误报告。
func TestStandardTrigramIndexStatementsExposeStableNames(t *testing.T) {
	store := newTestStore(Config{Schema: "public"})
	statements := standardTrigramIndexStatements(&store.repos.maintenance)
	if len(statements) != 2 {
		t.Fatalf("standard trigram index statement count = %d, want 2", len(statements))
	}
	for _, statement := range statements {
		if strings.TrimSpace(statement.name) == "" {
			t.Fatalf("standard trigram statement has empty diagnostic name: %+v", statement)
		}
		if !strings.Contains(statement.sql, statement.name) {
			t.Fatalf("standard trigram statement %q does not include its index name in SQL: %s", statement.name, statement.sql)
		}
	}
}

// TestNormalizeDirectMemoryNodeRecordAppliesDefaults verifies direct-write defaults still match the unified-memory contract when PostgreSQL combined mode is active.
// TestNormalizeDirectMemoryNodeRecordAppliesDefaults 用于验证 PostgreSQL 组合模式下的主动写默认值仍与统一记忆契约保持一致。
func TestNormalizeDirectMemoryNodeRecordAppliesDefaults(t *testing.T) {
	now := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	record := normalizeDirectMemoryNodeRecord(logicdomain.SessionRef{
		SessionID: 9,
		UserID:    8,
		TeamID:    7,
		SpaceID:   6,
		ProjectID: 5,
	}, logicdomain.MemoryNodeRecord{
		VectorID:    " vector-1 ",
		Abstract:    "  记忆摘要 ",
		Details:     "  记忆详情 ",
		ScopeLevel:  -1,
		Priority:    -1,
		MemoryLevel: -1,
		Status:      -1,
	}, now)
	if record.TeamID != 7 || record.SpaceID != 6 || record.ProjectID != 5 || record.UserID != 8 {
		t.Fatalf("normalizeDirectMemoryNodeRecord scope = %+v", record)
	}
	if record.OriginSessionID != 9 {
		t.Fatalf("normalizeDirectMemoryNodeRecord origin_session_id = %d, want 9", record.OriginSessionID)
	}
	if record.ScopeLevel != logicdomain.MemoryScopeLevelProject {
		t.Fatalf("normalizeDirectMemoryNodeRecord scope_level = %d, want %d", record.ScopeLevel, logicdomain.MemoryScopeLevelProject)
	}
	if record.Priority != logicdomain.MemoryPriorityP2 {
		t.Fatalf("normalizeDirectMemoryNodeRecord priority = %d, want %d", record.Priority, logicdomain.MemoryPriorityP2)
	}
	if record.MemoryLevel != logicdomain.MemoryLevelStable {
		t.Fatalf("normalizeDirectMemoryNodeRecord memory_level = %d, want %d", record.MemoryLevel, logicdomain.MemoryLevelStable)
	}
	if record.Status != logicdomain.MemoryStatusActive {
		t.Fatalf("normalizeDirectMemoryNodeRecord status = %d, want %d", record.Status, logicdomain.MemoryStatusActive)
	}
	if record.RefreshWeight != 1 {
		t.Fatalf("normalizeDirectMemoryNodeRecord refresh_weight = %d, want 1", record.RefreshWeight)
	}
	if record.ExpiresAt.IsZero() || !record.ExpiresAt.After(now) {
		t.Fatalf("normalizeDirectMemoryNodeRecord expires_at = %v, want non-zero future time", record.ExpiresAt)
	}
	if record.VectorID != "vector-1" || record.Abstract != "记忆摘要" || record.Details != "记忆详情" {
		t.Fatalf("normalizeDirectMemoryNodeRecord trimming mismatch: %+v", record)
	}
}

// TestEncodeAndParsePGVectorLiteralRoundTrip verifies the pgvector text helpers keep vector payloads stable enough for query building and row decoding.
// TestEncodeAndParsePGVectorLiteralRoundTrip 用于验证 pgvector 文本辅助函数在查询构建与行解码之间能稳定保持向量载荷。
func TestEncodeAndParsePGVectorLiteralRoundTrip(t *testing.T) {
	input := []float32{0.125, 0.5, 0.875}
	literal := encodePGVectorLiteral(input)
	if literal != "[0.125,0.5,0.875]" {
		t.Fatalf("encodePGVectorLiteral = %q", literal)
	}
	restored := parsePGVectorText(literal)
	if len(restored) != len(input) {
		t.Fatalf("parsePGVectorText length = %d, want %d", len(restored), len(input))
	}
	for idx := range input {
		if restored[idx] != input[idx] {
			t.Fatalf("parsePGVectorText[%d] = %v, want %v", idx, restored[idx], input[idx])
		}
	}
}

// TestBuildProjectMigrationSessionConflictIncludesPreview verifies project migration conflicts stay classified as conflicts and surface a short duplicate session-key preview for operators.
// TestBuildProjectMigrationSessionConflictIncludesPreview 用于验证项目迁移冲突仍会被归类为 conflict，并向运维侧返回重复 session key 的简短预览。
func TestBuildProjectMigrationSessionConflictIncludesPreview(t *testing.T) {
	source := logicdomain.ProjectRecord{TeamName: "team-a", SpaceName: "space-a", Name: "source"}
	target := logicdomain.ProjectRecord{TeamName: "team-b", SpaceName: "space-b", Name: "target"}
	err := buildProjectMigrationSessionConflict(source, target, []string{"alpha", "beta", "gamma", "delta"})
	if !logicdomain.IsConflictError(err) {
		t.Fatalf("buildProjectMigrationSessionConflict should return conflict error, got %v", err)
	}
	message := err.Error()
	requiredFragments := []string{
		"source",
		"target",
		"alpha, beta, gamma (+1 more)",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(message, fragment) {
			t.Fatalf("buildProjectMigrationSessionConflict missing fragment %q in %q", fragment, message)
		}
	}
}

// TestFinalizeDeleteConfirmationCode verifies guarded confirmation-code updates always return the durable token, including zero-row races that must fall back to the stored value.
// TestFinalizeDeleteConfirmationCode 用于验证带条件的确认码更新总会返回真实持久化令牌，并覆盖 0 行竞态时回退数据库值的场景。
func TestFinalizeDeleteConfirmationCode(t *testing.T) {
	testCases := []struct {
		name          string
		existingCode  string
		generatedCode string
		rowsAffected  int64
		persistedCode string
		wantCode      string
		wantErr       bool
	}{
		{
			name:         "existing code wins",
			existingCode: "keep-me",
			rowsAffected: 0,
			wantCode:     "keep-me",
		},
		{
			name:          "successful insert returns generated code",
			generatedCode: "generated",
			rowsAffected:  1,
			wantCode:      "generated",
		},
		{
			name:          "zero-row race falls back to persisted code",
			generatedCode: "generated",
			rowsAffected:  0,
			persistedCode: "stored",
			wantCode:      "stored",
		},
		{
			name:         "zero-row race without persisted code errors",
			rowsAffected: 0,
			wantErr:      true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotCode, err := finalizeDeleteConfirmationCode(tc.existingCode, tc.generatedCode, tc.rowsAffected, tc.persistedCode)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("finalizeDeleteConfirmationCode error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("finalizeDeleteConfirmationCode error = %v", err)
			}
			if gotCode != tc.wantCode {
				t.Fatalf("finalizeDeleteConfirmationCode code = %q, want %q", gotCode, tc.wantCode)
			}
		})
	}
}

// TestShouldSeedDebugWorkspace verifies the PostgreSQL startup seed gate stays aligned with SQLite by seeding only brand-new empty workspaces.
// TestShouldSeedDebugWorkspace 用于验证 PostgreSQL 启动期的默认种子门控与 SQLite 保持一致，只在全新空工作区时补种。
func TestShouldSeedDebugWorkspace(t *testing.T) {
	testCases := []struct {
		name         string
		projectCount int
		want         bool
	}{
		{
			name:         "empty workspace seeds defaults",
			projectCount: 0,
			want:         true,
		},
		{
			name:         "non-empty workspace skips defaults",
			projectCount: 1,
			want:         false,
		},
		{
			name:         "multiple projects also skip defaults",
			projectCount: 3,
			want:         false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSeedDebugWorkspace(tc.projectCount); got != tc.want {
				t.Fatalf("shouldSeedDebugWorkspace(%d) = %v, want %v", tc.projectCount, got, tc.want)
			}
		})
	}
}

// TestResolveProfileTargetWithQueryerUserScopeSkipsProjectLookup verifies user-scoped profile resolution only touches the durable user row, so callers do not need to send an unrelated project id.
// TestResolveProfileTargetWithQueryerUserScopeSkipsProjectLookup 用于验证 user 画像目标解析只访问长期用户行，从而保证调用方不必额外提供无关的 project id。
func TestResolveProfileTargetWithQueryerUserScopeSkipsProjectLookup(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(7), "alice", "", "", now, now},
	}

	target, err := store.repos.workspace.resolveProfileTargetWithQueryer(context.Background(), queryer, logicdomain.ProfileTypeUser, 7, 0)
	if err != nil {
		t.Fatalf("resolveProfileTargetWithQueryer error = %v", err)
	}
	if target.BindID != 7 || target.UserID != 7 || target.UserName != "alice" {
		t.Fatalf("resolveProfileTargetWithQueryer target = %+v", target)
	}
	if target.ProjectID != 0 || target.TeamID != 0 || target.SpaceID != 0 {
		t.Fatalf("resolveProfileTargetWithQueryer user scope should not hydrate project hierarchy, got %+v", target)
	}
	if queryer.queryRowCalls != 1 {
		t.Fatalf("resolveProfileTargetWithQueryer QueryRow calls = %d, want 1", queryer.queryRowCalls)
	}
	if !strings.Contains(queryer.lastSQL, store.repos.workspace.usersTable()) {
		t.Fatalf("resolveProfileTargetWithQueryer should query users table, got:\n%s", queryer.lastSQL)
	}
}

// TestResolveProfileTargetWithQueryerProjectScopesSkipUserLookup verifies project-derived profile scopes only resolve the durable project hierarchy row, so the caller can omit user id for project/team/space targets.
// TestResolveProfileTargetWithQueryerProjectScopesSkipUserLookup 用于验证 project 派生画像范围只解析长期项目层级行，从而保证 project/team/space 目标可省略 user id。
func TestResolveProfileTargetWithQueryerProjectScopesSkipUserLookup(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	testCases := []struct {
		name        string
		profileType int
		wantBindID  uint64
	}{
		{
			name:        "project scope binds project id",
			profileType: logicdomain.ProfileTypeProject,
			wantBindID:  41,
		},
		{
			name:        "team scope binds team id",
			profileType: logicdomain.ProfileTypeTeam,
			wantBindID:  11,
		},
		{
			name:        "space scope binds space id",
			profileType: logicdomain.ProfileTypeSpace,
			wantBindID:  21,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(Config{Schema: "public"})
			queryer := &captureProfileQueryer{
				rowValues: []any{uint64(41), uint64(11), uint64(21), "project-a", "", "team-a", "space-a", now, now},
			}

			target, err := store.repos.workspace.resolveProfileTargetWithQueryer(context.Background(), queryer, tc.profileType, 0, 41)
			if err != nil {
				t.Fatalf("resolveProfileTargetWithQueryer error = %v", err)
			}
			if target.BindID != tc.wantBindID {
				t.Fatalf("resolveProfileTargetWithQueryer bind_id = %d, want %d", target.BindID, tc.wantBindID)
			}
			if target.ProjectID != 41 || target.TeamID != 11 || target.SpaceID != 21 {
				t.Fatalf("resolveProfileTargetWithQueryer scope = %+v", target)
			}
			if target.ProjectName != "project-a" || target.TeamName != "team-a" || target.SpaceName != "space-a" {
				t.Fatalf("resolveProfileTargetWithQueryer names = %+v", target)
			}
			if queryer.queryRowCalls != 1 {
				t.Fatalf("resolveProfileTargetWithQueryer QueryRow calls = %d, want 1", queryer.queryRowCalls)
			}
			if !strings.Contains(queryer.lastSQL, store.repos.workspace.projectsTable()) {
				t.Fatalf("resolveProfileTargetWithQueryer should query projects table, got:\n%s", queryer.lastSQL)
			}
		})
	}
}

// TestInsertTeamUsesUpsertByName verifies project-path creation no longer relies on a plain insert for team rows, so duplicate concurrent requests can converge on the same durable row.
// TestInsertTeamUsesUpsertByName 用于验证项目路径创建不再对 team 行使用普通插入，从而让并发重复请求可以收敛到同一条长期行。
func TestInsertTeamUsesUpsertByName(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(11), "default", "", now, now},
	}

	team, err := store.repos.workspace.insertTeam(context.Background(), queryer, "default", now)
	if err != nil {
		t.Fatalf("insertTeam error = %v", err)
	}
	if team.ID != 11 || team.Name != "default" {
		t.Fatalf("insertTeam team = %+v", team)
	}
	if !strings.Contains(queryer.lastSQL, "ON CONFLICT (name)") {
		t.Fatalf("insertTeam SQL should upsert by name, got:\n%s", queryer.lastSQL)
	}
}

// TestInsertSpaceUsesUpsertByScopedName verifies project-path creation now upserts spaces by `(team_id, name)` instead of failing on duplicate concurrent creation.
// TestInsertSpaceUsesUpsertByScopedName 用于验证项目路径创建现在会按 `(team_id, name)` upsert space，而不是在并发重复创建时直接失败。
func TestInsertSpaceUsesUpsertByScopedName(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(21), uint64(11), "default", "", now, now},
	}

	space, err := store.repos.workspace.insertSpace(context.Background(), queryer, 11, "default", now)
	if err != nil {
		t.Fatalf("insertSpace error = %v", err)
	}
	if space.ID != 21 || space.TeamID != 11 || space.Name != "default" {
		t.Fatalf("insertSpace space = %+v", space)
	}
	if !strings.Contains(queryer.lastSQL, "ON CONFLICT (team_id, name)") {
		t.Fatalf("insertSpace SQL should upsert by scoped name, got:\n%s", queryer.lastSQL)
	}
}

// TestInsertProjectUsesUpsertByScopedName verifies project-path creation now upserts projects by `(space_id, name)` so duplicate admin creates remain idempotent.
// TestInsertProjectUsesUpsertByScopedName 用于验证项目路径创建现在会按 `(space_id, name)` upsert project，确保重复管理创建保持幂等。
func TestInsertProjectUsesUpsertByScopedName(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(31), uint64(11), uint64(21), "default", "", now, now},
	}

	project, err := store.repos.workspace.insertProject(
		context.Background(),
		queryer,
		logicdomain.TeamRecord{ID: 11, Name: "default"},
		logicdomain.SpaceRecord{ID: 21, TeamID: 11, Name: "default"},
		"default",
		now,
	)
	if err != nil {
		t.Fatalf("insertProject error = %v", err)
	}
	if project.ID != 31 || project.TeamID != 11 || project.SpaceID != 21 || project.Name != "default" {
		t.Fatalf("insertProject project = %+v", project)
	}
	if !strings.Contains(queryer.lastSQL, "ON CONFLICT (space_id, name)") {
		t.Fatalf("insertProject SQL should upsert by scoped name, got:\n%s", queryer.lastSQL)
	}
}

// TestUpsertDebugSeedUserUsesNameConflictKey verifies empty-workspace seed bootstrap resolves the default user by name instead of relying on one hard-coded numeric id.
// TestUpsertDebugSeedUserUsesNameConflictKey 用于验证空工作区默认补种会按名称解析默认用户，而不是依赖单个硬编码数字 id。
func TestUpsertDebugSeedUserUsesNameConflictKey(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(41)},
	}

	userID, err := store.repos.maintenance.upsertDebugSeedUser(context.Background(), queryer, "default", now)
	if err != nil {
		t.Fatalf("upsertDebugSeedUser error = %v", err)
	}
	if userID != 41 {
		t.Fatalf("upsertDebugSeedUser id = %d", userID)
	}
	if !strings.Contains(queryer.lastSQL, "ON CONFLICT (name)") {
		t.Fatalf("upsertDebugSeedUser SQL should upsert by name, got:\n%s", queryer.lastSQL)
	}
}

// TestUpsertDebugSeedProjectUsesResolvedPathKeys verifies debug seed bootstrap resolves the default project through the already-resolved Team/Space ids instead of fixed row ids.
// TestUpsertDebugSeedProjectUsesResolvedPathKeys 用于验证默认调试项目补种会通过已解析的 Team/Space id 落库，而不是继续依赖固定行 id。
func TestUpsertDebugSeedProjectUsesResolvedPathKeys(t *testing.T) {
	now := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	store := newTestStore(Config{Schema: "public"})
	queryer := &captureProfileQueryer{
		rowValues: []any{uint64(51)},
	}

	projectID, err := store.repos.maintenance.upsertDebugSeedProject(context.Background(), queryer, 11, 21, "default", now)
	if err != nil {
		t.Fatalf("upsertDebugSeedProject error = %v", err)
	}
	if projectID != 51 {
		t.Fatalf("upsertDebugSeedProject id = %d", projectID)
	}
	if !strings.Contains(queryer.lastSQL, "ON CONFLICT (space_id, name)") {
		t.Fatalf("upsertDebugSeedProject SQL should upsert by scoped business key, got:\n%s", queryer.lastSQL)
	}
	if len(queryer.lastArgs) < 2 || queryer.lastArgs[0] != int64(11) || queryer.lastArgs[1] != int64(21) {
		t.Fatalf("upsertDebugSeedProject args = %#v", queryer.lastArgs)
	}
}

// captureProfileQueryer stores the latest QueryRow call so SQL-only helper tests can assert the emitted PostgreSQL statement without a live database.
// captureProfileQueryer 用于记录最近一次 QueryRow 调用，让纯 SQL 辅助测试无需真实数据库也能断言 PostgreSQL 语句内容。
type captureProfileQueryer struct {
	lastArgs      []any
	lastSQL       string
	queryRowCalls int
	rowValues     []any
}

// Exec should stay unused in these SQL-only helper tests.
// Exec 在这些纯 SQL 辅助测试中不应被调用。
func (q *captureProfileQueryer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec call")
}

// Query should stay unused in these SQL-only helper tests.
// Query 在这些纯 SQL 辅助测试中不应被调用。
func (q *captureProfileQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query call")
}

// QueryRow records the latest SQL and returns one deterministic scan row for the caller under test.
// QueryRow 用于记录最近一次 SQL，并向被测调用方返回一条确定性的扫描结果。
func (q *captureProfileQueryer) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.queryRowCalls++
	q.lastSQL = sql
	q.lastArgs = append([]any(nil), args...)
	return captureProfileRow{values: q.rowValues}
}

// captureProfileRow replays one fixed row payload into Scan destinations so helper tests can exercise the surrounding mapping logic.
// captureProfileRow 用于把固定行数据回放到 Scan 目标里，让辅助测试可以覆盖周边映射逻辑。
type captureProfileRow struct {
	values []any
}

// Scan copies the fixed row payload into the provided scan destinations.
// Scan 用于把固定行载荷复制到调用方提供的扫描目标里。
func (r captureProfileRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(r.values))
	}
	for idx, value := range r.values {
		switch target := dest[idx].(type) {
		case *uint64:
			*target = value.(uint64)
		case *string:
			*target = value.(string)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unsupported scan target type %T", dest[idx])
		}
	}
	return nil
}
