// text_handler.go implements the human-readable multiline text logger used by the local runtime.
// text_handler.go 用于实现本地运行时使用的人类可读多行文本日志格式器。
package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// multilineTextHandler renders one log record as a visually separated block so long prompts and JSON payloads stay readable during debugging.
// multilineTextHandler 用于把一条日志记录渲染成视觉上分隔清晰的区块，让调试时的长提示词和 JSON 载荷更易阅读。
type multilineTextHandler struct {
	writer io.Writer
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

// newMultilineTextHandler creates the custom text handler used for the default non-JSON runtime log format.
// newMultilineTextHandler 用于创建默认非 JSON 运行时日志格式所使用的自定义文本 handler。
func newMultilineTextHandler(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
	var level slog.Leveler = slog.LevelInfo
	if opts != nil && opts.Level != nil {
		level = opts.Level
	}
	return &multilineTextHandler{
		writer: w,
		level:  level,
		attrs:  nil,
		groups: nil,
		mu:     &sync.Mutex{},
	}
}

// Enabled reports whether the incoming record level should be emitted for the current handler configuration.
// Enabled 用于判断当前 handler 配置下是否应输出目标级别的日志记录。
func (h *multilineTextHandler) Enabled(_ context.Context, level slog.Level) bool {
	if h == nil || h.level == nil {
		return level >= slog.LevelInfo
	}
	return level >= h.level.Level()
}

// Handle writes one record as a separated block with TYPE/TIME headers followed by one field per line.
// Handle 用于把一条记录输出成独立区块，先写 TYPE/TIME 头，再逐行输出每个字段。
func (h *multilineTextHandler) Handle(_ context.Context, record slog.Record) error {
	if h == nil || h.writer == nil {
		return nil
	}

	// Flatten inherited attrs plus record attrs first so the rendering step can stay focused on formatting.
	// 先展开继承属性和记录属性，让后续渲染阶段只关注格式化本身。
	fields := make([]renderedField, 0, len(h.attrs)+record.NumAttrs())
	for _, attr := range h.attrs {
		fields = append(fields, h.flattenAttr(attr)...)
	}
	record.Attrs(func(attr slog.Attr) bool {
		fields = append(fields, h.flattenAttr(attr)...)
		return true
	})

	// Render the full block in memory before taking the shared write lock, so concurrent goroutines only serialize the final write.
	// 先在内存里渲染完整区块，再占用共享写锁，避免并发 goroutine 长时间阻塞在格式化阶段。
	var buf bytes.Buffer
	buf.WriteString("========================================\n")
	buf.WriteString(fmt.Sprintf("TYPE：\"%s\"\n", strings.ToUpper(record.Level.String())))
	buf.WriteString(fmt.Sprintf("TIME：\"%s\"\n", record.Time.Format(time.RFC3339Nano)))
	buf.WriteString(fmt.Sprintf("MSG：\"%s\"\n", strings.TrimSpace(record.Message)))
	for _, field := range fields {
		writeRenderedField(&buf, field)
	}
	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.writer.Write(buf.Bytes())
	return err
}

// WithAttrs returns a cloned handler that always includes the provided structured attrs before record-level attrs.
// WithAttrs 用于返回一个克隆后的 handler，让它在记录级字段之前始终输出指定结构化属性。
func (h *multilineTextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	cloned := h.clone()
	cloned.attrs = append(cloned.attrs, attrs...)
	return cloned
}

// WithGroup prefixes subsequent attrs so grouped structured logging still stays distinguishable in multiline output.
// WithGroup 用于给后续字段增加分组前缀，保证分组结构化日志在多行输出里仍然可区分。
func (h *multilineTextHandler) WithGroup(name string) slog.Handler {
	if strings.TrimSpace(name) == "" {
		return h
	}
	cloned := h.clone()
	cloned.groups = append(cloned.groups, strings.TrimSpace(name))
	return cloned
}

// clone copies the handler configuration while sharing the same writer lock so concurrent child loggers do not interleave output.
// clone 用于复制 handler 配置，并共享同一把写锁，避免并发子日志器交错输出。
func (h *multilineTextHandler) clone() *multilineTextHandler {
	if h == nil {
		return nil
	}
	cloned := *h
	cloned.attrs = append([]slog.Attr(nil), h.attrs...)
	cloned.groups = append([]string(nil), h.groups...)
	return &cloned
}

// renderedField stores one flattened field together with the display mode chosen for the multiline text output.
// renderedField 用于保存一条展开后的字段，以及该字段在多行文本输出里应采用的显示模式。
type renderedField struct {
	Key  string
	Mode renderedFieldMode
	Text string
}

// renderedFieldMode identifies whether one field should render as a quoted scalar, raw text, or formatted JSON block.
// renderedFieldMode 用于标记字段应按带引号标量、原始文本还是格式化 JSON 块来渲染。
type renderedFieldMode string

const (
	// renderedFieldModeScalar keeps short scalar values compact on a single line.
	// renderedFieldModeScalar 用于让短标量值保持单行紧凑显示。
	renderedFieldModeScalar renderedFieldMode = "scalar"

	// renderedFieldModeText expands long human-readable text without escape noise.
	// renderedFieldModeText 用于展开长文本，避免转义符干扰阅读。
	renderedFieldModeText renderedFieldMode = "text"

	// renderedFieldModeJSON expands JSON payloads into pretty-printed blocks.
	// renderedFieldModeJSON 用于把 JSON 载荷展开成格式化区块。
	renderedFieldModeJSON renderedFieldMode = "json"
)

// flattenAttr resolves groups and value kinds into one or more rendered fields suitable for final block output.
// flattenAttr 用于把属性的分组和值类型解析成一个或多个可直接输出的字段。
func (h *multilineTextHandler) flattenAttr(attr slog.Attr) []renderedField {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return nil
	}

	// Expand grouped attrs recursively so nested payloads still render one key per line with stable prefixes.
	// 递归展开 grouped attr，让嵌套载荷仍能以稳定前缀按“一键一行”输出。
	if attr.Value.Kind() == slog.KindGroup {
		fields := make([]renderedField, 0, len(attr.Value.Group()))
		groupPrefix := h.prefixedKey(attr.Key)
		for _, child := range attr.Value.Group() {
			fields = append(fields, h.flattenGroupedAttr(groupPrefix, child)...)
		}
		return fields
	}
	return []renderedField{{
		Key:  h.prefixedKey(attr.Key),
		Mode: detectRenderedFieldMode(attr.Value),
		Text: renderAttrValue(attr.Value),
	}}
}

// flattenGroupedAttr expands one child attr under an already resolved parent prefix.
// flattenGroupedAttr 用于展开已经解析出父级前缀后的子属性。
func (h *multilineTextHandler) flattenGroupedAttr(prefix string, attr slog.Attr) []renderedField {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return nil
	}
	key := prefix
	if strings.TrimSpace(attr.Key) != "" {
		key = prefix + "." + strings.TrimSpace(attr.Key)
	}
	if attr.Value.Kind() == slog.KindGroup {
		fields := make([]renderedField, 0, len(attr.Value.Group()))
		for _, child := range attr.Value.Group() {
			fields = append(fields, h.flattenGroupedAttr(key, child)...)
		}
		return fields
	}
	return []renderedField{{
		Key:  key,
		Mode: detectRenderedFieldMode(attr.Value),
		Text: renderAttrValue(attr.Value),
	}}
}

// prefixedKey applies handler-level group prefixes to one attr key.
// prefixedKey 用于把 handler 级别的 group 前缀应用到属性键上。
func (h *multilineTextHandler) prefixedKey(key string) string {
	key = strings.TrimSpace(key)
	if len(h.groups) == 0 {
		return key
	}
	parts := append(append([]string(nil), h.groups...), key)
	return strings.Join(parts, ".")
}

// detectRenderedFieldMode chooses the display mode that best preserves readability for the current value.
// detectRenderedFieldMode 用于选择最能保留当前值可读性的显示模式。
func detectRenderedFieldMode(value slog.Value) renderedFieldMode {
	value = value.Resolve()
	if value.Kind() == slog.KindAny {
		if errValue, ok := value.Any().(error); ok && errValue != nil {
			return detectRenderedTextMode(errValue.Error())
		}
	}
	if value.Kind() != slog.KindString {
		return renderedFieldModeScalar
	}
	return detectRenderedTextMode(value.String())
}

// renderAttrValue converts one slog value into the textual payload written by the multiline handler.
// renderAttrValue 用于把一条 slog 值转换成多行 handler 最终写出的文本载荷。
func renderAttrValue(value slog.Value) string {
	value = value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		text := value.String()
		if pretty, ok := tryPrettyJSON(text); ok {
			return pretty
		}
		return text
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindBool:
		if value.Bool() {
			return "true"
		}
		return "false"
	case slog.KindInt64:
		return fmt.Sprintf("%d", value.Int64())
	case slog.KindUint64:
		return fmt.Sprintf("%d", value.Uint64())
	case slog.KindFloat64:
		return fmt.Sprintf("%v", value.Float64())
	case slog.KindAny:
		anyValue := value.Any()
		if anyValue == nil {
			return "<nil>"
		}
		if errValue, ok := anyValue.(error); ok {
			return errValue.Error()
		}
		if body, err := json.MarshalIndent(anyValue, "", "  "); err == nil {
			trimmed := strings.TrimSpace(string(body))
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				return trimmed
			}
		}
		return fmt.Sprint(anyValue)
	default:
		return value.String()
	}
}

// detectRenderedTextMode classifies one already-materialized string payload so errors and regular strings share the same readability rules.
// detectRenderedTextMode 用于对已经物化成字符串的载荷做显示模式分类，让错误和值字符串共享同一套可读性规则。
func detectRenderedTextMode(raw string) renderedFieldMode {
	text := strings.TrimSpace(raw)
	if pretty, ok := tryPrettyJSON(text); ok && pretty != "" {
		return renderedFieldModeJSON
	}
	runeCount := utf8.RuneCountInString(text)
	if strings.Contains(text, "\n") || strings.Contains(text, "\r") {
		return renderedFieldModeText
	}
	if containsCJK(text) && runeCount > 24 {
		return renderedFieldModeText
	}
	if strings.ContainsAny(text, " \t") && runeCount > 48 {
		return renderedFieldModeText
	}
	return renderedFieldModeScalar
}

// writeRenderedField renders one flattened field according to its chosen mode.
// writeRenderedField 用于根据字段选定的模式把它写入最终日志区块。
func writeRenderedField(buf *bytes.Buffer, field renderedField) {
	switch field.Mode {
	case renderedFieldModeJSON:
		buf.WriteString(fmt.Sprintf("JSON(%s)：\n", field.Key))
		buf.WriteString(strings.TrimRight(field.Text, "\n"))
		buf.WriteByte('\n')
	case renderedFieldModeText:
		buf.WriteString(fmt.Sprintf("TEXT(%s)：\n", field.Key))
		buf.WriteString(strings.TrimRight(field.Text, "\n"))
		buf.WriteByte('\n')
	default:
		if shouldQuoteScalar(field.Text) {
			buf.WriteString(fmt.Sprintf("%s：\"%s\"\n", field.Key, escapeScalarQuotes(field.Text)))
			return
		}
		buf.WriteString(fmt.Sprintf("%s：%s\n", field.Key, field.Text))
	}
}

// tryPrettyJSON detects raw JSON strings and returns a pretty-printed form when the payload is an object or array.
// tryPrettyJSON 用于识别原始 JSON 字符串，并在载荷是对象或数组时返回格式化后的内容。
func tryPrettyJSON(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	switch payload.(type) {
	case map[string]any, []any:
		body, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", false
		}
		return string(body), true
	default:
		return "", false
	}
}

// shouldQuoteScalar keeps plain string scalars quoted while leaving numbers, booleans, and nil-like markers compact.
// shouldQuoteScalar 用于让普通字符串标量保持带引号显示，而数字、布尔和 nil 标记则保持紧凑。
func shouldQuoteScalar(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	switch text {
	case "true", "false", "<nil>":
		return false
	}
	for _, r := range text {
		if (r < '0' || r > '9') && r != '.' && r != '-' {
			return true
		}
	}
	return false
}

// escapeScalarQuotes keeps scalar string lines valid when the value itself contains double quotes.
// escapeScalarQuotes 用于在标量字符串自身包含双引号时保持输出行结构稳定。
func escapeScalarQuotes(text string) string {
	return strings.ReplaceAll(text, "\"", "\\\"")
}

// containsCJK detects whether a string includes CJK runes, which helps classify long Chinese/Japanese/Korean sentences as raw text blocks.
// containsCJK 用于检测字符串是否包含中日韩文字，便于把较长的中文/日文/韩文句子归类为原始文本块。
func containsCJK(text string) bool {
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			return true
		case r >= 0x3400 && r <= 0x4DBF:
			return true
		case r >= 0x3040 && r <= 0x30FF:
			return true
		case r >= 0xAC00 && r <= 0xD7AF:
			return true
		}
	}
	return false
}
