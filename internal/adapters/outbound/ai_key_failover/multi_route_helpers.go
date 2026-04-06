// multi_route_helpers.go provides shared helpers for ordered route failover above the fixed-model, per-key failover clients.
// multi_route_helpers.go 用于提供有序路由容灾的共享辅助逻辑，服务于固定模型、每 Key 容灾客户端之上的多路由切换。
package ai_key_failover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// buildMultiRouteName returns one stable route label so logs and error paths can still point back to the configured route order when callers omit an explicit route name.
// buildMultiRouteName 用于返回稳定的路由标签，让调用方省略显式路由名时，日志和错误链路仍能回指到配置中的路由顺序。
func buildMultiRouteName(serviceName string, routeIndex int, routeName string) string {
	if trimmed := strings.TrimSpace(routeName); trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-route-%d", serviceName, routeIndex+1)
}

// shouldAbortRouteFailover reports whether the current request context has already been canceled or expired, in which case trying another route would only repeat the same failure.
// shouldAbortRouteFailover 用于判断当前请求上下文是否已经取消或过期；若已如此，再尝试下一条路由只会重复同样的失败。
func shouldAbortRouteFailover(ctx context.Context, err error) bool {
	if ctx == nil {
		return false
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded)
	}
	return false
}

// shouldSwitchRoute reports whether the current route failure should fan out to the next configured route after the route-local key pool has already been exhausted or proven unhealthy.
// shouldSwitchRoute 用于判断当前路由失败后是否应该扩散到下一条配置路由；调用它时，当前路由本地的 Key 池已经先被耗尽或证明不健康。
func shouldSwitchRoute(err error, classify func(error, time.Time) failureDecision) bool {
	if err == nil {
		return false
	}
	if isExhaustedCandidatesError(err) {
		return true
	}
	if classify == nil {
		return false
	}
	decision := classify(err, time.Now().UTC())
	switch decision.Class {
	// Route-level failover must still continue on invalid_request because one heterogeneous backup route
	// may accept the same logical request even when the current provider/model rejects its route-local payload.
	// 路由级容灾在 invalid_request 时也必须继续，因为异构备用路由仍可能接受同一逻辑请求，
	// 即使当前 provider/model 因自身路由级载荷约束而拒绝了这次调用。
	case errorClassNone, errorClassUnknown:
		return false
	default:
		return true
	}
}
