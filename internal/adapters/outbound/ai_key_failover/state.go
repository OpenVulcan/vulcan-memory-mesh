// state.go implements the shared in-memory API-key failover state used by fixed-model AI adapters.
// state.go 用于实现固定模型 AI 适配器共享的内存态 API Key 容灾状态。
package ai_key_failover

import (
	"fmt"
	"sync"
	"time"
)

// Options holds one fixed provider/endpoint/model key pool together with its in-memory failover policy.
// Options 用于保存一组固定 provider/endpoint/model 的 Key 池及其对应的内存态容灾策略。
type Options struct {
	ServiceName        string
	Enabled            bool
	Policy             string
	APIKeys            []string
	RespectRetryAfter  bool
	RateLimitCooldown  time.Duration
	QuotaCooldown      time.Duration
	AuthCooldown       time.Duration
	ProbeAfterCooldown bool
}

// keyState stores the volatile health markers for one API key inside the current process.
// keyState 用于保存当前进程内单个 API Key 的易失性健康状态标记。
type keyState struct {
	DisabledUntil       time.Time
	LastErrorClass      errorClass
	ConsecutiveFailures int
	LastUsedAt          time.Time
}

// selector keeps the ordered or round-robin key-pick state for one fixed-model key pool.
// selector 用于保存单个固定模型 Key 池的顺序选择或轮询选择状态。
type selector struct {
	serviceName        string
	enabled            bool
	policy             string
	apiKeys            []string
	probeAfterCooldown bool
	cursor             int
	states             []keyState
	mu                 sync.Mutex
}

// newSelector creates one selector for a fixed-model key pool and validates the minimum routing contract.
// newSelector 用于为固定模型 Key 池创建一个选择器，并校验最小路由契约。
func newSelector(options Options) (*selector, error) {
	if len(options.APIKeys) == 0 {
		return nil, fmt.Errorf("%s api key pool is empty", options.ServiceName)
	}
	return &selector{
		serviceName:        options.ServiceName,
		enabled:            options.Enabled && len(options.APIKeys) > 1,
		policy:             normalizePolicy(options.Policy),
		apiKeys:            append([]string(nil), options.APIKeys...),
		probeAfterCooldown: options.ProbeAfterCooldown,
		states:             make([]keyState, len(options.APIKeys)),
	}, nil
}

// candidateIndexes returns the current eligible key order for one request.
// candidateIndexes 用于返回单次请求当前可用的 Key 候选顺序。
func (s *selector) candidateIndexes(now time.Time) ([]int, error) {
	if s == nil {
		return nil, fmt.Errorf("api key selector is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.apiKeys) == 0 {
		return nil, fmt.Errorf("%s api key pool is empty", s.serviceName)
	}
	if !s.enabled {
		return []int{0}, nil
	}
	eligible := s.eligibleIndexesLocked(now)
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no healthy %s api keys available", s.serviceName)
	}
	if s.policy != "round_robin" {
		return eligible, nil
	}
	start := s.cursor % len(s.apiKeys)
	s.cursor = (s.cursor + 1) % len(s.apiKeys)
	ordered := make([]int, 0, len(eligible))
	for offset := 0; offset < len(s.apiKeys); offset++ {
		idx := (start + offset) % len(s.apiKeys)
		for _, candidate := range eligible {
			if candidate == idx {
				ordered = append(ordered, idx)
				break
			}
		}
	}
	return ordered, nil
}

// keyForIndex returns the concrete API key string for one candidate index.
// keyForIndex 用于返回某个候选下标对应的具体 API Key 字符串。
func (s *selector) keyForIndex(index int) string {
	if s == nil || index < 0 || index >= len(s.apiKeys) {
		return ""
	}
	return s.apiKeys[index]
}

// markSuccess clears the transient failure state for one API key after a successful provider call.
// markSuccess 用于在单次 provider 调用成功后清空某个 API Key 的瞬时失败状态。
func (s *selector) markSuccess(index int, now time.Time) {
	if s == nil || index < 0 || index >= len(s.states) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[index] = keyState{
		LastUsedAt: now,
	}
}

// markFailure records one switchable key-level failure and applies the computed cooldown window.
// markFailure 用于记录一次可切换的 Key 级故障，并应用计算后的冷却窗口。
func (s *selector) markFailure(index int, class errorClass, disabledUntil, now time.Time) {
	if s == nil || index < 0 || index >= len(s.states) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[index]
	state.LastErrorClass = class
	state.ConsecutiveFailures++
	state.LastUsedAt = now
	if !disabledUntil.IsZero() {
		state.DisabledUntil = disabledUntil
	}
	s.states[index] = state
}

// eligibleIndexesLocked returns every currently usable key index while the selector mutex is already held.
// eligibleIndexesLocked 用于在选择器互斥锁已持有时返回全部当前可用的 Key 下标。
func (s *selector) eligibleIndexesLocked(now time.Time) []int {
	eligible := make([]int, 0, len(s.states))
	for idx, state := range s.states {
		if state.DisabledUntil.IsZero() {
			eligible = append(eligible, idx)
			continue
		}
		if !state.DisabledUntil.After(now) && s.probeAfterCooldown {
			eligible = append(eligible, idx)
		}
	}
	return eligible
}

// normalizePolicy canonicalizes the selector policy token so runtime routing always compares one stable value.
// normalizePolicy 用于规范化选择器策略 token，保证运行时路由始终比较同一份稳定值。
func normalizePolicy(policy string) string {
	switch policy {
	case "round_robin":
		return "round_robin"
	default:
		return "ordered_failover"
	}
}
