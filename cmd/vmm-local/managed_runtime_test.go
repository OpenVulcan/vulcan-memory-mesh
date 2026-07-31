// managed_runtime_test.go verifies managed-runtime ownership safeguards.
// managed_runtime_test.go 用于验证托管运行时所有权护栏。
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagedHostIdentityMatches verifies both exact generation matches and PID-reuse rejection.
// TestManagedHostIdentityMatches 用于验证精确代次匹配以及 PID 复用拒绝。
func TestManagedHostIdentityMatches(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "inference-service.json")
	body := []byte(`{"process_id":42,"started_at_unix_ms":"1700000000000"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write discovery fixture: %v", err)
	}

	matches, err := managedHostIdentityMatches(path, 42, 1700000000000)
	if err != nil {
		t.Fatalf("match exact identity: %v", err)
	}
	if !matches {
		t.Fatal("expected exact managed host identity to match")
	}

	matches, err = managedHostIdentityMatches(path, 42, 1700000000001)
	if err != nil {
		t.Fatalf("compare reused process identity: %v", err)
	}
	if matches {
		t.Fatal("expected changed startup identity to reject reused process ID")
	}
}
