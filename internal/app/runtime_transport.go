// runtime_transport.go implements the transport-side gRPC server builders used by the local runtime composition root.
// runtime_transport.go 用于实现本地运行时组合根使用的传输侧 gRPC 服务构建逻辑。
package app

import (
	grpcapi "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi"
	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

// buildGRPCKeepaliveConfiguration converts the runtime config into gRPC keepalive primitives so the server can keep long-lived unary connections healthy without changing any RPC shape.
// buildGRPCKeepaliveConfiguration 用于把运行时配置转换成 gRPC keepalive 原语，让服务端在不改变任何 RPC 形态的前提下维持长时间一元连接的健康状态。
func buildGRPCKeepaliveConfiguration(cfg config.GRPCKeepaliveConfig) (keepalive.ServerParameters, keepalive.EnforcementPolicy, bool) {
	if !cfg.Enabled {
		return keepalive.ServerParameters{}, keepalive.EnforcementPolicy{}, false
	}
	params := keepalive.ServerParameters{
		Time:                  cfg.Time.Duration,
		Timeout:               cfg.Timeout.Duration,
		MaxConnectionIdle:     cfg.MaxConnectionIdle.Duration,
		MaxConnectionAge:      cfg.MaxConnectionAge.Duration,
		MaxConnectionAgeGrace: cfg.MaxConnectionAgeGrace.Duration,
	}
	policy := keepalive.EnforcementPolicy{
		MinTime:             cfg.MinPingInterval.Duration,
		PermitWithoutStream: cfg.PermitWithoutStream,
	}
	return params, policy, true
}

// buildGRPCServerOptions assembles the transport-level gRPC server options so runtime composition stays deterministic and tests can assert keepalive wiring without booting the full app.
// buildGRPCServerOptions 用于组装传输层 gRPC Server 选项，让运行时装配保持确定性，并让测试无需启动完整应用也能断言 keepalive 已接线。
func buildGRPCServerOptions(cfg config.Config, deps grpcapi.Dependencies) []grpc.ServerOption {
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveMessageBytes),
		grpc.ChainUnaryInterceptor(grpcapi.BuildUnaryInterceptors(deps)...),
	}
	if params, policy, ok := buildGRPCKeepaliveConfiguration(cfg.GRPC.Keepalive); ok {
		options = append(options, grpc.KeepaliveParams(params), grpc.KeepaliveEnforcementPolicy(policy))
	}
	return options
}

// buildRuntimeGRPCServer creates and registers the runtime gRPC server so the composition root can treat transport assembly as one focused step.
// buildRuntimeGRPCServer 用于创建并注册运行时 gRPC 服务，让组合根可以把传输层装配视为一个聚焦步骤。
func buildRuntimeGRPCServer(cfg config.Config, deps grpcapi.Dependencies) *grpc.Server {
	server := grpc.NewServer(buildGRPCServerOptions(cfg, deps)...)
	vmmv1.RegisterVMMServiceServer(server, grpcapi.NewServer(deps))
	reflection.Register(server)
	return server
}
