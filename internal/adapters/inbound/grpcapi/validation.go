// validation.go implements transport validation for the inbound gRPC adapter.
// validation.go 用于实现入站 gRPC 适配层的传输校验。
package grpcapi

import (
	"encoding/json"
	"fmt"
	"strings"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	postActionInputModeCompat = "compat"
	postActionInputModeStrict = "strict"
	defaultScopeValue         = "default"
)

// RequestValidator performs lightweight transport validation without introducing a heavy generic validator.
// RequestValidator 用于在不引入重型通用校验库的前提下执行轻量传输层校验。
type RequestValidator struct {
	postActionInputMode string
}

// NewRequestValidator creates the gRPC transport validator shared by unary handlers.
// NewRequestValidator 用于创建 gRPC 一元处理器共享的传输层校验器。
func NewRequestValidator(postActionInputMode string) *RequestValidator {
	mode := strings.ToLower(strings.TrimSpace(postActionInputMode))
	if mode != postActionInputModeStrict {
		mode = postActionInputModeCompat
	}
	return &RequestValidator{postActionInputMode: mode}
}

// normalizePreCheckRequest trims the simplified pre-check payload before it enters the use-case layer.
// normalizePreCheckRequest 用于在进入用例层前裁剪简化后的 pre-check 载荷。
func normalizePreCheckRequest(req *vmmv1.PreCheckRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.SessionId)
	req.UserId = strings.TrimSpace(req.UserId)
	req.TeamId = strings.TrimSpace(req.TeamId)
	req.SpaceId = strings.TrimSpace(req.SpaceId)
	req.ProjectId = strings.TrimSpace(req.ProjectId)
	req.UserContent = textutil.CleanConversationText(req.UserContent)
}

// normalizePostActionRequest trims scope identifiers and normalizes lightweight shape fields before storage-oriented cleaning happens later.
// normalizePostActionRequest 用于裁剪范围标识，并在后续存储型清洗执行前先规范化轻量结构字段。
func normalizePostActionRequest(req *vmmv1.PostActionRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.SessionId)
	req.UserId = defaultScope(strings.TrimSpace(req.UserId))
	req.TeamId = defaultScope(strings.TrimSpace(req.TeamId))
	req.SpaceId = defaultScope(strings.TrimSpace(req.SpaceId))
	req.ProjectId = defaultScope(strings.TrimSpace(req.ProjectId))
	req.UserContent = strings.TrimSpace(req.UserContent)
	req.AssistantContent = strings.TrimSpace(req.AssistantContent)
	for _, item := range req.Timeline {
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		item.Content = strings.TrimSpace(item.Content)
	}
}

// normalizePostActionOldRequest trims identifiers before legacy snapshot sanitization starts.
// normalizePostActionOldRequest 用于在旧版快照清洗开始前裁剪标识字段。
func normalizePostActionOldRequest(req *vmmv1.PostActionOldRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.SessionId)
	req.UserId = defaultScope(strings.TrimSpace(req.UserId))
	req.TeamId = defaultScope(strings.TrimSpace(req.TeamId))
	req.SpaceId = defaultScope(strings.TrimSpace(req.SpaceId))
	req.ProjectId = defaultScope(strings.TrimSpace(req.ProjectId))
	for _, item := range req.RawMessagesSnapshot {
		item.Role = strings.ToLower(strings.TrimSpace(item.Role))
	}
}

// normalizeSeedMemoryRequest trims the seed-memory payload before one embedding request is issued.
// normalizeSeedMemoryRequest 用于在发起 embedding 请求前裁剪 seed-memory 载荷。
func normalizeSeedMemoryRequest(req *vmmv1.SeedMemoryRequest) {
	if req == nil {
		return
	}
	req.UserId = strings.TrimSpace(req.UserId)
	req.ProjectId = strings.TrimSpace(req.ProjectId)
	req.MemoryText = strings.TrimSpace(req.MemoryText)
	req.SpaceId = strings.TrimSpace(req.SpaceId)
}

// normalizeChatRequest trims the lightweight chat payload before scrubbing and archive persistence.
// normalizeChatRequest 用于在脱敏和归档持久化前裁剪轻量聊天载荷。
func normalizeChatRequest(req *vmmv1.ChatRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.SessionId)
	req.Message = strings.TrimSpace(req.Message)
	req.AcceptLanguage = strings.TrimSpace(req.AcceptLanguage)
}

// ValidatePreCheck validates the pre-check RPC request before use-case mapping.
// ValidatePreCheck 用于在映射到用例前校验 pre-check RPC 请求。
func (v *RequestValidator) ValidatePreCheck(req *vmmv1.PreCheckRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "pre_check", Message: "is required"}
	}
	if err := requireString("session_id", req.SessionId, 128); err != nil {
		return err
	}
	if err := requireString("user_id", req.UserId, 128); err != nil {
		return err
	}
	if err := requireString("team_id", req.TeamId, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectId, 128); err != nil {
		return err
	}
	if err := requireString("user_content", req.UserContent, 16000); err != nil {
		return err
	}
	return nil
}

// ValidatePostAction validates the new text-only post-action RPC contract.
// ValidatePostAction 用于校验新的纯文本 post-action RPC 契约。
func (v *RequestValidator) ValidatePostAction(req *vmmv1.PostActionRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "post_action", Message: "is required"}
	}
	if err := requireString("session_id", req.SessionId, 128); err != nil {
		return err
	}
	if err := maxString("user_id", req.UserId, 128); err != nil {
		return err
	}
	if err := maxString("team_id", req.TeamId, 128); err != nil {
		return err
	}
	if err := maxString("space_id", req.SpaceId, 128); err != nil {
		return err
	}
	if err := maxString("project_id", req.ProjectId, 128); err != nil {
		return err
	}
	if err := requireString("user_content", req.UserContent, 16000); err != nil {
		return err
	}
	if err := requireString("assistant_content", req.AssistantContent, 16000); err != nil {
		return err
	}
	for idx, item := range req.Timeline {
		fieldPrefix := fmt.Sprintf("timeline[%d]", idx)
		if err := requireOneOf(fieldPrefix+".type", item.Type, "user", "assistant"); err != nil {
			return err
		}
		if err := requireString(fieldPrefix+".content", item.Content, 16000); err != nil {
			return err
		}
	}
	return nil
}

// PreparePostActionOld normalizes defaults and sanitizes the legacy snapshot according to the configured input mode.
// PreparePostActionOld 用于按配置的输入模式规范化默认值，并清洗旧版快照。
func (v *RequestValidator) PreparePostActionOld(req *vmmv1.PostActionOldRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "post_action_old", Message: "is required"}
	}
	normalizePostActionOldRequest(req)
	if req.RawMessagesSnapshot == nil {
		req.RawMessagesSnapshot = []*vmmv1.RawMessage{}
	}
	if err := v.ValidatePostActionOld(req); err != nil {
		return err
	}
	sanitized, err := v.sanitizePostActionSnapshot(req.RawMessagesSnapshot)
	if err != nil {
		return err
	}
	req.RawMessagesSnapshot = sanitized
	return nil
}

// ValidatePostActionOld validates the legacy raw snapshot request before sanitization.
// ValidatePostActionOld 用于在清洗前校验旧版原始快照请求。
func (v *RequestValidator) ValidatePostActionOld(req *vmmv1.PostActionOldRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "post_action_old", Message: "is required"}
	}
	if err := requireString("session_id", req.SessionId, 128); err != nil {
		return err
	}
	if err := requireString("user_id", req.UserId, 128); err != nil {
		return err
	}
	if err := requireString("team_id", req.TeamId, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectId, 128); err != nil {
		return err
	}
	if err := maxString("space_id", req.SpaceId, 128); err != nil {
		return err
	}
	return nil
}

// ValidateSeedMemory validates the seed-memory RPC request before one embedding call is issued.
// ValidateSeedMemory 用于在发起 embedding 调用前校验 seed-memory RPC 请求。
func (v *RequestValidator) ValidateSeedMemory(req *vmmv1.SeedMemoryRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "seed_memory", Message: "is required"}
	}
	if err := requireString("user_id", req.UserId, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectId, 128); err != nil {
		return err
	}
	if err := requireString("memory_text", req.MemoryText, 16000); err != nil {
		return err
	}
	return nil
}

// ValidateChat validates the lightweight chat RPC request before scrubbing and persistence.
// ValidateChat 用于在脱敏和持久化前校验轻量聊天 RPC 请求。
func (v *RequestValidator) ValidateChat(req *vmmv1.ChatRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "chat", Message: "is required"}
	}
	if err := requireString("session_id", req.SessionId, 128); err != nil {
		return err
	}
	if err := requireString("message", req.Message, 16000); err != nil {
		return err
	}
	return nil
}

// sanitizePostActionSnapshot validates or trims one legacy raw snapshot according to the compatibility mode.
// sanitizePostActionSnapshot 用于按照兼容模式校验或裁剪旧版原始快照。
func (v *RequestValidator) sanitizePostActionSnapshot(items []*vmmv1.RawMessage) ([]*vmmv1.RawMessage, error) {
	sanitized := make([]*vmmv1.RawMessage, 0, len(items))
	for idx, item := range items {
		fieldPrefix := fmt.Sprintf("raw_messages_snapshot[%d]", idx)
		role := strings.ToLower(strings.TrimSpace(item.GetRole()))
		switch role {
		case "system", "tool":
			continue
		case "user", "assistant":
		default:
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: fieldPrefix + ".role", Message: "must be one of [user assistant system tool]"}
			}
			continue
		}
		if len(item.GetToolCalls()) > 0 {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: fieldPrefix + ".tool_calls", Message: "must be empty for user or assistant messages"}
			}
			continue
		}
		content, keep, err := v.sanitizePostActionContent(item.GetContentJson(), fieldPrefix+".content")
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		sanitized = append(sanitized, &vmmv1.RawMessage{Role: role, ContentJson: content})
	}
	return sanitized, nil
}

// sanitizePostActionContent normalizes one legacy content payload into a pure text JSON string.
// sanitizePostActionContent 用于把一条旧版内容载荷规范化为纯文本 JSON 字符串。
func (v *RequestValidator) sanitizePostActionContent(raw string, field string) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		if v.postActionInputMode == postActionInputModeStrict {
			return "", false, logicdomain.ValidationError{Field: field, Message: "must contain a text string or text blocks"}
		}
		return "", false, nil
	}
	rawBytes := []byte(raw)
	var text string
	if err := json.Unmarshal(rawBytes, &text); err == nil {
		text = textutil.CleanConversationText(text)
		if text == "" {
			if v.postActionInputMode == postActionInputModeStrict {
				return "", false, logicdomain.ValidationError{Field: field, Message: "must contain non-empty text"}
			}
			return "", false, nil
		}
		return marshalTextContent(text)
	}

	var blocks []any
	if err := json.Unmarshal(rawBytes, &blocks); err == nil {
		parts, err := v.extractTextBlocks(blocks, field)
		if err != nil {
			return "", false, err
		}
		if len(parts) == 0 {
			if v.postActionInputMode == postActionInputModeStrict {
				return "", false, logicdomain.ValidationError{Field: field, Message: "must contain at least one text block"}
			}
			return "", false, nil
		}
		return marshalTextContent(strings.Join(parts, " "))
	}

	var obj map[string]any
	if err := json.Unmarshal(rawBytes, &obj); err == nil {
		if v.postActionInputMode == postActionInputModeStrict {
			return "", false, logicdomain.ValidationError{Field: field, Message: "must be either a string or an array of text blocks"}
		}
		part, ok := compatTextBlock(obj)
		if !ok {
			return "", false, nil
		}
		return marshalTextContent(part)
	}

	if v.postActionInputMode == postActionInputModeStrict {
		return "", false, logicdomain.ValidationError{Field: field, Message: "must be valid json content"}
	}
	return "", false, nil
}

// extractTextBlocks enforces text-only content arrays in strict mode and trims incompatible items in compat mode.
// extractTextBlocks 用于在严格模式下强制要求数组仅包含文本块，并在兼容模式下裁剪不兼容项。
func (v *RequestValidator) extractTextBlocks(blocks []any, field string) ([]string, error) {
	parts := make([]string, 0, len(blocks))
	for idx, block := range blocks {
		blockField := fmt.Sprintf("%s[%d]", field, idx)
		obj, ok := block.(map[string]any)
		if !ok {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: blockField, Message: "must be an object with type=text"}
			}
			continue
		}
		typeValue, _ := obj["type"].(string)
		typeValue = strings.ToLower(strings.TrimSpace(typeValue))
		if typeValue != "text" {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: blockField + ".type", Message: "must be text"}
			}
			continue
		}
		textValue, ok := obj["text"].(string)
		if !ok {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: blockField + ".text", Message: "must be a string"}
			}
			continue
		}
		textValue = textutil.CleanConversationText(textValue)
		if textValue == "" {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: blockField + ".text", Message: "must contain non-empty text"}
			}
			continue
		}
		parts = append(parts, textValue)
	}
	return parts, nil
}

// compatTextBlock extracts one text block from a non-standard object payload for compatibility mode.
// compatTextBlock 用于在兼容模式下从非标准对象载荷中提取一条文本块。
func compatTextBlock(obj map[string]any) (string, bool) {
	typeValue, _ := obj["type"].(string)
	if strings.ToLower(strings.TrimSpace(typeValue)) != "text" {
		return "", false
	}
	textValue, _ := obj["text"].(string)
	textValue = textutil.CleanConversationText(textValue)
	if textValue == "" {
		return "", false
	}
	return textValue, true
}

// marshalTextContent converts one normalized text payload back into JSON string form for downstream mapping.
// marshalTextContent 用于把规范化后的文本载荷重新编码为 JSON 字符串，供后续映射使用。
func marshalTextContent(text string) (string, bool, error) {
	raw, err := json.Marshal(text)
	if err != nil {
		return "", false, logicdomain.ValidationError{Field: "content", Message: "failed to encode normalized text"}
	}
	return string(raw), true, nil
}

// defaultScope fills empty scope identifiers with the stable local default value.
// defaultScope 用于把空的范围标识补成稳定的本地默认值。
func defaultScope(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultScopeValue
	}
	return value
}

// requireString enforces one non-empty bounded string field.
// requireString 用于校验单个非空且长度受限的字符串字段。
func requireString(field, value string, max int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return logicdomain.ValidationError{Field: field, Message: "is required"}
	}
	return maxString(field, value, max)
}

// maxString enforces the maximum length of one optional string field.
// maxString 用于校验单个可选字符串字段的最大长度。
func maxString(field, value string, max int) error {
	if max <= 0 {
		return nil
	}
	if len(value) > max {
		return logicdomain.ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", max)}
	}
	return nil
}

// requireOneOf enforces that one string field belongs to a small explicit allowlist.
// requireOneOf 用于校验单个字符串字段必须属于一个明确的小型允许列表。
func requireOneOf(field, value string, allowed ...string) error {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return logicdomain.ValidationError{Field: field, Message: "is required"}
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return logicdomain.ValidationError{Field: field, Message: fmt.Sprintf("must be one of [%s]", strings.Join(allowed, " "))}
}
