// state.go implements the shared in-memory routing state used by fixed-model AI adapters for per-key quota prechecks and key-level failover.
// state.go 用于实现固定模型 AI 适配器共享的内存态路由状态，负责每个 Key 自己的配额预检查和 Key 级故障切换。
package ai_key_failover

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// NodeOptions describes one fixed-model routing node that may own multiple API keys while assigning the same per-key RPM/TPM/RPD limits to each member.
// NodeOptions 用于描述一个固定模型轮询节点：它可以拥有多个 API Key，并给每个成员分配同一组独立的 RPM/TPM/RPD 限额。
type NodeOptions struct {
	Name    string
	APIKeys []string
	RPM     int
	TPM     int
	RPD     int
}

// Options holds one fixed provider/endpoint/model routing table together with its in-memory failover policy.
// Options 用于保存一组固定 provider/endpoint/model 的轮询表，以及对应的内存态 failover 策略。
type Options struct {
	ServiceName        string
	Enabled            bool
	Policy             string
	APIKeys            []string
	Nodes              []NodeOptions
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

// keyBudgetState stores the fixed-window request and token counters owned by one concrete API key.
// keyBudgetState 用于保存某个具体 API Key 自己持有的固定窗口请求数与 token 数统计。
type keyBudgetState struct {
	MinuteBucket   string
	MinuteRequests int
	MinuteTokens   int
	DayBucket      string
	DayRequests    int
}

// routingNode stores one routing group's fixed per-key limits together with each key's health and private runtime budget state.
// routingNode 用于保存单个路由分组的固定每 Key 限额，以及每个 Key 各自的健康状态和私有运行时预算状态。
type routingNode struct {
	name    string
	apiKeys []string
	rpm     int
	tpm     int
	rpd     int
	cursor  int
	states  []keyState
	budgets []keyBudgetState
}

// selector keeps the ordered or round-robin routing state for one fixed-model node table.
// selector 用于保存固定模型节点表的顺序或轮询路由状态。
type selector struct {
	serviceName        string
	enabled            bool
	policy             string
	probeAfterCooldown bool
	cursor             int
	nodes              []routingNode
	mu                 sync.Mutex
}

// newSelector creates one selector for a fixed-model routing table and validates the minimum routing contract.
// newSelector 用于为固定模型轮询表创建一个选择器，并校验最小路由契约。
func newSelector(options Options) (*selector, error) {
	nodes, err := buildRoutingNodes(options.ServiceName, options.APIKeys, options.Nodes)
	if err != nil {
		return nil, err
	}
	return &selector{
		serviceName:        options.ServiceName,
		enabled:            options.Enabled && countRoutingCandidates(nodes) > 1,
		policy:             normalizePolicy(options.Policy),
		probeAfterCooldown: options.ProbeAfterCooldown,
		nodes:              nodes,
	}, nil
}

// buildRoutingNodes normalizes legacy single-pool configuration into one node and validates every declared node.
// buildRoutingNodes 用于把旧版单池配置规范化成一个节点，并校验所有显式声明的节点。
func buildRoutingNodes(serviceName string, legacyAPIKeys []string, configured []NodeOptions) ([]routingNode, error) {
	if len(configured) == 0 {
		configured = []NodeOptions{{APIKeys: legacyAPIKeys}}
	}
	nodes := make([]routingNode, 0, len(configured))
	for idx, configuredNode := range configured {
		apiKeys := normalizeSelectorAPIKeys(configuredNode.APIKeys)
		if len(apiKeys) == 0 {
			return nil, fmt.Errorf("%s node %d api key pool is empty", serviceName, idx+1)
		}
		if configuredNode.RPM < 0 || configuredNode.TPM < 0 || configuredNode.RPD < 0 {
			return nil, fmt.Errorf("%s node %d rpm/tpm/rpd must be >= 0", serviceName, idx+1)
		}
		name := strings.TrimSpace(configuredNode.Name)
		if name == "" {
			name = fmt.Sprintf("%s-node-%d", serviceName, idx+1)
		}
		nodes = append(nodes, routingNode{
			name:    name,
			apiKeys: apiKeys,
			rpm:     configuredNode.RPM,
			tpm:     configuredNode.TPM,
			rpd:     configuredNode.RPD,
			states:  make([]keyState, len(apiKeys)),
			budgets: make([]keyBudgetState, len(apiKeys)),
		})
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%s routing node list is empty", serviceName)
	}
	return nodes, nil
}

// normalizeSelectorAPIKeys trims, splits, and deduplicates one routing node's API-key list while preserving caller order.
// normalizeSelectorAPIKeys 用于裁剪、分割并去重单个节点的 API Key 列表，同时保持调用方顺序不变。
func normalizeSelectorAPIKeys(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		for _, part := range splitConfigAPIKeyParts(raw) {
			if _, exists := seen[part]; exists {
				continue
			}
			seen[part] = struct{}{}
			normalized = append(normalized, part)
		}
	}
	return normalized
}

// splitConfigAPIKeyParts expands comma, semicolon, and newline separated key declarations into individual trimmed values.
// splitConfigAPIKeyParts 用于把逗号、分号和换行分隔的 key 声明展开成独立且裁剪后的值。
func splitConfigAPIKeyParts(raw string) []string {
	replacer := strings.NewReplacer("\r\n", "\n", "\r", "\n", ";", "\n", ",", "\n")
	parts := strings.Split(replacer.Replace(raw), "\n")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			normalized = append(normalized, part)
		}
	}
	return normalized
}

// countRoutingCandidates sums every concrete key slot so the selector can tell whether failover is meaningfully enabled.
// countRoutingCandidates 用于汇总所有具体的 Key 槽位，让选择器判断 failover 是否真的具备意义。
func countRoutingCandidates(nodes []routingNode) int {
	total := 0
	for _, node := range nodes {
		total += len(node.apiKeys)
	}
	return total
}

// candidateNodeIndexes returns the current eligible node order for one request while honoring per-key fixed-window budget prechecks.
// candidateNodeIndexes 用于返回单次请求当前可用的节点顺序，并遵守每个 Key 各自的固定窗口预算预检查。
func (s *selector) candidateNodeIndexes(now time.Time, cost requestCost) ([]int, error) {
	if s == nil {
		return nil, fmt.Errorf("api key selector is nil")
	}
	cost = normalizeRequestCost(cost)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.nodes) == 0 {
		return nil, fmt.Errorf("%s routing node list is empty", s.serviceName)
	}
	if !s.enabled {
		reason := s.nodeExhaustionReasonLocked(0, cost, now)
		if reason != "" {
			if reason == exhaustedCandidatesReasonBudget {
				return nil, newBudgetExhaustedCandidatesError(fmt.Sprintf("no %s api keys available within configured rpm/tpm/rpd budgets", s.serviceName))
			}
			return nil, newExhaustedCandidatesError(fmt.Sprintf("no healthy %s api keys available", s.serviceName))
		}
		return []int{0}, nil
	}
	eligible := make([]int, 0, len(s.nodes))
	sawBudgetBlockedNode := false
	for idx := range s.nodes {
		reason := s.nodeExhaustionReasonLocked(idx, cost, now)
		if reason != "" {
			if reason == exhaustedCandidatesReasonBudget {
				sawBudgetBlockedNode = true
			}
			continue
		}
		eligible = append(eligible, idx)
	}
	if len(eligible) == 0 {
		if sawBudgetBlockedNode {
			return nil, newBudgetExhaustedCandidatesError(fmt.Sprintf("no %s routing nodes available within configured rpm/tpm/rpd budgets", s.serviceName))
		}
		return nil, newExhaustedCandidatesError(fmt.Sprintf("no healthy %s routing nodes available", s.serviceName))
	}
	if s.policy != "round_robin" {
		return eligible, nil
	}
	start := s.cursor % len(s.nodes)
	s.cursor = (s.cursor + 1) % len(s.nodes)
	return reorderEligibleIndexes(start, len(s.nodes), eligible), nil
}

// candidateKeyIndexes returns the current eligible key order inside one node while skipping keys whose own budgets are already exhausted.
// candidateKeyIndexes 用于返回某个节点内部当前可用的 Key 顺序，并跳过那些自身预算已经耗尽的 Key。
func (s *selector) candidateKeyIndexes(nodeIndex int, now time.Time, cost requestCost) ([]int, error) {
	if s == nil {
		return nil, fmt.Errorf("api key selector is nil")
	}
	cost = normalizeRequestCost(cost)
	s.mu.Lock()
	defer s.mu.Unlock()
	if nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return nil, fmt.Errorf("invalid %s routing node index %d", s.serviceName, nodeIndex)
	}
	node := &s.nodes[nodeIndex]
	if !s.enabled {
		return []int{0}, nil
	}
	eligible, sawBudgetBlocked := s.eligibleKeyIndexesLocked(nodeIndex, cost, now)
	if len(eligible) == 0 {
		if sawBudgetBlocked {
			return nil, newBudgetExhaustedCandidatesError(fmt.Sprintf("no %s api keys available in %s within configured rpm/tpm/rpd budgets", s.serviceName, node.name))
		}
		return nil, newExhaustedCandidatesError(fmt.Sprintf("no healthy %s api keys available in %s", s.serviceName, node.name))
	}
	if s.policy != "round_robin" {
		return eligible, nil
	}
	start := node.cursor % len(node.apiKeys)
	node.cursor = (node.cursor + 1) % len(node.apiKeys)
	return reorderEligibleIndexes(start, len(node.apiKeys), eligible), nil
}

// reorderEligibleIndexes reorders a filtered index list by one round-robin start point while preserving only eligible members.
// reorderEligibleIndexes 用于按 round-robin 起点重排过滤后的下标列表，并且只保留可用成员。
func reorderEligibleIndexes(start, total int, eligible []int) []int {
	ordered := make([]int, 0, len(eligible))
	for offset := 0; offset < total; offset++ {
		idx := (start + offset) % total
		for _, candidate := range eligible {
			if candidate == idx {
				ordered = append(ordered, idx)
				break
			}
		}
	}
	return ordered
}

// keyForIndex returns the concrete API key string for one node/key pair.
// keyForIndex 用于返回某个节点/Key 下标组合对应的具体 API Key 字符串。
func (s *selector) keyForIndex(nodeIndex, keyIndex int) string {
	if s == nil || nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return ""
	}
	node := s.nodes[nodeIndex]
	if keyIndex < 0 || keyIndex >= len(node.apiKeys) {
		return ""
	}
	return node.apiKeys[keyIndex]
}

// markSuccess clears the transient failure state for one API key after a successful provider call.
// markSuccess 用于在单次 provider 调用成功后清空某个 API Key 的瞬时失败状态。
func (s *selector) markSuccess(nodeIndex, keyIndex int, now time.Time) {
	if s == nil || nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return
	}
	if keyIndex < 0 || keyIndex >= len(s.nodes[nodeIndex].states) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[nodeIndex].states[keyIndex] = keyState{
		LastUsedAt: now,
	}
}

// markFailure records one switchable key-level failure and applies the computed cooldown window.
// markFailure 用于记录一次可切换的 Key 级故障，并应用计算后的冷却窗口。
func (s *selector) markFailure(nodeIndex, keyIndex int, class errorClass, disabledUntil, now time.Time) {
	if s == nil || nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return
	}
	if keyIndex < 0 || keyIndex >= len(s.nodes[nodeIndex].states) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.nodes[nodeIndex].states[keyIndex]
	state.LastErrorClass = class
	state.ConsecutiveFailures++
	state.LastUsedAt = now
	if !disabledUntil.IsZero() {
		state.DisabledUntil = disabledUntil
	}
	s.nodes[nodeIndex].states[keyIndex] = state
}

// reserveKeyBudget attempts to reserve one concrete key's own RPM/TPM/RPD envelope for a concrete outbound attempt.
// reserveKeyBudget 用于尝试为某个具体 Key 的自有 RPM/TPM/RPD 预算预留一次具体的出站尝试。
func (s *selector) reserveKeyBudget(nodeIndex, keyIndex int, cost requestCost, now time.Time) bool {
	if s == nil {
		return false
	}
	cost = normalizeRequestCost(cost)
	s.mu.Lock()
	defer s.mu.Unlock()
	if nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return false
	}
	return s.reserveKeyBudgetLocked(&s.nodes[nodeIndex], keyIndex, cost, now)
}

// reconcileKeyBudget adjusts one key's reserved token usage with provider-reported actual usage after one successful call.
// reconcileKeyBudget 用于在一次成功调用后，用提供方返回的实际 token 用量回补某个 Key 已预留的预算。
func (s *selector) reconcileKeyBudget(nodeIndex, keyIndex int, reserved, actual requestCost, now time.Time) {
	if s == nil {
		return
	}
	reserved = normalizeRequestCost(reserved)
	actual = normalizeRequestCost(actual)
	s.mu.Lock()
	defer s.mu.Unlock()
	if nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return
	}
	if keyIndex < 0 || keyIndex >= len(s.nodes[nodeIndex].budgets) {
		return
	}
	node := &s.nodes[nodeIndex]
	budget := &node.budgets[keyIndex]
	s.syncKeyBudgetWindowLocked(budget, now)
	tokenDelta := actual.Tokens - reserved.Tokens
	if tokenDelta == 0 {
		return
	}
	budget.MinuteTokens += tokenDelta
	if budget.MinuteTokens < 0 {
		budget.MinuteTokens = 0
	}
}

// refundKeyBudget releases one key's previously reserved request/token envelope when the failed attempt should not keep consuming local budget.
// refundKeyBudget 用于在失败尝试不应继续占用本地预算时，释放某个 Key 之前预留的请求/Token 配额。
func (s *selector) refundKeyBudget(nodeIndex, keyIndex int, reserved requestCost, now time.Time) {
	if s == nil {
		return
	}
	reserved = normalizeRequestCost(reserved)
	s.mu.Lock()
	defer s.mu.Unlock()
	if nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return
	}
	if keyIndex < 0 || keyIndex >= len(s.nodes[nodeIndex].budgets) {
		return
	}
	node := &s.nodes[nodeIndex]
	budget := &node.budgets[keyIndex]
	s.syncKeyBudgetWindowLocked(budget, now)
	budget.MinuteRequests -= reserved.Requests
	if budget.MinuteRequests < 0 {
		budget.MinuteRequests = 0
	}
	budget.MinuteTokens -= reserved.Tokens
	if budget.MinuteTokens < 0 {
		budget.MinuteTokens = 0
	}
	budget.DayRequests -= reserved.Requests
	if budget.DayRequests < 0 {
		budget.DayRequests = 0
	}
}

// eligibleKeyIndexesLocked returns every currently usable key index together with a marker that tells callers whether any healthy key was skipped only because its local budget window is still closed.
// eligibleKeyIndexesLocked 用于在选择器互斥锁已持有时返回全部当前可用的 Key 下标，并额外告知调用方是否存在“仅因本地预算窗口未恢复而被跳过”的健康 Key。
func (s *selector) eligibleKeyIndexesLocked(nodeIndex int, cost requestCost, now time.Time) ([]int, bool) {
	if nodeIndex < 0 || nodeIndex >= len(s.nodes) {
		return nil, false
	}
	node := &s.nodes[nodeIndex]
	eligible := make([]int, 0, len(node.states))
	sawBudgetBlocked := false
	for idx, state := range node.states {
		if !s.isKeyHealthyLocked(state, now) {
			continue
		}
		if !s.canReserveKeyBudgetLocked(node, idx, cost, now) {
			sawBudgetBlocked = true
			continue
		}
		eligible = append(eligible, idx)
	}
	return eligible, sawBudgetBlocked
}

// nodeExhaustionReasonLocked summarizes why one routing node cannot currently produce an eligible key while the selector mutex is already held.
// nodeExhaustionReasonLocked 用于在选择器互斥锁已持有时，总结某个路由节点当前无法产出可用 Key 的原因。
func (s *selector) nodeExhaustionReasonLocked(nodeIndex int, cost requestCost, now time.Time) exhaustedCandidatesReason {
	eligible, sawBudgetBlocked := s.eligibleKeyIndexesLocked(nodeIndex, cost, now)
	if len(eligible) > 0 {
		return ""
	}
	if sawBudgetBlocked {
		return exhaustedCandidatesReasonBudget
	}
	return exhaustedCandidatesReasonUnavailable
}

// isKeyHealthyLocked reports whether one key can currently participate in routing based on its cooldown state.
// isKeyHealthyLocked 用于根据冷却状态判断某个 Key 当前是否还能参与路由。
func (s *selector) isKeyHealthyLocked(state keyState, now time.Time) bool {
	if state.DisabledUntil.IsZero() {
		return true
	}
	return !state.DisabledUntil.After(now) && s.probeAfterCooldown
}

// nodeHasCandidateKeyLocked reports whether one node still has at least one healthy key whose own budget can accept the request.
// nodeHasCandidateKeyLocked 用于判断某个节点是否仍有至少一个健康且自身预算足以承接请求的 Key。
func (s *selector) nodeHasCandidateKeyLocked(nodeIndex int, cost requestCost, now time.Time) bool {
	eligible, _ := s.eligibleKeyIndexesLocked(nodeIndex, cost, now)
	return len(eligible) > 0
}

// canReserveKeyBudgetLocked checks one key's private limits without mutating counters while the selector mutex is already held.
// canReserveKeyBudgetLocked 用于在选择器互斥锁已持有时检查某个 Key 的私有配额是否足够，但不修改计数器。
func (s *selector) canReserveKeyBudgetLocked(node *routingNode, keyIndex int, cost requestCost, now time.Time) bool {
	if node == nil {
		return false
	}
	if keyIndex < 0 || keyIndex >= len(node.budgets) {
		return false
	}
	budget := &node.budgets[keyIndex]
	s.syncKeyBudgetWindowLocked(budget, now)
	if node.rpm > 0 && budget.MinuteRequests+cost.Requests > node.rpm {
		return false
	}
	if node.tpm > 0 && budget.MinuteTokens+cost.Tokens > node.tpm {
		return false
	}
	if node.rpd > 0 && budget.DayRequests+cost.Requests > node.rpd {
		return false
	}
	return true
}

// reserveKeyBudgetLocked mutates one key's counters after the caller has decided to spend budget on a concrete attempt.
// reserveKeyBudgetLocked 用于在调用方决定消耗预算后，更新某个 Key 的私有计数器。
func (s *selector) reserveKeyBudgetLocked(node *routingNode, keyIndex int, cost requestCost, now time.Time) bool {
	if !s.canReserveKeyBudgetLocked(node, keyIndex, cost, now) {
		return false
	}
	budget := &node.budgets[keyIndex]
	budget.MinuteRequests += cost.Requests
	budget.MinuteTokens += cost.Tokens
	budget.DayRequests += cost.Requests
	return true
}

// syncKeyBudgetWindowLocked rolls minute/day windows when time moves forward so one key's stale counters do not leak across quota windows.
// syncKeyBudgetWindowLocked 用于在时间前进时滚动分钟/天窗口，避免某个 Key 的旧计数跨配额窗口泄漏。
func (s *selector) syncKeyBudgetWindowLocked(budget *keyBudgetState, now time.Time) {
	if budget == nil {
		return
	}
	minuteBucket := now.UTC().Format("200601021504")
	if budget.MinuteBucket != minuteBucket {
		budget.MinuteBucket = minuteBucket
		budget.MinuteRequests = 0
		budget.MinuteTokens = 0
	}
	dayBucket := now.UTC().Format("20060102")
	if budget.DayBucket != dayBucket {
		budget.DayBucket = dayBucket
		budget.DayRequests = 0
	}
}

// normalizePolicy canonicalizes the selector policy token so runtime routing always compares one stable value.
// normalizePolicy 用于规范化选择器策略 token，保证运行时路由始终比较同一份稳定值。
func normalizePolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "round_robin":
		return "round_robin"
	default:
		return "ordered_failover"
	}
}
