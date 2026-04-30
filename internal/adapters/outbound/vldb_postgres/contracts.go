// contracts.go pins compile-time port coverage for the PostgreSQL combined store so future partial migrations fail at build time instead of at runtime.
// contracts.go 用于固定 PostgreSQL 组合库的编译期端口覆盖，避免未来出现“只迁移一半”却要到运行时才暴露的问题。
package vldb_postgres

import (
	"context"
	"time"

	"github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// sessionCompactStoreContract captures the chat-compact method that is wired outside the shared ports package.
// sessionCompactStoreContract 用于承载共享 ports 包之外的 chat-compact 方法契约。
type sessionCompactStoreContract interface {
	MarkSessionCompacted(ctx context.Context, session logicdomain.SessionRef, compactedAt time.Time) (uint64, bool, error)
}

// interfaceAssertions keep the PostgreSQL combined store aligned with every runtime port it is expected to satisfy.
// interfaceAssertions 用于确保 PostgreSQL 组合库始终满足运行时装配所依赖的全部端口契约。
var (
	_ ports.VectorStore           = (*Store)(nil)
	_ ports.RelationalStore       = (*Store)(nil)
	_ ports.TurnLookupStore       = (*Store)(nil)
	_ ports.MemoryStore           = (*Store)(nil)
	_ ports.ProfileStore          = (*Store)(nil)
	_ ports.RequestScopeResolver  = (*Store)(nil)
	_ ports.SchemaVersionStore    = (*Store)(nil)
	_ ports.WorkspaceStore        = (*Store)(nil)
	_ sessionCompactStoreContract = (*Store)(nil)
)
