// config_provider_test.go proves network diagnostics cannot run through ordinary configuration checks or absent consent.
// config_provider_test.go 验证普通配置检查或缺少确认时不能执行网络诊断。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestProviderCommandRequiresNetworkConsent checks parser and execution entrypoint guards before any layout or client is opened.
// TestProviderCommandRequiresNetworkConsent 检查解析与执行入口在打开布局或客户端之前的确认门禁。
func TestProviderCommandRequiresNetworkConsent(t *testing.T) {
	for _, args := range [][]string{
		{"config", "test-provider", "--purpose", "llm"},
		{"config", "test-provider", "--purpose", "embedding", "--route", "1", "--allow-network"},
		{"config", "validate", "--allow-network"},
		{"config", "schema", "--route", "0"},
	} {
		if _, _, err := parseConfigCommand(args); err == nil {
			t.Fatalf("invalid consent or action accepted: %v", args)
		}
	}
	command, handled, err := parseConfigCommand([]string{"config", "test-provider", "--purpose", "llm", "--route", "1", "--allow-network", "--json"})
	if err != nil || !handled || !command.allowNetwork || command.route != 1 {
		t.Fatalf("explicit provider test rejected: %v", err)
	}
	var output, diagnostics bytes.Buffer
	if code := runProviderConfig(configCommand{purpose: "llm"}, "", "", &output, &diagnostics); code == 0 || output.Len() != 0 {
		t.Fatal("unconfirmed provider test reached configuration or network work")
	}
}

// TestProviderCommandUsesLoadedCandidate exercises the complete parser, real loader and selected client against a loopback endpoint.
// TestProviderCommandUsesLoadedCandidate 使用回环端点验证完整解析、真实加载器和所选客户端链路。
func TestProviderCommandUsesLoadedCandidate(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer candidate-probe-key" {
			http.Error(w, "unexpected candidate", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"candidate-response"}}],"model":"candidate-model"}`)
	}))
	defer server.Close()
	root, binary := writeConfigCommandFixture(t)
	userFile := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, userFile, fmt.Sprintf("llm:\n  routes:\n    - provider: openai\n      endpoint: %q\n      model: candidate-model\n      api_keys: [candidate-probe-key]\n", server.URL+"/v1"))
	command, _, err := parseConfigCommand([]string{"config", "test-provider", "--config", userFile, "--purpose", "llm", "--allow-network", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if code := runConfigCommand(command, binary, root, &output, &diagnostics); code != 0 || requests.Load() != 1 {
		t.Fatalf("candidate diagnostic failed: code=%d requests=%d output=%s diagnostics=%s", code, requests.Load(), output.String(), diagnostics.String())
	}
	var result struct {
		Success bool   `json:"success"`
		Class   string `json:"class"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.Success || result.Class != "ok" || strings.Contains(output.String(), "candidate-probe-key") || strings.Contains(output.String(), "candidate-response") {
		t.Fatal("provider protocol is inconsistent or contains private data")
	}
}
