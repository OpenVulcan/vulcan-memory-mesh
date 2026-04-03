// interceptors.go implements reusable gRPC unary interceptors for trace, scope resolution, logging, and panic recovery.
// interceptors.go 用于实现 gRPC 可复用的一元拦截器，覆盖 trace、范围解析、日志和 panic 恢复。
package grpcapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const traceIDHeader = "x-trace-id"

type resolvedSessionContextKey struct{}

// scopeResolvedRequest is the narrow request shape used by the scope interceptor to resolve project/user/session coordinates.
// scopeResolvedRequest 用于描述范围拦截器解析 project/user/session 坐标时需要的最小请求形态。
type scopeResolvedRequest interface {
	GetSessionId() string
	GetUserId() uint64
	GetProjectId() uint64
}

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

// ScopeResolutionInterceptor validates numeric project/user identifiers and injects the resolved session scope into context for business handlers.
// ScopeResolutionInterceptor 用于校验数字 project/user 标识，并把解析出的 session 范围注入上下文供业务处理器复用。
func ScopeResolutionInterceptor(resolver appports.RequestScopeResolver, logger *logx.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = logx.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Only the business-chain RPCs that depend on one resolved session need deterministic scope resolution before the handler starts.
		// 只有依赖已解析 session 的业务链路 RPC，才需要在处理器执行前完成确定性范围解析。
		if info == nil || !requiresResolvedScope(info.FullMethod) {
			return handler(ctx, req)
		}
		if resolver == nil {
			return nil, toStatus(withMessage(errInternal, "request scope resolver is not configured"))
		}
		payload, ok := req.(scopeResolvedRequest)
		if !ok {
			return nil, toStatus(withMessage(errInternal, "request does not expose scope fields"))
		}
		session, err := resolver.ResolveRequestScope(ctx, strings.TrimSpace(payload.GetSessionId()), payload.GetUserId(), payload.GetProjectId())
		if err != nil {
			return nil, toStatus(describeError(err))
		}
		logger.Info("grpc scope resolved", "trace_id", trace.IDFromContext(ctx), "method", info.FullMethod, "session_key", session.SessionKey, "session_id", session.SessionID, "user_id", session.UserID, "project_id", session.ProjectID)
		return handler(withResolvedSessionRef(ctx, session), req)
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
		method := unaryMethodName(info)
		resp, err := handler(ctx, req)
		code := status.Code(err)
		args := []any{
			"trace_id", trace.IDFromContext(ctx),
			"method", method,
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

// RecoveryInterceptor converts unexpected panics into stable internal gRPC errors.
// RecoveryInterceptor 用于把未预期的 panic 转换成稳定的 gRPC 内部错误。
func RecoveryInterceptor(logger *logx.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = logx.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (_ any, err error) {
		// Recover at the transport edge so panics never leak across the gRPC boundary.
		// 在传输层边界做 recover，确保 panic 不会越过 gRPC 边界泄露出去。
		method := unaryMethodName(info)
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", append([]any{
					"trace_id", trace.IDFromContext(ctx),
					"method", method,
				}, redactedPanicLogFields(rec)...)...)
				err = toStatus(errInternal)
			}
		}()
		return handler(ctx, req)
	}
}

// redactedPanicLogFields converts one panic value into stable diagnostics so recovery logs stay useful without writing raw panic payloads into runtime logs.
// redactedPanicLogFields 用于把 panic 值转换成稳定的诊断字段，让 recovery 日志保持可排障，同时不把原始 panic 载荷写入运行时日志。
func redactedPanicLogFields(rec any) []any {
	raw := strings.TrimSpace(fmt.Sprint(rec))
	return []any{
		"panic_type", fmt.Sprintf("%T", rec),
		"panic_len", len(raw),
		"panic_sha256", shortContentDigest(raw),
	}
}

// unaryMethodName returns a stable method label even when callers invoke exported interceptors with a nil UnaryServerInfo during tests or manual integration checks.
// unaryMethodName 用于在测试或手工集成检查里传入 nil UnaryServerInfo 时，仍返回稳定的方法标签，避免导出拦截器直接崩溃。
func unaryMethodName(info *grpc.UnaryServerInfo) string {
	if info == nil {
		return "unknown"
	}
	return strings.TrimSpace(info.FullMethod)
}

// withResolvedSessionRef stores the resolved session scope into context so handlers can reuse one shared lookup result.
// withResolvedSessionRef 用于把解析好的 session 范围写入上下文，让处理器复用同一份查询结果。
func withResolvedSessionRef(ctx context.Context, session logicdomain.SessionRef) context.Context {
	return context.WithValue(ctx, resolvedSessionContextKey{}, session)
}

// resolvedSessionRefFromContext extracts the resolved session scope prepared by the scope interceptor.
// resolvedSessionRefFromContext 用于提取由范围拦截器预先放入上下文的 session 范围。
func resolvedSessionRefFromContext(ctx context.Context) (logicdomain.SessionRef, bool) {
	session, ok := ctx.Value(resolvedSessionContextKey{}).(logicdomain.SessionRef)
	return session, ok
}

// requiresResolvedScope reports whether one gRPC method belongs to the business chain that needs deterministic project/user scope resolution.
// requiresResolvedScope 用于判断某个 gRPC 方法是否属于需要确定性 project/user 范围解析的业务链路。
func requiresResolvedScope(fullMethod string) bool {
	switch fullMethod {
	case "/vmm.v1.VMMService/PreCheck", "/vmm.v1.VMMService/PostAction", "/vmm.v1.VMMService/WriteMemories":
		return true
	default:
		return false
	}
}
