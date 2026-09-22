// native_storage_benchmark_test.go compares isolated real backends and reports Go allocations plus Windows resident working-set samples.
// native_storage_benchmark_test.go 对照隔离的真实后端，记录 Go 分配量及 Windows 常驻工作集采样。
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	lancedbadapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	sqliteadapter "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// benchmarkStorageTableName keeps every benchmark vector table isolated from production and other test tables.
	// benchmarkStorageTableName 让每个 benchmark 向量表与生产表和其他测试表隔离。
	benchmarkStorageTableName = "native_storage_benchmark_vectors"

	// benchmarkStorageVectorColumn is the shared vector payload column used by both storage modes.
	// benchmarkStorageVectorColumn 是两种存储模式共用的向量载荷列名。
	benchmarkStorageVectorColumn = "vector"

	// benchmarkStorageDimension fixes the small vector shape used by the deterministic benchmark corpus.
	// benchmarkStorageDimension 固定确定性 benchmark 小语料使用的向量维度。
	benchmarkStorageDimension = 3

	// benchmarkStorageTimeout bounds each real adapter operation without changing production configuration.
	// benchmarkStorageTimeout 为真实适配器操作设置边界，不改变生产配置。
	benchmarkStorageTimeout = 5 * time.Second
)

// BenchmarkNativeStorageComparison measures startup, Chinese lexical query, vector upsert, and vector query for split and native pairs.
// BenchmarkNativeStorageComparison 测量 split 与 native 存储组合的启动、中文词法查询、向量 upsert 和向量查询。
func BenchmarkNativeStorageComparison(b *testing.B) {
	legacySQLiteLibrary, legacyLanceLibrary, nativeLanceLibrary := benchmarkStorageLibraries(b)
	corpus := benchmarkStorageCorpus()

	b.Run("startup/legacy", func(b *testing.B) {
		b.ReportAllocs()
		base := b.TempDir()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			root := filepath.Join(base, fmt.Sprintf("legacy-startup-%d", i))
			sqlite, vector := openBenchmarkSplitPair(b, root, legacySQLiteLibrary, legacyLanceLibrary)
			b.StopTimer()
			closeBenchmarkPair(b, sqlite, vector)
			b.StartTimer()
		}
	})

	b.Run("startup/native", func(b *testing.B) {
		b.ReportAllocs()
		base := b.TempDir()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			root := filepath.Join(base, fmt.Sprintf("native-startup-%d", i))
			sqlite, vector := openBenchmarkNativePair(b, root, nativeLanceLibrary)
			b.StopTimer()
			closeBenchmarkPair(b, sqlite, vector)
			b.StartTimer()
		}
	})

	b.Run("chinese_query/legacy", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "legacy-chinese-query")
		sqlite, vector := openBenchmarkSplitPair(b, root, legacySQLiteLibrary, legacyLanceLibrary)
		session := seedBenchmarkRelationalCorpus(b, sqlite, corpus)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hits, err := sqlite.SearchLexicalMemory(context.Background(), "部署 模式", 8, session.SearchFilter())
			if err != nil {
				b.Fatalf("legacy Chinese lexical query: %v", err)
			}
			if len(hits) == 0 {
				b.Fatal("legacy Chinese query did not recall the benchmark corpus")
			}
		}
		b.StopTimer()
		reportBenchmarkWorkingSet(b)
		closeBenchmarkPair(b, sqlite, vector)
	})

	b.Run("chinese_query/native", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "native-chinese-query")
		sqlite, vector := openBenchmarkNativePair(b, root, nativeLanceLibrary)
		session := seedBenchmarkRelationalCorpus(b, sqlite, corpus)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hits, err := sqlite.SearchLexicalMemory(context.Background(), "部署 模式", 8, session.SearchFilter())
			if err != nil {
				b.Fatalf("native Chinese lexical query: %v", err)
			}
			if len(hits) == 0 {
				b.Fatal("native Chinese query did not recall the benchmark corpus")
			}
		}
		b.StopTimer()
		reportBenchmarkWorkingSet(b)
		closeBenchmarkPair(b, sqlite, vector)
	})

	b.Run("vector_upsert/legacy", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "legacy-vector-upsert")
		sqlite, vector := openBenchmarkSplitPair(b, root, legacySQLiteLibrary, legacyLanceLibrary)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := vector.Upsert(context.Background(), corpus[i%len(corpus)]); err != nil {
				b.Fatalf("legacy vector upsert: %v", err)
			}
		}
		b.StopTimer()
		closeBenchmarkPair(b, sqlite, vector)
	})

	b.Run("vector_upsert/native", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "native-vector-upsert")
		sqlite, vector := openBenchmarkNativePair(b, root, nativeLanceLibrary)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := vector.Upsert(context.Background(), corpus[i%len(corpus)]); err != nil {
				b.Fatalf("native vector upsert: %v", err)
			}
		}
		b.StopTimer()
		closeBenchmarkPair(b, sqlite, vector)
	})

	b.Run("vector_query/legacy", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "legacy-vector-query")
		sqlite, vector := openBenchmarkSplitPair(b, root, legacySQLiteLibrary, legacyLanceLibrary)
		seedBenchmarkVectors(b, vector, corpus)
		query := corpus[0]
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hits, err := vector.Search(context.Background(), query.Vector, 3, query.Filter)
			if err != nil {
				b.Fatalf("legacy vector query: %v", err)
			}
			if len(hits) == 0 || hits[0].ID != query.ID {
				b.Fatal("legacy vector query did not return the nearest fixture")
			}
		}
		b.StopTimer()
		reportBenchmarkWorkingSet(b)
		closeBenchmarkPair(b, sqlite, vector)
	})

	b.Run("vector_query/native", func(b *testing.B) {
		b.ReportAllocs()
		root := filepath.Join(b.TempDir(), "native-vector-query")
		sqlite, vector := openBenchmarkNativePair(b, root, nativeLanceLibrary)
		seedBenchmarkVectors(b, vector, corpus)
		query := corpus[0]
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hits, err := vector.Search(context.Background(), query.Vector, 3, query.Filter)
			if err != nil {
				b.Fatalf("native vector query: %v", err)
			}
			if len(hits) == 0 || hits[0].ID != query.ID {
				b.Fatal("native vector query did not return the nearest fixture")
			}
		}
		b.StopTimer()
		reportBenchmarkWorkingSet(b)
		closeBenchmarkPair(b, sqlite, vector)
	})
}

// reportBenchmarkWorkingSet samples the live Windows process outside the timer; compare backends in separate benchmark processes.
// reportBenchmarkWorkingSet 在计时外采样 Windows 活动进程工作集；比较两个后端时必须使用各自独立的基准进程。
func reportBenchmarkWorkingSet(b *testing.B) {
	b.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", fmt.Sprintf("(Get-Process -Id %d).WorkingSet64", os.Getpid()))
	output, err := command.Output()
	if err != nil {
		b.Fatalf("sample benchmark working set: %v", err)
	}
	residentBytes, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		b.Fatalf("decode benchmark working set: %v", err)
	}
	b.ReportMetric(float64(residentBytes), "resident-bytes")
}

// benchmarkStorageLibraries requires the explicit native library and reuses the existing packaged-root library resolver for legacy artifacts.
// benchmarkStorageLibraries 要求显式 native 动态库，并复用现有打包根目录解析器定位旧模式动态库。
func benchmarkStorageLibraries(b *testing.B) (string, string, string) {
	b.Helper()
	explicitNative := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY"))
	if explicitNative == "" {
		b.Skip("set VMM_NATIVE_LANCEDB_LIBRARY to run native storage benchmarks")
	}
	if !filepath.IsAbs(explicitNative) {
		explicitNative = filepath.Join(testRepositoryRoot(), explicitNative)
	}
	if !isRegularFile(explicitNative) {
		b.Skipf("VMM_NATIVE_LANCEDB_LIBRARY=%s is not a regular file", explicitNative)
	}

	root := testRepositoryRoot()
	sqliteName, lanceName := resolveHostLibraryNames()
	legacySQLite := resolveHostLibraryPath([]string{root}, sqliteName)
	legacyLance := resolveHostLibraryPath([]string{root}, lanceName)
	if !isRegularFile(legacySQLite) {
		b.Skipf("legacy SQLite library unavailable; checked %s", legacySQLite)
	}
	if !isRegularFile(legacyLance) {
		b.Skipf("legacy LanceDB library unavailable; checked %s", legacyLance)
	}
	return legacySQLite, legacyLance, explicitNative
}

// benchmarkStorageCorpus returns the same fixed Chinese records used by every mode and operation in this benchmark.
// benchmarkStorageCorpus 返回所有模式和操作共用的固定中文记录集。
func benchmarkStorageCorpus() []logicdomain.MemoryRecord {
	createdAt := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	return []logicdomain.MemoryRecord{
		{
			ID:   "native-bench-local",
			Text: "本地部署模式支持离线回滚和稳定启动",
			Vector: []float32{
				0.10, 0.20, 0.30,
			},
			Filter:    logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1},
			Status:    logicdomain.MemoryStatusActive,
			CreatedAt: createdAt,
		},
		{
			ID:   "native-bench-cloud",
			Text: "云端部署模式需要明确网络边界和审计策略",
			Vector: []float32{
				0.20, 0.30, 0.40,
			},
			Filter:    logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1},
			Status:    logicdomain.MemoryStatusActive,
			CreatedAt: createdAt.Add(time.Second),
		},
		{
			ID:   "native-bench-migration",
			Text: "迁移完成后需要重建中文 FTS 索引并校验向量身份",
			Vector: []float32{
				0.30, 0.40, 0.50,
			},
			Filter:    logicdomain.SearchFilter{UserID: 1, TeamID: 1, SpaceID: 1, ProjectID: 1},
			Status:    logicdomain.MemoryStatusActive,
			CreatedAt: createdAt.Add(2 * time.Second),
		},
	}
}

// openBenchmarkSplitPair opens the legacy SQLite and legacy LanceDB stores below one fresh temporary root.
// openBenchmarkSplitPair 在一个全新的临时根目录下打开旧 SQLite 与旧 LanceDB 存储。
func openBenchmarkSplitPair(b *testing.B, root, sqliteLibrary, lanceLibrary string) (*sqliteadapter.Store, *lancedbadapter.Store) {
	b.Helper()
	sqlite, err := sqliteadapter.NewStore(sqliteLibrary, filepath.Join(root, "sqlite.db"), benchmarkStorageTimeout, sqliteadapter.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		b.Fatalf("open legacy SQLite benchmark store: %v", err)
	}
	vector, err := lancedbadapter.NewStore(lanceLibrary, filepath.Join(root, "lancedb"), benchmarkStorageTimeout, benchmarkStorageTableName, benchmarkStorageVectorColumn, benchmarkStorageDimension)
	if err != nil {
		_ = sqlite.Shutdown(context.Background())
		b.Fatalf("open legacy LanceDB benchmark store: %v", err)
	}
	return sqlite, vector
}

// openBenchmarkNativePair opens the native SQLite and native LanceDB stores below one fresh temporary root.
// openBenchmarkNativePair 在一个全新的临时根目录下打开原生 SQLite 与原生 LanceDB 存储。
func openBenchmarkNativePair(b *testing.B, root, lanceLibrary string) (*sqliteadapter.Store, *lancedbadapter.Store) {
	b.Helper()
	sqlite, err := sqliteadapter.NewNativeStore(filepath.Join(root, "sqlite.db"), benchmarkStorageTimeout, sqliteadapter.StoreOptions{TokenizerMode: "gse"})
	if err != nil {
		b.Fatalf("open native SQLite benchmark store: %v", err)
	}
	vector, err := lancedbadapter.NewNativeStore(lanceLibrary, filepath.Join(root, "lancedb"), benchmarkStorageTimeout, benchmarkStorageTableName, benchmarkStorageVectorColumn, benchmarkStorageDimension)
	if err != nil {
		_ = sqlite.Shutdown(context.Background())
		b.Fatalf("open native LanceDB benchmark store: %v", err)
	}
	return sqlite, vector
}

// seedBenchmarkRelationalCorpus inserts the fixed corpus through the real relational memory write path and returns its resolved scope.
// seedBenchmarkRelationalCorpus 通过真实关系记忆写入路径插入固定语料，并返回解析后的范围。
func seedBenchmarkRelationalCorpus(b *testing.B, store *sqliteadapter.Store, corpus []logicdomain.MemoryRecord) logicdomain.SessionRef {
	b.Helper()
	ctx := context.Background()
	session, err := store.ResolveRequestScope(ctx, "native-storage-benchmark", 1, 1)
	if err != nil {
		b.Fatalf("resolve benchmark session: %v", err)
	}
	for _, record := range corpus {
		_, err := store.CreateDirectMemoryNode(ctx, session, logicdomain.MemoryNodeRecord{
			VectorID:    record.ID,
			Vector:      append([]float32(nil), record.Vector...),
			SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:    record.Text,
			Details:     record.Text,
			Status:      logicdomain.MemoryStatusActive,
			Priority:    logicdomain.MemoryPriorityP2,
			MemoryLevel: logicdomain.MemoryLevelStable,
			CreatedAt:   record.CreatedAt,
			UpdatedAt:   record.CreatedAt,
		})
		if err != nil {
			b.Fatalf("seed benchmark relational corpus %s: %v", record.ID, err)
		}
	}
	return session
}

// seedBenchmarkVectors inserts the same fixed corpus before vector-query timing starts.
// seedBenchmarkVectors 在向量查询计时开始前插入同一固定语料。
func seedBenchmarkVectors(b *testing.B, store *lancedbadapter.Store, corpus []logicdomain.MemoryRecord) {
	b.Helper()
	ctx := context.Background()
	for _, record := range corpus {
		if err := store.Upsert(ctx, record); err != nil {
			b.Fatalf("seed benchmark vector %s: %v", record.ID, err)
		}
	}
}

// closeBenchmarkPair releases both real adapters after the measured section so shutdown time is excluded from results.
// closeBenchmarkPair 在测量区间外释放两个真实适配器，避免关闭耗时进入结果。
func closeBenchmarkPair(b *testing.B, sqlite *sqliteadapter.Store, vector *lancedbadapter.Store) {
	b.Helper()
	ctx := context.Background()
	if vector != nil {
		if err := vector.Shutdown(ctx); err != nil {
			b.Errorf("shutdown benchmark LanceDB store: %v", err)
		}
	}
	if sqlite != nil {
		if err := sqlite.Shutdown(ctx); err != nil {
			b.Errorf("shutdown benchmark SQLite store: %v", err)
		}
	}
}
