// These tests compare source observation with real typed merges, including nulls and collection mutations.
// 这些测试将来源观察与真实类型合并比较，覆盖空值及集合变更，属于配置层回归测试。
package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestEffectiveSourcesFollowActualMerges covers same-value writes, ignored scalar nulls, nullable values, and removed array entries.
// TestEffectiveSourcesFollowActualMerges 覆盖同值赋值、被忽略的标量空值、可空字段及删除的数组项。
func TestEffectiveSourcesFollowActualMerges(t *testing.T) {
	var cfg Config
	trace := &configSourceTrace{}
	trace.observe(cfg, ConfigValueSource{Kind: "initial"}, nil)
	apply := func(file, body string) {
		t.Helper()
		if err := applyLayeredAIKeyOverrideReset(&cfg, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := unmarshalStrictConfigLayer([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := trace.file(file, []byte(body), []byte(body), cfg); err != nil {
			t.Fatal(err)
		}
	}
	apply("base.json", `{"logging":{"level":"debug","format":"text"},"memory_pipeline":{"min_similarity_score":0},"llm":{"routes":[{"model":"first"},{"model":"second"}]}}`)
	apply("user.json", `{"logging":{"level":"debug","format":null},"memory_pipeline":{"min_similarity_score":null},"llm":{"routes":[{"model":"third"}]}}`)
	for pointer, file := range map[string]string{"/logging/level": "user.json", "/logging/format": "base.json", "/memory_pipeline/min_similarity_score": "user.json", "/llm/routes/0/model": "user.json"} {
		if source := trace.sources[pointer]; source.Kind != "file" || source.File != file {
			t.Fatalf("%s: %+v", pointer, source)
		}
	}
	if _, exists := trace.sources["/llm/routes/1/model"]; exists {
		t.Fatal("removed array source survived")
	}
	if cfg.MemoryPipeline.MinSimilarityScore != nil || trace.values["/memory_pipeline/min_similarity_score"] != nil {
		t.Fatal("explicit null was lost")
	}
}

// TestEffectiveSourcesPreserveNormalizationAndMapKeys keeps input origins and unambiguous dynamic keys without secret values.
// TestEffectiveSourcesPreserveNormalizationAndMapKeys 保留归一化输入来源和无歧义动态键，不输出秘密值。
func TestEffectiveSourcesPreserveNormalizationAndMapKeys(t *testing.T) {
	var cfg Config
	body := []byte(`{"logging":{"level":" DEBUG "},"llm":{"routes":[{"model":"visible","api_keys":["private-secret"],"params":{"a/b~c.d":true}}]}}`)
	trace := &configSourceTrace{}
	trace.observe(cfg, ConfigValueSource{Kind: "initial"}, nil)
	if err := unmarshalStrictConfigLayer(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := trace.file("user.json", body, body, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Normalize()
	trace.observe(cfg, ConfigValueSource{Kind: "normalization"}, nil)
	if source := trace.sources["/logging/level"]; source.File != "user.json" || !source.Normalized {
		t.Fatalf("normalization lost input: %+v", source)
	}
	if source := trace.sources["/llm/routes/0/params/a~1b~0c.d"]; source.File != "user.json" {
		t.Fatalf("map key source lost: %+v", source)
	}
	var output bytes.Buffer
	if err := WriteEffectiveConfigWithSources(&output, cfg, trace.sources); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-secret") || strings.Contains(output.String(), "api_keys") {
		t.Fatal("secret leaked through trace")
	}
	var document EffectiveConfigDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	leaves := map[string]any{}
	effectiveLeaves(document.Config, "", leaves)
	if len(leaves) != len(document.Sources) {
		t.Fatalf("source coverage mismatch: %d %d", len(leaves), len(document.Sources))
	}
	for pointer := range leaves {
		if document.Sources[pointer].Kind == "" {
			t.Fatalf("missing source for %s", pointer)
		}
	}
}

// TestEffectiveSourcesTrackEnvironmentNames observes resolved environment assignment without retaining secret-bearing values.
// TestEffectiveSourcesTrackEnvironmentNames 观察已解析环境赋值，仅保存变量名，并确认观察不会修改配置。
func TestEffectiveSourcesTrackEnvironmentNames(t *testing.T) {
	var cfg Config
	body := []byte(`{"logging":{"level":"debug"}}`)
	trace := &configSourceTrace{}
	trace.observe(cfg, ConfigValueSource{Kind: "initial"}, nil)
	if err := unmarshalStrictConfigLayer(body, &cfg); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"logging":{"level":"${CUSTOM_LEVEL}"}}`)
	if err := trace.file("user.json", body, raw, cfg); err != nil {
		t.Fatal(err)
	}
	if got := trace.sources["/logging/level"].Environment; !reflect.DeepEqual(got, []string{"CUSTOM_LEVEL"}) {
		t.Fatalf("reference names lost: %v", got)
	}
	t.Setenv("VMM_LOG_LEVEL", "debug")
	allowed := map[string]struct{}{"VMM_LOG_LEVEL": {}}
	if failures := applyEnvOverrides(&cfg, allowed); len(failures) > 0 {
		t.Fatal(failures)
	}
	before, _ := json.Marshal(cfg)
	trace.environment(cfg, allowed)
	after, _ := json.Marshal(cfg)
	if !bytes.Equal(before, after) {
		t.Fatal("source observation changed config")
	}
	if source := trace.sources["/logging/level"]; source.Kind != "environment" || !reflect.DeepEqual(source.Environment, []string{"VMM_LOG_LEVEL"}) {
		t.Fatalf("same-value override missing: %+v", source)
	}
}

// TestEffectiveEnvironmentPointersDisambiguateDottedKeys prevents flat parameter keys from claiming nested parameter variables.
// TestEffectiveEnvironmentPointersDisambiguateDottedKeys 防止含点号的扁平参数键错误归属嵌套参数的环境变量。
func TestEffectiveEnvironmentPointersDisambiguateDottedKeys(t *testing.T) {
	raw := []byte(`{"llm":{"routes":[{"params":{"a.b":"${FLAT}","a":{"b":"${NESTED}"}}}]}}`)
	body := []byte(`{"llm":{"routes":[{"params":{"a.b":"flat-value","a":{"b":"nested-value"}}}]}}`)
	var cfg Config
	trace := &configSourceTrace{}
	trace.observe(cfg, ConfigValueSource{Kind: "initial"}, nil)
	if err := unmarshalStrictConfigLayer(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := trace.file("user.json", body, raw, cfg); err != nil {
		t.Fatal(err)
	}
	for pointer, name := range map[string]string{"/llm/routes/0/params/a.b": "FLAT", "/llm/routes/0/params/a/b": "NESTED"} {
		if !reflect.DeepEqual(trace.sources[pointer].Environment, []string{name}) {
			t.Fatalf("ambiguous source for %s: %+v", pointer, trace.sources[pointer])
		}
	}
}
