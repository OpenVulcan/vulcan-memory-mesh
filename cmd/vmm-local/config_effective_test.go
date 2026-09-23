// config_effective_test.go exercises the CLI export against real layered configuration fixtures.
// config_effective_test.go 使用真实分层配置夹具验证命令行导出。
package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestShowEffectiveUsesRuntimeLayers verifies base values, user overrides, environment overrides and normalized defaults together.
// TestShowEffectiveUsesRuntimeLayers 一并验证系统底座、用户覆盖、环境覆盖和归一化默认值。
func TestShowEffectiveUsesRuntimeLayers(t *testing.T) {
	root, binary := writeConfigCommandFixture(t)
	userFile := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, userFile, "logging:\n  level: debug\ngrpc:\n  listen_addr: ${VMM_GRPC_LISTEN_ADDR}\n")
	t.Setenv("VMM_GRPC_LISTEN_ADDR", "127.0.0.1:18002")
	command, handled, err := parseConfigCommand([]string{"config", "show-effective", "--config", userFile, "--json"})
	if err != nil || !handled {
		t.Fatalf("effective command rejected: %v", err)
	}
	var output, diagnostics bytes.Buffer
	if code := runConfigCommand(command, binary, root, &output, &diagnostics); code != 0 {
		t.Fatalf("effective export failed: %s", diagnostics.String())
	}
	var document struct {
		Version  string `json:"version"`
		Redacted bool   `json:"redacted"`
		Config   struct {
			GRPC struct {
				ListenAddr string `json:"listen_addr"`
			} `json:"grpc"`
			Logging struct {
				Level  string `json:"level"`
				Format string `json:"format"`
			} `json:"logging"`
			Embedding struct {
				Model string `json:"model"`
			} `json:"embedding"`
		} `json:"config"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != "v1" || !document.Redacted || document.Config.GRPC.ListenAddr != "127.0.0.1:18002" || document.Config.Logging.Level != "debug" || document.Config.Logging.Format == "" || document.Config.Embedding.Model != "test-embedding" {
		t.Fatalf("export did not reflect runtime layering: %+v", document)
	}
	if strings.Contains(output.String(), "llm-key") || strings.Contains(output.String(), "embedding-key") {
		t.Fatal("runtime secrets leaked into effective configuration")
	}
}

// TestShowEffectiveRejectsInvalidConfig ensures rejected input never leaks into either output stream.
// TestShowEffectiveRejectsInvalidConfig 确保被拒绝的输入不会泄漏到任何输出流。
func TestShowEffectiveRejectsInvalidConfig(t *testing.T) {
	root, binary := writeConfigCommandFixture(t)
	userFile := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, userFile, "unknown_field: 'private-test-secret'\n")
	var output, diagnostics bytes.Buffer
	code := runConfigCommand(configCommand{action: "show-effective", configPath: userFile, jsonOutput: true}, binary, root, &output, &diagnostics)
	if code == 0 || output.Len() != 0 || strings.Contains(diagnostics.String(), "private-test-secret") {
		t.Fatal("invalid effective config was accepted or leaked its input")
	}
}
