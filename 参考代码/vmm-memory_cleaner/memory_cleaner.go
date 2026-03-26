package memorycleaner

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Config controls how aggressively the cleaner compresses machine text.
// The defaults are intentionally conservative: missing some machine text is
// acceptable, but deleting human-authored text is not.
type Config struct {
	ShortTextImmunityRunes int
	Fenced                 Rules
	Plain                  Rules
}

// Rules define thresholds for compressing a contiguous block.
type Rules struct {
	MinLines        int
	MinRunes        int
	MaxHanRatio     float64
	HeadLines       int
	TailLines       int
	MinOmittedLines int
}

// DefaultConfig is tuned for conversational memory cleaning.
func DefaultConfig() Config {
	return Config{
		ShortTextImmunityRunes: 100,
		Fenced: Rules{
			MinLines:        10,
			MinRunes:        300,
			MaxHanRatio:     0.15,
			HeadLines:       3,
			TailLines:       2,
			MinOmittedLines: 4,
		},
		Plain: Rules{
			MinLines:        12,
			MinRunes:        400,
			MaxHanRatio:     0.15,
			HeadLines:       3,
			TailLines:       2,
			MinOmittedLines: 4,
		},
	}
}

// CleanMemoryText is the package entrypoint. It preserves human intent while
// compressing long machine text such as source code, JSON blobs, stack traces,
// and server logs.
func CleanMemoryText(raw string) string {
	return CleanMemoryTextWithConfig(raw, DefaultConfig())
}

// CleanMemoryTextWithConfig allows callers to customize thresholds.
func CleanMemoryTextWithConfig(raw string, cfg Config) string {
	if utf8.RuneCountInString(raw) < cfg.ShortTextImmunityRunes {
		return raw
	}

	segments := splitFenceSegments(raw)
	if len(segments) == 0 {
		return raw
	}

	var b strings.Builder
	b.Grow(len(raw))
	for _, seg := range segments {
		if !seg.Fenced {
			b.WriteString(processPlainText(seg.Content, cfg.Plain))
			continue
		}

		// Do not treat fenced text as disposable by marker alone.
		// First apply short-block immunity and Chinese-density protection.
		if keepWholeBlock(seg.Content, cfg.Fenced) {
			b.WriteString(seg.OpenFence)
			b.WriteString(seg.Content)
			b.WriteString(seg.CloseFence)
			continue
		}

		b.WriteString(seg.OpenFence)
		b.WriteString(processPlainText(seg.Content, cfg.Fenced))
		b.WriteString(seg.CloseFence)
	}
	return b.String()
}

type fenceSegment struct {
	Fenced     bool
	OpenFence  string
	Content    string
	CloseFence string
}

type lineKind uint8

const (
	lineBlank lineKind = iota
	lineNatural
	lineMachine
	lineNeutral
)

type lineInfo struct {
	raw           string
	text          string
	kind          lineKind
	runes         int
	han           int
	symbols       int
	naturalWords  int
	stackSignal   bool
	logSignal     bool
	jsonSignal    bool
	codeSignal    bool
	strongMachine bool
}

type blockStats struct {
	lineCount    int
	nonBlank     int
	runeCount    int
	hanCount     int
	symbolCount  int
	naturalLines int
	machineLines int
	blankLines   int
	stackSignals int
	logSignals   int
	jsonSignals  int
	codeSignals  int
}

type compression struct {
	ok   bool
	text string
}

func splitFenceSegments(s string) []fenceSegment {
	lines := splitLinesKeepEnd(s)
	if len(lines) == 0 {
		return []fenceSegment{{Content: s}}
	}

	var segments []fenceSegment
	var plain strings.Builder

	flushPlain := func() {
		if plain.Len() == 0 {
			return
		}
		segments = append(segments, fenceSegment{Content: plain.String()})
		plain.Reset()
	}

	i := 0
	for i < len(lines) {
		if !isFenceLine(lines[i]) {
			plain.WriteString(lines[i])
			i++
			continue
		}

		j := i + 1
		for j < len(lines) && !isFenceLine(lines[j]) {
			j++
		}
		if j >= len(lines) {
			// Unclosed fence: keep as plain text rather than risking corruption.
			plain.WriteString(lines[i])
			i++
			continue
		}

		flushPlain()
		var content strings.Builder
		for k := i + 1; k < j; k++ {
			content.WriteString(lines[k])
		}
		segments = append(segments, fenceSegment{
			Fenced:     true,
			OpenFence:  lines[i],
			Content:    content.String(),
			CloseFence: lines[j],
		})
		i = j + 1
	}
	flushPlain()
	if len(segments) == 0 {
		return []fenceSegment{{Content: s}}
	}
	return segments
}

func isFenceLine(line string) bool {
	trimmed := strings.TrimSpace(stripLineEnding(line))
	return strings.HasPrefix(trimmed, "```")
}

func splitLinesKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	lines := make([]string, 0, strings.Count(s, "\n")+1)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func stripLineEnding(s string) string {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s
}

func keepWholeBlock(content string, rules Rules) bool {
	lines := splitLinesKeepEnd(content)
	if len(lines) == 0 {
		return true
	}
	stats := analyzeBlock(lines)
	if stats.lineCount < rules.MinLines || stats.runeCount < rules.MinRunes {
		return true
	}
	if stats.runeCount == 0 {
		return true
	}
	return float64(stats.hanCount)/float64(stats.runeCount) >= rules.MaxHanRatio
}

func processPlainText(s string, rules Rules) string {
	lines := splitLinesKeepEnd(s)
	if len(lines) == 0 {
		return s
	}

	infos := make([]lineInfo, len(lines))
	for i, line := range lines {
		infos[i] = classifyLine(line)
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(lines); {
		if infos[i].kind != lineMachine {
			b.WriteString(lines[i])
			i++
			continue
		}

		start := i
		end := i + 1
		for end < len(lines) {
			if infos[end].kind == lineNatural {
				break
			}
			end++
		}

		cutEnd := end
		for cutEnd > start && infos[cutEnd-1].kind == lineBlank {
			cutEnd--
		}

		run := infos[start:cutEnd]
		cmp := compressRun(run, rules)
		if !cmp.ok {
			for j := start; j < end; j++ {
				b.WriteString(lines[j])
			}
			i = end
			continue
		}

		b.WriteString(cmp.text)
		for j := cutEnd; j < end; j++ {
			b.WriteString(lines[j])
		}
		i = end
	}

	return b.String()
}

func compressRun(run []lineInfo, rules Rules) compression {
	if len(run) == 0 {
		return compression{}
	}

	stats := analyzeInfos(run)
	if !shouldCompress(stats, rules) {
		return compression{}
	}

	omitted := stats.lineCount - rules.HeadLines - rules.TailLines
	if omitted < rules.MinOmittedLines {
		return compression{}
	}

	newline := detectNewline(run)
	label := inferLabel(stats)

	var b strings.Builder
	for i := 0; i < rules.HeadLines && i < len(run); i++ {
		b.WriteString(run[i].raw)
	}
	if rules.HeadLines > 0 && len(run) > 0 && !strings.HasSuffix(run[minInt(rules.HeadLines, len(run))-1].raw, "\n") {
		b.WriteString(newline)
	}
	b.WriteString("... [中间 ")
	b.WriteString(itoa(omitted))
	b.WriteString(" 行")
	b.WriteString(label)
	b.WriteString("已按策略折叠，节省上下文] ...")
	b.WriteString(newline)
	tailStart := len(run) - rules.TailLines
	if tailStart < rules.HeadLines {
		tailStart = rules.HeadLines
	}
	for i := tailStart; i < len(run); i++ {
		b.WriteString(run[i].raw)
	}
	return compression{ok: true, text: b.String()}
}

func shouldCompress(stats blockStats, rules Rules) bool {
	if stats.lineCount < rules.MinLines || stats.runeCount < rules.MinRunes {
		return false
	}
	if stats.nonBlank == 0 || stats.lineCount <= rules.HeadLines+rules.TailLines+rules.MinOmittedLines {
		return false
	}

	hanRatio := 0.0
	if stats.runeCount > 0 {
		hanRatio = float64(stats.hanCount) / float64(stats.runeCount)
	}
	if hanRatio >= rules.MaxHanRatio {
		return false
	}

	machineRatio := float64(stats.machineLines) / float64(stats.nonBlank)
	naturalRatio := float64(stats.naturalLines) / float64(stats.nonBlank)
	symbolRatio := float64(stats.symbolCount) / float64(maxInt(stats.runeCount, 1))

	if naturalRatio >= 0.35 {
		return false
	}
	if naturalRatio > 0.20 && stats.machineLines < stats.naturalLines*3 {
		return false
	}

	strongSignals := 0
	if stats.stackSignals >= 2 {
		strongSignals++
	}
	if stats.logSignals >= 3 {
		strongSignals++
	}
	if stats.jsonSignals >= 4 {
		strongSignals++
	}
	if stats.codeSignals >= maxInt(4, stats.lineCount/3) {
		strongSignals++
	}

	if machineRatio >= 0.80 {
		return true
	}
	if machineRatio >= 0.65 && symbolRatio >= 0.18 && strongSignals >= 1 {
		return true
	}
	if stats.stackSignals >= 2 && machineRatio >= 0.55 {
		return true
	}
	if stats.logSignals >= 4 && machineRatio >= 0.55 {
		return true
	}
	if stats.jsonSignals >= 6 && machineRatio >= 0.50 {
		return true
	}
	if stats.codeSignals >= maxInt(6, stats.lineCount/2) && machineRatio >= 0.60 && symbolRatio >= 0.16 {
		return true
	}
	return false
}

func analyzeBlock(lines []string) blockStats {
	infos := make([]lineInfo, len(lines))
	for i, line := range lines {
		infos[i] = classifyLine(line)
	}
	return analyzeInfos(infos)
}

func analyzeInfos(infos []lineInfo) blockStats {
	var stats blockStats
	stats.lineCount = len(infos)
	for _, info := range infos {
		stats.runeCount += info.runes
		stats.hanCount += info.han
		stats.symbolCount += info.symbols
		switch info.kind {
		case lineBlank:
			stats.blankLines++
		case lineNatural:
			stats.nonBlank++
			stats.naturalLines++
		case lineMachine:
			stats.nonBlank++
			stats.machineLines++
		case lineNeutral:
			stats.nonBlank++
		}
		if info.stackSignal {
			stats.stackSignals++
		}
		if info.logSignal {
			stats.logSignals++
		}
		if info.jsonSignal {
			stats.jsonSignals++
		}
		if info.codeSignal {
			stats.codeSignals++
		}
	}
	return stats
}

func classifyLine(raw string) lineInfo {
	text := stripLineEnding(raw)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return lineInfo{raw: raw, text: text, kind: lineBlank}
	}

	runes, han, symbols, asciiLetters, digits := countChars(trimmed)
	symbolRatio := float64(symbols) / float64(maxInt(runes, 1))
	hanRatio := float64(han) / float64(maxInt(runes, 1))
	words := countNaturalWords(trimmed)
	indent := hasIndent(text)

	stackSignal := hasStackSignal(trimmed)
	logSignal := hasLogSignal(trimmed)
	jsonSignal := hasJSONSignal(trimmed)
	codeSignal := hasCodeSignal(trimmed, indent, hanRatio)

	strongMachine := stackSignal || logSignal || jsonSignal || codeSignal

	naturalChinese := han >= 4 || (hanRatio >= 0.15 && runes >= 20)
	naturalEnglish := words >= 6 && symbolRatio < 0.18 && !strongMachine
	naturalSentence := (strings.ContainsAny(trimmed, "。！？?!") && (han >= 2 || words >= 5)) ||
		(strings.HasSuffix(trimmed, ".") && words >= 8 && !strongMachine)

	if naturalChinese || naturalEnglish || naturalSentence {
		return lineInfo{
			raw:          raw,
			text:         text,
			kind:         lineNatural,
			runes:        runes,
			han:          han,
			symbols:      symbols,
			naturalWords: words,
			stackSignal:  stackSignal,
			logSignal:    logSignal,
			jsonSignal:   jsonSignal,
			codeSignal:   codeSignal,
		}
	}

	lowHan := hanRatio < 0.08
	heavySymbols := symbolRatio >= 0.30 && runes >= 24
	codeishDensity := symbolRatio >= 0.18 && (strings.ContainsAny(trimmed, "{}[]();=<>") || indent)
	machine := false
	switch {
	case stackSignal:
		machine = true
	case logSignal && lowHan:
		machine = true
	case jsonSignal && lowHan:
		machine = true
	case codeSignal && lowHan && (codeishDensity || heavySymbols || indent):
		machine = true
	case lowHan && heavySymbols && (indent || digits > 0 || asciiLetters > 0):
		machine = true
	}

	kind := lineNeutral
	if machine {
		kind = lineMachine
	}

	return lineInfo{
		raw:           raw,
		text:          text,
		kind:          kind,
		runes:         runes,
		han:           han,
		symbols:       symbols,
		naturalWords:  words,
		stackSignal:   stackSignal,
		logSignal:     logSignal,
		jsonSignal:    jsonSignal,
		codeSignal:    codeSignal,
		strongMachine: strongMachine,
	}
}

func countChars(s string) (runes, han, symbols, asciiLetters, digits int) {
	for _, r := range s {
		runes++
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r <= unicode.MaxASCII && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')):
			asciiLetters++
		case r <= unicode.MaxASCII && r >= '0' && r <= '9':
			digits++
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			symbols++
		}
	}
	return
}

func countNaturalWords(s string) int {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		if unicode.IsSpace(r) {
			return true
		}
		switch r {
		case ',', '.', ';', ':', '!', '?', '，', '。', '；', '：', '！', '？', '(', ')', '[', ']', '{', '}', '<', '>', '/', '\\', '"', '\'', '`', '|':
			return true
		}
		return false
	})

	count := 0
	for _, f := range fields {
		letters := 0
		for _, r := range f {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters >= 2 {
			count++
		}
	}
	return count
}

func hasIndent(s string) bool {
	return strings.HasPrefix(s, "    ") || strings.HasPrefix(s, "\t") || strings.HasPrefix(s, "  ")
}

func hasStackSignal(s string) bool {
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "goroutine ") ||
		strings.HasPrefix(lower, "panic:") ||
		strings.HasPrefix(lower, "fatal error:") ||
		strings.HasPrefix(lower, "traceback") ||
		strings.HasPrefix(lower, "caused by:") ||
		strings.HasPrefix(lower, "runtime error:") {
		return true
	}
	if strings.Contains(lower, "exception") && (strings.Contains(s, ".") || strings.Contains(s, ":")) {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(s), "at ") && strings.Contains(s, "(") && strings.Contains(s, ":") {
		return true
	}
	if strings.Contains(lower, ".go:") || strings.Contains(lower, ".java:") || strings.Contains(lower, ".py:") || strings.Contains(lower, ".ts:") || strings.Contains(lower, ".js:") {
		return true
	}
	trimmed := strings.TrimSpace(s)
	if !strings.Contains(trimmed, " ") && strings.Contains(trimmed, ".") && (strings.HasSuffix(trimmed, "()") || strings.HasSuffix(trimmed, "(...)") || strings.HasSuffix(trimmed, "]:")) {
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(s), "File ") && strings.Contains(s, ", line ") {
		return true
	}
	return false
}

func hasLogSignal(s string) bool {
	trimmed := strings.TrimSpace(s)
	upper := strings.ToUpper(trimmed)

	if looksLikeTimestampPrefix(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, "[") && (strings.Contains(upper, "INFO") || strings.Contains(upper, "WARN") || strings.Contains(upper, "ERROR") || strings.Contains(upper, "DEBUG")) {
		return true
	}
	if strings.HasPrefix(upper, "INFO ") || strings.HasPrefix(upper, "WARN ") || strings.HasPrefix(upper, "ERROR ") || strings.HasPrefix(upper, "DEBUG ") {
		return true
	}
	if strings.Contains(upper, " LEVEL=") || strings.Contains(upper, " MSG=") || strings.Contains(upper, " TRACE=") {
		return true
	}
	return false
}

func looksLikeTimestampPrefix(s string) bool {
	if len(s) < 19 {
		return false
	}
	for _, idx := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if idx >= len(s) || s[idx] < '0' || s[idx] > '9' {
			return false
		}
	}
	return (s[4] == '-' || s[4] == '/') &&
		(s[7] == '-' || s[7] == '/') &&
		(s[10] == 'T' || s[10] == ' ') &&
		s[13] == ':' && s[16] == ':'
}

func hasJSONSignal(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "{" || trimmed == "}" || trimmed == "[" || trimmed == "]" || trimmed == "}," || trimmed == "]," {
		return true
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	if strings.HasPrefix(trimmed, "\"") && strings.Contains(trimmed, "\":") {
		return true
	}
	if strings.HasPrefix(trimmed, "'") && strings.Contains(trimmed, "':") {
		return true
	}
	return false
}

func hasCodeSignal(s string, indent bool, hanRatio float64) bool {
	if hanRatio >= 0.15 {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(s))
	structural := strings.ContainsAny(s, "{}[]();") || strings.Contains(s, ":=") || strings.Contains(s, "=>") || strings.Contains(s, "==") || strings.Contains(s, "!=") || strings.Contains(s, " <= ") || strings.Contains(s, ">=")
	if strings.HasSuffix(strings.TrimSpace(s), "{") || strings.HasSuffix(strings.TrimSpace(s), "};") {
		structural = true
	}

	keywords := []string{
		"package ", "import ", "func ", "return ", "if ", "for ", "switch ", "case ", "var ", "const ", "type ",
		"class ", "public ", "private ", "protected ", "static ", "void ", "new ", "def ", "let ", "const ",
		"select ", "insert ", "update ", "delete ", "from ", "where ", "try ", "catch ", "finally ", "throw ",
	}

	hit := 0
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			hit++
		}
	}

	if hit >= 2 && structural {
		return true
	}
	if hit >= 1 && (structural || indent) {
		return true
	}
	if structural && indent && (strings.Contains(lower, "(") || strings.Contains(lower, "[") || strings.Contains(lower, ".")) {
		return true
	}
	return false
}

func inferLabel(stats blockStats) string {
	switch {
	case stats.stackSignals >= maxInt(stats.logSignals, maxInt(stats.jsonSignals, stats.codeSignals)) && stats.stackSignals >= 1:
		return "超长报错日志"
	case stats.jsonSignals >= maxInt(stats.logSignals, stats.codeSignals) && stats.jsonSignals >= 2:
		return "超长结构化数据"
	case stats.logSignals >= maxInt(stats.jsonSignals, stats.codeSignals) && stats.logSignals >= 2:
		return "超长运行日志"
	case stats.codeSignals >= 1:
		return "超长代码"
	default:
		return "超长机器文本"
	}
}

func detectNewline(run []lineInfo) string {
	for _, info := range run {
		if strings.HasSuffix(info.raw, "\r\n") {
			return "\r\n"
		}
		if strings.HasSuffix(info.raw, "\n") {
			return "\n"
		}
	}
	return "\n"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}