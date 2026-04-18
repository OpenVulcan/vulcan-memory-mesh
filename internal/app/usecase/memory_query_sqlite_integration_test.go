// memory_query_sqlite_integration_test.go exercises the real SQLite FFI store plus the app-layer memory query flow,
// so BM25/tokenizer changes are validated against the packaged dynamic library instead of only mocked lexical hits.
// memory_query_sqlite_integration_test.go 用于联通真实 SQLite FFI 存储与应用层记忆查询链路，
// 从而基于已打包动态库验证 BM25 / tokenizer 变化，而不是只依赖 mocked lexical 命中。
package usecase

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	sqliteadapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRealSQLiteLexicalRecallReturnsRankedHits verifies the packaged SQLite FFI library can build the built-in FTS index
// and return a deterministic lexical order for seeded durable memory rows.
// TestRealSQLiteLexicalRecallReturnsRankedHits 用于验证已打包的 SQLite FFI 动态库能够构建内建 FTS 索引，
// 并对预置长期记忆行返回确定性的 lexical 排序。
func TestRealSQLiteLexicalRecallReturnsRankedHits(t *testing.T) {
	store := newRealSQLiteFTSTestStore(t)
	session := logicdomain.SessionRef{
		SessionID:  21,
		SessionKey: "sess-real-sqlite-fts",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	insertedIDs := seedRealSQLiteBM25Fixtures(t, store, session)

	hits, err := store.SearchLexicalMemory(context.Background(), "部署 模式", 2, logicdomain.SearchFilter{
		UserID:    session.UserID,
		TeamID:    session.TeamID,
		SpaceID:   session.SpaceID,
		ProjectID: session.ProjectID,
	})
	if err != nil {
		t.Fatalf("real sqlite lexical search returned error: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected two real lexical hits, got %+v", hits)
	}
	if hits[0].MemoryID != insertedIDs[0] || hits[1].MemoryID != insertedIDs[1] {
		t.Fatalf("expected real lexical order [%d %d], got %+v", insertedIDs[0], insertedIDs[1], hits)
	}
}

// TestMemoryUseCaseSearchWithRealSQLiteFTSKeepsRankNormalizedScores verifies the app-layer caller-facing scores still follow
// the shared rank-normalized contract even when lexical recall comes from the real SQLite BM25 path.
// TestMemoryUseCaseSearchWithRealSQLiteFTSKeepsRankNormalizedScores 用于验证当 lexical 召回来自真实 SQLite BM25 路径时，
// 应用层对外分数仍然遵循统一的 rank-normalized 契约。
func TestMemoryUseCaseSearchWithRealSQLiteFTSKeepsRankNormalizedScores(t *testing.T) {
	store := newRealSQLiteFTSTestStore(t)
	session := logicdomain.SessionRef{
		SessionID:  22,
		SessionKey: "sess-real-sqlite-usecase",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	insertedIDs := seedRealSQLiteBM25Fixtures(t, store, session)

	result := searchMemoryWithRealSQLiteFTS(t, store, session, "部署 模式")
	hits := result.Results[0].Hits
	if len(hits) != 2 {
		t.Fatalf("expected two real-usecase hits, got %+v", hits)
	}
	if hits[0].MemoryRef.ID != insertedIDs[0] || hits[1].MemoryRef.ID != insertedIDs[1] {
		t.Fatalf("expected rank order [%d %d], got %+v", insertedIDs[0], insertedIDs[1], hits)
	}
	if math.Abs(hits[0].Score-1.0) > 1e-9 || math.Abs(hits[1].Score-0.75) > 1e-9 {
		t.Fatalf("expected caller-facing scores to stay rank-normalized, got %+v", hits)
	}
	if hits[0].Origin != "lexical_search" || hits[1].Origin != "lexical_search" {
		t.Fatalf("expected real sqlite lexical origin, got %+v", hits)
	}
}

// TestMemoryUseCaseSearchWithRealSQLiteFTSChangesTopCandidateAcrossQueries verifies the real SQLite lexical order
// still propagates into the final top candidate, which is the practical risk surface after tokenizer/BM25 migration.
// TestMemoryUseCaseSearchWithRealSQLiteFTSChangesTopCandidateAcrossQueries 用于验证真实 SQLite lexical 排序仍会传导到最终首条候选，
// 这正是 tokenizer / BM25 迁移后最实际的风险面。
func TestMemoryUseCaseSearchWithRealSQLiteFTSChangesTopCandidateAcrossQueries(t *testing.T) {
	store := newRealSQLiteFTSTestStore(t)
	session := logicdomain.SessionRef{
		SessionID:  23,
		SessionKey: "sess-real-sqlite-query-drift",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	insertedIDs := seedRealSQLiteBM25Fixtures(t, store, session)

	localResult := searchMemoryWithRealSQLiteFTS(t, store, session, "本地 部署 模式")
	cloudResult := searchMemoryWithRealSQLiteFTS(t, store, session, "云端 部署 模式")
	if len(localResult.Results) != 1 || len(localResult.Results[0].Hits) == 0 {
		t.Fatalf("expected local query to return hits, got %+v", localResult.Results)
	}
	if len(cloudResult.Results) != 1 || len(cloudResult.Results[0].Hits) == 0 {
		t.Fatalf("expected cloud query to return hits, got %+v", cloudResult.Results)
	}
	if localResult.Results[0].Hits[0].MemoryRef.ID != insertedIDs[0] {
		t.Fatalf("expected local query top hit %d, got %+v", insertedIDs[0], localResult.Results[0].Hits)
	}
	if cloudResult.Results[0].Hits[0].MemoryRef.ID != insertedIDs[1] {
		t.Fatalf("expected cloud query top hit %d, got %+v", insertedIDs[1], cloudResult.Results[0].Hits)
	}
}

// newRealSQLiteFTSTestStore opens one real temporary SQLite store backed by the packaged dynamic library
// so integration tests can validate the same FFI path used by the shipped runtime.
// newRealSQLiteFTSTestStore 用于打开一个由已打包动态库驱动的临时 SQLite 存储，
// 让集成测试可以验证正式运行时使用的同一条 FFI 路径。
func newRealSQLiteFTSTestStore(t *testing.T) *sqliteadapter.Store {
	t.Helper()

	libraryPath := locatePackagedSQLiteLibrary(t)
	dbPath := filepath.Join(t.TempDir(), "database", "sqlite.db")
	store, err := sqliteadapter.NewStore(libraryPath, dbPath, 5*time.Second, sqliteadapter.StoreOptions{
		TokenizerMode: "jieba",
	})
	if err != nil {
		t.Fatalf("open real sqlite ffi store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown real sqlite ffi store: %v", err)
		}
	})
	return store
}

// locatePackagedSQLiteLibrary resolves the repository root from the current test file and then loads the packaged
// host-specific SQLite dynamic library from output/libs, skipping the test when the artifact is absent.
// locatePackagedSQLiteLibrary 用于从当前测试文件反推出仓库根目录，再到 output/libs 中定位已打包、
// 与当前宿主系统匹配的 SQLite 动态库；若产物缺失则跳过测试。
func locatePackagedSQLiteLibrary(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	libPath := filepath.Join(root, "output", "libs", sqliteLibraryFileName())
	if _, err := os.Stat(libPath); err != nil {
		t.Skipf("packaged sqlite library not found at %s: %v", libPath, err)
	}
	return libPath
}

// sqliteLibraryFileName returns the host-specific packaged SQLite library name so the test can load the same artifact
// produced by make/build on Windows, Linux, and macOS.
// sqliteLibraryFileName 用于返回当前宿主系统对应的 SQLite 动态库文件名，
// 让测试能加载 Windows、Linux、macOS 打包出的正式产物。
func sqliteLibraryFileName() string {
	switch runtime.GOOS {
	case "windows":
		return "vldb_sqlite.dll"
	case "darwin":
		return "libvldb_sqlite.dylib"
	default:
		return "libvldb_sqlite.so"
	}
}

// seedRealSQLiteBM25Fixtures inserts two durable memory rows whose lexical strength differs in a predictable way,
// so real FTS ranking can be asserted without relying on mocked recall hits.
// seedRealSQLiteBM25Fixtures 用于插入两条词法强度可预测的长期记忆行，
// 使测试能够在不依赖 mocked recall 的前提下断言真实 FTS 排序。
func seedRealSQLiteBM25Fixtures(t *testing.T, store *sqliteadapter.Store, session logicdomain.SessionRef) []uint64 {
	t.Helper()

	ctx := context.Background()
	rows := []logicdomain.MemoryNodeRecord{
		{
			VectorID:    "vec-real-local",
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "本地 部署 模式",
			Details:     "本地 部署 模式 本地 部署 模式 稳定 回滚 简单",
			Status:      logicdomain.MemoryStatusActive,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Time{},
			Priority:    2,
			MemoryLevel: 2,
		},
		{
			VectorID:    "vec-real-cloud",
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    "云端 部署 模式",
			Details:     "云端 部署 模式 弹性 扩缩容 网络 出口",
			Status:      logicdomain.MemoryStatusActive,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			ExpiresAt:   time.Time{},
			Priority:    2,
			MemoryLevel: 2,
		},
	}

	insertedIDs := make([]uint64, 0, len(rows))
	for _, row := range rows {
		inserted, err := store.CreateDirectMemoryNode(ctx, session, row)
		if err != nil {
			t.Fatalf("seed real sqlite memory node %q: %v", row.VectorID, err)
		}
		insertedIDs = append(insertedIDs, inserted.ID)
	}
	return insertedIDs
}

// searchMemoryWithRealSQLiteFTS runs the app-layer memory search against one real SQLite lexical store plus stubbed profile,
// embedding, and empty vector dependencies, so the integration test stays focused on the lexical/BM25 path.
// searchMemoryWithRealSQLiteFTS 用于在真实 SQLite lexical 存储之上运行应用层记忆检索，
// 同时用桩替代 profile、embedding 与空向量依赖，让集成测试聚焦 lexical / BM25 路径。
func searchMemoryWithRealSQLiteFTS(t *testing.T, store *sqliteadapter.Store, session logicdomain.SessionRef, query string) MemoryQueryResult {
	t.Helper()

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      session.UserID,
				UserID:      session.UserID,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      session.ProjectID,
				UserID:      session.UserID,
				TeamID:      session.TeamID,
				SpaceID:     session.SpaceID,
				ProjectID:   session.ProjectID,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureHybrid(true, 5, 60)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    session.UserID,
		ProjectID: session.ProjectID,
		Queries:   []string{query},
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search with real sqlite fts returned error: %v", err)
	}
	return result
}
