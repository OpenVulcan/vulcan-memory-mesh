// native_maintenance_test.go checks that invalid native actions fail before configuration and database access.
// native_maintenance_test.go 验证无效原生维护动作在读取配置或访问数据库前失败。
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestNativeMaintenanceOptionValidation checks explicit destination, confirmation, and action exclusivity.
// TestNativeMaintenanceOptionValidation 检查明确目标、确认参数以及动作互斥关系。
func TestNativeMaintenanceOptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		migrate string
		vector  bool
		options nativeMaintenanceOptions
		want    string
	}{
		{"missing-confirmation", "split-to-native", false, nativeMaintenanceOptions{OutputDirectory: "new"}, "requires -native-output"},
		{"missing-output", "controller-to-native", false, nativeMaintenanceOptions{Confirmed: true}, "requires -native-output"},
		{"irrelevant-output", "", true, nativeMaintenanceOptions{OutputDirectory: "new"}, "require split-to-native"},
		{"fts-and-vector", "", true, nativeMaintenanceOptions{RebuildFTS: true}, "exactly one"},
		{"fts-and-migrate", "split-to-native", false, nativeMaintenanceOptions{OutputDirectory: "new", Confirmed: true, RebuildFTS: true}, "exactly one"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := runWithMaintenanceOptions(context.Background(), "test", "missing-config", "", test.migrate, test.vector, false, "", test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestNativeMaintenanceRejectsSourceModeMismatch prevents a selector from opening the wrong backend.
// TestNativeMaintenanceRejectsSourceModeMismatch 防止迁移选择器打开错误后端。
func TestNativeMaintenanceRejectsSourceModeMismatch(t *testing.T) {
	cfg := config.Config{}
	cfg.Storage.Mode = "native"
	if err := runNativeStorageMigration(context.Background(), cfg, config.PromptLayout{}, "split-to-native", t.TempDir()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unexpected mismatch error: %v", err)
	}
	cfg.Storage.Mode = "split"
	if err := runNativeFTSRebuild(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "requires storage.mode=native") {
		t.Fatalf("unexpected FTS mode error: %v", err)
	}
}
