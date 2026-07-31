// managed_test.go verifies the strict Vulcan Code managed-runtime manifest boundary.
// managed_test.go 用于验证严格的 Vulcan Code 托管运行时清单边界。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestLoadVulcanManagedConfigAcceptsOneStrictDocument verifies a complete manifest derives the existing runtime without consulting layered config.
// TestLoadVulcanManagedConfigAcceptsOneStrictDocument 用于验证完整清单可直接派生现有运行时且不读取分层配置。
func TestLoadVulcanManagedConfigAcceptsOneStrictDocument(t *testing.T) {
	manifest := validManagedConfig(t)
	path := writeManagedFixture(t, manifest)

	bundle, err := LoadVulcanManagedConfig(path)
	if err != nil {
		t.Fatalf("LoadVulcanManagedConfig() error = %v", err)
	}
	if bundle.Manifest.Digest == "" {
		t.Fatal("managed manifest digest is empty")
	}
	if bundle.Config.Storage.Mode != "controller" || bundle.Config.Controller.AutoSpawn {
		t.Fatalf("derived storage boundary = %#v", bundle.Config.Controller)
	}
	if bundle.Config.Embedding.Dimension != manifest.Runtime.Inference.EmbeddingDimension {
		t.Fatalf(
			"embedding dimension = %d, want %d",
			bundle.Config.Embedding.Dimension,
			manifest.Runtime.Inference.EmbeddingDimension,
		)
	}
}

// TestLoadVulcanManagedConfigRejectsUnknownFields verifies additive fields require an explicit contract-version change.
// TestLoadVulcanManagedConfigRejectsUnknownFields 用于验证新增字段必须通过显式契约版本变更引入。
func TestLoadVulcanManagedConfigRejectsUnknownFields(t *testing.T) {
	manifest := validManagedConfig(t)
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(
		string(body),
		`"owner":"vulcan-code"`,
		`"owner":"vulcan-code","unexpected":true`,
		1,
	))
	path := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadVulcanManagedConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("LoadVulcanManagedConfig() error = %v, want unknown field", err)
	}
}

// TestManagedContractFixtureRoundTripsSemantically verifies the shared version-one fixture loses no fields in Go.
// TestManagedContractFixtureRoundTripsSemantically 用于验证共享的一版固定夹具经过 Go 往返后不会丢失字段。
func TestManagedContractFixtureRoundTripsSemantically(t *testing.T) {
	path := filepath.Join("testdata", "vulcan-managed-contract-v1.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read managed contract fixture: %v", err)
	}
	var manifest ManagedConfig
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode managed contract fixture: %v", err)
	}
	roundTrip, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode managed contract fixture: %v", err)
	}
	var expected any
	if err := json.Unmarshal(body, &expected); err != nil {
		t.Fatalf("decode expected fixture semantics: %v", err)
	}
	var actual any
	if err := json.Unmarshal(roundTrip, &actual); err != nil {
		t.Fatalf("decode round-trip fixture semantics: %v", err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("managed contract semantic round-trip changed fields:\nwant: %#v\ngot:  %#v", expected, actual)
	}
}

// TestManagedConfigRejectsNonLoopbackEndpoints verifies both gRPC and shared Controller endpoints remain local-only.
// TestManagedConfigRejectsNonLoopbackEndpoints 用于验证 gRPC 与共享 Controller 端点都保持仅回环可用。
func TestManagedConfigRejectsNonLoopbackEndpoints(t *testing.T) {
	manifest := validManagedConfig(t)
	manifest.Runtime.Storage.ControllerEndpoint = "http://192.0.2.10:17625"
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Validate() error = %v, want loopback rejection", err)
	}
}

// TestManagedConfigRejectsIdentityVersionAndNestedRuntimeViolations verifies strict validation extends beyond JSON shape.
// TestManagedConfigRejectsIdentityVersionAndNestedRuntimeViolations 用于验证严格校验不止覆盖 JSON 结构。
func TestManagedConfigRejectsIdentityVersionAndNestedRuntimeViolations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ManagedConfig)
		wantErr string
	}{
		{
			name: "contract version",
			mutate: func(manifest *ManagedConfig) {
				manifest.ContractVersion++
			},
			wantErr: "unsupported managed contract version",
		},
		{
			name: "parent identity",
			mutate: func(manifest *ManagedConfig) {
				manifest.Parent.StartedAtUnixMS = 0
			},
			wantErr: "parent identity is incomplete",
		},
		{
			name: "inference startup timeout",
			mutate: func(manifest *ManagedConfig) {
				manifest.Runtime.Inference.StartupTimeout = Duration{}
			},
			wantErr: "runtime.inference.startup_timeout must be positive",
		},
		{
			name: "managed pipeline",
			mutate: func(manifest *ManagedConfig) {
				manifest.Runtime.MemoryPipeline.MaxSearchKeywords = 0
			},
			wantErr: "memory_pipeline.max_search_keywords",
		},
		{
			name: "controller process mode",
			mutate: func(manifest *ManagedConfig) {
				manifest.Runtime.Storage.ControllerLease.ProcessMode = "unknown"
			},
			wantErr: "controller.process_mode",
		},
		{
			name: "managed retention",
			mutate: func(manifest *ManagedConfig) {
				manifest.Runtime.Retention.SessionIdleRecycleAfter = Duration{Duration: time.Hour}
			},
			wantErr: "managed retention limits are invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManagedConfig(t)
			test.mutate(&manifest)
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

// TestValidateManagedConfigPathRejectsParentSymlink verifies path ancestry cannot redirect the managed manifest.
// TestValidateManagedConfigPathRejectsParentSymlink 用于验证路径祖先不能通过符号链接重定向托管清单。
func TestValidateManagedConfigPathRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "managed.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable on this host: %v", err)
	}

	_, err := validateManagedConfigPath(filepath.Join(link, "managed.json"))
	if err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Fatalf("validateManagedConfigPath() error = %v, want symbolic-link rejection", err)
	}
}

// validManagedConfig builds one complete manifest from the audited standalone defaults.
// validManagedConfig 基于已审计的独立运行默认值构建一份完整清单。
func validManagedConfig(t *testing.T) ManagedConfig {
	t.Helper()
	root := t.TempDir()
	base := DefaultLocal()
	base.Controller.Endpoint = "127.0.0.1:17625"
	base.Controller.AutoSpawn = false
	base.Controller.SpaceID = "vmm-memory"
	base.Controller.SpaceLabel = "VulcanMemoryMesh"
	return ManagedConfig{
		ContractVersion: ManagedContractVersion,
		Owner:           ManagedContractOwner,
		InstanceID:      "managed-test",
		Generation:      1,
		Parent: ManagedParent{
			ProcessID:       os.Getpid(),
			StartedAtUnixMS: time.Now().UnixMilli(),
		},
		Assets: ManagedAssets{SystemRoot: root},
		Runtime: ManagedRuntime{
			DataRoot:     filepath.Join(root, "data"),
			StatusFile:   filepath.Join(root, "runtime", "status.json"),
			ShutdownFile: filepath.Join(root, "runtime", "shutdown"),
			GRPC: ManagedGRPCConfig{
				PreferredListenAddr:    "127.0.0.1:0",
				AllowEphemeralFallback: true,
				AccessToken:            "managed-test-token",
				MaxReceiveMessageBytes: 1 << 20,
				RequestTimeout:         base.GRPC.RequestTimeout,
				ShutdownTimeout:        base.GRPC.ShutdownTimeout,
				Keepalive:              base.GRPC.Keepalive,
			},
			Storage: ManagedStorageConfig{
				Mode:                 "controller",
				ControllerEndpoint:   "http://127.0.0.1:17625",
				ControllerAutoSpawn:  false,
				ControllerSpaceID:    "vmm-memory",
				ControllerSpaceLabel: "VulcanMemoryMesh",
				ControllerLease:      base.Controller,
			},
			Inference: ManagedInferenceConfig{
				Mode:               ManagedInferenceMode,
				DiscoveryFile:      filepath.Join(root, "inference-service.json"),
				ExpectedCallerID:   "vmm-local",
				ConsumerProfileID:  "vmm",
				StartupTimeout:     Duration{Duration: 30 * time.Second},
				EmbeddingDimension: 1024,
			},
			Logging:    base.Logging,
			PII:        base.PII,
			Noise:      base.Noise,
			Prompts:    base.Prompts,
			Vector:     base.Vector,
			Relational: base.Relational,
			PreCheck: ManagedPreCheckConfig{
				IntentTimeout:       base.PreCheck.IntentTimeout,
				TopK:                base.PreCheck.TopK,
				SearchScope:         base.PreCheck.SearchScope,
				SimilarityThreshold: base.PreCheck.SimilarityThreshold,
			},
			PostAction:     base.PostAction,
			MemoryPipeline: base.MemoryPipeline,
			Retention:      base.Retention,
		},
	}
}

// writeManagedFixture serializes one manifest into an isolated regular file.
// writeManagedFixture 用于把一份清单序列化到隔离的普通文件。
func writeManagedFixture(t *testing.T, manifest ManagedConfig) string {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
