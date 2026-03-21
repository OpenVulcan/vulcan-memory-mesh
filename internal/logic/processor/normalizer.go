// normalizer.go implements reusable business processors.
// normalizer.go 用于实现可复用的业务处理器。
package processor

import (
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

// MessageNormalizer converts raw chat snapshots into clean user-assistant turns ready for persistence.
// MessageNormalizer 用于把原始对话快照转换成可持久化的干净用户-助手轮次。
type MessageNormalizer struct{}

// NewMessageNormalizer creates a MessageNormalizer instance.
// NewMessageNormalizer 用于创建 MessageNormalizer 实例。
func NewMessageNormalizer() *MessageNormalizer { return &MessageNormalizer{} }

// Normalize executes the Normalize logic.
// Normalize 用于执行 Normalize 逻辑。
func (n *MessageNormalizer) Normalize(messages []logicdomain.RawMessage) []logicdomain.NormalizedTurn {
	type cleaned struct{ role, text string }

	// Filter unsupported roles and trust the inbound layer to provide already-sanitized text payloads.
	// 在组装对话轮次之前先过滤不支持的角色，并信任入站层已经完成文本清洗。
	filtered := make([]cleaned, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		if len(msg.ToolCalls) > 0 {
			continue
		}
		text := textutil.NormalizeWhitespace(textutil.ExtractTextFromAny(msg.Content))
		if text == "" {
			continue
		}
		filtered = append(filtered, cleaned{role: role, text: text})
	}
	turns := make([]logicdomain.NormalizedTurn, 0)
	var pendingUser, pendingAssistant string

	// Flush only complete user-assistant pairs into normalized turns.
	// 只有完整的用户-助手配对才会被刷入标准化轮次。
	flush := func() {
		if pendingUser == "" || pendingAssistant == "" {
			return
		}
		turns = append(turns, logicdomain.NormalizedTurn{TurnIndex: len(turns) + 1, UserMessage: pendingUser, AssistantReply: pendingAssistant, CreatedAt: time.Now().UTC()})
	}
	for _, msg := range filtered {
		if msg.role == "user" {
			flush()
			pendingUser = msg.text
			pendingAssistant = ""
			continue
		}
		if pendingUser == "" {
			continue
		}
		pendingAssistant = msg.text
	}
	flush()
	return turns
}
