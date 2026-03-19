// render.go implements reusable business processors.
// render.go 用于实现可复用的业务处理器。
package processor

import (
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
