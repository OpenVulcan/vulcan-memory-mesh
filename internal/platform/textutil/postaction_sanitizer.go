// postaction_sanitizer.go implements the post-action specific text sanitizer that combines media stripping, machine-text compression, and token-budget clipping.
// postaction_sanitizer.go 用于实现 post-action 专用文本清洗器，组合媒体清理、机器文本压缩和 token 预算裁剪。
package textutil

import (
	"strings"
)

// PostActionTextSanitizer combines media stripping, machine-text compression, and token budgeting for new post-action payloads before persistence.
// PostActionTextSanitizer 用于在新的 post-action 载荷持久化前组合执行媒体清理、机器文本压缩和 token 预算裁剪。
type PostActionTextSanitizer struct {
	cleanerCfg MemoryCleanerConfig
	budgetCfg  TokenBudgetConfig
}

// NewPostActionTextSanitizer creates the default sanitizer used by the gRPC post-action entry.
// NewPostActionTextSanitizer 用于创建 gRPC post-action 入口默认使用的清洗器。
func NewPostActionTextSanitizer() *PostActionTextSanitizer {
	return &PostActionTextSanitizer{
		cleanerCfg: DefaultMemoryCleanerConfig(),
		budgetCfg:  DefaultTokenBudgetConfig(),
	}
}

// Sanitize runs the storage-oriented cleaning pipeline on one post-action text fragment.
// Sanitize 用于对单条 post-action 文本片段执行面向存储的清洗流水线。
func (s *PostActionTextSanitizer) Sanitize(raw string) string {
	if s == nil {
		s = NewPostActionTextSanitizer()
	}

	// Strip media payloads and thought tags first while preserving line structure for the machine-text cleaner.
	// 先去除媒体载荷和思维标签，同时保留行结构，方便后续机器文本清洗器判断。
	out := CleanConversationTextForMemory(raw)

	// Fold long code, stack traces, logs, and structured machine text before estimating token budget.
	// 在估算 token 预算前先折叠长代码、堆栈、日志和结构化机器文本。
	out = CleanMemoryTextWithConfig(out, s.cleanerCfg)
	out = NormalizeMemoryWhitespace(out)

	// Clip extremely dense or oversized payloads so persistence stays bounded even after media stripping.
	// 对极高密度或超大载荷做预算裁剪，确保媒体清理后的文本仍然保持可控大小。
	out = EnforceTokenBudget(out, s.budgetCfg)
	return NormalizeMemoryWhitespace(out)
}

// CleanConversationTextForMemory strips noisy media and `<think>` style content while preserving useful line boundaries.
// CleanConversationTextForMemory 用于移除嘈杂媒体和 `<think>` 一类内容，同时保留有用的行边界。
func CleanConversationTextForMemory(s string) string {
	out := s
	out = markdownImagePattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := markdownImagePattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[图片已过滤]"
		}
		alt := NormalizeMemoryWhitespace(sub[1])
		target := strings.TrimSpace(sub[2])
		kind := classifyResourceTarget(target)
		if kind == "" {
			kind = "图片"
		}
		return buildResourcePlaceholder(kind, alt, target)
	})
	out = markdownLinkPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := markdownLinkPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		label := NormalizeMemoryWhitespace(sub[1])
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
			return "[链接已过滤]"
		}
		target := extractHTMLAttribute(sub[1], "href")
		kind := classifyResourceTarget(target)
		if kind == "" {
			return NormalizeMemoryWhitespace(stripHTML(sub[2]))
		}
		return buildResourcePlaceholder(kind, NormalizeMemoryWhitespace(stripHTML(sub[2])), target)
	})
	out = htmlVoidMediaPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlVoidMediaPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[媒体已过滤]"
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
		return buildResourcePlaceholder(classifyHTMLTag(tag, target), NormalizeMemoryWhitespace(alt), target)
	})
	out = htmlBlockMediaPattern.ReplaceAllStringFunc(out, func(match string) string {
		sub := htmlBlockMediaPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return "[媒体已过滤]"
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
		return buildResourcePlaceholder(classifyHTMLTag(tag, target), NormalizeMemoryWhitespace(alt), target)
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
	out = stripThoughtTagsPreserveLayout(out)
	return NormalizeMemoryWhitespace(out)
}

// NormalizeMemoryWhitespace trims line edges and collapses excessive blank lines without flattening the whole text into a single line.
// NormalizeMemoryWhitespace 用于裁剪每行空白并折叠过多空行，同时避免把整段文本压平成单行。
func NormalizeMemoryWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	blankCount := 0
	wroteLine := false
	justWroteBlankBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount > 1 || !wroteLine || justWroteBlankBlock {
				continue
			}
			b.WriteString("\n\n")
			justWroteBlankBlock = true
			continue
		}
		blankCount = 0
		if wroteLine && !justWroteBlankBlock {
			b.WriteByte('\n')
		}
		b.WriteString(trimmed)
		wroteLine = true
		justWroteBlankBlock = false
	}
	return strings.TrimSpace(b.String())
}

// stripThoughtTagsPreserveLayout removes internal reasoning tags but keeps surrounding newlines so later folding can still detect block boundaries.
// stripThoughtTagsPreserveLayout 用于移除内部思维标签，同时保留周围换行，方便后续折叠阶段识别文本块边界。
func stripThoughtTagsPreserveLayout(s string) string {
	out := s
	for _, re := range thoughtPatterns {
		out = re.ReplaceAllString(out, "\n")
	}
	return out
}
