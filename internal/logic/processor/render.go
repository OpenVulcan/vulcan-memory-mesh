// render.go implements reusable business processors.
// render.go 用于实现可复用的业务处理器。
package processor

import (
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// renderIntentUserPrompt serializes the mixed recent-turn window and current user question into a stable JSON body for the first-stage pre-check scene.
// renderIntentUserPrompt 用于把混合最近 turn 窗口和当前用户问题序列化成稳定 JSON 请求体，供 pre-check 第一层场景使用。
func renderIntentUserPrompt(turns []logicdomain.PreCheckTurnContext, current string, maxQueries int) string {
	type recentTurnInput struct {
		TurnID      uint64 `json:"turn_id"`
		ContentType string `json:"content_type"`
		Content     string `json:"content"`
	}
	type requestBody struct {
		RecentTurns         []recentTurnInput `json:"recent_turns,omitempty"`
		CurrentUserInput    string            `json:"current_user_input"`
		CurrentContextHints []string          `json:"current_context_hints,omitempty"`
		RecentContextHints  []string          `json:"recent_context_hints,omitempty"`
		MaxSearchQueries    int               `json:"max_search_queries"`
	}

	body := requestBody{
		RecentTurns:         make([]recentTurnInput, 0, len(turns)),
		CurrentUserInput:    strings.TrimSpace(current),
		CurrentContextHints: extractPreCheckContextHints(current, 4),
		RecentContextHints:  extractRecentTurnContextHints(turns, 4),
		MaxSearchQueries:    maxQueries,
	}
	for _, turn := range turns {
		content := strings.TrimSpace(turn.Content)
		if turn.TurnID == 0 || content == "" {
			continue
		}
		body.RecentTurns = append(body.RecentTurns, recentTurnInput{
			TurnID:      turn.TurnID,
			ContentType: strings.TrimSpace(turn.ContentType),
			Content:     content,
		})
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"current_user_input\":%q,\"max_search_queries\":%d}", strings.TrimSpace(current), maxQueries)
	}
	return string(rendered)
}

// renderContextSummary renders the target output.
// renderContextSummary 用于渲染目标输出。
func renderContextSummary(items []logicdomain.ContextItem) string {
	if len(items) == 0 {
		return ""
	}
	order := []struct{ Kind, Title string }{{"project_constraint", "项目约束"}, {"persona", "个人画像"}, {"preference", "偏好习惯"}, {"memory", "向量召回记忆"}}
	var b strings.Builder
	firstSection := true
	for _, group := range order {
		section := make([]logicdomain.ContextItem, 0)
		for _, item := range items {
			if item.Kind == group.Kind {
				section = append(section, item)
			}
		}
		if len(section) == 0 {
			continue
		}
		if !firstSection {
			b.WriteString("\n\n")
		}
		firstSection = false
		title := renderContextSectionTitle(group.Title, section)
		b.WriteString("[")
		b.WriteString(title)
		b.WriteString("]\n")
		for i, item := range section {
			if item.Kind == "memory" {
				b.WriteString(fmt.Sprintf("%d. %s (score=%.3f)\n", i+1, item.Text, item.Score))
			} else {
				b.WriteString("- ")
				b.WriteString(item.Text)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// renderContextSectionTitle keeps summary section labels aligned with the actual context items so the main assembler path does not drift away from fallback rendering.
// renderContextSectionTitle 用于让摘要 section 标题与实际 context item 保持一致，避免主 assembler 路径再次和 fallback 渲染发生漂移。
func renderContextSectionTitle(defaultTitle string, section []logicdomain.ContextItem) string {
	if len(section) == 0 {
		return defaultTitle
	}
	title := strings.TrimSpace(section[0].Title)
	if title == "" {
		return defaultTitle
	}
	return title
}

// renderTurnAnalysisRequest serializes one reference-aware single-turn analysis input into the stable JSON body consumed by the postaction_l1_main scene.
// renderTurnAnalysisRequest 用于把一份参考感知型单轮分析输入序列化成 postaction_l1_main 场景消费的稳定 JSON 请求体。
func renderTurnAnalysisRequest(input logicdomain.TurnAnalysisInput) (string, error) {
	type promptTimeInput struct {
		DateTime string `json:"datetime,omitempty"`
	}
	type referenceTurnInput struct {
		TurnID  uint64 `json:"turn_id"`
		Details string `json:"details"`
	}
	type targetTurnInput struct {
		TurnID          uint64          `json:"turn_id"`
		CreatedDateTime string          `json:"created_datetime,omitempty"`
		RawTurn         json.RawMessage `json:"raw_turn"`
	}
	type recentDirectWriteInput struct {
		MemoryID        uint64 `json:"memory_id"`
		ScopeLevel      string `json:"scope_level,omitempty"`
		Abstract        string `json:"abstract"`
		Details         string `json:"details"`
		CreatedDateTime string `json:"created_datetime,omitempty"`
	}
	type requestBody struct {
		CurrentTime            *promptTimeInput         `json:"current_time,omitempty"`
		ReferenceTurns         []referenceTurnInput     `json:"reference_turns"`
		TargetTurn             targetTurnInput          `json:"target_turn"`
		RecentGRPCMemoryWrites []recentDirectWriteInput `json:"recent_grpc_memory_writes,omitempty"`
	}

	rawTurn := json.RawMessage(strings.TrimSpace(input.TargetTurn.RawTurn))
	if input.TargetTurn.TurnID == 0 {
		return "", fmt.Errorf("target turn id is required")
	}
	if !json.Valid(rawTurn) {
		return "", fmt.Errorf("target turn raw_turn is not valid json")
	}

	currentDateTime, _ := formatPromptTimestampMillis(input.CurrentTimestamp)
	targetCreatedDateTime, _ := formatPromptTimestampMillis(input.TargetTurn.CreatedTimestamp)
	body := requestBody{
		ReferenceTurns: make([]referenceTurnInput, 0, len(input.ReferenceTurns)),
		TargetTurn: targetTurnInput{
			TurnID:          input.TargetTurn.TurnID,
			CreatedDateTime: targetCreatedDateTime,
			RawTurn:         rawTurn,
		},
		RecentGRPCMemoryWrites: make([]recentDirectWriteInput, 0, len(input.RecentGRPCMemoryWrites)),
	}
	if input.CurrentTimestamp > 0 {
		body.CurrentTime = &promptTimeInput{
			DateTime: currentDateTime,
		}
	}
	for _, turn := range input.ReferenceTurns {
		if turn.TurnID == 0 {
			continue
		}
		body.ReferenceTurns = append(body.ReferenceTurns, referenceTurnInput{
			TurnID:  turn.TurnID,
			Details: strings.TrimSpace(turn.Details),
		})
	}
	for _, memory := range input.RecentGRPCMemoryWrites {
		if memory.MemoryID == 0 || strings.TrimSpace(memory.Abstract) == "" {
			continue
		}
		details := strings.TrimSpace(memory.Details)
		if details == "" {
			details = strings.TrimSpace(memory.Abstract)
		}
		createdDateTime, _ := formatPromptTimestampMillis(memory.CreatedTimestamp)
		body.RecentGRPCMemoryWrites = append(body.RecentGRPCMemoryWrites, recentDirectWriteInput{
			MemoryID:        memory.MemoryID,
			ScopeLevel:      strings.TrimSpace(memory.ScopeLevel),
			Abstract:        strings.TrimSpace(memory.Abstract),
			Details:         details,
			CreatedDateTime: createdDateTime,
		})
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal postaction_l1_main request: %w", err)
	}
	return string(rendered), nil
}

// renderTurnAnalysisSystemPrompt keeps the postaction_l1_main system prompt static so provider prompt caches can reuse the same prefix across different input shapes.
// renderTurnAnalysisSystemPrompt 用于保持 postaction_l1_main 系统提示词静态，便于 provider prompt cache 在不同输入形态之间复用同一前缀。
func renderTurnAnalysisSystemPrompt(template string, input logicdomain.TurnAnalysisInput) string {
	_ = input
	return strings.TrimSpace(strings.ReplaceAll(template, "\r\n", "\n"))
}

// formatPromptTimestampMillis converts one millisecond timestamp into the shared local datetime/date prompt strings while keeping zero-values empty.
// formatPromptTimestampMillis 用于把毫秒时间戳转换成共享的本地 datetime/date 提示词字符串，并在零值时保持为空。
func formatPromptTimestampMillis(timestamp int64) (string, string) {
	return logicdomain.FormatDisplayTimeFromUnixMillis(timestamp)
}
