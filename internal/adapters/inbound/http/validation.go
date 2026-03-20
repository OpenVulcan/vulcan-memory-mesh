// validation.go implements the inbound HTTP adapter layer.
// validation.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// RequestValidator performs lightweight transport validation without pulling in a heavy generic library.
// RequestValidator 用于在不引入重型通用库的情况下执行轻量传输层校验。
type RequestValidator struct{}

// NewRequestValidator creates the transport validator shared by HTTP handlers.
// NewRequestValidator 用于创建 HTTP 处理器共享的传输层校验器。
func NewRequestValidator() *RequestValidator { return &RequestValidator{} }

// normalizePreCheckRequest trims transport strings before validation and use-case mapping.
// normalizePreCheckRequest 用于在校验和映射到用例前清理 pre-check 传输层字符串。
func normalizePreCheckRequest(req *PreCheckRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.CurrentContent = strings.TrimSpace(req.CurrentContent)
	for i := range req.HistoryContent {
		req.HistoryContent[i].Role = strings.ToLower(strings.TrimSpace(req.HistoryContent[i].Role))
		req.HistoryContent[i].Content = strings.TrimSpace(req.HistoryContent[i].Content)
	}
}

// normalizePostActionRequest trims transport strings before normalization and persistence.
// normalizePostActionRequest 用于在清洗和持久化之前裁剪 post-action 传输层字符串。
func normalizePostActionRequest(req *PostActionRequestDTO) {
	if req == nil {
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.TeamID = strings.TrimSpace(req.TeamID)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	for i := range req.RawMessagesSnapshot {
		req.RawMessagesSnapshot[i].Role = strings.ToLower(strings.TrimSpace(req.RawMessagesSnapshot[i].Role))
		for j := range req.RawMessagesSnapshot[i].ToolCalls {
			req.RawMessagesSnapshot[i].ToolCalls[j].ID = strings.TrimSpace(req.RawMessagesSnapshot[i].ToolCalls[j].ID)
			req.RawMessagesSnapshot[i].ToolCalls[j].Type = strings.TrimSpace(req.RawMessagesSnapshot[i].ToolCalls[j].Type)
		}
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
	if err := maxString("current_content", req.CurrentContent, 16000); err != nil {
		return err
	}
	for idx, item := range req.HistoryContent {
		if err := validateHistorySnippet(idx, item); err != nil {
			return err
		}
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
	for idx, item := range req.RawMessagesSnapshot {
		if err := validateRawMessage(idx, item); err != nil {
			return err
		}
	}
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

// validateHistorySnippet validates one text-only history message item.
// validateHistorySnippet 用于校验一条纯文本历史消息项。
func validateHistorySnippet(index int, item HistorySnippetDTO) error {
	fieldPrefix := fmt.Sprintf("history_content[%d]", index)
	if err := requireOneOf(fieldPrefix+".role", item.Role, "user", "assistant"); err != nil {
		return err
	}
	if err := requireString(fieldPrefix+".content", item.Content, 16000); err != nil {
		return err
	}
	return nil
}

// validateRawMessage validates one raw post-action message before the normalizer inspects its content.
// validateRawMessage 用于在 normalizer 检查内容前校验一条 post-action 原始消息。
func validateRawMessage(index int, item RawMessageDTO) error {
	fieldPrefix := fmt.Sprintf("raw_messages_snapshot[%d]", index)
	if err := requireOneOf(fieldPrefix+".role", item.Role, "user", "assistant", "system", "tool"); err != nil {
		return err
	}
	for toolIndex, toolCall := range item.ToolCalls {
		toolPrefix := fmt.Sprintf("%s.tool_calls[%d]", fieldPrefix, toolIndex)
		if err := maxString(toolPrefix+".id", toolCall.ID, 128); err != nil {
			return err
		}
		if err := maxString(toolPrefix+".type", toolCall.Type, 64); err != nil {
			return err
		}
	}
	return nil
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
