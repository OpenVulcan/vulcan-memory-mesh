// snapshot.go defines the shared split-to-combined migration payload exchanged between the SQLite exporter and PostgreSQL importer.
// snapshot.go 用于定义 SQLite 导出器与 PostgreSQL 导入器之间共享的 split-to-combined 迁移载荷。
package storagemigrate

import logicdomain "github.com/openvulcan/vmm/internal/logic/domain"

// Snapshot carries one complete VMM managed-data export produced from the split SQLite source of truth.
// Snapshot 用于承载从 split 模式 SQLite 事实库导出的整套 VMM 受管数据。
type Snapshot struct {
	NoiseEmbeddings     []logicdomain.NoiseEmbeddingCacheEntry
	Users               []logicdomain.UserRecord
	Teams               []logicdomain.TeamRecord
	Spaces              []logicdomain.SpaceRecord
	Projects            []logicdomain.ProjectRecord
	Sessions            []logicdomain.SessionRecord
	Turns               []logicdomain.SessionTurnRecord
	MemoryNodes         []logicdomain.MemoryNodeRecord
	MemoryContextEdges  []logicdomain.MemoryContextEdge
	ProfileNodes        []logicdomain.ProfileNodeRecord
	ProfileInstructions []logicdomain.ProfileInstructionRecord
}

// Report summarizes how many managed rows were exported or imported for each logical table.
// Report 用于汇总每张逻辑表在导出或导入过程中处理的受管行数。
type Report struct {
	NoiseEmbeddings     int
	Users               int
	Teams               int
	Spaces              int
	Projects            int
	Sessions            int
	Turns               int
	MemoryNodes         int
	MemoryContextEdges  int
	ProfileNodes        int
	ProfileInstructions int
}

// BuildReport derives one stable row-count summary from the current snapshot so CLI tools can print deterministic migration statistics.
// BuildReport 用于从当前快照中推导稳定的行数汇总，便于 CLI 工具输出确定性的迁移统计。
func (s Snapshot) BuildReport() Report {
	return Report{
		NoiseEmbeddings:     len(s.NoiseEmbeddings),
		Users:               len(s.Users),
		Teams:               len(s.Teams),
		Spaces:              len(s.Spaces),
		Projects:            len(s.Projects),
		Sessions:            len(s.Sessions),
		Turns:               len(s.Turns),
		MemoryNodes:         len(s.MemoryNodes),
		MemoryContextEdges:  len(s.MemoryContextEdges),
		ProfileNodes:        len(s.ProfileNodes),
		ProfileInstructions: len(s.ProfileInstructions),
	}
}

// TotalRows returns the aggregate managed-row count across all exported/imported logical tables.
// TotalRows 用于返回所有逻辑表导出或导入的受管行总数。
func (r Report) TotalRows() int {
	return r.NoiseEmbeddings +
		r.Users +
		r.Teams +
		r.Spaces +
		r.Projects +
		r.Sessions +
		r.Turns +
		r.MemoryNodes +
		r.MemoryContextEdges +
		r.ProfileNodes +
		r.ProfileInstructions
}
