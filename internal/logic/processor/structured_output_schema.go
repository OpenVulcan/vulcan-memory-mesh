// structured_output_schema.go owns the response payload types and reflection-driven JSON Schema generation used by VMM LLM processors.
// structured_output_schema.go 负责 VMM LLM 处理器使用的响应载荷类型，以及由反射驱动的 JSON Schema 生成逻辑。
package processor

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// intentResponsePayload is the exact decoded response contract for pre-check intent extraction.
// intentResponsePayload 是 pre-check 意图提炼实际解码使用的精确响应契约。
type intentResponsePayload struct {
	Reason     *string   `json:"reason"`
	NeedMemory *bool     `json:"need_memory"`
	Queries    *[]string `json:"queries"`
}

// preCheckMemoryReviewResponsePayload is the exact decoded response contract for pre-check candidate review.
// preCheckMemoryReviewResponsePayload 是 pre-check 候选评审实际解码使用的精确响应契约。
type preCheckMemoryReviewResponsePayload struct {
	SelectedCandidateNumbers *[]int `json:"selected_candidate_numbers"`
}

// turnAnalysisContextEdgePayload is one context edge decoded from a turn-analysis response.
// turnAnalysisContextEdgePayload 是从逐轮分析响应中解码的一条上下文边。
type turnAnalysisContextEdgePayload struct {
	ContextKey   string `json:"context_key"`
	ContextValue string `json:"context_value"`
	Relation     string `json:"relation"`
}

// turnAnalysisMemoryNodePayload is one memory candidate decoded from a turn-analysis response.
// turnAnalysisMemoryNodePayload 是从逐轮分析响应中解码的一条记忆候选。
type turnAnalysisMemoryNodePayload struct {
	Category        int                              `json:"category"`
	Abstract        string                           `json:"abstract"`
	Details         string                           `json:"details,omitempty"`
	EvidenceSource  string                           `json:"evidence_source"`
	Admission       string                           `json:"admission"`
	AdmissionReason *string                          `json:"admission_reason"`
	ContextEdges    []turnAnalysisContextEdgePayload `json:"context_edges,omitempty"`
}

// turnAnalysisProfileNodePayload is one profile candidate decoded from a turn-analysis response.
// turnAnalysisProfileNodePayload 是从逐轮分析响应中解码的一条画像候选。
type turnAnalysisProfileNodePayload struct {
	ProfileType     int     `json:"profile_type"`
	Content         string  `json:"content"`
	EvidenceSource  string  `json:"evidence_source"`
	Admission       string  `json:"admission"`
	AdmissionReason *string `json:"admission_reason"`
}

// turnAnalysisResponsePayload is the exact decoded response contract for post-action turn analysis.
// turnAnalysisResponsePayload 是 post-action 逐轮分析实际解码使用的精确响应契约。
type turnAnalysisResponsePayload struct {
	UserInputKind string                           `json:"user_input_kind"`
	TurnID        uint64                           `json:"turn_id"`
	Details       string                           `json:"details,omitempty"`
	MemoryNodes   []turnAnalysisMemoryNodePayload  `json:"memory_nodes,omitempty"`
	ProfileNodes  []turnAnalysisProfileNodePayload `json:"profile_nodes,omitempty"`
}

// profileRetirePayload is one profile-node retirement decision decoded from a reviewer response.
// profileRetirePayload 是从评审响应中解码的一条画像节点退役决策。
type profileRetirePayload struct {
	NodeID uint64 `json:"node_id"`
	Reason string `json:"reason,omitempty"`
}

// manualProfileAcceptedPayload is one accepted profile instruction decoded from a reviewer response.
// manualProfileAcceptedPayload 是从评审响应中解码的一条已接纳画像指令。
type manualProfileAcceptedPayload struct {
	NormalizedContent string                 `json:"normalized_content"`
	Priority          string                 `json:"priority"`
	Level             string                 `json:"level"`
	LevelReason       string                 `json:"level_reason,omitempty"`
	SupersedeNodes    []profileRetirePayload `json:"supersede_nodes,omitempty"`
}

// manualProfileReviewResponsePayload is the exact decoded response contract for manual profile review.
// manualProfileReviewResponsePayload 是手工画像评审实际解码使用的精确响应契约。
type manualProfileReviewResponsePayload struct {
	AcceptedNodes []manualProfileAcceptedPayload `json:"accepted_nodes,omitempty"`
	RetiredNodes  []profileRetirePayload         `json:"retired_nodes,omitempty"`
	Reason        string                         `json:"reason,omitempty"`
}

// postActionMemoryAcceptedPayload is one accepted memory candidate decoded from post-action review.
// postActionMemoryAcceptedPayload 是从 post-action 评审中解码的一条已接纳记忆候选。
type postActionMemoryAcceptedPayload struct {
	CandidateIndex     int      `json:"candidate_index"`
	SupersedeMemoryIDs []uint64 `json:"supersede_memory_ids,omitempty"`
}

// postActionMemoryDroppedPayload is one dropped memory candidate decoded from post-action review.
// postActionMemoryDroppedPayload 是从 post-action 评审中解码的一条已丢弃记忆候选。
type postActionMemoryDroppedPayload struct {
	CandidateIndex int    `json:"candidate_index"`
	DedupeMemoryID uint64 `json:"dedupe_memory_id"`
}

// postActionMemoryReviewPayload is the exact memory section decoded from post-action review.
// postActionMemoryReviewPayload 是 post-action 评审实际解码使用的精确记忆区段。
type postActionMemoryReviewPayload struct {
	AcceptedCandidates []postActionMemoryAcceptedPayload `json:"accepted_candidates,omitempty"`
	DroppedCandidates  []postActionMemoryDroppedPayload  `json:"dropped_candidates,omitempty"`
	Reason             string                            `json:"reason,omitempty"`
}

// postActionCandidateReviewResponsePayload is the exact decoded response contract for post-action candidate review.
// postActionCandidateReviewResponsePayload 是 post-action 候选评审实际解码使用的精确响应契约。
type postActionCandidateReviewResponsePayload struct {
	Memory  *postActionMemoryReviewPayload `json:"memory,omitempty" vmm_schema:"nullable"`
	User    *profileReviewSectionPayload   `json:"user,omitempty" vmm_schema:"nullable"`
	Project *profileReviewSectionPayload   `json:"project,omitempty" vmm_schema:"nullable"`
}

// structuredOutputFor builds one provider-neutral structured-output contract from the exact Go type used for decoding.
// structuredOutputFor 根据实际用于解码的 Go 类型构建一份供应商无关的结构化输出契约。
func structuredOutputFor[T any](name string, description string) (*logicports.LLMStructuredOutput, error) {
	target := reflect.TypeFor[T]()
	schema, err := reflectStructuredOutputSchema(target, map[reflect.Type]bool{})
	if err != nil {
		return nil, fmt.Errorf("build structured output schema %s: %w", name, err)
	}
	root, ok := schema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("structured output schema %s root must be an object", name)
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode structured output schema %s: %w", name, err)
	}
	return &logicports.LLMStructuredOutput{
		Name:        name,
		Description: description,
		Schema:      encoded,
		Strict:      true,
	}, nil
}

// reflectStructuredOutputSchema projects the supported Go decoding shape into a closed Draft 2020-12 JSON Schema.
// reflectStructuredOutputSchema 将受支持的 Go 解码结构投影成封闭的 Draft 2020-12 JSON Schema。
func reflectStructuredOutputSchema(target reflect.Type, visiting map[reflect.Type]bool) (any, error) {
	if target == nil {
		return nil, fmt.Errorf("schema target is nil")
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		// Provider structured-output subsets reject numeric range keywords; Go decoding and domain validation still reject negative identifiers.
		// 供应商结构化输出子集会拒绝数值范围关键字；负数标识仍由 Go 解码和领域校验拒绝。
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		items, err := reflectStructuredOutputSchema(target.Elem(), visiting)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Struct:
		if visiting[target] {
			return nil, fmt.Errorf("recursive response type %s is unsupported", target.String())
		}
		visiting[target] = true
		defer delete(visiting, target)
		properties := map[string]any{}
		required := make([]string, 0, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			if !field.IsExported() {
				continue
			}
			jsonName, omitted, optional := structuredOutputJSONFieldName(field)
			if omitted {
				continue
			}
			fieldSchema, err := reflectStructuredOutputSchema(field.Type, visiting)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.Name, err)
			}
			if field.Type.Kind() == reflect.Pointer && field.Tag.Get("vmm_schema") == "nullable" {
				fieldSchema = map[string]any{"anyOf": []any{fieldSchema, map[string]any{"type": "null"}}}
			}
			properties[jsonName] = fieldSchema
			if !optional {
				required = append(required, jsonName)
			}
		}
		return map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported response type %s", target.String())
	}
}

// structuredOutputJSONFieldName resolves the exact JSON field name and optionality used by Go decoding.
// structuredOutputJSONFieldName 用于解析 Go 解码实际使用的精确 JSON 字段名与可选性。
func structuredOutputJSONFieldName(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "-" {
		return "", true, false
	}
	if name == "" {
		name = field.Name
	}
	optional := false
	for _, option := range parts[1:] {
		if option == "omitempty" || option == "omitzero" {
			optional = true
			break
		}
	}
	return name, false, optional
}
