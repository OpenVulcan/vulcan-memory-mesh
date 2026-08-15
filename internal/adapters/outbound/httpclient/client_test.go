// client_test.go verifies bounded outbound connection ownership without reintroducing one global request deadline.
// client_test.go 用于验证出站连接的有界所有权，同时避免重新引入全局请求截止时间。
package httpclient

import (
	"net/http"
	"testing"
	"time"
)

// TestNewBoundedCapsConnectionsWithoutGlobalTimeout verifies caller contexts remain the only request-duration authority.
// TestNewBoundedCapsConnectionsWithoutGlobalTimeout 用于验证调用方 context 仍是请求时长的唯一权威。
func TestNewBoundedCapsConnectionsWithoutGlobalTimeout(t *testing.T) {
	client := NewBounded(13)
	if client.Timeout != 0 {
		t.Fatalf("client timeout = %s, want zero", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.MaxConnsPerHost != 13 || transport.MaxIdleConnsPerHost != 13 {
		t.Fatalf("connection limits = active:%d idle:%d, want 13", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout <= 0*time.Second {
		t.Fatalf("idle connection timeout = %s, want positive", transport.IdleConnTimeout)
	}
}

// TestSharedDefaultReusesOneProcessWideTransport verifies route and key fan-out cannot allocate independent standalone connection pools.
// TestSharedDefaultReusesOneProcessWideTransport 用于验证路由与备用 Key 扩展不能分配彼此独立的连接池。
func TestSharedDefaultReusesOneProcessWideTransport(t *testing.T) {
	first := SharedDefault()
	second := SharedDefault()
	if first != second {
		t.Fatal("shared default clients do not have identical process-wide ownership")
	}
	transport, ok := first.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("shared transport type = %T, want *http.Transport", first.Transport)
	}
	if transport.MaxConnsPerHost != DefaultMaxConnectionsPerHost ||
		transport.MaxIdleConnsPerHost != DefaultMaxConnectionsPerHost {
		t.Fatalf(
			"shared connection limits = active:%d idle:%d, want %d",
			transport.MaxConnsPerHost,
			transport.MaxIdleConnsPerHost,
			DefaultMaxConnectionsPerHost,
		)
	}
	if first.Timeout != 0 {
		t.Fatalf("shared client timeout = %s, want zero", first.Timeout)
	}
}

// TestNewBoundedUsesDefaultForInvalidLimit verifies invalid standalone values cannot silently disable the concurrency ceiling.
// TestNewBoundedUsesDefaultForInvalidLimit 用于验证无效独立配置不会静默取消并发上限。
func TestNewBoundedUsesDefaultForInvalidLimit(t *testing.T) {
	transport := NewBounded(0).Transport.(*http.Transport)
	if transport.MaxConnsPerHost != DefaultMaxConnectionsPerHost {
		t.Fatalf("max connections per host = %d, want %d", transport.MaxConnsPerHost, DefaultMaxConnectionsPerHost)
	}
}
