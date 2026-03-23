// interfaces.go declares application-facing storage and utility ports used by use cases.
// interfaces.go 用于声明应用层用例依赖的存储与基础能力端口。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Shutdowner abstracts dependencies that must participate in application shutdown sequencing.
// Shutdowner 用于抽象需要参与应用关闭顺序控制的依赖。
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// EmbeddingRequest carries the normalized embedding call parameters passed from use cases to adapters.
// EmbeddingRequest 用于承载从用例层传给适配器的标准化 embedding 调用参数。
type EmbeddingRequest struct {
	Model         string
	Texts         []string
	Dimension     int
	ProviderHints map[string]any
}

// EmbeddingResponse returns vectors produced by the configured embedding backend.
// EmbeddingResponse 用于返回当前 embedding 后端生成的向量结果。
type EmbeddingResponse struct {
	Vectors [][]float32
}

// EmbeddingClient is the port used by use cases to obtain embeddings without depending on a concrete SDK.
// EmbeddingClient 用于让用例层在不依赖具体 SDK 的前提下获取向量表示。
type EmbeddingClient interface {
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// VectorStore is the port used to persist and search memory vectors inside the recall pipeline.
// VectorStore 用于抽象记忆召回流水线中的向量写入与检索能力。
type VectorStore interface {
	Upsert(ctx context.Context, record logicdomain.MemoryRecord) error
	Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error)
	Shutdowner
}

// RelationalStore is the port used by post-action flows to persist cleaned chat turns and session metadata.
// RelationalStore 用于给 post-action 流程持久化清洗后的对话轮次和会话元数据。
type RelationalStore interface {
	UpsertChatLogs(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.NormalizedTurn) error
	RefreshSession(ctx context.Context, session logicdomain.SessionRef) error
	Shutdowner
}

// NoiseTurnFilter is the port used by post-action flows to drop noisy normalized turns before relational persistence.
// NoiseTurnFilter 用于让 post-action 流程在关系持久化前过滤掉噪声标准化轮次。
type NoiseTurnFilter interface {
	FilterPersistableTurns(ctx context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn
}

// MemoryArchiveStore is the port used by the /chat archive flow to persist scrubbed messages without storing raw PII.
// MemoryArchiveStore 用于让 /chat 归档流程在不保存原始敏感信息的前提下持久化已脱敏消息。
type MemoryArchiveStore interface {
	SaveMemory(ctx context.Context, record logicdomain.ArchivedMemory) error
	Shutdowner
}

// ContextPersonaProvider is the port used by pre-check flows to load stable persona and project context.
// ContextPersonaProvider 用于给 pre-check 流程加载稳定的画像和项目上下文。
type ContextPersonaProvider interface {
	Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error)
}

// TextScrubber is the utility port used by chat archive flows to scrub PII according to the active language rules.
// TextScrubber 用于让聊天归档流程根据当前语言规则执行 PII 脱敏。
type TextScrubber interface {
	Scrub(text string, lang string) string
}

// IDGenerator is the utility port used to create stable IDs for traces and seeded memories.
// IDGenerator 用于生成 trace 和 seed-memory 等场景所需的稳定 ID。
type IDGenerator interface {
	NewID(prefix string) string
}
