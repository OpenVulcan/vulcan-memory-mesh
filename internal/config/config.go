package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	HTTP       HTTPConfig       `json:"http"`
	LLM        LLMConfig        `json:"llm"`
	Embedding  EmbeddingConfig  `json:"embedding"`
	Vector     VectorConfig     `json:"vector"`
	Relational RelationalConfig `json:"relational"`
	PreCheck   PreCheckConfig   `json:"precheck"`
	Admin      AdminConfig      `json:"admin"`
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
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
}

type VectorConfig struct {
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint,omitempty"`
	APIKey     string `json:"api_key,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Collection string `json:"collection,omitempty"`
}

type RelationalConfig struct {
	Provider string `json:"provider"`
	DSN      string `json:"dsn,omitempty"`
}

type PreCheckConfig struct {
	IntentTimeout       Duration `json:"intent_timeout"`
	TopK                int      `json:"top_k"`
	SimilarityThreshold float64  `json:"similarity_threshold"`
}

type AdminConfig struct{ SeedEnabled bool `json:"seed_enabled"` }

func DefaultLocal() Config {
	return Config{
		HTTP: HTTPConfig{
			ListenAddr: ":8080",
			RequestTimeout: HTTPRequestTimeout{
				PreCheck:   Duration{3 * time.Second},
				PostAction: Duration{3 * time.Second},
				SeedMemory: Duration{3 * time.Second},
			},
			ShutdownTimeout: Duration{10 * time.Second},
		},
		LLM:        LLMConfig{Provider: "mock", Model: "mock-intent-fast"},
		Embedding:  EmbeddingConfig{Provider: "mock", Model: "mock-embedding-v1"},
		Vector:     VectorConfig{Provider: "memory", Namespace: "local", Collection: "vmm_memories"},
		Relational: RelationalConfig{Provider: "memory"},
		PreCheck:   PreCheckConfig{IntentTimeout: Duration{2 * time.Second}, TopK: 5, SimilarityThreshold: 0.4},
		Admin:      AdminConfig{SeedEnabled: true},
	}
}

func DefaultSaaS() Config {
	cfg := DefaultLocal()
	cfg.HTTP.ListenAddr = ":8081"
	cfg.Admin.SeedEnabled = false
	cfg.LLM.Provider = "aliyun_dashscope"
	cfg.Embedding.Provider = "aliyun_dashscope"
	cfg.Vector.Provider = "aliyun_dashvector"
	cfg.Relational.Provider = "postgres"
	return cfg
}

func Load(path string, fallback Config) (Config, error) {
	cfg := fallback
	if strings.TrimSpace(path) != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(body, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnvOverrides(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTP.ListenAddr) == "" {
		return errors.New("http.listen_addr is required")
	}
	if c.PreCheck.TopK <= 0 {
		return errors.New("precheck.top_k must be > 0")
	}
	if c.PreCheck.SimilarityThreshold < 0 || c.PreCheck.SimilarityThreshold > 1 {
		return errors.New("precheck.similarity_threshold must be in [0,1]")
	}
	if strings.TrimSpace(c.LLM.Provider) == "" || strings.TrimSpace(c.Embedding.Provider) == "" || strings.TrimSpace(c.Vector.Provider) == "" || strings.TrimSpace(c.Relational.Provider) == "" {
		return errors.New("provider fields are required")
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	setString := func(k string, target *string) { if v := strings.TrimSpace(os.Getenv(k)); v != "" { *target = v } }
	setInt := func(k string, target *int) { if v := strings.TrimSpace(os.Getenv(k)); v != "" { if n, err := strconv.Atoi(v); err == nil { *target = n } } }
	setFloat := func(k string, target *float64) { if v := strings.TrimSpace(os.Getenv(k)); v != "" { if n, err := strconv.ParseFloat(v, 64); err == nil { *target = n } } }
	setBool := func(k string, target *bool) { if v := strings.TrimSpace(os.Getenv(k)); v != "" { if n, err := strconv.ParseBool(v); err == nil { *target = n } } }
	setDuration := func(k string, target *Duration) { if v := strings.TrimSpace(os.Getenv(k)); v != "" { if n, err := time.ParseDuration(v); err == nil { target.Duration = n } } }

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
	setString("VMM_EMBED_ORGANIZATION", &cfg.Embedding.Organization)
	setString("VMM_EMBED_PROJECT", &cfg.Embedding.Project)
	setString("VMM_VECTOR_PROVIDER", &cfg.Vector.Provider)
	setString("VMM_VECTOR_ENDPOINT", &cfg.Vector.Endpoint)
	setString("VMM_VECTOR_API_KEY", &cfg.Vector.APIKey)
	setString("VMM_VECTOR_NAMESPACE", &cfg.Vector.Namespace)
	setString("VMM_VECTOR_COLLECTION", &cfg.Vector.Collection)
	setString("VMM_RELATIONAL_PROVIDER", &cfg.Relational.Provider)
	setString("VMM_RELATIONAL_DSN", &cfg.Relational.DSN)
	setDuration("VMM_PRECHECK_INTENT_TIMEOUT", &cfg.PreCheck.IntentTimeout)
	setInt("VMM_PRECHECK_TOPK", &cfg.PreCheck.TopK)
	setFloat("VMM_PRECHECK_SIMILARITY_THRESHOLD", &cfg.PreCheck.SimilarityThreshold)
	setBool("VMM_ADMIN_SEED_ENABLED", &cfg.Admin.SeedEnabled)
}
