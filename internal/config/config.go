package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		dur, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		d.Duration = dur
		return nil
	}
	var ms int64
	if err := json.Unmarshal(data, &ms); err != nil {
		return err
	}
	d.Duration = time.Duration(ms) * time.Millisecond
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

type Config struct {
	HTTP           HTTPConfig           `json:"http"`
	LLM            LLMConfig            `json:"llm"`
	Embedding      EmbeddingConfig      `json:"embedding"`
	Vector         VectorConfig         `json:"vector"`
	Relational     RelationalConfig     `json:"relational"`
	PreCheck       PreCheckConfig       `json:"precheck"`
	MemoryPipeline MemoryPipelineConfig `json:"memory_pipeline"`
	Admin          AdminConfig          `json:"admin"`
}

type HTTPConfig struct {
	ListenAddr      string             `json:"listen_addr"`
	RequestTimeout  HTTPRequestTimeout `json:"request_timeout"`
	ShutdownTimeout Duration           `json:"shutdown_timeout"`
}

type HTTPRequestTimeout struct {
	PreCheck   Duration `json:"pre_check"`
	PostAction Duration `json:"post_action"`
	SeedMemory Duration `json:"seed_memory"`
}

type LLMConfig struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	Model        string `json:"model,omitempty"`
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
}

type EmbeddingConfig struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	Model        string `json:"model,omitempty"`
	Dimension    int    `json:"dimension,omitempty"`
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
}

type VectorConfig struct {
	Provider string `json:"provider"`
}

type RelationalConfig struct {
	Provider string `json:"provider"`
	DSN      string `json:"dsn,omitempty"`
}

type PreCheckConfig struct {
	IntentTimeout       Duration `json:"intent_timeout"`
	TopK                int      `json:"top_k"`
	SimilarityThreshold float64  `json:"similarity_threshold,omitempty"`
}

type MemoryPipelineConfig struct {
	MaxSearchKeywords  int      `json:"max_search_keywords"`
	MinSimilarityScore *float64 `json:"min_similarity_score,omitempty"`
}

type AdminConfig struct {
	SeedEnabled bool `json:"seed_enabled"`
}

func DefaultLocal() Config {
	return Config{
		HTTP: HTTPConfig{
			ListenAddr:      ":8080",
			RequestTimeout:  HTTPRequestTimeout{PreCheck: Duration{3 * time.Second}, PostAction: Duration{3 * time.Second}, SeedMemory: Duration{3 * time.Second}},
			ShutdownTimeout: Duration{10 * time.Second},
		},
		LLM:            LLMConfig{Provider: "mock", Model: "mock-intent-fast"},
		Embedding:      EmbeddingConfig{Provider: "mock", Model: "mock-embedding-v1", Dimension: 64},
		Vector:         VectorConfig{Provider: "memory"},
		Relational:     RelationalConfig{Provider: "memory"},
		PreCheck:       PreCheckConfig{IntentTimeout: Duration{2 * time.Second}, TopK: 5},
		MemoryPipeline: MemoryPipelineConfig{MaxSearchKeywords: 5, MinSimilarityScore: float64Ptr(0.75)},
		Admin:          AdminConfig{SeedEnabled: true},
	}
}

func Load(path string, fallback Config) (Config, error) {
	return LoadPaths([]string{path}, fallback)
}

func LoadPaths(paths []string, fallback Config) (Config, error) {
	cfg := fallback
	normalizedPaths := normalizeConfigPaths(paths)
	if err := loadDotEnv(normalizedPaths); err != nil {
		return Config{}, err
	}
	for _, path := range normalizedPaths {
		body, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		expandedBody := os.ExpandEnv(string(body))
		if err := json.Unmarshal([]byte(expandedBody), &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnvOverrides(&cfg)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeConfigPaths(paths []string) []string {
	normalized := make([]string, 0, len(paths))
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		cleaned := filepath.Clean(raw)
		duplicate := false
		for _, existing := range normalized {
			if existing == cleaned {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		normalized = append(normalized, cleaned)
	}
	return normalized
}

func loadDotEnv(configPaths []string) error {
	mergedEnv := map[string]string{}
	for _, configPath := range configPaths {
		for _, candidate := range dotEnvCandidates(configPath) {
			envMap, err := godotenv.Read(candidate)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("load .env %q: %w", candidate, err)
			}
			for key, value := range envMap {
				mergedEnv[key] = value
			}
		}
	}
	for key, value := range mergedEnv {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set env %q from .env: %w", key, err)
		}
	}
	return nil
}

func dotEnvCandidates(configPath string) []string {
	candidates := make([]string, 0, 2)
	seen := map[string]struct{}{}
	addCandidate := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	if strings.TrimSpace(configPath) != "" {
		if absPath, err := filepath.Abs(configPath); err == nil {
			configDir := filepath.Dir(absPath)
			if strings.EqualFold(filepath.Base(configDir), "configs") {
				addCandidate(filepath.Join(configDir, "..", ".env"))
			}
			addCandidate(filepath.Join(configDir, ".env"))
		}
	}
	return candidates
}

func float64Ptr(v float64) *float64 { return &v }

func (c *Config) Normalize() {
	if c.HTTP.RequestTimeout.PreCheck.Duration <= 0 {
		c.HTTP.RequestTimeout.PreCheck = Duration{3 * time.Second}
	}
	if c.HTTP.RequestTimeout.PostAction.Duration <= 0 {
		c.HTTP.RequestTimeout.PostAction = Duration{3 * time.Second}
	}
	if c.HTTP.RequestTimeout.SeedMemory.Duration <= 0 {
		c.HTTP.RequestTimeout.SeedMemory = Duration{3 * time.Second}
	}
	if c.HTTP.ShutdownTimeout.Duration <= 0 {
		c.HTTP.ShutdownTimeout = Duration{10 * time.Second}
	}
	if c.PreCheck.IntentTimeout.Duration <= 0 {
		c.PreCheck.IntentTimeout = Duration{2 * time.Second}
	}
	if c.PreCheck.TopK <= 0 {
		c.PreCheck.TopK = 5
	}
	if c.MemoryPipeline.MaxSearchKeywords <= 0 {
		c.MemoryPipeline.MaxSearchKeywords = 5
	}
	if c.MemoryPipeline.MaxSearchKeywords > 10 {
		c.MemoryPipeline.MaxSearchKeywords = 10
	}
	if c.MemoryPipeline.MinSimilarityScore == nil {
		if c.PreCheck.SimilarityThreshold > 0 {
			c.MemoryPipeline.MinSimilarityScore = float64Ptr(c.PreCheck.SimilarityThreshold)
		} else {
			c.MemoryPipeline.MinSimilarityScore = float64Ptr(0.75)
		}
	}
	if c.Embedding.Dimension <= 0 && strings.EqualFold(c.Embedding.Provider, "mock") {
		c.Embedding.Dimension = 64
	}
	if strings.TrimSpace(c.Relational.Provider) == "" {
		c.Relational.Provider = "memory"
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTP.ListenAddr) == "" {
		return errors.New("http.listen_addr is required")
	}
	if c.PreCheck.TopK <= 0 {
		return errors.New("precheck.top_k must be > 0")
	}
	if c.MemoryPipeline.MaxSearchKeywords <= 0 || c.MemoryPipeline.MaxSearchKeywords > 10 {
		return errors.New("memory_pipeline.max_search_keywords must be in [1,10]")
	}
	if c.MemoryPipeline.MinSimilarityScore == nil {
		return errors.New("memory_pipeline.min_similarity_score must be set")
	}
	if *c.MemoryPipeline.MinSimilarityScore < 0 || *c.MemoryPipeline.MinSimilarityScore > 1 {
		return errors.New("memory_pipeline.min_similarity_score must be in [0,1]")
	}
	if strings.TrimSpace(c.LLM.Provider) == "" || strings.TrimSpace(c.Embedding.Provider) == "" || strings.TrimSpace(c.Vector.Provider) == "" || strings.TrimSpace(c.Relational.Provider) == "" {
		return errors.New("provider fields are required")
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	setString := func(k string, target *string) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			*target = v
		}
	}
	setInt := func(k string, target *int) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*target = n
			}
		}
	}
	setFloat := func(k string, target *float64) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = n
			}
		}
	}
	setOptionalFloat := func(k string, target **float64) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				*target = float64Ptr(n)
			}
		}
	}
	setBool := func(k string, target *bool) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := strconv.ParseBool(v); err == nil {
				*target = n
			}
		}
	}
	setDuration := func(k string, target *Duration) {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if n, err := time.ParseDuration(v); err == nil {
				target.Duration = n
			}
		}
	}

	setString("VMM_HTTP_LISTEN_ADDR", &cfg.HTTP.ListenAddr)
	setDuration("VMM_HTTP_PRECHECK_TIMEOUT", &cfg.HTTP.RequestTimeout.PreCheck)
	setDuration("VMM_HTTP_POSTACTION_TIMEOUT", &cfg.HTTP.RequestTimeout.PostAction)
	setDuration("VMM_HTTP_SEED_TIMEOUT", &cfg.HTTP.RequestTimeout.SeedMemory)
	setDuration("VMM_HTTP_SHUTDOWN_TIMEOUT", &cfg.HTTP.ShutdownTimeout)
	setString("VMM_LLM_PROVIDER", &cfg.LLM.Provider)
	setString("VMM_LLM_ENDPOINT", &cfg.LLM.Endpoint)
	setString("VMM_LLM_API_KEY", &cfg.LLM.APIKey)
	setString("VMM_LLM_MODEL", &cfg.LLM.Model)
	setString("VMM_LLM_ORGANIZATION", &cfg.LLM.Organization)
	setString("VMM_LLM_PROJECT", &cfg.LLM.Project)
	setString("VMM_EMBED_PROVIDER", &cfg.Embedding.Provider)
	setString("VMM_EMBED_ENDPOINT", &cfg.Embedding.Endpoint)
	setString("VMM_EMBED_API_KEY", &cfg.Embedding.APIKey)
	setString("VMM_EMBED_MODEL", &cfg.Embedding.Model)
	setInt("VMM_EMBED_DIMENSION", &cfg.Embedding.Dimension)
	setString("VMM_EMBED_ORGANIZATION", &cfg.Embedding.Organization)
	setString("VMM_EMBED_PROJECT", &cfg.Embedding.Project)
	setString("VMM_VECTOR_PROVIDER", &cfg.Vector.Provider)
	setString("VMM_RELATIONAL_PROVIDER", &cfg.Relational.Provider)
	setString("VMM_RELATIONAL_DSN", &cfg.Relational.DSN)
	setDuration("VMM_PRECHECK_INTENT_TIMEOUT", &cfg.PreCheck.IntentTimeout)
	setInt("VMM_PRECHECK_TOPK", &cfg.PreCheck.TopK)
	setFloat("VMM_PRECHECK_SIMILARITY_THRESHOLD", &cfg.PreCheck.SimilarityThreshold)
	setInt("VMM_MEMORY_MAX_SEARCH_KEYWORDS", &cfg.MemoryPipeline.MaxSearchKeywords)
	setOptionalFloat("VMM_MEMORY_MIN_SIMILARITY_SCORE", &cfg.MemoryPipeline.MinSimilarityScore)
	setBool("VMM_ADMIN_SEED_ENABLED", &cfg.Admin.SeedEnabled)
}
