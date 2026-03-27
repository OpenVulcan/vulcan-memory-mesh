// memory_cleaner.go implements machine-text compression helpers used before post-action content enters long-term storage.
// memory_cleaner.go 用于实现机器文本压缩助手，在 post-action 内容进入长期存储前执行清理。
package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MemoryCleanerConfig controls how aggressively the cleaner folds code, logs, and structured machine text before persistence.
// MemoryCleanerConfig 用于控制清洗器在持久化前折叠代码、日志和结构化机器文本的激进程度。
type MemoryCleanerConfig struct {
	ShortTextImmunityRunes int
	MaxLineRunes           int
	LongLineHeadRunes      int
	LongLineTailRunes      int
	Fenced                 MemoryCleanerRules
	Plain                  MemoryCleanerRules
}

// MemoryCleanerRules describes when one contiguous block should be folded into a shorter placeholder form.
// MemoryCleanerRules 用于描述一段连续文本在什么条件下会被折叠为更短的占位形式。
type MemoryCleanerRules struct {
	MinLines        int
	MinRunes        int
	MaxHanRatio     float64
	HeadLines       int
	TailLines       int
	MinOmittedLines int
}

// DefaultMemoryCleanerConfig returns the conservative defaults used by the post-action storage sanitizer.
// DefaultMemoryCleanerConfig 用于返回 post-action 存储清洗器使用的保守默认配置。
func DefaultMemoryCleanerConfig() MemoryCleanerConfig {
	return MemoryCleanerConfig{
		ShortTextImmunityRunes: 100,
		MaxLineRunes:           1000,
		LongLineHeadRunes:      240,
		LongLineTailRunes:      120,
		Fenced: MemoryCleanerRules{
			MinLines:        10,
			MinRunes:        300,
			MaxHanRatio:     0.15,
			HeadLines:       3,
			TailLines:       2,
			MinOmittedLines: 4,
		},
		Plain: MemoryCleanerRules{
			MinLines:        12,
			MinRunes:        400,
			MaxHanRatio:     0.15,
			HeadLines:       3,
			TailLines:       2,
			MinOmittedLines: 4,
		},
	}
}

// CleanMemoryText applies the default machine-text compression strategy to one raw post-action text fragment.
// CleanMemoryText 用于对单条原始 post-action 文本片段应用默认的机器文本压缩策略。
func CleanMemoryText(raw string) string {
	return CleanMemoryTextWithConfig(raw, DefaultMemoryCleanerConfig())
}

// CleanMemoryTextWithConfig applies one caller-provided compression profile while preserving human-authored prose whenever possible.
// CleanMemoryTextWithConfig 用于在尽量保留人工撰写语义的前提下，按调用方提供的配置执行文本压缩。
func CleanMemoryTextWithConfig(raw string, cfg MemoryCleanerConfig) string {
	if utf8.RuneCountInString(raw) < cfg.ShortTextImmunityRunes {
		return raw
	}
	segments := splitFenceSegments(raw)
	if len(segments) == 0 {
		return raw
	}

	// Process fenced and plain segments separately so long code or log blocks can be folded without rewriting nearby prose.
	// 对 fenced 片段和普通片段分别处理，避免长代码或日志块在折叠时误伤周围自然语言。
	var b strings.Builder
	b.Grow(len(raw))
	for _, seg := range segments {
		if !seg.Fenced {
			b.WriteString(processPlainText(seg.Content, cfg, cfg.Plain))
			continue
		}
		if keepWholeBlock(seg.Content, cfg, cfg.Fenced) {
			b.WriteString(seg.OpenFence)
			b.WriteString(seg.Content)
			b.WriteString(seg.CloseFence)
			continue
		}
		b.WriteString(seg.OpenFence)
		b.WriteString(processPlainText(seg.Content, cfg, cfg.Fenced))
		b.WriteString(seg.CloseFence)
	}
	return b.String()
}

// fenceSegment stores one plain-text or fenced block produced by the fence splitter.
// fenceSegment 用于保存 fence 切分器产出的一段普通文本或 fenced 代码块。
type fenceSegment struct {
	Fenced     bool
	OpenFence  string
	Content    string
	CloseFence string
}

// lineKind classifies one line by how machine-like it looks during block folding.
// lineKind 用于表示一行文本在折叠阶段看起来更像哪种类型。
type lineKind uint8

const (
	lineBlank lineKind = iota
	lineNatural
	lineMachine
	lineNeutral
)

// lineInfo captures the features extracted from one line for machine-text classification and folding.
// lineInfo 用于保存单行文本在机器文本分类和折叠阶段提取出的特征。
type lineInfo struct {
	raw            string
	text           string
	kind           lineKind
	runes          int
	han            int
	symbols        int
	asciiLetters   int
	digits         int
	spaces         int
	naturalWords   int
	stackSignal    bool
	logSignal      bool
	jsonSignal     bool
	codeSignal     bool
	anchorSignal   bool
	horizontalClip bool
	strongMachine  bool
}

// blockStats aggregates the line-level features of one contiguous machine-like block.
// blockStats 用于聚合一段连续机器文本块的行级特征。
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
	anchorLines  int
}

// compression stores the result of one folding attempt on a contiguous machine-text run.
// compression 用于保存一次连续机器文本折叠尝试的结果。
type compression struct {
	ok   bool
	text string
}

// splitFenceSegments separates fenced blocks from plain text so the cleaner can fold them independently.
// splitFenceSegments 用于把 fenced 块和普通文本拆开，方便清洗器分别折叠。
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

	for i := 0; i < len(lines); {
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

// isFenceLine checks whether one line starts a fenced code block.
// isFenceLine 用于判断一行是否是 fenced 代码块的起始标记。
func isFenceLine(line string) bool {
	trimmed := strings.TrimSpace(stripLineEnding(line))
	return strings.HasPrefix(trimmed, "```")
}

// splitLinesKeepEnd keeps original line endings so folded output can preserve the caller's layout style.
// splitLinesKeepEnd 用于保留原始换行符，确保折叠结果仍能延续调用方原有布局风格。
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

// stripLineEnding removes one trailing newline marker when the classifier only needs the textual content.
// stripLineEnding 用于在分类器只关注正文时移除尾部换行标记。
func stripLineEnding(s string) string {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s
}

// keepWholeBlock decides whether one fenced block is still small enough to keep untouched.
// keepWholeBlock 用于判断一个 fenced 块是否足够小，从而可以原样保留。
func keepWholeBlock(content string, cfg MemoryCleanerConfig, rules MemoryCleanerRules) bool {
	lines := splitLinesKeepEnd(content)
	if len(lines) == 0 {
		return true
	}

	infos := make([]lineInfo, len(lines))
	for i, line := range lines {
		infos[i] = classifyLine(line, cfg)
		if infos[i].horizontalClip {
			return false
		}
	}
	stats := analyzeInfos(infos)
	return stats.lineCount < rules.MinLines && stats.runeCount < rules.MinRunes
}

// processPlainText walks plain text line by line so machine-like runs can be folded without rewriting natural language.
// processPlainText 用于逐行处理普通文本，让机器文本连续段在不改写自然语言的情况下被折叠。
func processPlainText(s string, cfg MemoryCleanerConfig, rules MemoryCleanerRules) string {
	lines := splitLinesKeepEnd(s)
	if len(lines) == 0 {
		return s
	}

	infos := make([]lineInfo, len(lines))
	for i, line := range lines {
		infos[i] = classifyLine(line, cfg)
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(lines); {
		if infos[i].kind != lineMachine {
			b.WriteString(renderLine(lines[i], infos[i], cfg))
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
				b.WriteString(renderLine(lines[j], infos[j], cfg))
			}
			i = end
			continue
		}

		b.WriteString(cmp.text)
		for j := cutEnd; j < end; j++ {
			b.WriteString(renderLine(lines[j], infos[j], cfg))
		}
		i = end
	}
	return b.String()
}

// renderLine applies line-level horizontal truncation for extremely long machine-only lines.
// renderLine 用于对超长机器行执行单行级的水平截断。
func renderLine(raw string, info lineInfo, cfg MemoryCleanerConfig) string {
	if info.horizontalClip && !info.anchorSignal {
		return truncateLongLine(raw, cfg)
	}
	return raw
}

// compressRun folds one contiguous machine-text run into head/tail excerpts plus omission markers when it is worth compressing.
// compressRun 用于在值得压缩时，把一段连续机器文本折叠为头尾摘录加省略标记。
func compressRun(run []lineInfo, rules MemoryCleanerRules) compression {
	if len(run) == 0 {
		return compression{}
	}
	stats := analyzeInfos(run)
	if !shouldCompress(stats, rules) {
		return compression{}
	}

	headEnd := minInt(rules.HeadLines, len(run))
	tailStart := len(run) - rules.TailLines
	if tailStart < headEnd {
		tailStart = headEnd
	}
	if tailStart > len(run) {
		tailStart = len(run)
	}

	totalOmitted := 0
	for i := headEnd; i < tailStart; i++ {
		if !run[i].anchorSignal {
			totalOmitted++
		}
	}
	if totalOmitted < rules.MinOmittedLines {
		return compression{}
	}

	newline := detectNewline(run)
	label := inferLabel(stats)
	var b strings.Builder
	for i := 0; i < headEnd; i++ {
		b.WriteString(run[i].raw)
	}
	prev := headEnd
	for i := headEnd; i < tailStart; i++ {
		if !run[i].anchorSignal {
			continue
		}
		if i > prev {
			writeFoldMarker(&b, i-prev, label, newline)
		}
		b.WriteString(run[i].raw)
		if !strings.HasSuffix(run[i].raw, "\n") && !strings.HasSuffix(run[i].raw, "\r\n") && (i+1 < tailStart || tailStart < len(run)) {
			b.WriteString(newline)
		}
		prev = i + 1
	}
	if tailStart > prev {
		writeFoldMarker(&b, tailStart-prev, label, newline)
	}
	for i := tailStart; i < len(run); i++ {
		b.WriteString(run[i].raw)
	}
	return compression{ok: true, text: b.String()}
}

// writeFoldMarker inserts a human-readable omission marker between the preserved head and tail excerpts.
// writeFoldMarker 用于在保留的头尾摘录之间插入可读的省略标记。
func writeFoldMarker(b *strings.Builder, omitted int, label, newline string) {
	if omitted <= 0 {
		return
	}
	b.WriteString("... [")
	b.WriteString(itoa(omitted))
	b.WriteString(" lines of ")
	b.WriteString(label)
	b.WriteString(" folded by policy to save context] ...")
	b.WriteString(newline)
}

// shouldCompress decides whether one machine-like block is large and structured enough to justify folding.
// shouldCompress 用于判断一段机器文本块是否足够大且足够结构化，从而值得折叠。
func shouldCompress(stats blockStats, rules MemoryCleanerRules) bool {
	if stats.lineCount < rules.MinLines || stats.runeCount < rules.MinRunes {
		return false
	}
	if stats.nonBlank == 0 || stats.lineCount <= rules.HeadLines+rules.TailLines+rules.MinOmittedLines {
		return false
	}

	machineRatio := float64(stats.machineLines) / float64(stats.nonBlank)
	naturalRatio := float64(stats.naturalLines) / float64(stats.nonBlank)
	symbolRatio := float64(stats.symbolCount) / float64(maxInt(stats.runeCount, 1))
	if dominantStructured(stats) {
		return true
	}

	hanRatio := 0.0
	if stats.runeCount > 0 {
		hanRatio = float64(stats.hanCount) / float64(stats.runeCount)
	}
	if hanRatio >= rules.MaxHanRatio {
		return false
	}
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

	switch {
	case machineRatio >= 0.80:
		return true
	case machineRatio >= 0.65 && symbolRatio >= 0.18 && strongSignals >= 1:
		return true
	case stats.stackSignals >= 2 && machineRatio >= 0.55:
		return true
	case stats.logSignals >= 4 && machineRatio >= 0.55:
		return true
	case stats.jsonSignals >= 6 && machineRatio >= 0.50:
		return true
	case stats.codeSignals >= maxInt(6, stats.lineCount/2) && machineRatio >= 0.60 && symbolRatio >= 0.16:
		return true
	default:
		return false
	}
}

// dominantStructured fast-tracks heavily structured JSON or log blocks even when language ratio heuristics are ambiguous.
// dominantStructured 用于在语言比例判断不明确时，对明显结构化的 JSON 或日志块快速放行折叠。
func dominantStructured(stats blockStats) bool {
	denom := maxInt(stats.nonBlank, 1)
	return stats.jsonSignals*100 >= denom*60 || stats.logSignals*100 >= denom*60
}

// analyzeInfos aggregates a classified line slice into one blockStats summary.
// analyzeInfos 用于把已分类的行切片聚合成一份 blockStats 摘要。
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
		if info.anchorSignal {
			stats.anchorLines++
		}
	}
	return stats
}

// classifyLine extracts text-shape signals from one line so the cleaner can distinguish prose from machine output.
// classifyLine 用于从单行文本提取形态信号，让清洗器区分自然语言和机器输出。
func classifyLine(raw string, cfg MemoryCleanerConfig) lineInfo {
	text := stripLineEnding(raw)
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return lineInfo{raw: raw, text: text, kind: lineBlank}
	}

	runes, han, symbols, asciiLetters, digits, spaces := countChars(trimmed)
	symbolRatio := float64(symbols) / float64(maxInt(runes, 1))
	hanRatio := float64(han) / float64(maxInt(runes, 1))
	words := countNaturalWords(trimmed)
	indent := hasIndent(text)
	anchorSignal := hasAnchorSignal(trimmed)
	stackSignal := hasStackSignal(trimmed)
	logSignal := hasLogSignal(trimmed)
	jsonSignal := hasJSONSignal(trimmed)
	codeSignal := hasCodeSignal(trimmed, indent, hanRatio)
	structuredMachine := logSignal || (jsonSignal && (symbolRatio >= 0.08 || indent || startsStructured(trimmed)))
	strongMachine := stackSignal || structuredMachine || codeSignal

	naturalChinese := !structuredMachine && (han >= 4 || (hanRatio >= 0.15 && runes >= 20))
	englishRich := han == 0 && words >= 8 && asciiLetters >= 24 && !stackSignal && !logSignal && !jsonSignal
	englishVeryRich := han == 0 && words >= 12 && asciiLetters >= maxInt(30, runes/3) && !stackSignal && !logSignal && !jsonSignal
	naturalEnglish := (words >= 6 && symbolRatio < 0.18 && !stackSignal && !logSignal && !jsonSignal) ||
		(englishRich && symbolRatio < 0.32) ||
		(englishVeryRich && symbolRatio < 0.40)
	naturalSentence := (strings.ContainsAny(trimmed, "。！？?!") && (han >= 2 || words >= 5)) ||
		(strings.HasSuffix(trimmed, ".") && words >= 8 && !stackSignal && !logSignal && !jsonSignal)

	kind := lineNeutral
	if naturalChinese || naturalEnglish || naturalSentence {
		kind = lineNatural
	}

	lowHan := hanRatio < 0.08
	heavySymbols := symbolRatio >= 0.30 && runes >= 24
	codeishDensity := symbolRatio >= 0.18 && (strings.ContainsAny(trimmed, "{}[]();=<>") || indent)
	machine := false
	switch {
	case stackSignal:
		machine = true
	case structuredMachine:
		machine = true
	case codeSignal && lowHan && (codeishDensity || heavySymbols || indent):
		machine = true
	case lowHan && heavySymbols && (indent || digits > 0 || asciiLetters > 0):
		machine = true
	}
	if machine {
		kind = lineMachine
	}

	horizontalClip := shouldHorizontallyTruncate(trimmed, cfg, runes, han, symbols, asciiLetters, digits, spaces, words, anchorSignal, stackSignal, logSignal, jsonSignal, codeSignal)
	return lineInfo{
		raw:            raw,
		text:           text,
		kind:           kind,
		runes:          runes,
		han:            han,
		symbols:        symbols,
		asciiLetters:   asciiLetters,
		digits:         digits,
		spaces:         spaces,
		naturalWords:   words,
		stackSignal:    stackSignal,
		logSignal:      logSignal,
		jsonSignal:     jsonSignal,
		codeSignal:     codeSignal,
		anchorSignal:   anchorSignal,
		horizontalClip: horizontalClip,
		strongMachine:  strongMachine,
	}
}

// shouldHorizontallyTruncate decides whether one single line is so wide and machine-like that it should be clipped in place.
// shouldHorizontallyTruncate 用于判断单行文本是否又宽又像机器输出，从而需要原地水平截断。
func shouldHorizontallyTruncate(trimmed string, cfg MemoryCleanerConfig, runes, han, symbols, asciiLetters, digits, spaces, words int, anchorSignal, stackSignal, logSignal, jsonSignal, codeSignal bool) bool {
	if anchorSignal || runes < cfg.MaxLineRunes {
		return false
	}
	if jsonSignal || logSignal || stackSignal {
		return true
	}

	symbolRatio := float64(symbols) / float64(maxInt(runes, 1))
	machineDensity := float64(asciiLetters+digits+symbols) / float64(maxInt(runes, 1))
	noSpaces := spaces == 0
	switch {
	case noSpaces && looksLikeBase64OrToken(trimmed):
		return true
	case han == 0 && words <= 8 && spaces <= 8 && machineDensity >= 0.92 && (symbolRatio >= 0.10 || codeSignal):
		return true
	case han == 0 && words <= 4 && machineDensity >= 0.97:
		return true
	default:
		return false
	}
}

// looksLikeBase64OrToken classifies one long compact line that mostly contains token-ish or base64-like bytes.
// looksLikeBase64OrToken 用于识别长而紧凑、主要由 token 或 base64 形态字符组成的单行文本。
func looksLikeBase64OrToken(s string) bool {
	if s == "" {
		return false
	}
	valid := 0
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			valid++
		case r >= 'A' && r <= 'Z':
			valid++
		case r >= '0' && r <= '9':
			valid++
		case r == '+' || r == '/' || r == '=' || r == '-' || r == '_':
			valid++
		default:
			return false
		}
	}
	return valid*100 >= len(s)*95
}

// truncateLongLine preserves the ends of one huge machine line while replacing the middle section with a short marker.
// truncateLongLine 用于保留超长机器行的首尾，并把中间替换为简短标记。
func truncateLongLine(raw string, cfg MemoryCleanerConfig) string {
	text := stripLineEnding(raw)
	totalRunes := utf8.RuneCountInString(text)
	if totalRunes <= cfg.MaxLineRunes {
		return raw
	}
	headRunes := minInt(cfg.LongLineHeadRunes, totalRunes)
	tailRunes := minInt(cfg.LongLineTailRunes, totalRunes-headRunes)
	headByte := byteIndexAtRune(text, headRunes)
	tailByte := byteIndexAtRune(text, totalRunes-tailRunes)

	var b strings.Builder
	b.Grow(headByte + (len(text) - tailByte) + 64)
	b.WriteString(text[:headByte])
	b.WriteString("...[long machine line clipped]...")
	b.WriteString(text[tailByte:])
	b.WriteString(raw[len(text):])
	return b.String()
}

// byteIndexAtRune converts one rune offset into a byte offset so head/tail truncation can preserve valid UTF-8 slices.
// byteIndexAtRune 用于把 rune 偏移转换成字节偏移，确保头尾截断仍然保持有效 UTF-8 切片。
func byteIndexAtRune(s string, runePos int) int {
	if runePos <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == runePos {
			return i
		}
		count++
	}
	return len(s)
}

// startsStructured checks whether one line begins with structured data punctuation such as JSON delimiters.
// startsStructured 用于判断一行是否以 JSON 等结构化数据标点开头。
func startsStructured(trimmed string) bool {
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "\"") || strings.HasPrefix(trimmed, "'")
}

// countChars extracts coarse rune statistics used by the machine-text classifier.
// countChars 用于提取机器文本分类器使用的粗粒度字符统计信息。
func countChars(s string) (runes, han, symbols, asciiLetters, digits, spaces int) {
	for _, r := range s {
		runes++
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.IsSpace(r):
			spaces++
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

// countNaturalWords counts word-like runs so the classifier can distinguish human prose from compact machine lines.
// countNaturalWords 用于统计近似自然语言词数，帮助分类器区分人类文本和紧凑机器行。
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

// hasIndent checks whether one line starts with indentation that often appears in code, logs, or JSON.
// hasIndent 用于判断一行是否带有常见于代码、日志或 JSON 的缩进。
func hasIndent(s string) bool {
	return strings.HasPrefix(s, "    ") || strings.HasPrefix(s, "\t") || strings.HasPrefix(s, "  ")
}

// hasAnchorSignal finds stack-trace or root-cause anchors that should survive folding as landmarks.
// hasAnchorSignal 用于识别堆栈或根因锚点，让这些关键行在折叠时仍被保留。
func hasAnchorSignal(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.Contains(lower, "caused by:") ||
		strings.Contains(lower, "panic:") ||
		strings.Contains(lower, "fatal error:") ||
		strings.Contains(lower, "runtime error:") ||
		strings.Contains(lower, "root cause:")
}

// hasStackSignal detects stack-trace shaped lines.
// hasStackSignal 用于识别具有堆栈轨迹形态的文本行。
func hasStackSignal(s string) bool {
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "goroutine "),
		strings.HasPrefix(lower, "panic:"),
		strings.HasPrefix(lower, "fatal error:"),
		strings.HasPrefix(lower, "traceback"),
		strings.HasPrefix(lower, "caused by:"),
		strings.HasPrefix(lower, "runtime error:"):
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

// hasLogSignal detects common log prefixes so runtime traces and logs can be folded safely.
// hasLogSignal 用于识别常见日志前缀，让运行日志可以被安全折叠。
func hasLogSignal(s string) bool {
	trimmed := strings.TrimSpace(s)
	upper := strings.ToUpper(trimmed)
	switch {
	case looksLikeTimestampPrefix(trimmed):
		return true
	case strings.HasPrefix(trimmed, "[") && (strings.Contains(upper, "INFO") || strings.Contains(upper, "WARN") || strings.Contains(upper, "ERROR") || strings.Contains(upper, "DEBUG")):
		return true
	case strings.HasPrefix(upper, "INFO ") || strings.HasPrefix(upper, "WARN ") || strings.HasPrefix(upper, "ERROR ") || strings.HasPrefix(upper, "DEBUG "):
		return true
	case strings.Contains(upper, " LEVEL=") || strings.Contains(upper, " MSG=") || strings.Contains(upper, " TRACE="):
		return true
	default:
		return false
	}
}

// looksLikeTimestampPrefix checks the common YYYY-MM-DD HH:MM:SS style prefix used by logs.
// looksLikeTimestampPrefix 用于识别日志中常见的 YYYY-MM-DD HH:MM:SS 风格时间前缀。
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

// hasJSONSignal detects lines that strongly resemble JSON fragments.
// hasJSONSignal 用于识别明显类似 JSON 片段的文本行。
func hasJSONSignal(s string) bool {
	trimmed := strings.TrimSpace(s)
	switch trimmed {
	case "{", "}", "[", "]", "},", "],":
		return true
	}
	return strings.HasPrefix(trimmed, "{") ||
		strings.HasPrefix(trimmed, "[") ||
		(strings.HasPrefix(trimmed, "\"") && strings.Contains(trimmed, "\":")) ||
		(strings.HasPrefix(trimmed, "'") && strings.Contains(trimmed, "':"))
}

// hasCodeSignal detects source-code style lines while trying not to overmatch natural-language Chinese content.
// hasCodeSignal 用于识别源码风格文本，同时尽量避免误判中文自然语言。
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
	switch {
	case hit >= 2 && structural:
		return true
	case hit >= 1 && (structural || indent):
		return true
	case structural && indent && (strings.Contains(lower, "(") || strings.Contains(lower, "[") || strings.Contains(lower, ".")):
		return true
	default:
		return false
	}
}

// inferLabel selects a human-readable label for the omission marker based on the dominant machine-text shape.
// inferLabel 用于根据机器文本块的主导形态选择省略标记中的说明标签。
func inferLabel(stats blockStats) string {
	switch {
	case stats.stackSignals >= maxInt(stats.logSignals, maxInt(stats.jsonSignals, stats.codeSignals)) && stats.stackSignals >= 1:
		return "long error logs"
	case stats.jsonSignals >= maxInt(stats.logSignals, stats.codeSignals) && stats.jsonSignals >= 2:
		return "long structured data"
	case stats.logSignals >= maxInt(stats.jsonSignals, stats.codeSignals) && stats.logSignals >= 2:
		return "long runtime logs"
	case stats.codeSignals >= 1:
		return "long code"
	default:
		return "long machine text"
	}
}

// detectNewline keeps the caller's newline convention when the cleaner inserts fold markers.
// detectNewline 用于在插入折叠标记时保留调用方原本的换行风格。
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

// itoa converts one positive integer into ASCII digits without pulling in heavier formatting helpers.
// itoa 用于在不引入更重格式化助手的前提下，把正整数转成 ASCII 数字。
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

// minInt returns the smaller of two ints inside the cleaner's threshold math.
// minInt 用于在清洗器阈值计算中返回较小的整数。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt returns the larger of two ints inside the cleaner's threshold math.
// maxInt 用于在清洗器阈值计算中返回较大的整数。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
