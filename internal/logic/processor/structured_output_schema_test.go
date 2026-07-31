// structured_output_schema_test.go verifies generated schemas remain aligned with the exact response decoding structs.
// structured_output_schema_test.go 用于验证生成的 Schema 始终与实际响应解码结构体保持一致。
package processor

import (
	"encoding/json"
	"testing"
)

// TestStructuredOutputForTurnAnalysisUsesDecoderShape verifies nested arrays, required fields, and closed objects come from the decoder type.
// TestStructuredOutputForTurnAnalysisUsesDecoderShape 用于验证嵌套数组、必填字段和封闭对象均来自解码类型。
func TestStructuredOutputForTurnAnalysisUsesDecoderShape(t *testing.T) {
	output, err := structuredOutputFor[turnAnalysisResponsePayload]("turn_analysis", "test")
	if err != nil {
		t.Fatalf("structuredOutputFor() error = %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(output.Schema, &schema); err != nil {
		t.Fatalf("decode generated schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema["properties"])
	}
	for _, field := range []string{"user_input_kind", "turn_id", "details", "memory_nodes", "profile_nodes"} {
		if _, exists := properties[field]; !exists {
			t.Fatalf("generated schema is missing %q", field)
		}
	}
	if schema["additionalProperties"] != false || output.Name != "turn_analysis" || !output.Strict {
		t.Fatalf("structured output = %#v, schema = %#v", output, schema)
	}
	turnID := properties["turn_id"].(map[string]any)
	if _, exists := turnID["minimum"]; exists {
		t.Fatalf("provider-compatible schema must not include unsupported numeric ranges: %#v", turnID)
	}
	memoryNodes := properties["memory_nodes"].(map[string]any)
	memoryItem := memoryNodes["items"].(map[string]any)
	memoryRequired := memoryItem["required"].([]any)
	for _, requiredField := range memoryRequired {
		if requiredField == "context_edges" || requiredField == "details" {
			t.Fatalf("parser-optional memory field became schema-required: %#v", memoryRequired)
		}
	}
}

// TestStructuredOutputForPostActionPreservesNullableConditionalSections verifies conditional reviewer blocks remain explicit nullable fields.
// TestStructuredOutputForPostActionPreservesNullableConditionalSections 用于验证条件式评审区段仍是显式的可空字段。
func TestStructuredOutputForPostActionPreservesNullableConditionalSections(t *testing.T) {
	output, err := structuredOutputFor[postActionCandidateReviewResponsePayload]("postaction_review", "test")
	if err != nil {
		t.Fatalf("structuredOutputFor() error = %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(output.Schema, &schema); err != nil {
		t.Fatalf("decode generated schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	memory := properties["memory"].(map[string]any)
	if _, ok := memory["anyOf"].([]any); !ok {
		t.Fatalf("memory schema = %#v", memory)
	}
	required := schema["required"].([]any)
	if len(required) != 0 {
		t.Fatalf("conditional post-action sections must remain optional, got %#v", required)
	}
}
