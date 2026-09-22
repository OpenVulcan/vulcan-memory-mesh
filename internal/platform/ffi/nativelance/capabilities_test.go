// capabilities_test.go rejects libraries that export ABI symbols but change storage semantics.
// capabilities_test.go 验证即使库导出 ABI 符号，也不能改变存储语义。
package nativelance

import (
	"encoding/json"
	"testing"
)

// TestNativeCapabilitiesRejectIncompatibleContracts checks each startup guarantee independently.
// TestNativeCapabilitiesRejectIncompatibleContracts 逐项检查启动保证，输入为变异能力文档，预期均拒绝加载。
func TestNativeCapabilitiesRejectIncompatibleContracts(t *testing.T) {
	const compatible = `{"schema_version":1,"abi_version":1,"engine_version":"0.39.0","distance_type":"l2","prefilter":true,"atomic_merge_insert":true,"operations":["create_or_check","upsert","search","delete","count","schema","optimize","drop"],"remote_storage":false,"context_deadline":"per_call_timeout","context_cancellation":"before_call_only","mutation_timeout_result":"outcome_uncertain","library_lifetime":"process_resident"}`
	if err := validateNativeCapabilities("0.39.0", []byte(compatible)); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCapabilities("0.38.0", []byte(compatible)); err == nil {
		t.Fatal("accepted incompatible exported engine version")
	}
	if err := validateNativeCapabilities("0.39.0", []byte(`{"schema_version":`)); err == nil {
		t.Fatal("accepted malformed capability document")
	}
	changes := map[string]any{
		"schema_version": 2, "abi_version": 2, "engine_version": "0.38.0",
		"distance_type": "cosine", "prefilter": false, "atomic_merge_insert": false,
		"remote_storage": true, "context_deadline": "none", "context_cancellation": "none",
		"mutation_timeout_result": "failure", "library_lifetime": "unloadable",
		"operations": []string{"search"},
	}
	for key, changed := range changes {
		t.Run(key, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(compatible), &document); err != nil {
				t.Fatal(err)
			}
			document[key] = changed
			body, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateNativeCapabilities("0.39.0", body); err == nil {
				t.Fatal("accepted incompatible capability")
			}
		})
	}
}
