// noise_rules.go implements noise rule loading and JSON decoding for the memory-admission gate.
// noise_rules.go 用于实现记忆准入门控器的噪声规则加载与 JSON 解码。
package processor

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// noiseRuleFile is the on-disk JSON container for one common or language-specific noise rule bundle.
// noiseRuleFile 用于表示磁盘上的单个公共或语言级噪声规则包 JSON 容器。
type noiseRuleFile struct {
	Language   string                    `json:"language"`
	Version    string                    `json:"version"`
	Categories []noiseCategoryDefinition `json:"categories"`
}

// noiseCategoryDefinition describes one category of turns that should be blocked before long-term persistence.
// noiseCategoryDefinition 用于描述一类需要在长期持久化前被阻断的轮次。
type noiseCategoryDefinition struct {
	Name      string   `json:"name"`
	Targets   []string `json:"targets"`
	Threshold float64  `json:"threshold,omitempty"`
	Patterns  []string `json:"patterns"`
	Phrases   []string `json:"phrases"`
}

// compiledNoiseCategory holds the immutable runtime form of one JSON category.
// compiledNoiseCategory 用于保存单个 JSON 类别的不可变运行时形态。
type compiledNoiseCategory struct {
	Name      string
	Targets   []string
	Patterns  []*regexp.Regexp
	Phrases   []string
	Vectors   [][]float32
	Threshold float64
}

// loadNoiseRuleFile reads and validates one JSON rule file from disk.
// loadNoiseRuleFile 用于从磁盘读取并校验单个 JSON 规则文件。
func loadNoiseRuleFile(path string) (noiseRuleFile, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return noiseRuleFile{}, fmt.Errorf("read noise rule file %q: %w", path, err)
	}
	var file noiseRuleFile
	if err := json.Unmarshal(body, &file); err != nil {
		return noiseRuleFile{}, fmt.Errorf("parse noise rule file %q: %w", path, err)
	}
	seen := map[string]struct{}{}
	for _, category := range file.Categories {
		name := strings.TrimSpace(category.Name)
		if name == "" {
			return noiseRuleFile{}, fmt.Errorf("noise rule file %q contains a category without name", path)
		}
		if _, ok := seen[name]; ok {
			return noiseRuleFile{}, fmt.Errorf("noise rule file %q contains duplicate category %q", path, name)
		}
		seen[name] = struct{}{}
	}
	return file, nil
}
