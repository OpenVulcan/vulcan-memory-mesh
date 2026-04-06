// selector.go implements the shared request loop that first prechecks per-key budgets inside routing nodes and then rotates only on switchable key-level failures.
// selector.go 用于实现共享请求循环：它会先预检查路由节点内每个 Key 的预算，再仅在可切换的 Key 级故障下继续轮换。
package ai_key_failover

import (
	"context"
	"fmt"
	"time"
)

// executeWithFailover runs one provider call against eligible routing nodes and rotates only on switchable key-level failures.
// executeWithFailover 用于针对可用轮询节点执行一次 provider 调用，并且只在可切换的 Key 级故障下继续轮换。
func executeWithFailover[T any](
	ctx context.Context,
	selector *selector,
	cost requestCost,
	call func(context.Context, string) (T, error),
	classify func(error, time.Time) failureDecision,
	actualUsage func(T) requestCost,
) (T, error) {
	var zero T
	if selector == nil {
		return zero, fmt.Errorf("api key selector is nil")
	}
	cost = normalizeRequestCost(cost)
	nodeIndexes, err := selector.candidateNodeIndexes(time.Now().UTC(), cost)
	if err != nil {
		return zero, err
	}
	var lastSwitchableErr error
	for _, nodeIndex := range nodeIndexes {
		keyIndexes, keyErr := selector.candidateKeyIndexes(nodeIndex, time.Now().UTC(), cost)
		if keyErr != nil {
			continue
		}
		for _, keyIndex := range keyIndexes {
			reservedAt := time.Now().UTC()
			if !selector.reserveKeyBudget(nodeIndex, keyIndex, cost, reservedAt) {
				continue
			}
			response, callErr := call(ctx, selector.keyForIndex(nodeIndex, keyIndex))
			if callErr == nil {
				completedAt := time.Now().UTC()
				selector.markSuccess(nodeIndex, keyIndex, completedAt)
				if actualUsage != nil {
					selector.reconcileKeyBudget(nodeIndex, keyIndex, cost, actualUsage(response), completedAt)
				}
				return response, nil
			}
			decision := classify(callErr, time.Now().UTC())
			if shouldRefundReservedBudget(decision.Class) {
				selector.refundKeyBudget(nodeIndex, keyIndex, cost, time.Now().UTC())
			}
			if !decision.SwitchKey {
				return zero, callErr
			}
			disabledUntil := time.Time{}
			if decision.Cooldown > 0 {
				disabledUntil = time.Now().UTC().Add(decision.Cooldown)
			}
			selector.markFailure(nodeIndex, keyIndex, decision.Class, disabledUntil, time.Now().UTC())
			lastSwitchableErr = callErr
		}
	}
	if lastSwitchableErr != nil {
		return zero, lastSwitchableErr
	}
	return zero, fmt.Errorf("no healthy %s routing candidates available", selector.serviceName)
}

// shouldRefundReservedBudget reports whether one classified failure should release the local pre-reserved key budget so later requests are not penalized.
// shouldRefundReservedBudget 用于判断某类失败是否应该释放本地预留的 Key 预算，避免误伤后续请求。
func shouldRefundReservedBudget(class errorClass) bool {
	switch class {
	case errorClassAuth, errorClassInvalidRequest, errorClassPublicFault:
		return true
	default:
		return false
	}
}
