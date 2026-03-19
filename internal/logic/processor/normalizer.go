package processor

import (
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

type MessageNormalizer struct{}

func NewMessageNormalizer() *MessageNormalizer { return &MessageNormalizer{} }

func (n *MessageNormalizer) Normalize(messages []logicdomain.RawMessage) []logicdomain.NormalizedTurn {
	type cleaned struct{ role, text string }
	filtered := make([]cleaned, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		if len(msg.ToolCalls) > 0 {
			continue
		}
		text := textutil.StripThoughtTags(textutil.ExtractTextFromAny(msg.Content))
		if text == "" {
			continue
		}
		filtered = append(filtered, cleaned{role: role, text: text})
	}
	turns := make([]logicdomain.NormalizedTurn, 0)
	var pendingUser, pendingAssistant string
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
