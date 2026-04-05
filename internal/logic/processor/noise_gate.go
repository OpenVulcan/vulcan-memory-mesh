// noise_gate.go implements one hybrid regex-and-semantic admission gate for long-term memory persistence.
// noise_gate.go 用于实现一套结合正则和语义判断的长期记忆准入门控器。
package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	noiseTargetUser      = "user"
	noiseTargetAssistant = "assistant"
	noiseLanguageCommon  = "common"
)

// NoiseReasonCode identifies why one turn was blocked before relational persistence.
// NoiseReasonCode 用于标识某条轮次在写入关系存储前被阻断的原因。
type NoiseReasonCode string

const (
	// NoiseReasonAllow marks turns that passed all regex and semantic checks.
	// NoiseReasonAllow 用于标记通过全部正则和语义检查的轮次。
	NoiseReasonAllow NoiseReasonCode = "ALLOW"
	// NoiseReasonRegex marks turns blocked by a direct regex hit.
	// NoiseReasonRegex 用于标记被直接正则命中的轮次。
	NoiseReasonRegex NoiseReasonCode = "REGEX_MATCH"
	// NoiseReasonSemantic marks turns blocked by semantic similarity to one noise category.
	// NoiseReasonSemantic 用于标记因语义相似度命中某个噪声类别而被阻断的轮次。
	NoiseReasonSemantic NoiseReasonCode = "SEMANTIC_MATCH"
	// NoiseReasonSemanticUnavailable marks turns that could only use regex checks because embeddings were unavailable.
	// NoiseReasonSemanticUnavailable 用于标记因 embedding 不可用而只能执行正则检查的轮次。
	NoiseReasonSemanticUnavailable NoiseReasonCode = "SEMANTIC_UNAVAILABLE"
)

// NoiseDecision describes the gate decision for one normalized turn.
// NoiseDecision 用于描述一条标准化轮次的门控判定结果。
type NoiseDecision struct {
	Allow          bool
	Target         string
	Category       string
	ReasonCode     NoiseReasonCode
	DecisionSource string
	Score          float64
}

// NoiseGateConfig carries the fixed-rule directories plus runtime thresholds used when building the gate.
// NoiseGateConfig 用于承载构建门控器时使用的固定规则目录和运行时阈值。
type NoiseGateConfig struct {
	SystemDir         string
	UserDir           string
	DefaultLanguage   string
	Enabled           bool
	SemanticEnabled   bool
	SemanticThreshold float64
	Model             string
	Dimension         int
	Cache             logicports.NoiseEmbeddingCache
}

// NoiseGate holds compiled regex and semantic prototypes used to reject noisy turns before persistence.
// NoiseGate 用于持有写库前阻断噪声轮次所需的编译正则和语义原型。
type NoiseGate struct {
	enabled           bool
	semanticEnabled   bool
	defaultLanguage   string
	semanticThreshold float64
	model             string
	dimension         int
	rulesHash         string
	embedding         logicports.EmbeddingClient
	cache             logicports.NoiseEmbeddingCache
	logger            *logx.Logger
	categories        []*compiledNoiseCategory
	byTarget          map[string][]*compiledNoiseCategory
}

// NewNoiseGate loads, compiles, and optionally pre-embeds one language bundle before the application starts serving.
// NewNoiseGate 用于在应用开始对外服务前加载、编译并按需预先生成某个语言包的语义向量。
func NewNoiseGate(ctx context.Context, embedding logicports.EmbeddingClient, logger *logx.Logger, cfg NoiseGateConfig) (*NoiseGate, error) {
	if logger == nil {
		logger = logx.Default()
	}
	gate := &NoiseGate{
		enabled:           cfg.Enabled,
		semanticEnabled:   cfg.Enabled && cfg.SemanticEnabled,
		defaultLanguage:   strings.TrimSpace(cfg.DefaultLanguage),
		semanticThreshold: cfg.SemanticThreshold,
		model:             strings.TrimSpace(cfg.Model),
		dimension:         cfg.Dimension,
		embedding:         embedding,
		cache:             cfg.Cache,
		logger:            logger,
		byTarget:          map[string][]*compiledNoiseCategory{},
	}
	if !gate.enabled {
		return gate, nil
	}
	if gate.defaultLanguage == "" {
		gate.defaultLanguage = "zh-CN"
	}
	if gate.semanticThreshold <= 0 {
		gate.semanticThreshold = 0.88
	}
	compiled, rulesHash, err := loadCompiledNoiseCategories(cfg.SystemDir, cfg.UserDir, gate.defaultLanguage, gate.semanticThreshold)
	if err != nil {
		return nil, err
	}
	gate.categories = compiled
	gate.rulesHash = rulesHash
	for _, category := range compiled {
		for _, target := range category.Targets {
			gate.byTarget[target] = append(gate.byTarget[target], category)
		}
	}
	if gate.semanticEnabled && gate.embedding != nil {
		if err := gate.preloadSemanticPrototypes(ctx); err != nil {
			gate.logger.Warn("noise gate semantic preload degraded to regex-only", "err", err, "language", gate.defaultLanguage)
			gate.semanticEnabled = false
		}
	}
	return gate, nil
}

// FilterTurns executes the admission checks for one slice of normalized turns and returns only the turns that should be persisted.
// FilterTurns 用于对一组标准化轮次执行准入检查，并只返回允许持久化的轮次。
func (g *NoiseGate) FilterTurns(ctx context.Context, turns []logicdomain.NormalizedTurn) ([]logicdomain.NormalizedTurn, []NoiseDecision) {
	if g == nil || !g.enabled || len(turns) == 0 {
		decisions := make([]NoiseDecision, 0, len(turns))
		for range turns {
			decisions = append(decisions, NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow})
		}
		return turns, decisions
	}
	kept := make([]logicdomain.NormalizedTurn, 0, len(turns))
	decisions := make([]NoiseDecision, 0, len(turns))
	for _, turn := range turns {
		decision := g.AllowTurn(ctx, turn)
		decisions = append(decisions, decision)
		if decision.Allow {
			kept = append(kept, turn)
			continue
		}
		g.logger.Info(
			"noise gate dropped turn",
			"category", decision.Category,
			"reason_code", string(decision.ReasonCode),
			"source", decision.DecisionSource,
			"target", decision.Target,
			"score", decision.Score,
			"turn_index", turn.TurnIndex,
		)
	}
	return kept, decisions
}

// FilterPersistableTurns exposes the post-action-facing filtering capability without leaking decision details into the app layer.
// FilterPersistableTurns 用于向 post-action 暴露过滤能力，同时避免把判定细节泄漏到应用层。
func (g *NoiseGate) FilterPersistableTurns(ctx context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn {
	kept, _ := g.FilterTurns(ctx, turns)
	return kept
}

// AllowTurn decides whether one normalized turn is valuable enough to enter long-term storage.
// AllowTurn 用于判断一条标准化轮次是否有足够价值进入长期存储。
func (g *NoiseGate) AllowTurn(ctx context.Context, turn logicdomain.NormalizedTurn) NoiseDecision {
	if g == nil || !g.enabled {
		return NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}
	}
	if decision, ok := g.evaluateTargetRegex(noiseTargetUser, turn.UserMessage); ok {
		return decision
	}
	if decision, ok := g.evaluateTargetRegex(noiseTargetAssistant, turn.AssistantReply); ok {
		return decision
	}
	if !g.semanticEnabled || g.embedding == nil {
		return NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}
	}
	return g.evaluateSemantic(ctx, turn)
}

// preloadSemanticPrototypes embeds category phrases at startup so runtime admission checks only compare vectors.
// preloadSemanticPrototypes 用于在启动阶段对类别短语做 embedding，让运行时只需做向量比较。
func (g *NoiseGate) preloadSemanticPrototypes(ctx context.Context) error {
	// Reuse persisted vectors whenever the active rules, model, and dimension fingerprint has not changed.
	// 当当前规则、模型和维度指纹未变化时，优先复用持久化向量。
	if g.cache != nil {
		query := logicdomain.NoiseEmbeddingCacheQuery{
			Scope:     "noise_gate",
			Language:  g.defaultLanguage,
			Model:     g.model,
			Dimension: g.dimension,
			RulesHash: g.rulesHash,
		}
		entries, err := g.cache.LoadNoiseEmbeddingCache(ctx, query)
		if err != nil {
			g.logger.Warn("noise gate cache load degraded to live embedding", "err", err, "language", g.defaultLanguage)
		} else if g.restoreCachedVectors(entries) {
			g.logger.Info("noise gate semantic prototypes restored from cache", "language", g.defaultLanguage, "model", g.model, "dimension", g.dimension)
			return nil
		}
	}

	// Fall back to live embedding only for missing or invalid caches, then refresh the persistent cache.
	// 仅在缓存缺失或无效时回退到实时 embedding，并刷新持久化缓存。
	for _, category := range g.categories {
		if len(category.Phrases) == 0 {
			continue
		}
		resp, err := g.embedding.Embed(ctx, logicports.EmbeddingRequest{
			Model:     g.model,
			Texts:     category.Phrases,
			Dimension: g.dimension,
			ProviderHints: map[string]any{
				"purpose": "noise_gate_bootstrap",
			},
		})
		if err != nil {
			return fmt.Errorf("embed noise category %s: %w", category.Name, err)
		}
		category.Vectors = make([][]float32, 0, len(resp.Vectors))
		for _, vector := range resp.Vectors {
			category.Vectors = append(category.Vectors, normalizeFloat32Vector(vector))
		}
	}
	if g.cache != nil {
		query := logicdomain.NoiseEmbeddingCacheQuery{
			Scope:     "noise_gate",
			Language:  g.defaultLanguage,
			Model:     g.model,
			Dimension: g.dimension,
			RulesHash: g.rulesHash,
		}
		if err := g.cache.ReplaceNoiseEmbeddingCache(ctx, query, g.buildCacheEntries(query)); err != nil {
			g.logger.Warn("noise gate cache refresh degraded", "err", err, "language", g.defaultLanguage)
		}
	}
	return nil
}

// restoreCachedVectors maps persisted vectors back onto the compiled categories and returns whether the cache is complete.
// restoreCachedVectors 用于把持久化向量回填到已编译类别，并返回缓存是否完整可用。
func (g *NoiseGate) restoreCachedVectors(entries []logicdomain.NoiseEmbeddingCacheEntry) bool {
	// Accept the cache only when every phrase-backed category is fully covered by persisted rows.
	// 只有当所有带短语的类别都被持久化记录完整覆盖时，才接受这份缓存。
	if len(entries) == 0 {
		return false
	}
	lookup := make(map[string][]float32, len(entries))
	for _, entry := range entries {
		key := entry.CategoryName + "\x00" + entry.Phrase
		lookup[key] = normalizeFloat32Vector(entry.Vector)
	}
	for _, category := range g.categories {
		if len(category.Phrases) == 0 {
			category.Vectors = nil
			continue
		}
		vectors := make([][]float32, 0, len(category.Phrases))
		for _, phrase := range category.Phrases {
			vector, ok := lookup[category.Name+"\x00"+phrase]
			if !ok {
				category.Vectors = nil
				return false
			}
			vectors = append(vectors, vector)
		}
		category.Vectors = vectors
	}
	return true
}

// buildCacheEntries converts the in-memory category vectors into durable cache rows for the active rule fingerprint.
// buildCacheEntries 用于把内存中的类别向量转换成当前规则指纹对应的持久化缓存记录。
func (g *NoiseGate) buildCacheEntries(query logicdomain.NoiseEmbeddingCacheQuery) []logicdomain.NoiseEmbeddingCacheEntry {
	// Flatten every phrase-vector pair so the durable SQL backend can persist one stable row per semantic prototype.
	// 将每个短语与向量展开成稳定的单行记录，便于长期 SQL 后端持久化。
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0)
	now := time.Now().UTC()
	for _, category := range g.categories {
		for idx, phrase := range category.Phrases {
			if idx >= len(category.Vectors) {
				continue
			}
			entries = append(entries, logicdomain.NoiseEmbeddingCacheEntry{
				Scope:        query.Scope,
				Language:     query.Language,
				CategoryName: category.Name,
				Phrase:       phrase,
				Model:        query.Model,
				Dimension:    query.Dimension,
				RulesHash:    query.RulesHash,
				Vector:       category.Vectors[idx],
				UpdatedAt:    now,
			})
		}
	}
	return entries
}

// evaluateTargetRegex checks direct regex hits before any semantic work is attempted.
// evaluateTargetRegex 用于在进入语义阶段之前先检查直接正则命中。
func (g *NoiseGate) evaluateTargetRegex(target, text string) (NoiseDecision, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}, false
	}
	for _, category := range g.byTarget[target] {
		for _, pattern := range category.Patterns {
			if pattern.MatchString(text) {
				return NoiseDecision{
					Allow:          false,
					Target:         target,
					Category:       category.Name,
					ReasonCode:     NoiseReasonRegex,
					DecisionSource: "regex",
				}, true
			}
		}
	}
	return NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}, false
}

// evaluateSemantic embeds the current turn texts once and checks them against all preloaded category prototypes.
// evaluateSemantic 用于对当前轮次文本做一次 embedding，并与预加载的类别原型做对比。
func (g *NoiseGate) evaluateSemantic(ctx context.Context, turn logicdomain.NormalizedTurn) NoiseDecision {
	type targetText struct {
		target string
		text   string
	}
	queries := make([]targetText, 0, 2)
	if g.hasSemanticTarget(noiseTargetUser) && strings.TrimSpace(turn.UserMessage) != "" {
		queries = append(queries, targetText{target: noiseTargetUser, text: turn.UserMessage})
	}
	if g.hasSemanticTarget(noiseTargetAssistant) && strings.TrimSpace(turn.AssistantReply) != "" {
		queries = append(queries, targetText{target: noiseTargetAssistant, text: turn.AssistantReply})
	}
	if len(queries) == 0 {
		return NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}
	}
	texts := make([]string, 0, len(queries))
	for _, query := range queries {
		texts = append(texts, query.text)
	}
	resp, err := g.embedding.Embed(ctx, logicports.EmbeddingRequest{
		Model:     g.model,
		Texts:     texts,
		Dimension: g.dimension,
		ProviderHints: map[string]any{
			"purpose": "noise_gate_runtime",
		},
	})
	if err != nil {
		g.logger.Warn("noise gate semantic runtime degraded", "err", err)
		return NoiseDecision{Allow: true, ReasonCode: NoiseReasonSemanticUnavailable}
	}
	bestDecision := NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}
	for idx, vector := range resp.Vectors {
		query := queries[idx]
		decision, matched := g.matchSemanticTarget(query.target, normalizeFloat32Vector(vector))
		if matched {
			return decision
		}
		bestDecision = decision
	}
	return bestDecision
}

// hasSemanticTarget reports whether the target has at least one category with preloaded prototype vectors.
// hasSemanticTarget 用于判断某个目标是否存在至少一个已预加载原型向量的类别。
func (g *NoiseGate) hasSemanticTarget(target string) bool {
	for _, category := range g.byTarget[target] {
		if len(category.Vectors) > 0 {
			return true
		}
	}
	return false
}

// matchSemanticTarget checks one embedded text vector against every prototype in the target category set.
// matchSemanticTarget 用于把单个文本向量与目标类别集合中的所有原型进行比对。
func (g *NoiseGate) matchSemanticTarget(target string, vector []float32) (NoiseDecision, bool) {
	best := NoiseDecision{Allow: true, ReasonCode: NoiseReasonAllow}
	for _, category := range g.byTarget[target] {
		if len(category.Vectors) == 0 {
			continue
		}
		threshold := category.Threshold
		for _, prototype := range category.Vectors {
			score := cosineSimilarity(vector, prototype)
			if score > best.Score {
				best = NoiseDecision{
					Allow:          true,
					Target:         target,
					Category:       category.Name,
					ReasonCode:     NoiseReasonAllow,
					DecisionSource: "semantic",
					Score:          score,
				}
			}
			if score >= threshold {
				return NoiseDecision{
					Allow:          false,
					Target:         target,
					Category:       category.Name,
					ReasonCode:     NoiseReasonSemantic,
					DecisionSource: "semantic",
					Score:          score,
				}, true
			}
		}
	}
	return best, false
}

// loadCompiledNoiseCategories resolves the selected common/language files and compiles them into immutable runtime categories.
// loadCompiledNoiseCategories 用于解析选中的公共/语言规则文件，并把它们编译为不可变运行时类别。
func loadCompiledNoiseCategories(systemDir, userDir, language string, defaultThreshold float64) ([]*compiledNoiseCategory, string, error) {
	commonRules, err := loadSelectedNoiseRuleFile(systemDir, userDir, noiseLanguageCommon)
	if err != nil {
		return nil, "", err
	}
	languageRules, err := loadSelectedNoiseRuleFile(systemDir, userDir, language)
	if err != nil {
		return nil, "", err
	}
	if len(commonRules.Categories) == 0 && len(languageRules.Categories) == 0 {
		return nil, "", fmt.Errorf("noise rules are missing for language %q", language)
	}
	rulesHash, err := hashNoiseRuleBundle(commonRules, languageRules)
	if err != nil {
		return nil, "", err
	}
	merged := map[string]noiseCategoryDefinition{}
	order := make([]string, 0, len(commonRules.Categories)+len(languageRules.Categories))
	for _, category := range commonRules.Categories {
		if _, ok := merged[category.Name]; !ok {
			order = append(order, category.Name)
		}
		merged[category.Name] = category
	}
	for _, category := range languageRules.Categories {
		if _, ok := merged[category.Name]; !ok {
			order = append(order, category.Name)
		}
		merged[category.Name] = category
	}
	out := make([]*compiledNoiseCategory, 0, len(order))
	for _, name := range order {
		compiled, err := compileNoiseCategory(merged[name], defaultThreshold)
		if err != nil {
			return nil, "", err
		}
		out = append(out, compiled)
	}
	return out, rulesHash, nil
}

// hashNoiseRuleBundle fingerprints the selected common plus language bundles so cache reuse is invalidated by any rule edit.
// hashNoiseRuleBundle 用于为选中的公共和语言规则包生成指纹，保证任意规则修改都会让缓存失效。
func hashNoiseRuleBundle(commonRules, languageRules noiseRuleFile) (string, error) {
	// Hash the selected rule payloads rather than file paths so user overrides and content edits are both reflected.
	// 对选中的规则内容而不是文件路径做哈希，这样用户覆盖和内容编辑都会被反映出来。
	payload, err := json.Marshal(struct {
		Common   noiseRuleFile `json:"common"`
		Language noiseRuleFile `json:"language"`
	}{
		Common:   commonRules,
		Language: languageRules,
	})
	if err != nil {
		return "", fmt.Errorf("marshal noise rule bundle fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// loadSelectedNoiseRuleFile picks the user file when it exists, otherwise falls back to the system file.
// loadSelectedNoiseRuleFile 用于在用户文件存在时优先选用用户文件，否则回退到系统文件。
func loadSelectedNoiseRuleFile(systemDir, userDir, language string) (noiseRuleFile, error) {
	candidate := selectNoiseRuleFile(systemDir, userDir, language)
	if candidate == "" {
		return noiseRuleFile{}, nil
	}
	return loadNoiseRuleFile(candidate)
}

// selectNoiseRuleFile selects the effective rule file for one language layer.
// selectNoiseRuleFile 用于为单个语言层选择最终生效的规则文件。
func selectNoiseRuleFile(systemDir, userDir, language string) string {
	if strings.TrimSpace(userDir) != "" {
		userPath := filepath.Join(userDir, language+".json")
		if info, err := os.Stat(userPath); err == nil && !info.IsDir() {
			return userPath
		}
	}
	if strings.TrimSpace(systemDir) != "" {
		systemPath := filepath.Join(systemDir, language+".json")
		if info, err := os.Stat(systemPath); err == nil && !info.IsDir() {
			return systemPath
		}
	}
	return ""
}

// compileNoiseCategory converts one JSON category into an immutable runtime category with compiled regex.
// compileNoiseCategory 用于把单个 JSON 类别转换成带编译正则的不可变运行时类别。
func compileNoiseCategory(def noiseCategoryDefinition, defaultThreshold float64) (*compiledNoiseCategory, error) {
	name := strings.TrimSpace(def.Name)
	if name == "" {
		return nil, fmt.Errorf("noise category name is required")
	}
	targets := compileNoiseTargets(def.Targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("noise category %s must target user or assistant", name)
	}
	patterns := make([]*regexp.Regexp, 0, len(def.Patterns))
	for _, raw := range def.Patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		pattern, err := regexp.Compile("(?i)" + raw)
		if err != nil {
			return nil, fmt.Errorf("compile noise pattern for %s: %w", name, err)
		}
		patterns = append(patterns, pattern)
	}
	phrases := make([]string, 0, len(def.Phrases))
	for _, phrase := range def.Phrases {
		phrase = textutil.NormalizeWhitespace(strings.TrimSpace(phrase))
		if phrase == "" {
			continue
		}
		phrases = append(phrases, phrase)
	}
	threshold := def.Threshold
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	return &compiledNoiseCategory{
		Name:      name,
		Targets:   targets,
		Patterns:  patterns,
		Phrases:   phrases,
		Threshold: threshold,
	}, nil
}

// compileNoiseTargets validates and normalizes the configured target list.
// compileNoiseTargets 用于校验并规范化配置中的目标列表。
func compileNoiseTargets(targets []string) []string {
	if len(targets) == 0 {
		return []string{noiseTargetUser, noiseTargetAssistant}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.ToLower(strings.TrimSpace(target))
		switch target {
		case noiseTargetUser, noiseTargetAssistant:
			if _, ok := seen[target]; ok {
				continue
			}
			seen[target] = struct{}{}
			out = append(out, target)
		}
	}
	return out
}

// normalizeFloat32Vector converts one embedding vector into unit length to keep cosine comparisons stable across providers.
// normalizeFloat32Vector 用于把单个 embedding 向量归一成单位长度，保证不同 provider 下余弦比较稳定。
func normalizeFloat32Vector(vector []float32) []float32 {
	if len(vector) == 0 {
		return vector
	}
	out := make([]float32, len(vector))
	copy(out, vector)
	var norm float64
	for _, value := range out {
		norm += float64(value * value)
	}
	if norm == 0 {
		return out
	}
	scale := float32(math.Sqrt(norm))
	for idx := range out {
		out[idx] /= scale
	}
	return out
}

// cosineSimilarity computes one cosine score assuming both vectors are already normalized or close to normalized.
// cosineSimilarity 用于在向量已归一或接近归一的前提下计算一次余弦相似度。
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for idx := range a {
		dot += float64(a[idx] * b[idx])
	}
	return dot
}
