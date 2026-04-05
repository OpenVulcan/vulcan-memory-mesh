// selector.go implements the shared request loop that tries eligible API keys and applies switchable key-level cooldowns.
// selector.go 用于实现共享请求循环：它会尝试可用 API Key，并对可切换的 Key 级故障应用冷却。
package ai_key_failover

import (
	"context"
	"fmt"
	"time"
)

// executeWithFailover runs one provider call against the selector's current key order and rotates only on switchable key-level failures.
// executeWithFailover 用于按选择器当前的 Key 顺序执行一次 provider 调用，并且只在可切换的 Key 级故障下轮换。
func executeWithFailover[T any](
	ctx context.Context,
	selector *selector,
	call func(context.Context, string) (T, error),
	classify func(error, time.Time) failureDecision,
) (T, error) {
	var zero T
	if selector == nil {
		return zero, fmt.Errorf("api key selector is nil")
	}
	now := time.Now().UTC()
	indexes, err := selector.candidateIndexes(now)
	if err != nil {
		return zero, err
	}
	var lastSwitchableErr error
	for _, index := range indexes {
		response, callErr := call(ctx, selector.keyForIndex(index))
		if callErr == nil {
			selector.markSuccess(index, time.Now().UTC())
			return response, nil
		}
		decision := classify(callErr, time.Now().UTC())
		if !decision.SwitchKey {
			return zero, callErr
		}
		disabledUntil := time.Time{}
		if decision.Cooldown > 0 {
			disabledUntil = time.Now().UTC().Add(decision.Cooldown)
		}
		selector.markFailure(index, decision.Class, disabledUntil, time.Now().UTC())
		lastSwitchableErr = callErr
	}
	if lastSwitchableErr != nil {
		return zero, lastSwitchableErr
	}
	return zero, fmt.Errorf("no healthy %s api keys available", selector.serviceName)
}
