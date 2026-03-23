// validation.go implements the inbound HTTP adapter layer.
// validation.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	postActionInputModeCompat = "compat"
	postActionInputModeStrict = "strict"
	defaultScopeValue         = "default"
)

// RequestValidator performs lightweight transport validation without pulling in a heavy generic library.
// RequestValidator 用于在不引入重型通用库的情况下执行轻量传输层校验。
type RequestValidator struct {
	postActionInputMode string
}

// NewRequestValidator creates the transport validator shared by HTTP handlers.
// NewRequestValidator 用于创建 HTTP 处理器共享的传输层校验器。
func NewRequestValidator(postActionInputMode string) *RequestValidator {
	mode := strings.ToLower(strings.TrimSpace(postActionInputMode))
	if mode != postActionInputModeStrict {
		mode = postActionInputModeCompat
	}
	return &RequestValidator{postActionInputMode: mode}
}

// normalizePreCheckRequest trims the current pre-check user_content text before validation and use-case mapping.
// normalizePreCheckRequest 用于在校验和映射到用例前清理 pre-check 当前 user_content 文本。
func normalizePreCheckRequest(req *PreCheckRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.UserText = textutil.CleanConversationText(req.UserText)
}

// normalizePostActionRequest trims transport strings before normalization and persistence.
// normalizePostActionRequest 用于在清洗和持久化之前裁剪 post-action 传输层字符串。
func normalizePostActionRequest(req *PostActionRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = defaultScope(strings.TrimSpace(req.UserID))
	req.TeamID = defaultScope(strings.TrimSpace(req.TeamID))
	req.SpaceID = defaultScope(strings.TrimSpace(req.SpaceID))
	req.ProjectID = defaultScope(strings.TrimSpace(req.ProjectID))
	for i := range req.RawMessagesSnapshot {
		req.RawMessagesSnapshot[i].Role = strings.ToLower(strings.TrimSpace(req.RawMessagesSnapshot[i].Role))
	}
}

// normalizePostActionAsyncRequest trims scope identifiers and sanitizes the text-only fields used by the new async post-action route.
// normalizePostActionAsyncRequest 用于裁剪范围标识，并清洗新异步 post-action 路由使用的纯文本字段。
func normalizePostActionAsyncRequest(req *PostActionAsyncRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = defaultScope(strings.TrimSpace(req.UserID))
	req.TeamID = defaultScope(strings.TrimSpace(req.TeamID))
	req.SpaceID = defaultScope(strings.TrimSpace(req.SpaceID))
	req.ProjectID = defaultScope(strings.TrimSpace(req.ProjectID))
	req.UserContent = textutil.CleanConversationText(req.UserContent)
	req.AssistantContent = textutil.CleanConversationText(req.AssistantContent)
	for idx := range req.Timeline {
		req.Timeline[idx].Type = strings.ToLower(strings.TrimSpace(req.Timeline[idx].Type))
		req.Timeline[idx].Content = textutil.CleanConversationText(req.Timeline[idx].Content)
	}
}

// normalizeSeedMemoryRequest trims transport strings before the seed-memory use case runs.
// normalizeSeedMemoryRequest 用于在执行 seed-memory 用例之前裁剪传输层字符串。
func normalizeSeedMemoryRequest(req *SeedMemoryRequestDTO) {
	if req == nil {
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.MemoryText = strings.TrimSpace(req.MemoryText)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
}

// normalizeChatRequest trims the /chat transport payload before validation and use-case mapping.
// normalizeChatRequest 用于在校验和映射到用例前清理 /chat 传输层载荷。
func normalizeChatRequest(req *ChatRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Message = strings.TrimSpace(req.Message)
}

// ValidatePreCheck validates the pre-check DTO before it is mapped into a use-case command.
// ValidatePreCheck 用于在映射到用例命令前校验 pre-check DTO。
func (v *RequestValidator) ValidatePreCheck(req PreCheckRequestDTO) error {
	if err := requireString("session_id", req.SessionID, 128); err != nil {
		return err
	}
	if err := requireString("user_id", req.UserID, 128); err != nil {
		return err
	}
	if err := requireString("team_id", req.TeamID, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectID, 128); err != nil {
		return err
	}
	if err := requireString("user_content", req.UserText, 16000); err != nil {
		return err
	}
	return nil
}

// ValidatePostAction validates the post-action DTO before it enters normalization and persistence.
// ValidatePostAction 用于在进入清洗和持久化前校验 post-action DTO。
func (v *RequestValidator) ValidatePostAction(req PostActionRequestDTO) error {
	if err := requireString("session_id", req.SessionID, 128); err != nil {
		return err
	}
	if err := requireString("user_id", req.UserID, 128); err != nil {
		return err
	}
	if err := requireString("team_id", req.TeamID, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectID, 128); err != nil {
		return err
	}
	if err := maxString("space_id", req.SpaceID, 128); err != nil {
		return err
	}
	return nil
}

// ValidatePostActionAsync validates the new text-only post-action contract before it is converted into a background persistence command.
// ValidatePostActionAsync 用于在转换成后台持久化命令前校验新的纯文本 post-action 契约。
func (v *RequestValidator) ValidatePostActionAsync(req PostActionAsyncRequestDTO) error {
	if err := requireString("session_id", req.SessionID, 128); err != nil {
		return err
	}
	if err := maxString("user_id", req.UserID, 128); err != nil {
		return err
	}
	if err := maxString("team_id", req.TeamID, 128); err != nil {
		return err
	}
	if err := maxString("space_id", req.SpaceID, 128); err != nil {
		return err
	}
	if err := maxString("project_id", req.ProjectID, 128); err != nil {
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
	if len(req.Timeline) > 0 {
		first := req.Timeline[0]
		last := req.Timeline[len(req.Timeline)-1]
		if first.Type != "user" {
			return logicdomain.ValidationError{Field: "timeline[0].type", Message: "must be user so the timeline starts from the first user question"}
		}
		if first.Content != req.UserContent {
			return logicdomain.ValidationError{Field: "timeline[0].content", Message: "must match user_content"}
		}
		if last.Type != "assistant" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("timeline[%d].type", len(req.Timeline)-1), Message: "must be assistant so the timeline ends at the last assistant answer"}
		}
		if last.Content != req.AssistantContent {
			return logicdomain.ValidationError{Field: fmt.Sprintf("timeline[%d].content", len(req.Timeline)-1), Message: "must match assistant_content"}
		}
	}
	return nil
}

// PreparePostAction normalizes scope defaults and sanitizes the raw snapshot according to the configured input mode.
// PreparePostAction 用于按配置的输入模式规范化范围字段默认值，并清洗原始快照。
func (v *RequestValidator) PreparePostAction(req *PostActionRequestDTO) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "post_action", Message: "is required"}
	}
	normalizePostActionRequest(req)
	if req.RawMessagesSnapshot == nil {
		req.RawMessagesSnapshot = []RawMessageDTO{}
	}
	if err := v.ValidatePostAction(*req); err != nil {
		return err
	}
	sanitized, err := v.sanitizePostActionSnapshot(req.RawMessagesSnapshot)
	if err != nil {
		return err
	}
	req.RawMessagesSnapshot = sanitized
	return nil
}

// ValidateSeedMemory validates the seed-memory DTO before one embedding request is issued.
// ValidateSeedMemory 用于在发起 embedding 请求前校验 seed-memory DTO。
func (v *RequestValidator) ValidateSeedMemory(req SeedMemoryRequestDTO) error {
	if err := requireString("user_id", req.UserID, 128); err != nil {
		return err
	}
	if err := requireString("project_id", req.ProjectID, 128); err != nil {
		return err
	}
	if err := requireString("memory_text", req.MemoryText, 16000); err != nil {
		return err
	}
	return nil
}

// ValidateChat validates the /chat DTO before PII scrubbing and persistence.
// ValidateChat 用于在 PII 脱敏和持久化之前校验 /chat DTO。
func (v *RequestValidator) ValidateChat(req ChatRequestDTO) error {
	if err := requireString("session_id", req.SessionID, 128); err != nil {
		return err
	}
	if err := requireString("message", req.Message, 16000); err != nil {
		return err
	}
	return nil
}

// sanitizePostActionSnapshot validates or trims one raw snapshot according to the configured compatibility mode.
// sanitizePostActionSnapshot 用于按照当前兼容模式校验或裁剪原始快照。
func (v *RequestValidator) sanitizePostActionSnapshot(items []RawMessageDTO) ([]RawMessageDTO, error) {
	sanitized := make([]RawMessageDTO, 0, len(items))
	for idx, item := range items {
		fieldPrefix := fmt.Sprintf("raw_messages_snapshot[%d]", idx)
		role := strings.ToLower(strings.TrimSpace(item.Role))
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
		if len(item.ToolCalls) > 0 {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, logicdomain.ValidationError{Field: fieldPrefix + ".tool_calls", Message: "must be empty for user or assistant messages"}
			}
			continue
		}
		content, keep, err := v.sanitizePostActionContent(item.Content, fieldPrefix+".content")
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		sanitized = append(sanitized, RawMessageDTO{Role: role, Content: content})
	}
	return sanitized, nil
}

// sanitizePostActionContent normalizes one user/assistant content payload into a pure text JSON string.
// sanitizePostActionContent 用于把一条 user/assistant content 载荷规范化为纯文本 JSON 字符串。
func (v *RequestValidator) sanitizePostActionContent(raw json.RawMessage, field string) (json.RawMessage, bool, error) {
	if len(raw) == 0 {
		if v.postActionInputMode == postActionInputModeStrict {
			return nil, false, logicdomain.ValidationError{Field: field, Message: "must contain a text string or text blocks"}
		}
		return nil, false, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = textutil.CleanConversationText(text)
		if text == "" {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, false, logicdomain.ValidationError{Field: field, Message: "must contain non-empty text"}
			}
			return nil, false, nil
		}
		return marshalTextContent(text)
	}

	var blocks []any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts, err := v.extractTextBlocks(blocks, field)
		if err != nil {
			return nil, false, err
		}
		if len(parts) == 0 {
			if v.postActionInputMode == postActionInputModeStrict {
				return nil, false, logicdomain.ValidationError{Field: field, Message: "must contain at least one text block"}
			}
			return nil, false, nil
		}
		return marshalTextContent(strings.Join(parts, " "))
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if v.postActionInputMode == postActionInputModeStrict {
			return nil, false, logicdomain.ValidationError{Field: field, Message: "must be either a string or an array of text blocks"}
		}
		part, ok := compatTextBlock(obj)
		if !ok {
			return nil, false, nil
		}
		return marshalTextContent(part)
	}

	if v.postActionInputMode == postActionInputModeStrict {
		return nil, false, logicdomain.ValidationError{Field: field, Message: "must be valid json content"}
	}
	return nil, false, nil
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

// marshalTextContent converts one normalized text payload back into JSON string form for downstream domain mapping.
// marshalTextContent 用于把规范化后的文本载荷重新编码为 JSON 字符串，供后续领域映射使用。
func marshalTextContent(text string) (json.RawMessage, bool, error) {
	raw, err := json.Marshal(text)
	if err != nil {
		return nil, false, logicdomain.ValidationError{Field: "content", Message: "failed to encode normalized text"}
	}
	return json.RawMessage(raw), true, nil
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
