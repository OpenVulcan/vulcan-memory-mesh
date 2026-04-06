// pii_redaction_test.go provides shared test doubles for the pre-check and post-action PII redaction paths.
// pii_redaction_test.go 用于为 pre-check 与 post-action 的 PII 脱敏路径提供共享测试桩。
package usecase

import "strings"

// stubPIIScrubber replaces configured literals so unit tests can assert where the use-case layer applies redaction without depending on the real regex rule engine.
// stubPIIScrubber 用于替换预设字面量，让单元测试无需依赖真实正则规则引擎，也能断言用例层的脱敏落点。
type stubPIIScrubber struct {
	replacements map[string]string
}

// Scrub executes the configured literal replacements in sequence, mimicking a deterministic masking pass for focused unit tests.
// Scrub 用于按顺序执行预设的字面量替换，在聚焦型单元测试中模拟一次确定性的脱敏过程。
func (s stubPIIScrubber) Scrub(text string) string {
	out := text
	for from, to := range s.replacements {
		out = strings.ReplaceAll(out, from, to)
	}
	return out
}

// countingPIIScrubber counts how many times the use-case layer asks for one masking pass, so tests can detect accidental duplicate scrubbing in one chain.
// countingPIIScrubber 用于统计用例层实际触发了多少次脱敏，方便测试识别同一条链路里的重复脱敏。
type countingPIIScrubber struct {
	calls int
}

// Scrub records one invocation and returns the original text unchanged, so tests can isolate whether the code path still performs unnecessary extra masking.
// Scrub 用于记录一次调用并原样返回文本，方便测试只关注代码路径是否还在执行不必要的额外脱敏。
func (s *countingPIIScrubber) Scrub(text string) string {
	if s != nil {
		s.calls++
	}
	return text
}
