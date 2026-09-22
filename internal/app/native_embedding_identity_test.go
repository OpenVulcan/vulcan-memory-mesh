// native_embedding_identity_test.go verifies fail-closed identity tracking without contacting any provider or database engine.
// native_embedding_identity_test.go 用于验证失败关闭的 identity 跟踪，不连接任何 provider 或数据库引擎。
package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// nativeEmbeddingIdentityTestConfig returns a native configuration whose credentials must never reach the sidecar.
// nativeEmbeddingIdentityTestConfig 返回一份凭据绝不能进入 sidecar 的原生配置。
func nativeEmbeddingIdentityTestConfig(root string) config.Config {
	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "native"
	cfg.SQLite.Native.Path = filepath.Join(root, "database", "native.db")
	cfg.LanceDB.Native.Path = filepath.Join(root, "database", "lancedb")
	cfg.LanceDB.Native.LibraryPath = filepath.Join(root, "libs", "vmm_lancedb_native.dll")
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.Model = "text-embedding-test"
	cfg.Embedding.Dimension = 3
	cfg.Embedding.APIKeys = []string{"secret-api-key"}
	cfg.Embedding.Endpoint = "https://secret.example.invalid"
	cfg.Embedding.Params = map[string]any{
		"encoding_format": "float",
		"nested":          map[string]any{"beta": 2, "alpha": 1},
	}
	cfg.Embedding.ModelParams = map[string]map[string]any{
		"text-embedding-test": {"dimensions": 3},
		"other-model":         {"dimensions": 99},
	}
	return cfg
}

// TestEnsureNativeEmbeddingIdentityCreatesOnlyForNewDatabase verifies first startup atomically creates a credential-free sidecar while an existing database requires prior identity.
// TestEnsureNativeEmbeddingIdentityCreatesOnlyForNewDatabase 用于验证首次启动只为新数据库创建无凭据 sidecar，而已有数据库必须先有 identity。
func TestEnsureNativeEmbeddingIdentityCreatesOnlyForNewDatabase(t *testing.T) {
	root := t.TempDir()
	cfg := nativeEmbeddingIdentityTestConfig(root)
	if err := ensureNativeEmbeddingIdentity(cfg, cfg.SQLite.Native.Path, false); err != nil {
		t.Fatalf("create identity for new database: %v", err)
	}
	identityPath := nativeEmbeddingIdentityPath(cfg.SQLite.Native.Path)
	identity, err := readNativeEmbeddingIdentity(identityPath)
	if err != nil {
		t.Fatalf("read created identity: %v", err)
	}
	if identity.Provider != cfg.Embedding.Provider || identity.Model != cfg.Embedding.Model || identity.Dimension != cfg.Embedding.Dimension {
		t.Fatalf("identity metadata = %+v", identity)
	}
	raw, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read identity bytes: %v", err)
	}
	if strings.Contains(string(raw), "secret-api-key") || strings.Contains(string(raw), "secret.example.invalid") {
		t.Fatalf("identity persisted credentials or endpoint: %s", raw)
	}
	if _, err := os.Stat(cfg.SQLite.Native.Path); !os.IsNotExist(err) {
		t.Fatalf("identity creation unexpectedly created database: err=%v", err)
	}

	existingDB := filepath.Join(root, "existing.db")
	if err := os.WriteFile(existingDB, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeEmbeddingIdentity(cfg, existingDB, false); err == nil {
		t.Fatal("accepted existing database without identity")
	}
	if err := ensureNativeEmbeddingIdentity(cfg, existingDB, true); err == nil {
		t.Fatal("maintenance accepted existing database without identity")
	}
}

// TestEnsureNativeEmbeddingIdentityAllowsExplicitMaintenanceChangeWithoutWriting verifies allowChange only validates structure and leaves the previous identity untouched.
// TestEnsureNativeEmbeddingIdentityAllowsExplicitMaintenanceChangeWithoutWriting 用于验证 allowChange 只校验结构，不会改写旧 identity。
func TestEnsureNativeEmbeddingIdentityAllowsExplicitMaintenanceChangeWithoutWriting(t *testing.T) {
	root := t.TempDir()
	cfg := nativeEmbeddingIdentityTestConfig(root)
	if err := os.MkdirAll(filepath.Dir(cfg.SQLite.Native.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SQLite.Native.Path, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	identityPath := nativeEmbeddingIdentityPath(cfg.SQLite.Native.Path)
	if err := writeNativeEmbeddingIdentity(identityPath, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}

	changed := cfg
	changed.Embedding.Model = "changed-model"
	changed.Embedding.Dimension = 4
	if err := ensureNativeEmbeddingIdentity(changed, changed.SQLite.Native.Path, true); err != nil {
		t.Fatalf("allow explicit maintenance identity change: %v", err)
	}
	after, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("maintenance validation rewrote identity: before=%s after=%s", before, after)
	}
	if err := ensureNativeEmbeddingIdentity(changed, changed.SQLite.Native.Path, false); err == nil {
		t.Fatal("normal startup accepted changed embedding identity")
	}
}

// TestEnsureNativeEmbeddingIdentityRejectsMalformedOrphanSidecars verifies structural corruption and an orphan sidecar both fail closed.
// TestEnsureNativeEmbeddingIdentityRejectsMalformedOrphanSidecars 用于验证结构损坏或孤立 sidecar 都会失败关闭。
func TestEnsureNativeEmbeddingIdentityRejectsMalformedOrphanSidecars(t *testing.T) {
	root := t.TempDir()
	cfg := nativeEmbeddingIdentityTestConfig(root)
	identityPath := nativeEmbeddingIdentityPath(cfg.SQLite.Native.Path)
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, []byte(`{"version":1,"provider":"openai","model":"model","dimension":3,"params_sha256":"bad","model_params_sha256":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeEmbeddingIdentity(cfg, cfg.SQLite.Native.Path, true); err == nil {
		t.Fatal("accepted malformed identity in maintenance mode")
	}

	orphanDB := filepath.Join(root, "orphan.db")
	orphanIdentity := nativeEmbeddingIdentityPath(orphanDB)
	identity, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNativeEmbeddingIdentity(orphanIdentity, identity); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeEmbeddingIdentity(cfg, orphanDB, false); err == nil {
		t.Fatal("accepted orphan identity without database")
	}
}

// TestUpdateNativeEmbeddingIdentityPublishesOnlyAfterDatabaseExists verifies the configuration-based update resolves the native path and replaces the sidecar after a successful rebuild.
// TestUpdateNativeEmbeddingIdentityPublishesOnlyAfterDatabaseExists 用于验证配置式更新会解析原生路径，并且只在数据库存在时替换 sidecar。
func TestUpdateNativeEmbeddingIdentityPublishesOnlyAfterDatabaseExists(t *testing.T) {
	root := t.TempDir()
	cfg := nativeEmbeddingIdentityTestConfig(root)
	if err := os.MkdirAll(filepath.Dir(cfg.SQLite.Native.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.SQLite.Native.Path, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfg
	old.Embedding.Model = "old-model"
	oldIdentity, err := nativeEmbeddingIdentityForConfig(old)
	if err != nil {
		t.Fatal(err)
	}
	identityPath := nativeEmbeddingIdentityPath(cfg.SQLite.Native.Path)
	if err := writeNativeEmbeddingIdentity(identityPath, oldIdentity); err != nil {
		t.Fatal(err)
	}
	if err := updateNativeEmbeddingIdentity(cfg); err != nil {
		t.Fatalf("update native embedding identity: %v", err)
	}
	got, err := readNativeEmbeddingIdentity(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("updated identity = %+v, want %+v", got, want)
	}

	missing := cfg
	missing.SQLite.Native.Path = filepath.Join(root, "missing.db")
	if err := updateNativeEmbeddingIdentity(missing); err == nil {
		t.Fatal("updated identity without SQLite database")
	}
}

// TestNativeEmbeddingParamsDigestUsesCurrentModelOnly verifies parameter summaries are stable across map order and ignore unrelated model entries.
// TestNativeEmbeddingParamsDigestUsesCurrentModelOnly 用于验证参数摘要不受 map 顺序影响，并忽略无关模型条目。
func TestNativeEmbeddingParamsDigestUsesCurrentModelOnly(t *testing.T) {
	cfg := nativeEmbeddingIdentityTestConfig(t.TempDir())
	first, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Embedding.Params = map[string]any{"nested": map[string]any{"alpha": 1, "beta": 2}, "encoding_format": "float"}
	cfg.Embedding.ModelParams = map[string]map[string]any{
		"other-model":         {"dimensions": 100},
		"text-embedding-test": {"dimensions": 3},
	}
	second, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.ParamsSHA256 != second.ParamsSHA256 || first.ModelParamsSHA256 != second.ModelParamsSHA256 {
		t.Fatalf("parameter digests changed with map order or unrelated model: first=%+v second=%+v", first, second)
	}
	cfg.Embedding.ModelParams["text-embedding-test"]["dimensions"] = 4
	third, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.ModelParamsSHA256 == third.ModelParamsSHA256 {
		t.Fatal("current model parameter change did not change identity digest")
	}
}

// TestNativeEmbeddingIdentityIncludesEndpointAndNodeTopology verifies endpoint normalization and non-sensitive routing topology tracking while ignoring keys and mutable budgets.
// TestNativeEmbeddingIdentityIncludesEndpointAndNodeTopology 验证 endpoint 归一化与非敏感路由拓扑会被跟踪，同时忽略 API Key 与可变预算。
func TestNativeEmbeddingIdentityIncludesEndpointAndNodeTopology(t *testing.T) {
	cfg := nativeEmbeddingIdentityTestConfig(t.TempDir())
	cfg.Embedding.Endpoint = " HTTPS://Embed.EXAMPLE/v1/?b=2&a=1#ignored "
	cfg.Embedding.APIKeys = nil
	cfg.Embedding.Nodes = []config.AIRoutingNodeConfig{{
		Name:    " primary ",
		APIKeys: []string{"first-key"},
		RPM:     10,
		TPM:     20,
		RPD:     30,
	}}
	first, err := nativeEmbeddingIdentityForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	stable := cfg
	stable.Embedding.Endpoint = "https://embed.example/v1?a=1&b=2"
	stable.Embedding.Nodes = []config.AIRoutingNodeConfig{{
		Name:    "primary",
		APIKeys: []string{"rotated-key"},
		RPM:     999,
		TPM:     888,
		RPD:     777,
	}}
	second, err := nativeEmbeddingIdentityForConfig(stable)
	if err != nil {
		t.Fatal(err)
	}
	if first.EndpointSHA256 != second.EndpointSHA256 {
		t.Fatalf("equivalent endpoint forms changed digest: first=%q second=%q", first.EndpointSHA256, second.EndpointSHA256)
	}
	if first.NodesSHA256 != second.NodesSHA256 {
		t.Fatalf("key or routing-budget rotation changed node digest: first=%q second=%q", first.NodesSHA256, second.NodesSHA256)
	}

	changedEndpoint := stable
	changedEndpoint.Embedding.Endpoint = "https://other.example/v1"
	third, err := nativeEmbeddingIdentityForConfig(changedEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if second.EndpointSHA256 == third.EndpointSHA256 {
		t.Fatal("endpoint route change did not change identity digest")
	}

	changedNode := stable
	changedNode.Embedding.Nodes = []config.AIRoutingNodeConfig{{
		Name:    "backup",
		APIKeys: []string{"rotated-key"},
		RPM:     999,
		TPM:     888,
		RPD:     777,
	}}
	fourth, err := nativeEmbeddingIdentityForConfig(changedNode)
	if err != nil {
		t.Fatal(err)
	}
	if second.NodesSHA256 == fourth.NodesSHA256 {
		t.Fatal("routing-node topology change did not change identity digest")
	}
}

// TestNativeEmbeddingEndpointIdentityPreservesEscapedPathSemantics verifies encoded and literal slashes remain distinct routes.
// TestNativeEmbeddingEndpointIdentityPreservesEscapedPathSemantics 验证编码斜杠与字面斜杠保持不同路由身份。
func TestNativeEmbeddingEndpointIdentityPreservesEscapedPathSemantics(t *testing.T) {
	encoded, err := nativeEmbeddingEndpointSHA256("https://embed.example/a%2Fb/")
	if err != nil {
		t.Fatalf("normalize encoded endpoint: %v", err)
	}
	literal, err := nativeEmbeddingEndpointSHA256("https://embed.example/a/b/")
	if err != nil {
		t.Fatalf("normalize literal endpoint: %v", err)
	}
	if encoded == literal {
		t.Fatal("encoded slash and literal slash unexpectedly share endpoint identity")
	}
}

// TestNativeEmbeddingEndpointIdentityRejectsInvalidQuery verifies malformed query syntax cannot silently change provider identity.
// TestNativeEmbeddingEndpointIdentityRejectsInvalidQuery 验证 malformed query 不能被静默丢弃并改变 provider 身份。
func TestNativeEmbeddingEndpointIdentityRejectsInvalidQuery(t *testing.T) {
	if _, err := nativeEmbeddingEndpointSHA256("https://embed.example/v1?a=1;b=2"); err == nil {
		t.Fatal("expected invalid semicolon query to be rejected")
	}
}
