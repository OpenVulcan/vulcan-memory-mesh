// text.go implements shared platform helpers.
// text.go 用于实现共享平台辅助能力。
package textutil

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Variables hold reusable regex helpers for whitespace cleanup, tokenization, and thought-tag stripping.
// Variables 用于保存空白清理、分词和思维标签剥离所需的正则辅助工具。
var (
	thoughtPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<think>.*?</think>`),
		regexp.MustCompile(`(?is)<analysis>.*?</analysis>`),
		regexp.MustCompile(`(?is)<reasoning>.*?</reasoning>`),
		// Keep <scratchpad> stripping as generic hidden-thought hygiene, not as the removed work-memory feature.
		// 保留 <scratchpad> 清洗作为通用隐藏思考卫生处理，而不是已移除的工作记忆功能。
		regexp.MustCompile(`(?is)<scratchpad>.*?</scratchpad>`),
		regexp.MustCompile(`(?is)<reflection>.*?</reflection>`),
	}
	spacePattern          = regexp.MustCompile(`\s+`)
	tokenPattern          = regexp.MustCompile(`[\p{Han}]+|[a-zA-Z0-9_\-\.]+`)
	markdownImagePattern  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	markdownLinkPattern   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	htmlAnchorPattern     = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	htmlVoidMediaPattern  = regexp.MustCompile(`(?is)<(img|source)\b([^>]*)>`)
	htmlBlockMediaPattern = regexp.MustCompile(`(?is)<(video|audio|object|embed|iframe)\b([^>]*)>.*?</(?:video|audio|object|embed|iframe)>`)
	rawDataURLPattern     = regexp.MustCompile(`(?i)data:[a-z0-9.+-]+/[a-z0-9.+-]+(?:;[a-z0-9=._+-]+)*;base64,[a-z0-9+/=\r\n]+`)
	rawResourceURLPattern = regexp.MustCompile(`(?i)\b(?:https?://|blob:|file:/+)[^\s<>"')]+`)
	htmlAnyTagPattern     = regexp.MustCompile(`(?is)<[^>]+>`)
)

// NormalizeWhitespace executes the NormalizeWhitespace logic.
// NormalizeWhitespace 用于执行 NormalizeWhitespace 逻辑。
func NormalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(s, " "))
}

// StripThoughtTags strips the target content.
// StripThoughtTags 用于剥离目标内容。
func StripThoughtTags(s string) string {
	out := s
	for _, re := range thoughtPatterns {
		out = re.ReplaceAllString(out, " ")
	}
	return NormalizeWhitespace(out)
}

// CleanConversationText strips thought tags, inline media payloads, and noisy media/file references from flattened chat text.
// CleanConversationText 用于从压平后的聊天文本中移除思维标签、内联媒体载荷和冗长的媒体/文件引用。
func CleanConversationText(s string) string {
	out := s
	out = markdownImagePattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := markdownImagePattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[Image filtered]"
		}
		alt := NormalizeWhitespace(sub[1])
		target := strings.TrimSpace(sub[2])
		kind := classifyResourceTarget(target)
		if kind == "" {
			kind = "Image"
		}
		return buildResourcePlaceholder(kind, alt, target)
	})
	out = markdownLinkPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := markdownLinkPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		label := NormalizeWhitespace(sub[1])
		target := strings.TrimSpace(sub[2])
		kind := classifyResourceTarget(target)
		if kind == "" {
			return match
		}
		return buildResourcePlaceholder(kind, label, target)
	})
	out = htmlAnchorPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlAnchorPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[Link filtered]"
		}
		target := extractHTMLAttribute(sub[1], "href")
		kind := classifyResourceTarget(target)
		if kind == "" {
			return NormalizeWhitespace(sub[2])
		}
		return buildResourcePlaceholder(kind, NormalizeWhitespace(stripHTML(sub[2])), target)
	})
	out = htmlVoidMediaPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlVoidMediaPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[Media filtered]"
		}
		tag := strings.ToLower(strings.TrimSpace(sub[1]))
		target := extractHTMLAttribute(sub[2], "src")
		if target == "" {
			target = extractHTMLAttribute(sub[2], "data")
		}
		if target == "" {
			target = extractHTMLAttribute(sub[2], "href")
		}
		alt := extractHTMLAttribute(sub[2], "alt")
		kind := classifyHTMLTag(tag, target)
		return buildResourcePlaceholder(kind, NormalizeWhitespace(alt), target)
	})
	out = htmlBlockMediaPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlBlockMediaPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[Media filtered]"
		}
		tag := strings.ToLower(strings.TrimSpace(sub[1]))
		target := extractHTMLAttribute(sub[2], "src")
		if target == "" {
			target = extractHTMLAttribute(sub[2], "data")
		}
		if target == "" {
			target = extractHTMLAttribute(sub[2], "href")
		}
		alt := extractHTMLAttribute(sub[2], "alt")
		kind := classifyHTMLTag(tag, target)
		return buildResourcePlaceholder(kind, NormalizeWhitespace(alt), target)
	})
	out = rawDataURLPattern.ReplaceAllStringFunc(out, func(match string) string {
		return buildResourcePlaceholder(classifyResourceTarget(match), "", match)
	})
	out = rawResourceURLPattern.ReplaceAllStringFunc(out, func(match string) string {
		kind := classifyResourceTarget(match)
		if kind == "" {
			return match
		}
		return buildResourcePlaceholder(kind, "", match)
	})
	return StripThoughtTags(out)
}

// Tokenize executes the Tokenize logic.
// Tokenize 用于执行 Tokenize 逻辑。
func Tokenize(s string) []string {
	out := make([]string, 0)
	for _, token := range tokenPattern.FindAllString(strings.ToLower(NormalizeWhitespace(s)), -1) {
		if token = strings.TrimSpace(token); token != "" {
			out = append(out, token)
		}
	}
	return out
}

// ExtractTextFromAny extracts the target data.
// ExtractTextFromAny 用于提取目标数据。
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

// ExtractTextFromRawJSON extracts the target data.
// ExtractTextFromRawJSON 用于提取目标数据。
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

// buildResourcePlaceholder converts one filtered resource into a short placeholder that preserves useful human context.
// buildResourcePlaceholder 用于把被过滤的资源转换成保留必要语义的短占位符。
func buildResourcePlaceholder(kind, label, target string) string {
	kind = NormalizeWhitespace(kind)
	if kind == "" {
		kind = "Resource"
	}
	label = NormalizeWhitespace(label)
	if label == "" {
		label = extractResourceName(target)
	}
	if label == "" {
		return "[" + kind + " filtered]"
	}
	return "[" + kind + ": " + label + "]"
}

// classifyResourceTarget maps one resource target into a short semantic kind used by placeholders.
// classifyResourceTarget 用于把资源目标映射成占位符里使用的短语义类别。
func classifyResourceTarget(target string) string {
	trimmed := strings.TrimSpace(strings.ToLower(target))
	if trimmed == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(trimmed, "data:image/"):
		return "Image"
	case strings.HasPrefix(trimmed, "data:video/"):
		return "Video"
	case strings.HasPrefix(trimmed, "data:audio/"):
		return "Audio"
	case strings.HasPrefix(trimmed, "data:"):
		return "Attachment"
	case strings.HasPrefix(trimmed, "blob:"):
		if ext := resourceExtension(trimmed); ext != "" {
			break
		}
		return "Attachment"
	case strings.HasPrefix(trimmed, "file:"):
		if ext := resourceExtension(trimmed); ext != "" {
			break
		}
		return "Attachment"
	}
	ext := resourceExtension(trimmed)
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".ico":
		return "Image"
	case ".mp4", ".mov", ".avi", ".webm", ".mkv":
		return "Video"
	case ".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a":
		return "Audio"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".tgz", ".bz2", ".xz":
		return "Archive"
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".csv", ".txt", ".md":
		return "File"
	default:
		return ""
	}
}

// classifyHTMLTag resolves one HTML media tag into a semantic kind when the target URL alone is not enough.
// classifyHTMLTag 用于在目标地址不足以判断时，把 HTML 媒体标签解析成语义类别。
func classifyHTMLTag(tag, target string) string {
	if kind := classifyResourceTarget(target); kind != "" {
		return kind
	}
	switch tag {
	case "img":
		return "Image"
	case "video":
		return "Video"
	case "audio":
		return "Audio"
	case "source", "object", "embed", "iframe":
		return "Attachment"
	default:
		return "Resource"
	}
}

// extractResourceName tries to derive a short human-readable name from one resource target.
// extractResourceName 用于尝试从资源目标中提取简短可读的名称。
func extractResourceName(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(target), "data:") {
		return ""
	}
	if parsed, err := url.Parse(target); err == nil {
		base := path.Base(parsed.Path)
		if base != "." && base != "/" && base != "" {
			return NormalizeWhitespace(base)
		}
	}
	return ""
}

// resourceExtension extracts the lowercase file extension from one resource target.
// resourceExtension 用于从资源目标中提取小写文件扩展名。
func resourceExtension(target string) string {
	if parsed, err := url.Parse(target); err == nil {
		return strings.ToLower(path.Ext(parsed.Path))
	}
	return strings.ToLower(path.Ext(target))
}

// extractHTMLAttribute extracts one quoted HTML attribute value from a raw attribute string.
// extractHTMLAttribute 用于从原始 HTML 属性串中提取一个带引号的属性值。
func extractHTMLAttribute(attrs, name string) string {
	needle := strings.ToLower(strings.TrimSpace(name)) + "="
	lower := strings.ToLower(attrs)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(attrs[idx+len(needle):])
	if rest == "" {
		return ""
	}
	if rest[0] == '"' || rest[0] == '\'' {
		quote := rest[0]
		rest = rest[1:]
		end := strings.IndexByte(rest, quote)
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(rest[:end])
	}
	end := strings.IndexAny(rest, " \t\r\n>")
	if end < 0 {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// stripHTML drops raw HTML tags from one short snippet.
// stripHTML 用于从短文本片段中移除原始 HTML 标签。
func stripHTML(s string) string {
	return htmlAnyTagPattern.ReplaceAllString(s, " ")
}
