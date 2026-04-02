// render.go implements reusable business processors.
// render.go 用于实现可复用的业务处理器。
package processor

import (
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// renderIntentUserPrompt renders the target output.
// renderIntentUserPrompt 用于渲染目标输出。
func renderIntentUserPrompt(history []logicdomain.HistorySnippet, current string, maxKeywords int) string {
	var b strings.Builder
	b.WriteString("请结合最近对话和当前输入，输出 JSON。\n")
	b.WriteString(fmt.Sprintf("最多返回 %d 个搜索关键词。\n\n", maxKeywords))
	if len(history) > 0 {
		b.WriteString("最近 2 轮历史：\n")
		for _, item := range history {
			b.WriteString("- ")
			b.WriteString(item.Role)
			b.WriteString(": ")
			b.WriteString(item.Content)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("当前问题：\n")
	b.WriteString(strings.TrimSpace(current))
	return b.String()
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
		b.WriteString("[")
		b.WriteString(group.Title)
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

// renderTurnAnalysisRequest serializes one reference-aware single-turn analysis input into the stable JSON body consumed by the analyze_turn scene.
// renderTurnAnalysisRequest 用于把一份参考感知型单轮分析输入序列化成 analyze_turn 场景消费的稳定 JSON 请求体。
func renderTurnAnalysisRequest(input logicdomain.TurnAnalysisInput) (string, error) {
	type referenceTurnInput struct {
		TurnID  uint64 `json:"turn_id"`
		Details string `json:"details"`
	}
	type targetTurnInput struct {
		TurnID  uint64          `json:"turn_id"`
		RawTurn json.RawMessage `json:"raw_turn"`
	}
	type activeMemoryNodeInput struct {
		MemoryID     uint64 `json:"memory_id"`
		SourceTurnID uint64 `json:"source_turn_id,omitempty"`
		Category     int    `json:"category"`
		Abstract     string `json:"abstract"`
		Details      string `json:"details"`
		SourceKind   string `json:"source_kind,omitempty"`
		ScopeLevel   string `json:"scope_level,omitempty"`
	}
	type recentDirectWriteInput struct {
		MemoryID   uint64 `json:"memory_id"`
		ScopeLevel string `json:"scope_level,omitempty"`
		Abstract   string `json:"abstract"`
		Details    string `json:"details"`
	}
	type requestBody struct {
		ReferenceTurns         []referenceTurnInput     `json:"reference_turns"`
		TargetTurn             targetTurnInput          `json:"target_turn"`
		ActiveMemoryNodes      []activeMemoryNodeInput  `json:"active_memory_nodes"`
		RecentGRPCMemoryWrites []recentDirectWriteInput `json:"recent_grpc_memory_writes,omitempty"`
	}

	rawTurn := json.RawMessage(strings.TrimSpace(input.TargetTurn.RawTurn))
	if input.TargetTurn.TurnID == 0 {
		return "", fmt.Errorf("target turn id is required")
	}
	if !json.Valid(rawTurn) {
		return "", fmt.Errorf("target turn raw_turn is not valid json")
	}

	body := requestBody{
		ReferenceTurns:         make([]referenceTurnInput, 0, len(input.ReferenceTurns)),
		TargetTurn:             targetTurnInput{TurnID: input.TargetTurn.TurnID, RawTurn: rawTurn},
		ActiveMemoryNodes:      make([]activeMemoryNodeInput, 0, len(input.ActiveMemoryNodes)),
		RecentGRPCMemoryWrites: make([]recentDirectWriteInput, 0, len(input.RecentGRPCMemoryWrites)),
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
	for _, node := range input.ActiveMemoryNodes {
		if node.MemoryID == 0 || strings.TrimSpace(node.Abstract) == "" {
			continue
		}
		details := strings.TrimSpace(node.Details)
		if details == "" {
			details = strings.TrimSpace(node.Abstract)
		}
		body.ActiveMemoryNodes = append(body.ActiveMemoryNodes, activeMemoryNodeInput{
			MemoryID:     node.MemoryID,
			SourceTurnID: node.SourceTurnID,
			Category:     node.Category,
			Abstract:     strings.TrimSpace(node.Abstract),
			Details:      details,
			SourceKind:   strings.TrimSpace(node.SourceKind),
			ScopeLevel:   strings.TrimSpace(node.ScopeLevel),
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
		body.RecentGRPCMemoryWrites = append(body.RecentGRPCMemoryWrites, recentDirectWriteInput{
			MemoryID:   memory.MemoryID,
			ScopeLevel: strings.TrimSpace(memory.ScopeLevel),
			Abstract:   strings.TrimSpace(memory.Abstract),
			Details:    details,
		})
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal analyze_turn request: %w", err)
	}
	return string(rendered), nil
}

// renderTurnAnalysisSystemPrompt applies the optional analyze_turn TAG blocks so direct-write exclusion and active-memory instructions only appear when the corresponding inputs exist.
// renderTurnAnalysisSystemPrompt 用于对 analyze_turn 的可选 TAG 块做替换，让主动写入排斥和活跃记忆规则只在对应输入存在时出现。
func renderTurnAnalysisSystemPrompt(template string, input logicdomain.TurnAnalysisInput) string {
	replacements := map[string]string{
		"REFERENCE_RULE":              "",
		"ACTIVE_MEMORY_RULE":          "",
		"DIRECT_WRITE_EXCLUSION_RULE": "",
	}
	if len(input.ReferenceTurns) > 0 {
		replacements["REFERENCE_RULE"] = "- 如果 `reference_turns` 非空，它们只用于帮助你理解 `target_turn` 的上下文；绝对禁止把这些历史摘要重新提炼成当前 turn 的新增记忆或画像。"
	}
	if len(input.ActiveMemoryNodes) > 0 {
		replacements["ACTIVE_MEMORY_RULE"] = "- 如果 `active_memory_nodes` 非空，先判断 `target_turn` 是否只是重复已有记忆；只有在当前 turn 明确覆盖、推翻或使旧记忆失效时，才把对应 `memory_id` 写入 `superseded_memory_ids`。"
	}
	if len(input.RecentGRPCMemoryWrites) > 0 {
		replacements["DIRECT_WRITE_EXCLUSION_RULE"] = "- 如果 `recent_grpc_memory_writes` 非空，它们属于绝对排斥区；这些事实已经由工具链主动写入，禁止再次提炼入库。若当前 turn 的有效信息已经被它们完全覆盖，必须返回空 `details`、空 `memory_nodes`、空 `profile_nodes`。"
	}
	return renderTaggedPrompt(template, replacements)
}

// renderTaggedPrompt replaces known {#TAG NAME#} placeholders line-by-line and drops the whole line when one placeholder resolves to an empty string.
// renderTaggedPrompt 用于按行替换 {#TAG NAME#} 占位符，并在占位符结果为空时删除整行，避免模板结构被空块打乱。
func renderTaggedPrompt(template string, replacements map[string]string) string {
	lines := strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n")
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		current := line
		for tag, value := range replacements {
			current = strings.ReplaceAll(current, "{#TAG "+tag+"#}", strings.TrimSpace(value))
		}
		if strings.TrimSpace(current) == "" {
			if strings.TrimSpace(line) == "" {
				rendered = append(rendered, "")
			}
			continue
		}
		rendered = append(rendered, strings.TrimRight(current, " \t"))
	}
	return strings.TrimSpace(collapseBlankLines(rendered))
}

// collapseBlankLines keeps at most one empty line between rendered prompt blocks so optional TAG removal does not leave large gaps.
// collapseBlankLines 用于在渲染后的提示词块之间最多保留一行空行，避免可选 TAG 被删除后留下大段空白。
func collapseBlankLines(lines []string) string {
	var b strings.Builder
	lastBlank := false
	for idx, line := range lines {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && lastBlank {
			continue
		}
		if idx > 0 && (!isBlank || !lastBlank) {
			b.WriteString("\n")
		}
		b.WriteString(line)
		lastBlank = isBlank
	}
	return b.String()
}
