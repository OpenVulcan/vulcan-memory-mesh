// vector_schema_sync.go coordinates vector-schema version upgrades from the application composition root.
// vector_schema_sync.go 用于在应用组合根协调向量 schema 版本升级。
package app

import (
	"context"
	"fmt"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// vectorSchemaResetter is the narrow adapter capability required to rebuild the runtime vector table when its schema version changes.
// vectorSchemaResetter 用于描述当向量 schema 版本变化时，重建运行时向量表所需的最小适配器能力。
type vectorSchemaResetter interface {
	RecreateTable(ctx context.Context) error
}

// ensureVectorSchema upgrades the vector table only when the tracked LanceDB schema version changes, then rebuilds rows from SQLite as the durable source of truth.
// ensureVectorSchema 用于仅在 LanceDB schema 版本变化时升级向量表，并把 SQLite 作为长期事实源重新回灌向量行。
func ensureVectorSchema(ctx context.Context, versions appports.SchemaVersionStore, workspace appports.WorkspaceStore, vector appports.VectorStore, logger *logx.Logger) error {
	if versions == nil {
		return fmt.Errorf("schema version store is not configured")
	}
	if workspace == nil {
		return fmt.Errorf("workspace store is not configured")
	}
	if vector == nil {
		return fmt.Errorf("vector store is not configured")
	}
	resetter, ok := vector.(vectorSchemaResetter)
	if !ok {
		return fmt.Errorf("vector store does not support schema recreation")
	}
	if logger == nil {
		logger = logx.Default()
	}

	currentVersion, err := versions.GetSchemaComponentVersion(ctx, "lancedb")
	if err != nil {
		return err
	}
	if currentVersion == vldb_lancedb.CurrentSchemaVersion {
		return nil
	}
	if currentVersion > vldb_lancedb.CurrentSchemaVersion {
		return fmt.Errorf("tracked lancedb schema version %d is newer than runtime target %d", currentVersion, vldb_lancedb.CurrentSchemaVersion)
	}

	// Load durable memory rows before rebuilding the vector table so schema upgrades replay exactly what SQLite currently considers active.
	// 在重建向量表前先加载长期记忆行，确保 schema 升级只回放 SQLite 当前认定为 active 的数据。
	projects, err := workspace.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list projects for lancedb schema rebuild: %w", err)
	}
	rebuildRecords := make([]logicdomain.MemoryRecord, 0)
	for _, project := range projects {
		projectRecords, listErr := workspace.ListProjectMemories(ctx, project.ID)
		if listErr != nil {
			return fmt.Errorf("list project %d memories for lancedb schema rebuild: %w", project.ID, listErr)
		}
		rebuildRecords = append(rebuildRecords, projectRecords...)
	}

	logger.Info("lancedb schema rebuild starting", "from_version", currentVersion, "to_version", vldb_lancedb.CurrentSchemaVersion, "record_count", len(rebuildRecords))
	if err := resetter.RecreateTable(ctx); err != nil {
		return err
	}
	for idx, record := range rebuildRecords {
		if err := vector.Upsert(ctx, record); err != nil {
			return fmt.Errorf("rebuild lancedb vector row %d/%d (%s): %w", idx+1, len(rebuildRecords), record.ID, err)
		}
	}
	if err := versions.SetSchemaComponentVersion(ctx, "lancedb", vldb_lancedb.CurrentSchemaVersion); err != nil {
		return err
	}
	logger.Info("lancedb schema rebuild completed", "schema_version", vldb_lancedb.CurrentSchemaVersion, "record_count", len(rebuildRecords))
	return nil
}
