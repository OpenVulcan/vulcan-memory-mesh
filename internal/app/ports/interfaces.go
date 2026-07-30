// interfaces.go keeps the shared utility and cross-cutting ports used across multiple application workflows.
// interfaces.go 用于承载多个应用工作流共享的基础能力端口与横切接口。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// Shutdowner abstracts dependencies that must participate in application shutdown sequencing.
// Shutdowner 用于抽象需要参与应用关闭顺序控制的依赖。
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// HealthChecker verifies whether one runtime dependency is ready to serve application requests.
// HealthChecker 用于校验某个运行时依赖是否已经准备好承载应用请求。
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// NoiseTurnFilter is the port used by post-action flows to drop noisy normalized turns before relational persistence.
// NoiseTurnFilter 用于让 post-action 流程在关系持久化前过滤掉噪声标准化轮次。
type NoiseTurnFilter interface {
	FilterPersistableTurns(ctx context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn
}

// NoiseEmbeddingCache is the port used by startup processors to reuse previously computed semantic prototype vectors.
// NoiseEmbeddingCache 用于让启动期处理器复用已计算好的语义原型向量缓存。
type NoiseEmbeddingCache = logicports.NoiseEmbeddingCache

// RequestScopeResolver is the port used by gRPC interceptors to validate numeric project/user ids and auto-create sessions.
// RequestScopeResolver 用于让 gRPC 拦截器校验数字 project/user id，并在需要时自动创建 session。
type RequestScopeResolver interface {
	ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error)
}

// IDGenerator is the utility port used to create stable IDs for traces and seeded memories.
// IDGenerator 用于生成 trace 等运行时标识所需的稳定 ID。
type IDGenerator interface {
	NewID(prefix string) string
}
