package textutil

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	thoughtPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<think>.*?</think>`),
		regexp.MustCompile(`(?is)<analysis>.*?</analysis>`),
		regexp.MustCompile(`(?is)<reasoning>.*?</reasoning>`),
		regexp.MustCompile(`(?is)<scratchpad>.*?</scratchpad>`),
		regexp.MustCompile(`(?is)<reflection>.*?</reflection>`),
	}
	spacePattern = regexp.MustCompile(`\s+`)
	tokenPattern = regexp.MustCompile(`[\p{Han}]+|[a-zA-Z0-9_\-\.]+`)
)

func NormalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(s, " "))
}

func StripThoughtTags(s string) string {
	out := s
	for _, re := range thoughtPatterns {
		out = re.ReplaceAllString(out, " ")
	}
	return NormalizeWhitespace(out)
}

func Tokenize(s string) []string {
	out := make([]string, 0)
	for _, token := range tokenPattern.FindAllString(strings.ToLower(NormalizeWhitespace(s)), -1) {
		if token = strings.TrimSpace(token); token != "" {
			out = append(out, token)
		}
	}
	return out
}

func ExtractTextFromAny(v any) string {
	switch tv := v.(type) {
	case nil:
		return ""
	case string:
		return NormalizeWhitespace(tv)
	case []any:
		parts := make([]string, 0, len(tv))
		for _, item := range tv {
			if txt := ExtractTextFromAny(item); txt != "" {
				parts = append(parts, txt)
			}
		}
		return NormalizeWhitespace(strings.Join(parts, " "))
	case map[string]any:
		if txt, ok := tv["text"].(string); ok {
			return NormalizeWhitespace(txt)
		}
		if content, ok := tv["content"]; ok {
			return ExtractTextFromAny(content)
		}
		raw, _ := json.Marshal(tv)
		return NormalizeWhitespace(string(raw))
	case json.RawMessage:
		return ExtractTextFromRawJSON(tv)
	default:
		raw, _ := json.Marshal(tv)
		return NormalizeWhitespace(string(raw))
	}
}

func ExtractTextFromRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return NormalizeWhitespace(s)
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		return ExtractTextFromAny(list)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return ExtractTextFromAny(obj)
	}
	return NormalizeWhitespace(string(raw))
}
