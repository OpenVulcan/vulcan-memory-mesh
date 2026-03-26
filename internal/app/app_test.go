// app_test.go verifies runtime-only composition details such as gRPC reflection registration.
// app_test.go 用于验证仅运行时相关的装配细节，例如 gRPC 反射注册。
package app

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/config"
)

// TestNewLocalRegistersReflection verifies the local runtime exposes gRPC reflection for grpcurl-based debugging.
// TestNewLocalRegistersReflection 用于验证本地运行时会暴露 gRPC 反射，方便基于 grpcurl 的调试。
func TestNewLocalRegistersReflection(t *testing.T) {
	// Resolve the repository-backed prompt layout so the composition root can build against real config assets.
	// 解析仓库中的提示词布局，让组合根能够基于真实配置资源完成装配。
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"),
		filepath.Join(root, "cmd", "vmm-local"),
		"",
		"local",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir)
	if err != nil {
		t.Fatal(err)
	}

	// Build the local runtime and assert that reflection is registered alongside the main VMM service.
	// 构建本地运行时，并断言反射服务已和主 VMM 服务一起注册。
	cfg := config.DefaultLocal()
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.LanceDB.TableName = fmt.Sprintf("vmm_reflection_%d", time.Now().UnixNano())
	app, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	info := app.Server.GetServiceInfo()
	if _, ok := info["grpc.reflection.v1alpha.ServerReflection"]; !ok {
		t.Fatalf("expected gRPC reflection to be registered, got services: %#v", info)
	}
	if _, ok := info["vmm.v1.VMMService"]; !ok {
		t.Fatalf("expected VMM service to be registered, got services: %#v", info)
	}
}
