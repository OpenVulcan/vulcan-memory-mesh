// capabilities.go validates the fixed engine semantics before the application opens any native database.
// capabilities.go 在应用打开原生数据库前校验固定的引擎语义，属于动态库边界层。
package nativelance

import (
	"encoding/json"
	"fmt"
)

// expectedEngineVersion pins the official engine paired with this Go ABI implementation.
// expectedEngineVersion 固定与此 Go ABI 实现配对的官方引擎版本。
const expectedEngineVersion = "0.39.0"

// nativeCapabilities mirrors the operational guarantees published by the checked-in Rust C ABI.
// nativeCapabilities 对应仓库内 Rust C ABI 发布的操作保证，用于加载时拒绝不兼容实现。
type nativeCapabilities struct {
	SchemaVersion         int      `json:"schema_version"`
	ABIVersion            int      `json:"abi_version"`
	EngineVersion         string   `json:"engine_version"`
	DistanceType          string   `json:"distance_type"`
	Prefilter             bool     `json:"prefilter"`
	AtomicMergeInsert     bool     `json:"atomic_merge_insert"`
	Operations            []string `json:"operations"`
	RemoteStorage         bool     `json:"remote_storage"`
	ContextDeadline       string   `json:"context_deadline"`
	ContextCancellation   string   `json:"context_cancellation"`
	MutationTimeoutResult string   `json:"mutation_timeout_result"`
	LibraryLifetime       string   `json:"library_lifetime"`
}

// validateNativeCapabilities checks the exported version and capability JSON and returns an explicit incompatibility error.
// validateNativeCapabilities 校验导出的版本和能力 JSON，返回明确的不兼容错误。
func validateNativeCapabilities(engineVersion string, body []byte) error {
	if engineVersion != expectedEngineVersion {
		return fmt.Errorf("native LanceDB engine version %q differs from required %q", engineVersion, expectedEngineVersion)
	}
	var capabilities nativeCapabilities
	if err := json.Unmarshal(body, &capabilities); err != nil {
		return fmt.Errorf("decode native LanceDB capabilities: %w", err)
	}
	// Matching symbols alone cannot prove distance, write atomicity, or timeout behavior.
	// 仅有同名导出符号不能证明距离、写入原子性或超时语义一致。
	if capabilities.SchemaVersion != 1 || capabilities.ABIVersion != 1 || capabilities.EngineVersion != expectedEngineVersion ||
		capabilities.DistanceType != "l2" || !capabilities.Prefilter || !capabilities.AtomicMergeInsert || capabilities.RemoteStorage ||
		capabilities.ContextDeadline != "per_call_timeout" || capabilities.ContextCancellation != "before_call_only" ||
		capabilities.MutationTimeoutResult != "outcome_uncertain" || capabilities.LibraryLifetime != "process_resident" {
		return fmt.Errorf("native LanceDB capabilities do not match the required local atomic L2 storage contract")
	}
	operations := make(map[string]bool, len(capabilities.Operations))
	for _, operation := range capabilities.Operations {
		operations[operation] = true
	}
	for _, operation := range []string{"create_or_check", "upsert", "search", "delete", "count", "schema", "optimize", "drop"} {
		if !operations[operation] {
			return fmt.Errorf("native LanceDB is missing required operation %q", operation)
		}
	}
	return nil
}
