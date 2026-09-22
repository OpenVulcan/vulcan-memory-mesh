// native_vector_rebuild_marker_test.go verifies the fail-closed marker lifecycle used by native vector rebuild maintenance.
// native_vector_rebuild_marker_test.go 用于验证原生向量维护使用的失败关闭标记生命周期。
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeVectorRebuildMarkerLifecycle verifies creation is durable, retries preserve the marker, and completion removes only the paired marker.
// TestNativeVectorRebuildMarkerLifecycle 验证标记可持久创建、重试会保留标记，完成时只删除配对标记。
func TestNativeVectorRebuildMarkerLifecycle(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "native", "vmm.db")
	markerPath := nativeVectorRebuildIncompleteMarkerPath(databasePath)
	if want := databasePath + ".vector-rebuild-incomplete"; markerPath != want {
		t.Fatalf("marker path = %q, want %q", markerPath, want)
	}
	exists, err := nativeVectorRebuildMarkerExists(databasePath)
	if err != nil {
		t.Fatalf("inspect missing marker: %v", err)
	}
	if exists {
		t.Fatal("missing marker reported as present")
	}
	if err := writeNativeVectorRebuildMarker(databasePath); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	first, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !strings.Contains(string(first), `"version":1`) || !strings.Contains(string(first), `"started_at"`) {
		t.Fatalf("unexpected marker payload: %s", first)
	}
	if err := writeNativeVectorRebuildMarker(databasePath); err != nil {
		t.Fatalf("rewrite marker during recovery retry: %v", err)
	}
	second, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker after retry: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("recovery retry replaced marker payload: first=%s second=%s", first, second)
	}
	exists, err = nativeVectorRebuildMarkerExists(databasePath)
	if err != nil || !exists {
		t.Fatalf("marker should remain during recovery: exists=%v err=%v", exists, err)
	}
	if err := removeNativeVectorRebuildMarker(databasePath); err != nil {
		t.Fatalf("remove marker after successful rebuild: %v", err)
	}
	exists, err = nativeVectorRebuildMarkerExists(databasePath)
	if err != nil {
		t.Fatalf("inspect removed marker: %v", err)
	}
	if exists {
		t.Fatal("marker remained after successful rebuild")
	}
	if err := removeNativeVectorRebuildMarker(databasePath); err != nil {
		t.Fatalf("remove already absent marker should be idempotent: %v", err)
	}
}

// TestNativeVectorRebuildMarkerRejectsNonRegularReplacement verifies a directory cannot masquerade as the recovery marker.
// TestNativeVectorRebuildMarkerRejectsNonRegularReplacement 验证目录不能伪装成恢复标记。
func TestNativeVectorRebuildMarkerRejectsNonRegularReplacement(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "native.db")
	markerPath := nativeVectorRebuildIncompleteMarkerPath(databasePath)
	if err := os.MkdirAll(markerPath, 0o700); err != nil {
		t.Fatalf("create marker directory: %v", err)
	}
	if _, err := nativeVectorRebuildMarkerExists(databasePath); err == nil {
		t.Fatal("expected non-regular marker to fail closed")
	}
	if err := writeNativeVectorRebuildMarker(databasePath); err == nil {
		t.Fatal("expected marker write to reject non-regular destination")
	}
}
