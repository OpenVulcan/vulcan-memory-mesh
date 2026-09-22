// vector_rebuild.go coordinates one-shot durable/vector-store rebuilds so maintenance tools can refresh embeddings after the configured model changes.
// vector_rebuild.go 用于编排一次性 durable/向量库重建流程，让维护工具在切换配置中的 embedding 模型后刷新向量。
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

const (
	// vectorRebuildBudgetRetryDelay keeps the maintenance loop patient when every configured key is only temporarily blocked by the current RPM/TPM/RPD budget window.
	// vectorRebuildBudgetRetryDelay 用于在所有已配置 Key 只是暂时被 RPM/TPM/RPD 预算窗口卡住时，让维护流程保持耐心等待。
	vectorRebuildBudgetRetryDelay = 30 * time.Second

	// vectorRebuildWriteBatchSize keeps split-mode durable/vector refill writes bounded without coupling storage write chunking to the embedding model's API batch contract.
	// vectorRebuildWriteBatchSize 用于让 split 模式的 durable/向量回填写入保持有界，同时避免把存储写入分批错误绑死到 embedding 模型的 API 批契约上。
	vectorRebuildWriteBatchSize = 10

	// nativeVectorRebuildRecoveryTimeout bounds detached publication after a cancelled repair attempt.
	// nativeVectorRebuildRecoveryTimeout 限制取消后的脱离修复发布时长。
	nativeVectorRebuildRecoveryTimeout = 10 * time.Minute
)

// vectorRebuildResetter is the narrow vector-store capability needed by split-mode rebuilds that must recreate the sidecar LanceDB table from the durable SQL source.
// vectorRebuildResetter 用于描述分离模式重建所需的最小向量库能力：先重建旁路 LanceDB 表，再从长期 SQL 事实源回灌。
type vectorRebuildResetter interface {
	RecreateTable(ctx context.Context) error
}

// vectorRebuildDurableResetter is the split-mode durable-store capability used to clear stale SQLite vector payloads before the detached vector store is recreated from the same empty baseline.
// vectorRebuildDurableResetter 用于描述 split 模式 durable 存储的能力：先清空陈旧 SQLite 向量载荷，再让独立向量库从同一个空基线重建。
type vectorRebuildDurableResetter interface {
	ClearMemoryVectors(ctx context.Context, vectorIDs []string) error
}

// nativeVectorRebuildRestorableTrashWalker is the narrow durable read contract required to avoid dropping recoverable trash vectors during native table recreation.
// nativeVectorRebuildRestorableTrashWalker 是原生表重建前必须具备的 durable 读取契约，用于避免丢失可恢复 trash 向量。
type nativeVectorRebuildRestorableTrashRowsWalker interface {
	WalkNativeMigrationRestorableTrashRows(ctx context.Context, now time.Time, visit func(vldb_sqlite.NativeRestorableTrashMemory) error) error
}

// nativeVectorRebuildMemoryWalker reads all native durable memory facts for a fixed observation time.
// nativeVectorRebuildMemoryWalker 按固定观察时刻读取 native durable 全部记忆事实。
type nativeVectorRebuildMemoryWalker interface {
	WalkNativeMigrationMemories(ctx context.Context, visit func(logicdomain.MemoryRecord) error) error
}

// nativeVectorRebuildRestorableTrashWriter rewrites one exact trash row after the new embedding is materialized.
// nativeVectorRebuildRestorableTrashWriter 在新 embedding 生成后精确重写一条 trash 行。
type nativeVectorRebuildRestorableTrashWriter interface {
	UpdateNativeRestorableTrashVector(ctx context.Context, batchID, memoryID uint64, vector []float32) error
}

// nativeVectorRebuildSnapshot keeps live rows, eligible trash rows, and unique Lance rows from one pre-reset observation.
// nativeVectorRebuildSnapshot 保存一次 reset 前观察到的 live 行、eligible trash 行和唯一 Lance 行。
type nativeVectorRebuildSnapshot struct {
	ProjectCount  int
	EligibilityAt time.Time
	Active        []logicdomain.MemoryRecord
	Trash         []vldb_sqlite.NativeRestorableTrashMemory
	Unique        []logicdomain.MemoryRecord
}

// loadNativeVectorRebuildActiveFacts loads project metadata and filters native durable facts at one captured time.
// loadNativeVectorRebuildActiveFacts 加载项目元数据，并在同一捕获时刻过滤 native durable 事实。
func loadNativeVectorRebuildActiveFacts(ctx context.Context, workspace appports.WorkspaceStore, relational any, capturedAt time.Time) ([]logicdomain.ProjectRecord, []logicdomain.MemoryRecord, error) {
	projectLister, hasProjectLister := workspace.(appports.MaintenanceProjectLister)
	var (
		projects []logicdomain.ProjectRecord
		err      error
	)
	if hasProjectLister {
		projects, err = projectLister.ListProjectsForMaintenance(ctx)
	} else {
		projects, err = workspace.ListProjects(ctx)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list projects for native vector rebuild: %w", err)
	}
	walker, ok := relational.(nativeVectorRebuildMemoryWalker)
	if !ok {
		return nil, nil, fmt.Errorf("native vector rebuild requires durable all-facts walker")
	}
	active := make([]logicdomain.MemoryRecord, 0)
	err = walker.WalkNativeMigrationMemories(ctx, func(record logicdomain.MemoryRecord) error {
		if record.Status != logicdomain.MemoryStatusActive || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(capturedAt)) {
			return nil
		}
		active = append(active, cloneVectorRebuildRecord(record))
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk native active memory facts: %w", err)
	}
	return projects, active, nil
}

// loadNativeVectorRebuildSnapshot reads live and eligible trash facts before any destructive reset.
// loadNativeVectorRebuildSnapshot 在任何破坏性 reset 前读取 live 与 eligible trash 事实。
func loadNativeVectorRebuildSnapshot(ctx context.Context, workspace appports.WorkspaceStore, relational any) (nativeVectorRebuildSnapshot, error) {
	capturedAt := time.Now().UTC()
	projects, active, err := loadNativeVectorRebuildActiveFacts(ctx, workspace, relational, capturedAt)
	if err != nil {
		return nativeVectorRebuildSnapshot{}, err
	}
	walker, ok := relational.(nativeVectorRebuildRestorableTrashRowsWalker)
	if !ok {
		return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild cannot inspect restorable trash vectors: relational store does not expose composite trash rows")
	}

	snapshot := nativeVectorRebuildSnapshot{
		ProjectCount:  len(projects),
		EligibilityAt: capturedAt,
		Active:        cloneVectorRebuildRecords(active),
		Trash:         make([]vldb_sqlite.NativeRestorableTrashMemory, 0),
		Unique:        make([]logicdomain.MemoryRecord, 0, len(active)),
	}
	payloads := make(map[string][sha256.Size]byte, len(active))
	activeIDs := make(map[string]struct{}, len(active))
	for _, record := range active {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild snapshot contains an empty active vector id")
		}
		if _, exists := activeIDs[id]; exists {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild snapshot contains duplicate active vector id %q", id)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return nativeVectorRebuildSnapshot{}, fingerprintErr
		}
		activeIDs[id] = struct{}{}
		payloads[id] = fingerprint
		snapshot.Unique = append(snapshot.Unique, cloneVectorRebuildRecord(record))
	}

	// The walker applies the same eligibility filters used by native migration at the captured observation time.
	// walker 使用与 native migration 相同的资格过滤，并固定本次快照的观察时刻。
	err = walker.WalkNativeMigrationRestorableTrashRows(ctx, snapshot.EligibilityAt, func(row vldb_sqlite.NativeRestorableTrashMemory) error {
		if row.BatchID == 0 || row.MemoryID == 0 {
			return fmt.Errorf("native vector rebuild snapshot contains invalid trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		id := strings.TrimSpace(row.Record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty trash vector id for batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(row.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if existing, exists := payloads[id]; exists {
			if existing != fingerprint {
				return fmt.Errorf("native vector rebuild found conflicting live or restorable trash payloads for vector id %q", id)
			}
		} else {
			payloads[id] = fingerprint
			snapshot.Unique = append(snapshot.Unique, cloneVectorRebuildRecord(row.Record))
		}
		snapshot.Trash = append(snapshot.Trash, vldb_sqlite.NativeRestorableTrashMemory{
			BatchID:  row.BatchID,
			MemoryID: row.MemoryID,
			Record:   cloneVectorRebuildRecord(row.Record),
		})
		return nil
	})
	if err != nil {
		return nativeVectorRebuildSnapshot{}, fmt.Errorf("inspect native restorable trash vectors: %w", err)
	}
	if len(snapshot.Trash) > 0 {
		if _, ok := relational.(nativeVectorRebuildRestorableTrashWriter); !ok {
			return nativeVectorRebuildSnapshot{}, fmt.Errorf("native vector rebuild refuses eligible restorable trash vectors: relational store does not expose exact trash-vector updates")
		}
	}
	return snapshot, nil
}

// nativeVectorRebuildPayloadFingerprint compares source facts while ignoring the old model's vector payload.
// nativeVectorRebuildPayloadFingerprint 比较源事实并忽略旧模型向量载荷。
func nativeVectorRebuildPayloadFingerprint(record logicdomain.MemoryRecord) ([sha256.Size]byte, error) {
	withoutVector := cloneVectorRebuildRecord(record)
	withoutVector.Vector = nil
	encoded, err := json.Marshal(withoutVector)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode native vector rebuild payload %q: %w", record.ID, err)
	}
	return sha256.Sum256(encoded), nil
}

// VectorRebuildReport stores one concise rebuild summary for CLI operators and tests.
// VectorRebuildReport 用于保存一份简洁的重建汇总，供 CLI 运维输出与测试断言使用。
type VectorRebuildReport struct {
	Mode               string
	ProjectCount       int
	MemoryCount        int
	DurableRowsUpdated int
	VectorRowsRebuilt  int
}

// VectorRebuildProgress describes one bounded progress update emitted by the rebuild workflow.
// VectorRebuildProgress 描述重建流程发出的一次有界进度更新。
type VectorRebuildProgress struct {
	// Stage identifies the current embedding, writing, or commit sub-stage.
	// Stage 标识当前的 embedding、写入或提交子阶段。
	Stage string
	// Processed counts records completed in the current sub-stage.
	// Processed 统计当前子阶段已经完成的记录数。
	Processed int
	// Total counts records in the current sub-stage.
	// Total 统计当前子阶段的记录总数。
	Total int
}

// VectorRebuildProgressReporter receives machine-readable progress without changing rebuild data semantics.
// VectorRebuildProgressReporter 接收机器可读进度，且不改变重建数据语义。
type VectorRebuildProgressReporter func(VectorRebuildProgress)

// RunVectorRebuild refreshes active durable memory vectors with the currently configured embedding model and then rebuilds the active vector backend when the runtime uses split storage.
// RunVectorRebuild 用于使用当前配置的 embedding 模型刷新 active 长期记忆向量；若运行时使用分离存储，则继续重建当前向量后端。
func RunVectorRebuild(ctx context.Context, cfg config.Config, deps MaintenanceDependencies, logger *logx.Logger) (VectorRebuildReport, error) {
	return RunVectorRebuildWithProgress(ctx, cfg, deps, logger, nil)
}

// RunVectorRebuildWithProgress runs the rebuild and reports each completed preparation or refill batch.
// RunVectorRebuildWithProgress 执行重建，并报告每个完成的准备或回填批次。
func RunVectorRebuildWithProgress(ctx context.Context, cfg config.Config, deps MaintenanceDependencies, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = logx.Default()
	}
	if deps.Embedding == nil {
		return VectorRebuildReport{}, fmt.Errorf("maintenance embedding client is not configured")
	}
	if deps.Relational == nil {
		return VectorRebuildReport{}, fmt.Errorf("maintenance relational store is not configured")
	}

	// Resolve the narrow maintenance ports up front so the tool fails fast before it starts any destructive or long-running work.
	// 在真正开始破坏性或长耗时工作前先解析狭窄维护端口，确保缺失能力时尽早失败。
	workspace, ok := deps.Relational.(appports.WorkspaceStore)
	if !ok {
		return VectorRebuildReport{}, fmt.Errorf("relational store does not support workspace listing for vector rebuild")
	}
	durable, ok := deps.Relational.(appports.MemoryVectorRebuildStore)
	if !ok {
		return VectorRebuildReport{}, fmt.Errorf("relational store does not support durable vector replacement")
	}
	var (
		nativeSQLitePath string
		nativeSnapshot   nativeVectorRebuildSnapshot
	)
	if cfg.UsesNative() {
		// Capture the relationship-backed active vector set before publishing the marker so completion can prove the same facts were rebuilt.
		// 在发布标记前记录关系层 active 向量集合，让完成阶段可以证明重建的仍是同一批事实。
		layout, layoutErr := resolveNativeStorageLayout(cfg, config.PromptLayout{})
		if layoutErr != nil {
			return VectorRebuildReport{}, fmt.Errorf("resolve native vector rebuild marker path: %w", layoutErr)
		}
		var snapshotErr error
		nativeSnapshot, snapshotErr = loadNativeVectorRebuildSnapshot(ctx, workspace, deps.Relational)
		if snapshotErr != nil {
			return VectorRebuildReport{}, snapshotErr
		}
		nativeSQLitePath = layout.SQLiteDatabase
		// The marker is deliberately durable before RecreateTable/ClearMemoryVectors; every later error leaves startup fail-closed.
		// 标记会在 RecreateTable/ClearMemoryVectors 前持久化；后续任何错误都会让启动保持失败关闭。
		if err := writeNativeVectorRebuildMarker(nativeSQLitePath); err != nil {
			return VectorRebuildReport{}, fmt.Errorf("mark native vector rebuild incomplete: %w", err)
		}
	}
	var report VectorRebuildReport
	var err error
	if cfg.UsesNative() {
		report, err = runNativeVectorRebuildWithSnapshot(ctx, cfg, deps.Embedding, workspace, durable, deps.Vector, nativeSnapshot, logger, reporter)
	} else {
		report, err = runVectorRebuildWithPortsAndProgress(ctx, cfg, deps.Embedding, workspace, durable, deps.Vector, logger, reporter)
	}
	if err != nil {
		return report, err
	}
	if cfg.UsesNative() {
		publishCtx, cancelPublish := nativeVectorRebuildPublishContext(ctx)
		defer cancelPublish()
		if err := verifyNativeVectorRebuildCompletion(publishCtx, workspace, deps.Relational, deps.Vector, nativeSnapshot, cfg.Embedding.Dimension); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed without a matching relationship/vector count", err)
		}
		if err := updateNativeEmbeddingIdentity(cfg); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed before embedding identity persistence failed", err)
		}
		if err := removeNativeVectorRebuildMarker(nativeSQLitePath); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed before incomplete marker removal failed", err)
		}
	}
	return report, nil
}

// nativeVectorRebuildCounter is the optional health surface used to verify the physical native vector row count after a rebuild.
// nativeVectorRebuildCounter 是重建后核对原生物理向量行数所需的可选健康能力。
type nativeVectorRebuildCounter interface {
	Count(context.Context) (int64, error)
}

// verifyNativeVectorRebuildCompletion proves that relationship facts, vector dimensions, and physical vector rows converged to one snapshot.
// verifyNativeVectorRebuildCompletion 证明关系事实、向量维度和物理向量行数已经收敛到同一份快照。
func verifyNativeVectorRebuildCompletion(ctx context.Context, workspace appports.WorkspaceStore, relational any, vector appports.VectorStore, expected nativeVectorRebuildSnapshot, expectedDimension int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if workspace == nil {
		return fmt.Errorf("native vector rebuild verification requires workspace store")
	}
	if relational == nil {
		return fmt.Errorf("native vector rebuild verification requires relational store")
	}
	if vector == nil {
		return fmt.Errorf("native vector rebuild verification requires vector store")
	}
	if expected.EligibilityAt.IsZero() {
		return fmt.Errorf("native vector rebuild verification requires the snapshot eligibility time")
	}
	eligibilityAt := expected.EligibilityAt
	_, actual, err := loadNativeVectorRebuildActiveFacts(ctx, workspace, relational, eligibilityAt)
	if err != nil {
		return fmt.Errorf("reload relationship facts after native vector rebuild: %w", err)
	}
	expectedActiveIDs := make(map[string]struct{}, len(expected.Active))
	expectedPayloads := make(map[string][sha256.Size]byte, len(expected.Unique))
	for _, record := range expected.Active {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty active vector id")
		}
		if _, exists := expectedActiveIDs[id]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate active vector id %q", id)
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		expectedActiveIDs[id] = struct{}{}
		expectedPayloads[id] = fingerprint
	}
	for _, record := range expected.Unique {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty unique vector id")
		}
		fingerprint, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if existing, exists := expectedPayloads[id]; exists && existing != fingerprint {
			return fmt.Errorf("native vector rebuild snapshot contains conflicting unique payloads for vector id %q", id)
		}
		expectedPayloads[id] = fingerprint
	}
	if len(actual) != len(expected.Active) {
		return fmt.Errorf("relationship fact count changed during native vector rebuild: got %d, want %d", len(actual), len(expected.Active))
	}
	actualIDs := make(map[string]struct{}, len(actual))
	for _, record := range actual {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("relationship facts contain an empty vector id after native vector rebuild")
		}
		if _, exists := actualIDs[id]; exists {
			return fmt.Errorf("relationship facts contain duplicate vector id %q after native vector rebuild", id)
		}
		actualIDs[id] = struct{}{}
		expectedPayload, exists := expectedPayloads[id]
		if !exists {
			return fmt.Errorf("native vector rebuild produced unexpected relationship vector id %q", id)
		}
		actualPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if actualPayload != expectedPayload {
			return fmt.Errorf("relationship payload hash changed for vector id %q after native vector rebuild", id)
		}
		if expectedDimension <= 0 || len(record.Vector) != expectedDimension {
			return fmt.Errorf("relationship vector %q has dimension %d after native vector rebuild, want %d", id, len(record.Vector), expectedDimension)
		}
	}
	trashWalker, ok := relational.(nativeVectorRebuildRestorableTrashRowsWalker)
	if !ok {
		return fmt.Errorf("native vector rebuild verification requires composite restorable-trash rows")
	}
	type trashKey struct {
		batchID  uint64
		memoryID uint64
	}
	expectedTrash := make(map[trashKey]vldb_sqlite.NativeRestorableTrashMemory, len(expected.Trash))
	for _, row := range expected.Trash {
		key := trashKey{batchID: row.BatchID, memoryID: row.MemoryID}
		if row.BatchID == 0 || row.MemoryID == 0 {
			return fmt.Errorf("native vector rebuild snapshot contains invalid trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		if _, exists := expectedTrash[key]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedTrash[key] = row
	}
	actualTrash := make(map[trashKey]vldb_sqlite.NativeRestorableTrashMemory, len(expected.Trash))
	err = trashWalker.WalkNativeMigrationRestorableTrashRows(ctx, eligibilityAt, func(row vldb_sqlite.NativeRestorableTrashMemory) error {
		key := trashKey{batchID: row.BatchID, memoryID: row.MemoryID}
		if _, exists := actualTrash[key]; exists {
			return fmt.Errorf("native vector rebuild produced duplicate trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedRow, exists := expectedTrash[key]
		if !exists {
			return fmt.Errorf("native vector rebuild produced unexpected trash identity batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		id := strings.TrimSpace(row.Record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild produced empty trash vector id batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		expectedPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(expectedRow.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		actualPayload, fingerprintErr := nativeVectorRebuildPayloadFingerprint(row.Record)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if actualPayload != expectedPayload {
			return fmt.Errorf("trash payload hash changed for batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		uniquePayload, exists := expectedPayloads[id]
		if !exists || uniquePayload != actualPayload {
			return fmt.Errorf("trash payload hash does not match unique vector id %q", id)
		}
		if expectedDimension <= 0 || len(row.Record.Vector) != expectedDimension {
			return fmt.Errorf("trash vector %q has dimension %d after native vector rebuild, want %d", id, len(row.Record.Vector), expectedDimension)
		}
		actualTrash[key] = row
		return nil
	})
	if err != nil {
		return fmt.Errorf("reload restorable trash facts after native vector rebuild: %w", err)
	}
	if len(actualTrash) != len(expectedTrash) {
		return fmt.Errorf("restorable trash fact count changed during native vector rebuild: got %d, want %d", len(actualTrash), len(expectedTrash))
	}
	uniqueIDs := make(map[string]struct{}, len(expected.Unique))
	for _, record := range expected.Unique {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return fmt.Errorf("native vector rebuild snapshot contains an empty unique vector id")
		}
		if _, exists := uniqueIDs[id]; exists {
			return fmt.Errorf("native vector rebuild snapshot contains duplicate unique vector id %q", id)
		}
		uniqueIDs[id] = struct{}{}
	}
	for _, row := range actualTrash {
		id := strings.TrimSpace(row.Record.ID)
		if _, exists := uniqueIDs[id]; !exists {
			return fmt.Errorf("native vector rebuild produced unexpected unique trash vector id %q", id)
		}
	}
	counter, ok := vector.(nativeVectorRebuildCounter)
	if !ok {
		return fmt.Errorf("native vector store does not expose physical row counting")
	}
	count, err := counter.Count(ctx)
	if err != nil {
		return fmt.Errorf("count native vector rows after rebuild: %w", err)
	}
	if count != int64(len(uniqueIDs)) {
		return fmt.Errorf("native vector row count after rebuild: got %d, want %d", count, len(uniqueIDs))
	}
	return nil
}

// runVectorRebuildWithPorts executes the rebuild workflow against the already-resolved narrow ports so tests can verify the orchestration without constructing the full maintenance dependency bundle.
// runVectorRebuildWithPorts 用于针对已经解析好的狭窄端口执行重建流程，让测试无需构造完整维护依赖包也能验证编排行为。
func runVectorRebuildWithPorts(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, logger *logx.Logger) (VectorRebuildReport, error) {
	return runVectorRebuildWithPortsAndProgress(ctx, cfg, embedding, workspace, durable, vector, logger, nil)
}

// runNativeVectorRebuildWithSnapshot rebuilds live and eligible trash vectors from one captured source image.
// runNativeVectorRebuildWithSnapshot 使用一次捕获的源快照重建 live 与 eligible trash 向量。
func runNativeVectorRebuildWithSnapshot(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if logger == nil {
		logger = logx.Default()
	}
	report := VectorRebuildReport{
		Mode:         "native",
		ProjectCount: snapshot.ProjectCount,
		MemoryCount:  len(snapshot.Active) + len(snapshot.Trash),
	}
	if vector == nil {
		return report, fmt.Errorf("maintenance vector store is not configured for native rebuild")
	}
	resetter, ok := vector.(vectorRebuildResetter)
	if !ok {
		return report, fmt.Errorf("vector store does not support table recreation for native rebuild")
	}
	durableResetter, ok := durable.(vectorRebuildDurableResetter)
	if !ok {
		return report, fmt.Errorf("relational store does not support durable vector reset for native rebuild")
	}
	if len(snapshot.Trash) > 0 {
		if _, ok := durable.(nativeVectorRebuildRestorableTrashWriter); !ok {
			return report, fmt.Errorf("native vector rebuild refuses eligible restorable trash vectors: relational store does not expose exact trash-vector updates")
		}
	}
	rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, snapshot.Unique, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
	if err != nil {
		return report, err
	}
	applyResult, applyErr := applyNativeVectorRebuildSnapshotWithProgress(ctx, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter)
	report.DurableRowsUpdated = applyResult.DurableRowsUpdated
	report.VectorRowsRebuilt = applyResult.VectorRowsRebuilt
	publishCtx := ctx
	cancelPublish := func() {}
	if applyErr != nil {
		if !applyResult.ResetCompleted {
			return report, applyErr
		}
		if recoverErr := recoverNativeVectorRebuildAfterResetWithProgress(ctx, applyErr, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter); recoverErr != nil {
			return report, vectorRebuildOutcomeUncertainError("native vector rebuild failed after reset", applyErr, recoverErr)
		}
		report.DurableRowsUpdated = len(snapshot.Active) + len(snapshot.Trash)
		report.VectorRowsRebuilt = len(snapshot.Unique)
		publishCtx, cancelPublish = nativeVectorRebuildPublishContext(ctx)
		defer cancelPublish()
		logger.Warn("native vector rebuild recovered after intermediate failure", "mode", report.Mode, "memory_count", report.MemoryCount, "err", applyErr)
	}
	if err := recordNativeVectorSchemaVersion(publishCtx, cfg, durable); err != nil {
		return report, err
	}
	logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
	return report, nil
}

// runVectorRebuildWithPortsAndProgress keeps the testable narrow-port workflow and adds an optional progress channel.
// runVectorRebuildWithPortsAndProgress 保留可测试的窄端口流程，并增加可选进度通道。
func runVectorRebuildWithPortsAndProgress(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if cfg.UsesNative() {
		snapshot, snapshotErr := loadNativeVectorRebuildSnapshot(ctx, workspace, durable)
		if snapshotErr != nil {
			return VectorRebuildReport{Mode: "native"}, snapshotErr
		}
		return runNativeVectorRebuildWithSnapshot(ctx, cfg, embedding, workspace, durable, vector, snapshot, logger, reporter)
	}

	// Load only active/unexpired durable memories because inactive rows and trash data are intentionally outside the current recall surface and do not need rebuilding.
	// 只加载 active 且未过期的长期记忆，因为 inactive 行和垃圾箱数据本就不在当前召回面上，没有重建意义。
	projects, records, err := loadVectorRebuildRecords(ctx, workspace)
	if err != nil {
		return VectorRebuildReport{}, err
	}
	report := VectorRebuildReport{
		ProjectCount: len(projects),
		MemoryCount:  len(records),
	}
	if cfg.UsesCombinedPostgres() {
		report.Mode = "combined-postgres"
	} else {
		report.Mode = "split-lancedb"
	}
	if cfg.UsesCombinedPostgres() {
		migrator, ok := durable.(appports.MemoryVectorDimensionMigrationStore)
		if !ok {
			return report, fmt.Errorf("relational store does not support combined vector-dimension migration")
		}
		if len(records) == 0 {
			// Still migrate the combined-store vector columns even on an empty active dataset, because operators usually invoke this command after changing the configured embedding dimension.
			// 即使当前 active 数据集为空，也仍然要迁移组合库存储的向量列，因为运维通常是在切换 embedding 维度后执行该命令。
			publishVectorRebuildProgress(reporter, "committing", 0, 0)
			if err := migrator.RebuildMemoryVectorDimensions(ctx, nil); err != nil {
				return report, fmt.Errorf("rebuild combined vector dimensions: %w", err)
			}
			logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
			return report, nil
		}

		// Materialize every rebuilt vector first so combined mode can keep the old live embeddings untouched until the final PostgreSQL schema swap is ready to commit atomically.
		// 先把全部重建后的向量载荷准备好，确保组合模式在最终 PostgreSQL schema 交换准备原子提交前，不会提前触碰线上仍在使用的旧 embedding。
		rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
		if err != nil {
			return report, err
		}
		publishVectorRebuildProgress(reporter, "committing", len(rebuiltRecords), len(rebuiltRecords))
		if err := migrator.RebuildMemoryVectorDimensions(ctx, rebuiltRecords); err != nil {
			return report, fmt.Errorf("rebuild combined vector dimensions: %w", err)
		}
		report.DurableRowsUpdated = len(rebuiltRecords)
		report.VectorRowsRebuilt = len(rebuiltRecords)
		logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
		return report, nil
	}

	// Split mode still rebuilds from one shared empty baseline, but it now materializes every target vector first so preparation failures cannot leave the live sidecar half-reset.
	// split 模式仍然会从同一个空基线重建，不过现在会先准备好全部目标向量，避免准备阶段失败时把线上 sidecar 留在“只 reset 了一半”的状态。
	// Even on an empty active dataset, the detached vector table still needs to be recreated so stale sidecar rows are removed before the command exits.
	// 即使当前 active 数据集为空，也仍然要重建独立向量表，确保命令结束前顺手清掉陈旧的旁路向量行。
	if vector == nil {
		return report, fmt.Errorf("maintenance vector store is not configured for split rebuild")
	}
	resetter, ok := vector.(vectorRebuildResetter)
	if !ok {
		return report, fmt.Errorf("vector store does not support table recreation for split rebuild")
	}
	durableResetter, ok := durable.(vectorRebuildDurableResetter)
	if !ok {
		return report, fmt.Errorf("relational store does not support durable vector reset for split rebuild")
	}
	if len(records) == 0 {
		logger.Info("vector rebuild reset phase starting", "mode", report.Mode, "memory_count", report.MemoryCount)
		publishVectorRebuildProgress(reporter, "writing", 0, 0)
		if err := resetter.RecreateTable(ctx); err != nil {
			return report, fmt.Errorf("recreate split vector table: %w", err)
		}
		if err := recordNativeVectorSchemaVersion(ctx, cfg, durable); err != nil {
			return report, err
		}
		logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
		return report, nil
	}

	// Materialize the target vectors before any destructive split reset starts so model-switch failures never leave the current-dimension sidecar empty just because embedding or validation failed midway.
	// 在任何破坏性的 split reset 开始前，先把目标向量全部准备好，避免模型切换时仅因 embedding 或维度校验中途失败，就把当前维度 sidecar 留成空表。
	logger.Info("vector rebuild materialization phase starting", "mode", report.Mode, "project_count", report.ProjectCount, "memory_count", report.MemoryCount)
	rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
	if err != nil {
		return report, err
	}
	applyResult, err := applySplitVectorRebuildRecordsWithProgress(ctx, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter)
	report.DurableRowsUpdated = applyResult.DurableRowsUpdated
	report.VectorRowsRebuilt = applyResult.VectorRowsRebuilt
	if err != nil {
		if !applyResult.ResetCompleted {
			return report, err
		}
		if recoverErr := recoverSplitVectorRebuildAfterResetWithProgress(ctx, err, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter); recoverErr != nil {
			return report, vectorRebuildOutcomeUncertainError("split vector rebuild failed after reset", err, recoverErr)
		}
		report.DurableRowsUpdated = len(rebuiltRecords)
		report.VectorRowsRebuilt = len(rebuiltRecords)
		logger.Warn("split vector rebuild recovered after intermediate failure", "mode", report.Mode, "memory_count", report.MemoryCount, "err", err)
	}
	if err := recordNativeVectorSchemaVersion(ctx, cfg, durable); err != nil {
		return report, err
	}
	logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
	return report, nil
}

// recordNativeVectorSchemaVersion persists the current native LanceDB schema only after the rebuild has converged to its target state.
// recordNativeVectorSchemaVersion 仅在原生向量重建已经收敛到目标状态后持久化当前 LanceDB schema 版本。
func recordNativeVectorSchemaVersion(ctx context.Context, cfg config.Config, durable appports.MemoryVectorRebuildStore) error {
	if !cfg.UsesNative() {
		return nil
	}
	versions, ok := durable.(appports.SchemaVersionStore)
	if !ok {
		return nativeVectorRebuildOutcomeUncertainError("native relational store does not expose schema version persistence")
	}
	if err := versions.SetSchemaComponentVersion(ctx, "lancedb", vldb_lancedb.CurrentSchemaVersion); err != nil {
		return nativeVectorRebuildOutcomeUncertainError("native vector table was rebuilt before schema version persistence failed", err)
	}
	return nil
}

// nativeVectorRebuildOutcomeUncertainError marks native rebuild failures that occur after the vector table or durable vectors changed.
// nativeVectorRebuildOutcomeUncertainError 用于标记原生重建在向量表或 durable 向量已发生变化后出现的失败。
func nativeVectorRebuildOutcomeUncertainError(message string, causes ...error) error {
	parts := make([]string, 0, 1+len(causes))
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, cause := range causes {
		if cause == nil {
			continue
		}
		if trimmed := strings.TrimSpace(cause.Error()); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return logicdomain.OutcomeUncertainError{
		Operation: "rebuild native vector table",
		Message:   strings.Join(parts, "; "),
	}
}

// splitVectorRebuildApplyResult records how far one destructive split-mode rebuild attempt progressed before returning.
// splitVectorRebuildApplyResult 用于记录一次破坏性 split 重建尝试在返回前推进到了哪个阶段。
type splitVectorRebuildApplyResult struct {
	// DurableRowsUpdated counts durable vector rows whose replacement write has already succeeded.
	// DurableRowsUpdated 用于统计已经成功完成替换写入的 durable 向量行数。
	DurableRowsUpdated int
	// VectorRowsRebuilt counts detached vector rows successfully written after the reset.
	// VectorRowsRebuilt 用于统计 reset 后已经成功写入的独立向量行数。
	VectorRowsRebuilt int
	// ResetCompleted marks whether this attempt crossed the destructive table-reset boundary.
	// ResetCompleted 用于标记本次尝试是否已经越过破坏性的表重置边界。
	ResetCompleted bool
}

// applySplitVectorRebuildRecords performs the destructive split-mode reset plus refill using already-materialized target vectors so retries never need to call the embedding backend again.
// applySplitVectorRebuildRecords 用于基于已准备好的目标向量执行 split 模式的破坏性 reset 与回填，让重试路径无需再次调用 embedding 后端。
func applySplitVectorRebuildRecords(ctx context.Context, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger) (splitVectorRebuildApplyResult, error) {
	return applySplitVectorRebuildRecordsWithProgress(ctx, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, nil)
}

// applySplitVectorRebuildRecordsWithProgress performs split refill and publishes completed write batches.
// applySplitVectorRebuildRecordsWithProgress 执行 split 回填，并发布已完成的写入批次。
func applySplitVectorRebuildRecordsWithProgress(ctx context.Context, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) (splitVectorRebuildApplyResult, error) {
	if logger == nil {
		logger = logx.Default()
	}
	result := splitVectorRebuildApplyResult{}
	logger.Info("vector rebuild reset phase starting", "mode", mode, "memory_count", len(rebuiltRecords))
	if err := resetter.RecreateTable(ctx); err != nil {
		result.ResetCompleted = logicdomain.IsOutcomeUncertain(err)
		return result, fmt.Errorf("recreate split vector table: %w", err)
	}
	result.ResetCompleted = true
	vectorIDs := collectVectorRebuildVectorIDs(rebuiltRecords)
	if len(vectorIDs) > 0 {
		if err := durableResetter.ClearMemoryVectors(ctx, vectorIDs); err != nil {
			return result, fmt.Errorf("clear durable vectors before split rebuild: %w", err)
		}
	}
	if len(rebuiltRecords) == 0 {
		return result, nil
	}

	logger.Info("vector rebuild refill phase starting", "mode", mode, "memory_count", len(rebuiltRecords))
	publishVectorRebuildProgress(reporter, "writing", 0, len(rebuiltRecords))
	for start := 0; start < len(rebuiltRecords); start += vectorRebuildWriteBatchSize {
		end := start + vectorRebuildWriteBatchSize
		if end > len(rebuiltRecords) {
			end = len(rebuiltRecords)
		}
		batchRecords := cloneVectorRebuildRecords(rebuiltRecords[start:end])
		if err := durable.ReplaceMemoryVectors(ctx, batchRecords); err != nil {
			return result, fmt.Errorf("replace durable vectors for batch %d-%d: %w", start, end, err)
		}

		// Count the durable replacement as soon as it succeeds because later sidecar failures cannot roll that write back.
		// durable 替换一旦成功就立即计数，因为后续 sidecar 失败无法回滚这次写入。
		result.DurableRowsUpdated += len(batchRecords)
		for idx, record := range batchRecords {
			if err := vector.Upsert(ctx, record); err != nil {
				return result, fmt.Errorf("upsert rebuilt vector row %d/%d (%s): %w", start+idx+1, len(rebuiltRecords), record.ID, err)
			}
			result.VectorRowsRebuilt++
		}
		logger.Info("vector rebuild refill phase advanced", "mode", mode, "processed", result.DurableRowsUpdated, "total", len(rebuiltRecords))
		publishVectorRebuildProgress(reporter, "writing", result.DurableRowsUpdated, len(rebuiltRecords))
	}
	return result, nil
}

// applyNativeVectorRebuildSnapshotWithProgress resets native Lance, rewrites live and trash durable rows, and then publishes unique vector ids.
// applyNativeVectorRebuildSnapshotWithProgress 重置 native Lance，重写 live 与 trash durable 行，最后发布唯一向量 ID。
func applyNativeVectorRebuildSnapshotWithProgress(ctx context.Context, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) (splitVectorRebuildApplyResult, error) {
	if logger == nil {
		logger = logx.Default()
	}
	result := splitVectorRebuildApplyResult{}
	logger.Info("native vector rebuild reset phase starting", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash))
	if err := resetter.RecreateTable(ctx); err != nil {
		result.ResetCompleted = logicdomain.IsOutcomeUncertain(err)
		return result, fmt.Errorf("recreate native vector table: %w", err)
	}
	result.ResetCompleted = true
	activeIDs := collectVectorRebuildVectorIDs(snapshot.Active)
	if len(activeIDs) > 0 {
		if err := durableResetter.ClearMemoryVectors(ctx, activeIDs); err != nil {
			return result, fmt.Errorf("clear native durable vectors before rebuild: %w", err)
		}
	}
	rebuiltByID := make(map[string]logicdomain.MemoryRecord, len(rebuiltRecords))
	for _, record := range rebuiltRecords {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return result, fmt.Errorf("native rebuilt vector has an empty id")
		}
		if _, exists := rebuiltByID[id]; exists {
			return result, fmt.Errorf("native rebuilt vectors contain duplicate id %q", id)
		}
		rebuiltByID[id] = cloneVectorRebuildRecord(record)
	}
	activeRecords := make([]logicdomain.MemoryRecord, 0, len(snapshot.Active))
	for _, source := range snapshot.Active {
		id := strings.TrimSpace(source.ID)
		rebuilt, exists := rebuiltByID[id]
		if !exists {
			return result, fmt.Errorf("native rebuilt vector missing active id %q", id)
		}
		activeRecords = append(activeRecords, rebuilt)
	}
	trashWriter, _ := durable.(nativeVectorRebuildRestorableTrashWriter)
	logger.Info("native vector rebuild durable refill phase starting", "mode", "native", "active_rows", len(activeRecords), "trash_rows", len(snapshot.Trash))
	publishVectorRebuildProgress(reporter, "writing", 0, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	for start := 0; start < len(activeRecords); start += vectorRebuildWriteBatchSize {
		end := start + vectorRebuildWriteBatchSize
		if end > len(activeRecords) {
			end = len(activeRecords)
		}
		batch := cloneVectorRebuildRecords(activeRecords[start:end])
		if err := durable.ReplaceMemoryVectors(ctx, batch); err != nil {
			return result, fmt.Errorf("replace native live vectors for batch %d-%d: %w", start, end, err)
		}
		result.DurableRowsUpdated += len(batch)
	}
	for _, row := range snapshot.Trash {
		rebuilt, exists := rebuiltByID[strings.TrimSpace(row.Record.ID)]
		if !exists {
			return result, fmt.Errorf("native rebuilt vector missing trash id %q", row.Record.ID)
		}
		if trashWriter == nil {
			return result, fmt.Errorf("native vector rebuild cannot update trash row batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		if err := trashWriter.UpdateNativeRestorableTrashVector(ctx, row.BatchID, row.MemoryID, append([]float32(nil), rebuilt.Vector...)); err != nil {
			return result, fmt.Errorf("update native trash vector batch=%d memory=%d: %w", row.BatchID, row.MemoryID, err)
		}
		result.DurableRowsUpdated++
		publishVectorRebuildProgress(reporter, "writing", result.DurableRowsUpdated, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	}
	for idx, record := range rebuiltRecords {
		if err := vector.Upsert(ctx, record); err != nil {
			return result, fmt.Errorf("upsert rebuilt native vector row %d/%d (%s): %w", idx+1, len(rebuiltRecords), record.ID, err)
		}
		result.VectorRowsRebuilt++
		publishVectorRebuildProgress(reporter, "writing", len(activeRecords)+len(snapshot.Trash)+result.VectorRowsRebuilt, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	}
	return result, nil
}

// recoverNativeVectorRebuildAfterResetWithProgress retries the complete native target using a bounded detached context.
// recoverNativeVectorRebuildAfterResetWithProgress 使用有界脱离上下文重试完整 native 目标。
func recoverNativeVectorRebuildAfterResetWithProgress(ctx context.Context, cause error, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) error {
	if logger == nil {
		logger = logx.Default()
	}
	logger.Warn("native vector rebuild failed after reset, attempting automatic repair", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash), "err", cause)
	repairBase := ctx
	if repairBase == nil {
		repairBase = context.Background()
	} else {
		repairBase = context.WithoutCancel(repairBase)
	}
	repairCtx, cancel := context.WithTimeout(repairBase, nativeVectorRebuildRecoveryTimeout)
	defer cancel()
	if _, err := applyNativeVectorRebuildSnapshotWithProgress(repairCtx, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter); err != nil {
		logger.Error("native vector rebuild automatic repair failed", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash), "err", err)
		return err
	}
	logger.Warn("native vector rebuild automatic repair completed", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash))
	return nil
}

// nativeVectorRebuildPublishContext keeps post-recovery schema and fact publication bounded after caller cancellation.
// nativeVectorRebuildPublishContext 在调用方取消后为恢复完成的 schema 与事实发布提供有界上下文。
func nativeVectorRebuildPublishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), nativeVectorRebuildRecoveryTimeout)
}

// vectorRebuildOutcomeUncertainError marks split-mode rebuild failures that may have already changed durable vectors, the detached vector table, or both.
// vectorRebuildOutcomeUncertainError 用于标记 split 模式重建中可能已经改变 durable 向量、独立向量表或二者的失败。
func vectorRebuildOutcomeUncertainError(message string, causes ...error) error {
	parts := make([]string, 0, 1+len(causes))
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, cause := range causes {
		if cause == nil {
			continue
		}
		if trimmed := strings.TrimSpace(cause.Error()); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return logicdomain.OutcomeUncertainError{
		Operation: "rebuild split vector table",
		Message:   strings.Join(parts, "; "),
	}
}

// recoverSplitVectorRebuildAfterReset replays the already-materialized target vectors after one intermediate split-mode failure so the command prefers converging to the new current-dimension state over leaving the runtime sidecar empty.
// recoverSplitVectorRebuildAfterReset 用于在 split 模式中途失败后，重放已经准备好的目标向量，让命令优先收敛到新的当前维度状态，而不是把运行时 sidecar 留成空表。
func recoverSplitVectorRebuildAfterReset(ctx context.Context, cause error, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger) error {
	return recoverSplitVectorRebuildAfterResetWithProgress(ctx, cause, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, nil)
}

// recoverSplitVectorRebuildAfterResetWithProgress retries an interrupted refill while retaining progress reporting.
// recoverSplitVectorRebuildAfterResetWithProgress 在保留进度报告的同时重试中断的回填。
func recoverSplitVectorRebuildAfterResetWithProgress(ctx context.Context, cause error, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) error {
	if cause == nil {
		return nil
	}
	if len(rebuiltRecords) == 0 {
		return nil
	}
	if logger == nil {
		logger = logx.Default()
	}
	logger.Warn("split vector rebuild failed after reset, attempting automatic repair", "mode", mode, "memory_count", len(rebuiltRecords), "err", cause)
	repairCtx := ctx
	if repairCtx == nil {
		repairCtx = context.Background()
	} else {
		repairCtx = context.WithoutCancel(repairCtx)
	}
	if _, err := applySplitVectorRebuildRecordsWithProgress(repairCtx, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter); err != nil {
		logger.Error("split vector rebuild automatic repair failed", "mode", mode, "memory_count", len(rebuiltRecords), "err", err)
		return err
	}
	logger.Warn("split vector rebuild automatic repair completed", "mode", mode, "memory_count", len(rebuiltRecords))
	return nil
}

// collectVectorRebuildVectorIDs extracts one stable list of durable vector ids so split-mode reset phases can clear SQLite payloads before rebuilding them from scratch.
// collectVectorRebuildVectorIDs 用于提取一份稳定的 durable vector id 列表，让 split 模式的重置阶段可以先清空 SQLite 载荷，再从零开始重建。
func collectVectorRebuildVectorIDs(records []logicdomain.MemoryRecord) []string {
	vectorIDs := make([]string, 0, len(records))
	for _, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			continue
		}
		vectorIDs = append(vectorIDs, vectorID)
	}
	return vectorIDs
}

// loadVectorRebuildRecords enumerates every active project and flattens their active durable memories into one deterministic rebuild list.
// loadVectorRebuildRecords 用于枚举所有活跃项目，并把它们的 active 长期记忆摊平成一个确定性的重建列表。
func loadVectorRebuildRecords(ctx context.Context, workspace appports.WorkspaceStore) ([]logicdomain.ProjectRecord, []logicdomain.MemoryRecord, error) {
	projectLister, hasProjectLister := workspace.(appports.MaintenanceProjectLister)
	var (
		projects []logicdomain.ProjectRecord
		err      error
	)
	if hasProjectLister {
		projects, err = projectLister.ListProjectsForMaintenance(ctx)
	} else {
		projects, err = workspace.ListProjects(ctx)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list projects for vector rebuild: %w", err)
	}
	maintenanceLister, hasMaintenanceLister := workspace.(appports.MaintenanceProjectMemoryLister)
	records := make([]logicdomain.MemoryRecord, 0)
	for _, project := range projects {
		var (
			projectRecords []logicdomain.MemoryRecord
			listErr        error
		)
		if hasMaintenanceLister {
			projectRecords, listErr = maintenanceLister.ListProjectMemoriesForMaintenance(ctx, project.ID)
		} else {
			projectRecords, listErr = workspace.ListProjectMemories(ctx, project.ID)
		}
		if listErr != nil {
			return nil, nil, fmt.Errorf("list project %d memories for vector rebuild: %w", project.ID, listErr)
		}
		records = append(records, projectRecords...)
	}
	return projects, records, nil
}

// materializeVectorRebuildRecords prepares one fully rebuilt in-memory durable snapshot in bounded batches before combined PostgreSQL mode starts its atomic schema swap, so later maintenance writes never expose half-finished live vectors or unbounded temporary vector payloads.
// materializeVectorRebuildRecords 用于在组合 PostgreSQL 模式开始原子 schema 交换前，以有界批次准备完整的内存态 durable 重建快照，既避免后续维护写入暴露“只重建了一半”的线上向量，也避免临时向量载荷无限膨胀。
func materializeVectorRebuildRecords(ctx context.Context, embedding appports.EmbeddingClient, records []logicdomain.MemoryRecord, expectedDimension, batchSize int, logger *logx.Logger) ([]logicdomain.MemoryRecord, error) {
	return materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, expectedDimension, batchSize, logger, nil)
}

// materializeVectorRebuildRecordsWithProgress embeds bounded batches and reports each completed batch.
// materializeVectorRebuildRecordsWithProgress 按有界批次生成向量，并报告每个已完成批次。
func materializeVectorRebuildRecordsWithProgress(ctx context.Context, embedding appports.EmbeddingClient, records []logicdomain.MemoryRecord, expectedDimension, batchSize int, logger *logx.Logger, reporter VectorRebuildProgressReporter) ([]logicdomain.MemoryRecord, error) {
	if batchSize <= 0 {
		batchSize = len(records)
	}
	rebuilt := make([]logicdomain.MemoryRecord, 0, len(records))
	publishVectorRebuildProgress(reporter, "embedding", 0, len(records))
	for start := 0; start < len(records); start += batchSize {
		end := start + batchSize
		if end > len(records) {
			end = len(records)
		}
		vectors, err := embedVectorRebuildBatch(ctx, embedding, records[start:end], logger)
		if err != nil {
			return nil, fmt.Errorf("embed rebuild batch %d-%d: %w", start, end, err)
		}
		if err := validateVectorRebuildDimensions(vectors, expectedDimension); err != nil {
			return nil, fmt.Errorf("validate rebuild batch %d-%d dimensions: %w", start, end, err)
		}
		rebuilt = append(rebuilt, cloneVectorRebuildBatch(records[start:end], vectors)...)
		publishVectorRebuildProgress(reporter, "embedding", end, len(records))
	}
	return rebuilt, nil
}

// publishVectorRebuildProgress forwards one validated workflow update to the optional observer.
// publishVectorRebuildProgress 将一条经过边界控制的流程更新转发给可选观察者。
func publishVectorRebuildProgress(reporter VectorRebuildProgressReporter, stage string, processed, total int) {
	if reporter == nil {
		return
	}
	if total < 0 {
		total = 0
	}
	if processed < 0 {
		processed = 0
	}
	if processed > total {
		processed = total
	}
	reporter(VectorRebuildProgress{Stage: stage, Processed: processed, Total: total})
}

// embedVectorRebuildBatch embeds one durable-memory batch and patiently waits when every configured key is only temporarily blocked by runtime budgets.
// embedVectorRebuildBatch 用于为一批长期记忆生成向量；若所有已配置 Key 只是临时被运行时预算挡住，则耐心等待后继续。
func embedVectorRebuildBatch(ctx context.Context, client appports.EmbeddingClient, records []logicdomain.MemoryRecord, logger *logx.Logger) ([][]float32, error) {
	texts := make([]string, 0, len(records))
	for idx, record := range records {
		text := strings.TrimSpace(record.Text)
		if text == "" {
			return nil, fmt.Errorf("memory record %d (%s) has empty rebuild text", idx, strings.TrimSpace(record.ID))
		}
		texts = append(texts, text)
	}
	for {
		resp, err := client.Embed(ctx, appports.EmbeddingRequest{Texts: texts})
		if err == nil {
			if err := resp.ValidateStrict(len(texts)); err != nil {
				return nil, err
			}
			return resp.Vectors, nil
		}
		if !ai_key_failover.IsBudgetExhaustedCandidatesError(err) {
			return nil, err
		}
		logger.Info("vector rebuild embedding budgets exhausted, waiting before retry", "retry_after", vectorRebuildBudgetRetryDelay.String(), "batch_size", len(records))
		if err := waitVectorRebuildBudgetWindow(ctx, vectorRebuildBudgetRetryDelay); err != nil {
			return nil, err
		}
	}
}

// validateVectorRebuildDimensions rejects unexpected embedding lengths before any durable write starts so rebuilds cannot leave split storage half-updated.
// validateVectorRebuildDimensions 用于在任何 durable 写入之前拒绝异常的 embedding 维度，避免重建把分离存储留在“只更新一半”的状态。
func validateVectorRebuildDimensions(vectors [][]float32, expectedDimension int) error {
	if expectedDimension <= 0 {
		return fmt.Errorf("configured embedding dimension must be > 0")
	}
	for idx, vector := range vectors {
		if len(vector) != expectedDimension {
			return logicdomain.ValidationError{
				Field:   fmt.Sprintf("vectors[%d]", idx),
				Message: fmt.Sprintf("must contain exactly %d dimensions", expectedDimension),
			}
		}
	}
	return nil
}

// cloneVectorRebuildRecords deep-copies one rebuild snapshot so automatic repairs and later batch rewrites never mutate the caller's original in-memory durable image.
// cloneVectorRebuildRecords 用于深拷贝一份重建快照，避免自动修复和后续批次写回意外修改调用方手里的原始 durable 内存镜像。
func cloneVectorRebuildRecords(records []logicdomain.MemoryRecord) []logicdomain.MemoryRecord {
	cloned := make([]logicdomain.MemoryRecord, 0, len(records))
	for _, record := range records {
		cloned = append(cloned, cloneVectorRebuildRecord(record))
	}
	return cloned
}

// cloneVectorRebuildRecord deep-copies one memory record's vector and metadata payload so maintenance snapshots stay isolated from later mutations.
// cloneVectorRebuildRecord 用于深拷贝单条记忆记录的向量和元数据载荷，确保维护快照与后续修改相互隔离。
func cloneVectorRebuildRecord(record logicdomain.MemoryRecord) logicdomain.MemoryRecord {
	current := record
	current.Vector = append([]float32(nil), record.Vector...)
	if record.Metadata != nil {
		current.Metadata = make(map[string]string, len(record.Metadata))
		for key, value := range record.Metadata {
			current.Metadata[key] = value
		}
	}
	return current
}

// cloneVectorRebuildBatch copies one memory batch and injects the freshly generated vectors so durable writes and later sidecar rebuilds observe the same exact payload.
// cloneVectorRebuildBatch 用于复制一批记忆记录并注入新生成的向量，让 durable 写回和后续旁路重建看到完全一致的载荷。
func cloneVectorRebuildBatch(records []logicdomain.MemoryRecord, vectors [][]float32) []logicdomain.MemoryRecord {
	cloned := make([]logicdomain.MemoryRecord, 0, len(records))
	for idx, record := range records {
		current := cloneVectorRebuildRecord(record)
		current.Vector = append([]float32(nil), vectors[idx]...)
		cloned = append(cloned, current)
	}
	return cloned
}

// waitVectorRebuildBudgetWindow blocks until either the retry delay elapses or the caller cancels the maintenance context.
// waitVectorRebuildBudgetWindow 用于阻塞到重试延迟结束，或者调用方取消维护上下文为止。
func waitVectorRebuildBudgetWindow(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
