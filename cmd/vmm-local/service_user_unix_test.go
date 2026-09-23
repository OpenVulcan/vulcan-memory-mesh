//go:build linux || darwin

// service_user_unix_test.go verifies Unix service-account storage ownership checks.
// service_user_unix_test.go 用于验证 Unix 服务账户的数据目录归属检查。
package main

import (
	"os"
	osuser "os/user"
	"path/filepath"
	"testing"
)

// TestValidateServiceStoragePathUsesOwnerIdentity verifies an existing data root must belong to the selected account.
// TestValidateServiceStoragePathUsesOwnerIdentity 验证已有数据根必须属于选定账户。
func TestValidateServiceStoragePathUsesOwnerIdentity(t *testing.T) {
	current, err := osuser.Current()
	if err != nil {
		t.Skipf("current local account is unavailable: %v", err)
	}
	account, err := resolveServiceUser(current.Username)
	if err != nil {
		t.Skipf("current account cannot be resolved as a service user: %v", err)
	}
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create private data root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod private data root: %v", err)
	}
	if err := validateServiceStoragePath(root, account, true); err != nil {
		t.Fatalf("owned data root rejected: %v", err)
	}
	wrong := account
	wrong.UID = "4294967294"
	if err := validateServiceStoragePath(root, wrong, true); err == nil {
		t.Fatal("data root with a different owner uid should be rejected")
	}
}

// TestValidateServiceStoragePathChecksCreationParent verifies missing database files require a writable target-owned parent.
// TestValidateServiceStoragePathChecksCreationParent 验证缺失数据库文件必须位于目标账户可写且拥有的父目录中。
func TestValidateServiceStoragePathChecksCreationParent(t *testing.T) {
	current, err := osuser.Current()
	if err != nil {
		t.Skipf("current local account is unavailable: %v", err)
	}
	account, err := resolveServiceUser(current.Username)
	if err != nil {
		t.Skipf("current account cannot be resolved as a service user: %v", err)
	}
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create private data root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod private data root: %v", err)
	}
	missingDatabase := filepath.Join(root, "sqlite.db")
	if err := validateServiceStoragePath(missingDatabase, account, false); err != nil {
		t.Fatalf("missing database under owned root rejected: %v", err)
	}
}

// TestValidateServiceStoragePathRejectsInaccessibleAncestor verifies every existing ancestor is traversable.
// TestValidateServiceStoragePathRejectsInaccessibleAncestor 验证每个现存祖先目录都必须允许目标账户穿越。
func TestValidateServiceStoragePathRejectsInaccessibleAncestor(t *testing.T) {
	current, err := osuser.Current()
	if err != nil {
		t.Skipf("current local account is unavailable: %v", err)
	}
	account, err := resolveServiceUser(current.Username)
	if err != nil {
		t.Skipf("current account cannot be resolved as a service user: %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("create blocked ancestor: %v", err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("remove blocked ancestor traversal permission: %v", err)
	}
	if err := validateServiceStoragePath(filepath.Join(blocked, "data"), account, true); err == nil {
		t.Fatal("storage path under an inaccessible ancestor should be rejected")
	}
}

// TestServiceDotEnvCandidatesMatchesRuntimeOrder verifies the service preflight mirrors config.dotEnvCandidates exactly.
// TestServiceDotEnvCandidatesMatchesRuntimeOrder 验证服务预检严格复现 config.dotEnvCandidates 的顺序。
func TestServiceDotEnvCandidatesMatchesRuntimeOrder(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "configs", "base.yaml")
	want := []string{filepath.Join(root, ".env"), filepath.Join(root, "configs", ".env")}
	got := serviceDotEnvCandidates(configPath)
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("candidate[%d] = %q, want %q", index, got[index], want[index])
		}
	}
	nonPackaged := serviceDotEnvCandidates(filepath.Join(root, "overrides", "config.yaml"))
	if len(nonPackaged) != 1 || nonPackaged[0] != filepath.Join(root, "overrides", ".env") {
		t.Fatalf("non-packaged candidates = %#v", nonPackaged)
	}
}

// TestValidateServiceAccountEnvCandidatesChecksExistingFiles verifies every existing candidate is readable by the service account.
// TestValidateServiceAccountEnvCandidatesChecksExistingFiles 验证每个现存候选文件都必须可由服务账户读取。
func TestValidateServiceAccountEnvCandidatesChecksExistingFiles(t *testing.T) {
	current, err := osuser.Current()
	if err != nil {
		t.Skipf("current local account is unavailable: %v", err)
	}
	account, err := resolveServiceUser(current.Username)
	if err != nil {
		t.Skipf("current account cannot be resolved as a service user: %v", err)
	}
	root := t.TempDir()
	configPath := filepath.Join(root, "configs", "base.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	parentEnv := filepath.Join(root, ".env")
	localEnv := filepath.Join(root, "configs", ".env")
	for _, candidate := range []string{parentEnv, localEnv} {
		if err := os.WriteFile(candidate, []byte("VMM_TEST=value\n"), 0o600); err != nil {
			t.Fatalf("create .env candidate %q: %v", candidate, err)
		}
	}
	if err := validateServiceAccountEnvCandidates([]string{configPath}, account); err != nil {
		t.Fatalf("readable .env candidates rejected: %v", err)
	}
	if err := os.Chmod(parentEnv, 0o000); err != nil {
		t.Fatalf("remove parent .env read permission: %v", err)
	}
	if err := validateServiceAccountEnvCandidates([]string{configPath}, account); err == nil {
		t.Fatal("unreadable parent .env candidate should be rejected")
	}
	if err := os.Chmod(parentEnv, 0o600); err != nil {
		t.Fatalf("restore parent .env permission: %v", err)
	}
	if err := os.Chmod(localEnv, 0o000); err != nil {
		t.Fatalf("remove local .env read permission: %v", err)
	}
	if err := validateServiceAccountEnvCandidates([]string{configPath}, account); err == nil {
		t.Fatal("unreadable local .env candidate should be rejected")
	}
}
