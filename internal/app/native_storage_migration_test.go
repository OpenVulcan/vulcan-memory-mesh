// native_storage_migration_test.go verifies migration preconditions and credential-free handoff artifacts.
// native_storage_migration_test.go 验证迁移前置条件与不含凭据的交接产物。
package app

import (
	"crypto/sha256"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"gopkg.in/yaml.v3"
)

// TestNativeMigrationFileDoesNotReplaceExistingArtifacts protects a failed run's evidence from accidental reuse.
// TestNativeMigrationFileDoesNotReplaceExistingArtifacts 保护失败迁移留下的证据，防止后续写入意外覆盖。
func TestNativeMigrationFileDoesNotReplaceExistingArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration-incomplete")
	if err := writeNativeMigrationFile(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeMigrationFile(path, []byte("replacement")); !os.IsExist(err) {
		t.Fatalf("existing artifact was not rejected: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "original" {
		t.Fatalf("existing artifact changed: %q, %v", content, err)
	}
}

// TestNativeMigrationVectorDeduplicationRejectsConflictingRestorableRows protects the one-row-per-vector contract during trash replay.
// TestNativeMigrationVectorDeduplicationRejectsConflictingRestorableRows 在回放回收数据时保护每个向量标识唯一且无歧义的契约。
func TestNativeMigrationVectorDeduplicationRejectsConflictingRestorableRows(t *testing.T) {
	record := logicdomain.MemoryRecord{ID: "restorable-vector", Text: "中文记忆", Vector: []float32{1, 2}, Filter: logicdomain.SearchFilter{ProjectID: 7}}
	seen := make(map[string][sha256.Size]byte)
	if fresh, err := registerNativeMigrationVector(record, 2, seen); err != nil || !fresh {
		t.Fatalf("first payload rejected: fresh=%v err=%v", fresh, err)
	}
	if fresh, err := registerNativeMigrationVector(record, 2, seen); err != nil || fresh {
		t.Fatalf("identical payload not deduplicated: fresh=%v err=%v", fresh, err)
	}
	for _, conflict := range []logicdomain.MemoryRecord{
		{ID: record.ID, Text: record.Text, Vector: []float32{2, 1}, Filter: record.Filter},
		{ID: record.ID, Text: record.Text, Vector: record.Vector, Filter: logicdomain.SearchFilter{ProjectID: 8}},
		{ID: record.ID, Text: "不同内容", Vector: record.Vector, Filter: record.Filter},
	} {
		if _, err := registerNativeMigrationVector(conflict, 2, seen); err == nil {
			t.Fatalf("ambiguous payload accepted: %+v", conflict)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("conflict changed the accepted vector set: %d", len(seen))
	}
}

// TestValidateNativeMigrationVectorRejectsInvalidFacts ensures migration never silently replaces invalid vectors.
// TestValidateNativeMigrationVectorRejectsInvalidFacts 确保迁移不静默替换无效向量。
func TestValidateNativeMigrationVectorRejectsInvalidFacts(t *testing.T) {
	for _, record := range []logicdomain.MemoryRecord{
		{ID: "", Vector: []float32{1, 2}},
		{ID: "missing"},
		{ID: "dimension", Vector: []float32{1}},
		{ID: "nan", Vector: []float32{1, float32(math.NaN())}},
		{ID: "inf", Vector: []float32{1, float32(math.Inf(1))}},
	} {
		if err := validateNativeMigrationVector(record, 2); err == nil {
			t.Fatalf("invalid vector accepted: %q", record.ID)
		}
	}
	if err := validateNativeMigrationVector(logicdomain.MemoryRecord{ID: "valid", Vector: []float32{1, 2}}, 2); err != nil {
		t.Fatal(err)
	}
}

// TestNativeMigrationArtifactsPreserveLiteralPaths verifies YAML escaping and explicit vector schema parameters.
// TestNativeMigrationArtifactsPreserveLiteralPaths 验证 YAML 路径转义与明确的向量结构参数。
func TestNativeMigrationArtifactsPreserveLiteralPaths(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Config{}
	cfg.SQLite.Native.Tokenizer = "gse"
	cfg.LanceDB.TableName = "migration_vectors"
	cfg.LanceDB.VectorColumn = "vector_float32"
	cfg.Embedding.Dimension = 2
	layout := nativeStorageLayout{SQLiteDatabase: `C:\用户\a # b\sqlite.db`, LanceDBDirectory: `C:\用户\a # b\lancedb`, LanceDBLibrary: `C:\运行\vmm_lancedb_native.dll`}
	if err := writeNativeMigrationArtifacts(directory, cfg, layout, nativeMigrationReport{SourceMode: "split", Dimension: 2}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(directory, "native-storage-override.fragment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	native := document["sqlite"].(map[string]any)["native"].(map[string]any)
	if native["path"] != layout.SQLiteDatabase {
		t.Fatalf("path changed by YAML encoding: %v", native["path"])
	}
	if strings.Contains(string(encoded), "api_key") {
		t.Fatal("storage override unexpectedly contains provider credentials")
	}
}
