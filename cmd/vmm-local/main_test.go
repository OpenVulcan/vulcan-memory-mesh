// main_test.go verifies standalone entrypoint argument boundaries.
// main_test.go 用于验证独立入口的参数边界。
package main

import (
	"strings"
	"testing"
)

// TestParseRuntimeArgumentsRejectsRemovedManagedConfig verifies the retired hosted flag fails explicitly instead of falling through to a standalone launch.
// TestParseRuntimeArgumentsRejectsRemovedManagedConfig 用于验证已移除的托管参数会明确失败，不会误落入独立启动流程。
func TestParseRuntimeArgumentsRejectsRemovedManagedConfig(t *testing.T) {
	for _, args := range [][]string{
		{"-vulcan-managed-config", "managed.json"},
		{"--vulcan-managed-config=managed.json"},
	} {
		_, err := parseRuntimeArguments(args)
		if err == nil || !strings.Contains(err.Error(), "no longer supported") {
			t.Fatalf("parseRuntimeArguments(%q) error = %v, want explicit unsupported error", args, err)
		}
	}
}
