// interceptors.go implements reusable gRPC unary interceptors for the inbound adapter.
// interceptors.go 用于实现入站适配层复用的 gRPC 一元拦截器。
package grpcapi

import (
	"context"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const traceIDHeader = "x-trace-id"

// TraceIDInterceptor reuses inbound trace ids or generates a new one before the RPC reaches business code.
// TraceIDInterceptor 用于在 RPC 进入业务代码前复用上游 trace id，或生成新的 trace id。
func TraceIDInterceptor(ids appports.IDGenerator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Resolve the canonical trace id first so every downstream log line can share the same identifier.
		// 先解析标准 trace id，确保下游所有日志行都能共享同一个标识。
		traceID := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(traceIDHeader); len(values) > 0 {
				traceID = strings.TrimSpace(values[0])
			}
		}
		if traceID == "" && ids != nil {
			traceID = ids.NewID("trc")
		}
		if traceID != "" {
			_ = grpc.SetHeader(ctx, metadata.Pairs(traceIDHeader, traceID))
			ctx = trace.WithTraceID(ctx, traceID)
		}
		return handler(ctx, req)
	}
}

// RecoveryInterceptor converts unexpected panics into stable internal gRPC errors.
// RecoveryInterceptor 用于把未预期的 panic 转换成稳定的 gRPC 内部错误。
func RecoveryInterceptor(logger *logx.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = logx.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ any, err error) {
		// Recover at the transport edge so panics never leak across the gRPC boundary.
		// 在传输层边界做 recover，确保 panic 不会越过 gRPC 边界泄露出去。
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", "trace_id", trace.IDFromContext(ctx), "method", info.FullMethod, "err", rec)
				err = toStatus(errInternal)
			}
		}()
		return handler(ctx, req)
	}
}

// RequestLoggerInterceptor emits one structured log line per unary RPC.
// RequestLoggerInterceptor 用于为每个一元 RPC 输出一条结构化日志。
func RequestLoggerInterceptor(logger *logx.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = logx.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Capture timing, peer address, and status code after the RPC completes.
		// 在 RPC 完成后采集耗时、对端地址和状态码。
		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		args := []any{
			"trace_id", trace.IDFromContext(ctx),
			"method", info.FullMethod,
			"code", code.String(),
			"latency", time.Since(start).String(),
		}
		if p, ok := peer.FromContext(ctx); ok && p != nil && p.Addr != nil {
			args = append(args, "client_ip", p.Addr.String())
		}
		if code == codes.OK {
			logger.Info("grpc request", args...)
		} else {
			logger.Warn("grpc request", args...)
		}
		return resp, err
	}
}
