// client.go provides bounded shared HTTP transports for outbound AI adapters.
// client.go 用于为出站 AI 适配器提供连接数有界的共享 HTTP transport。
package httpclient

import (
	"net/http"
	"time"
)

const (
	// DefaultMaxConnectionsPerHost caps standalone provider concurrency when no managed value exists.
	// DefaultMaxConnectionsPerHost 用于在没有托管配置值时限制独立供应商的并发连接数。
	DefaultMaxConnectionsPerHost = 8
)

// sharedDefaultClient owns the single process-wide standalone transport so route and key fan-out cannot multiply the per-host connection ceiling.
// sharedDefaultClient 持有独立运行模式下唯一的进程级 Transport，避免路由与备用 Key 扩展放大每主机连接上限。
var sharedDefaultClient = NewBounded(DefaultMaxConnectionsPerHost)

// SharedDefault returns the process-wide bounded client used by every standalone provider adapter.
// SharedDefault 返回所有独立供应商适配器共同使用的进程级有界客户端。
//
// The returned client is safe for concurrent use and must not be mutated by callers.
// 返回的客户端可安全并发使用，调用方不得修改它。
//
// Returns:
// 返回值：
// The immutable process-wide HTTP client whose transport enforces the standalone per-host ceiling.
// 返回由 Transport 强制执行独立模式每主机上限的不可变进程级 HTTP 客户端。
func SharedDefault() *http.Client {
	return sharedDefaultClient
}

// NewBounded creates an HTTP client whose connection pool is capped per host while request duration remains owned by caller contexts.
// NewBounded 用于创建按主机限制连接池且由调用方 context 管理请求时长的 HTTP 客户端。
//
// Parameters:
// 参数：
//   - maxConnectionsPerHost: positive active and idle connection ceiling for each upstream host; non-positive values select the standalone default.
//   - maxConnectionsPerHost：每个上游主机的正数活动与空闲连接上限；非正数使用独立模式默认值。
//
// Returns:
// 返回值：
//   - *http.Client: a caller-owned client with no global request timeout and one bounded cloned transport.
//   - *http.Client：不含全局请求超时并持有一份有界克隆 Transport 的调用方自有客户端。
func NewBounded(maxConnectionsPerHost int) *http.Client {
	if maxConnectionsPerHost <= 0 {
		maxConnectionsPerHost = DefaultMaxConnectionsPerHost
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		base = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	transport := base.Clone()
	transport.MaxConnsPerHost = maxConnectionsPerHost
	transport.MaxIdleConnsPerHost = maxConnectionsPerHost
	if transport.MaxIdleConns < maxConnectionsPerHost {
		transport.MaxIdleConns = maxConnectionsPerHost
	}
	return &http.Client{Transport: transport}
}
