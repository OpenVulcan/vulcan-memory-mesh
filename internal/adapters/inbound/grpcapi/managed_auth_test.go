// managed_auth_test.go verifies managed-runtime Bearer authentication.
// managed_auth_test.go 用于验证托管运行时 Bearer 鉴权。
package grpcapi

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestBearerTokenInterceptorAcceptsOnlyTheExactToken verifies missing and mismatched credentials never reach business logic.
// TestBearerTokenInterceptorAcceptsOnlyTheExactToken 用于验证缺失或不匹配的凭据不会进入业务逻辑。
func TestBearerTokenInterceptorAcceptsOnlyTheExactToken(t *testing.T) {
	interceptor := BearerTokenInterceptor("managed-secret")
	info := &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}
	handlerCalls := 0
	handler := func(context.Context, any) (any, error) {
		handlerCalls++
		return "ok", nil
	}

	for _, authorization := range []string{"", "Bearer wrong"} {
		ctx := context.Background()
		if authorization != "" {
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", authorization))
		}
		_, err := interceptor(ctx, nil, info, handler)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("authorization %q returned %v", authorization, err)
		}
	}

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer managed-secret"),
	)
	response, err := interceptor(ctx, nil, info, handler)
	if err != nil || response != "ok" {
		t.Fatalf("authorized call response = %v, error = %v", response, err)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler calls = %d, want 1", handlerCalls)
	}
}
