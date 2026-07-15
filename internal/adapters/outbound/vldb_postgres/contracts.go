// contracts.go pins compile-time coverage for the runtime ports implemented by the PostgreSQL combined store.
// contracts.go 用于固定 PostgreSQL 组合库实现的运行时端口编译期覆盖关系。
package vldb_postgres

import (
	"github.com/openvulcan/vmm/internal/app/ports"
)

// interfaceAssertions keep the PostgreSQL combined store aligned with every runtime port it is expected to satisfy.
// interfaceAssertions 用于确保 PostgreSQL 组合库始终满足运行时装配所依赖的全部端口契约。
var (
	_ ports.VectorStore          = (*Store)(nil)
	_ ports.RelationalStore      = (*Store)(nil)
	_ ports.TurnLookupStore      = (*Store)(nil)
	_ ports.MemoryStore          = (*Store)(nil)
	_ ports.ProfileStore         = (*Store)(nil)
	_ ports.RequestScopeResolver = (*Store)(nil)
	_ ports.SchemaVersionStore   = (*Store)(nil)
	_ ports.WorkspaceStore       = (*Store)(nil)
)
