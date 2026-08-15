// management.go implements bounded session and turn reads for the isolated management service.
// management.go 用于实现独立管理服务所需的有界会话与回合读取。
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// ManagementContentChunkBytes bounds one decoded turn-content response below normal HTTP response budgets.
	// ManagementContentChunkBytes 用于把单次解码回合内容响应限制在常规 HTTP 响应预算以内。
	ManagementContentChunkBytes = 256 << 10
	// ManagementPreviewTTL bounds the time in which one impact preview may be executed.
	// ManagementPreviewTTL 用于限制一次影响预览可被执行的有效时间。
	ManagementPreviewTTL = 15 * time.Minute
	// ManagementPurgeConfirmation is the exact localized phrase required for permanent deletion.
	// ManagementPurgeConfirmation 是永久删除必须输入的精确本地化确认短语。
	ManagementPurgeConfirmation = "永久删除"
	// ManagementMaxQueryRunes bounds human search input before it reaches a full-text adapter.
	// ManagementMaxQueryRunes 用于在人类搜索输入到达全文检索适配器前限制其字符数。
	ManagementMaxQueryRunes = 512
	// ManagementMaxOpaqueIDBytes bounds previews, operations, cursors, and idempotency coordinates.
	// ManagementMaxOpaqueIDBytes 用于限制预览、操作、游标与幂等坐标的字节长度。
	ManagementMaxOpaqueIDBytes = 256
	// ManagementMaxSourceBytes bounds the persisted human-client source marker.
	// ManagementMaxSourceBytes 用于限制持久化的人类客户端来源标记字节长度。
	ManagementMaxSourceBytes = 128
)

// ManagementUseCase owns validated management reads and opaque cursor encoding.
// ManagementUseCase 负责经过校验的管理读取与不透明游标编码。
type ManagementUseCase struct {
	store            appports.ManagementStore
	mutations        appports.ManagementMutationStore
	defaultPageSize  int
	maxPageSize      int
	hotWindowSize    int
	trashRetention   time.Duration
	vector           appports.VectorStore
	vectorQuarantine appports.ManagementVectorQuarantineStore
}

// NewManagementUseCase creates one management reader with explicit pagination limits.
// NewManagementUseCase 使用明确的分页限制创建管理读取器。
func NewManagementUseCase(store appports.ManagementStore, defaultPageSize, maxPageSize int) (*ManagementUseCase, error) {
	if store == nil {
		return nil, fmt.Errorf("management store is required")
	}
	if defaultPageSize <= 0 || maxPageSize <= 0 || defaultPageSize > maxPageSize {
		return nil, fmt.Errorf("management page limits are invalid")
	}
	useCase := &ManagementUseCase{
		store: store, defaultPageSize: defaultPageSize, maxPageSize: maxPageSize,
		hotWindowSize: 8, trashRetention: 30 * 24 * time.Hour,
	}
	useCase.mutations, _ = store.(appports.ManagementMutationStore)
	return useCase, nil
}

// mutationStore returns the configured write adapter or one explicit capability error.
// mutationStore 用于返回已配置的写入适配器，或返回明确的能力缺失错误。
func (u *ManagementUseCase) mutationStore() (appports.ManagementMutationStore, error) {
	if u.mutations == nil {
		return nil, fmt.Errorf("management mutation store is unavailable")
	}
	return u.mutations, nil
}

// ConfigureMutationPolicy applies runtime retention values used by management previews and recycle batches.
// ConfigureMutationPolicy 用于应用管理预览与回收批次使用的运行时保留策略。
func (u *ManagementUseCase) ConfigureMutationPolicy(hotWindowSize int, trashRetention time.Duration) {
	u.hotWindowSize = max(hotWindowSize, 0)
	if trashRetention > 0 {
		u.trashRetention = trashRetention
	}
}

// ConfigureVectorQuarantine enables permanent sidecar cleanup while keeping restorable recycle batches vector-complete.
// ConfigureVectorQuarantine 用于启用旁路向量的永久清理，同时保证可恢复回收批次保留完整向量。
func (u *ManagementUseCase) ConfigureVectorQuarantine(vector appports.VectorStore) error {
	if vector == nil {
		return fmt.Errorf("management vector store is required")
	}
	quarantine, ok := u.store.(appports.ManagementVectorQuarantineStore)
	if !ok || quarantine == nil {
		return fmt.Errorf("management vector quarantine store is unavailable")
	}
	u.vector = vector
	u.vectorQuarantine = quarantine
	return nil
}

// CreateRemovalPreview validates one target set, computes its live impact, and persists a short-lived token.
// CreateRemovalPreview 用于校验目标集合、计算实时影响并持久化短期令牌。
func (u *ManagementUseCase) CreateRemovalPreview(ctx context.Context, selection logicdomain.ManagementRemovalSelection) (logicdomain.ManagementPreview, error) {
	selection, err := normalizeManagementSelection(selection)
	if err != nil {
		return logicdomain.ManagementPreview{}, err
	}
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementPreview{}, err
	}
	impact, err := mutations.InspectManagementSelection(ctx, selection, u.hotWindowSize)
	if err != nil {
		return logicdomain.ManagementPreview{}, err
	}
	now := time.Now().UTC()
	previewToken, err := randomManagementID("preview")
	if err != nil {
		return logicdomain.ManagementPreview{}, err
	}
	preview := logicdomain.ManagementPreview{
		Token: previewToken, Selection: selection, Impact: impact,
		CreatedAt: now, ExpiresAt: now.Add(ManagementPreviewTTL),
	}
	if selection.Action == logicdomain.ManagementActionPurge {
		preview.ConfirmText = ManagementPurgeConfirmation
	}
	if err := mutations.SaveManagementPreview(ctx, preview); err != nil {
		return logicdomain.ManagementPreview{}, err
	}
	return preview, nil
}

// ExecuteRemoval executes one preview-bound archive, unarchive, recycle, restart, or purge operation exactly once per idempotency key.
// ExecuteRemoval 用于按预览执行归档、取消归档、回收、重新开始或永久清理，并按幂等键保证只执行一次。
func (u *ManagementUseCase) ExecuteRemoval(ctx context.Context, previewToken, idempotencyKey, confirmText string) (logicdomain.ManagementOperation, error) {
	return u.ExecuteRemovalForAction(ctx, previewToken, idempotencyKey, confirmText, "")
}

// ExecuteRemovalForAction executes one preview only when it belongs to the expected endpoint action.
// ExecuteRemovalForAction 用于仅在预览属于端点期望动作时执行该预览。
func (u *ManagementUseCase) ExecuteRemovalForAction(ctx context.Context, previewToken, idempotencyKey, confirmText, expectedAction string) (logicdomain.ManagementOperation, error) {
	previewToken = strings.TrimSpace(previewToken)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if previewToken == "" {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "preview_token", Message: "is required"}
	}
	if len(previewToken) > ManagementMaxOpaqueIDBytes {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "preview_token", Message: "is too long"}
	}
	if idempotencyKey == "" {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "Idempotency-Key", Message: "is required"}
	}
	if len(idempotencyKey) > ManagementMaxOpaqueIDBytes {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "Idempotency-Key", Message: "is too long"}
	}
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	preview, err := mutations.GetManagementPreview(ctx, previewToken)
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if (expectedAction == "non_purge" && preview.Selection.Action == logicdomain.ManagementActionPurge) ||
		(expectedAction != "" && expectedAction != "non_purge" && preview.Selection.Action != expectedAction) {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "preview_token", Message: "was issued for a different endpoint"}
	}
	if existing, lookupErr := mutations.GetManagementOperationByIdempotencyKey(ctx, idempotencyKey); lookupErr == nil {
		if !preview.Selection.MatchesOperation(existing) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return existing, nil
	} else if !logicdomain.IsNotFoundError(lookupErr) {
		return logicdomain.ManagementOperation{}, lookupErr
	}
	now := time.Now().UTC()
	if !preview.ConsumedAt.IsZero() {
		return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "preview", Message: "preview has already been consumed"}
	}
	if !preview.ExpiresAt.After(now) {
		return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "preview", Message: "preview has expired"}
	}
	currentImpact, err := mutations.InspectManagementSelection(ctx, preview.Selection, u.hotWindowSize)
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if currentImpact.Revision != preview.Impact.Revision {
		return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "preview", Message: "preview is stale"}
	}
	if preview.Selection.Action == logicdomain.ManagementActionRecycle && currentImpact.ProtectedCount > 0 {
		return logicdomain.ManagementOperation{}, logicdomain.ProtectedResourceError{Resource: preview.Selection.TargetType, Message: "selected data includes pending or hot-window turns"}
	}
	if preview.Selection.Action == logicdomain.ManagementActionPurge && confirmText != ManagementPurgeConfirmation {
		return logicdomain.ManagementOperation{}, logicdomain.ConfirmationRequiredError{Resource: "recycle batch", Message: "type the required permanent-delete phrase", Code: ManagementPurgeConfirmation}
	}
	operationID, err := randomManagementID("operation")
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	command := logicdomain.ManagementMutationCommand{
		OperationID: operationID, IdempotencyKey: idempotencyKey,
		Preview: preview, ConfirmText: confirmText, Now: now, ExpiresAt: now.Add(u.trashRetention),
	}
	if preview.Selection.Action == logicdomain.ManagementActionPurge {
		vectorIDs := []string(nil)
		if u.vectorQuarantine != nil {
			vectorIDs, err = u.vectorQuarantine.ListManagementRecycleBatchVectorIDs(ctx, preview.Selection.TargetIDs)
			if err != nil {
				return logicdomain.ManagementOperation{}, fmt.Errorf("load management purge vectors: %w", err)
			}
		}
		operation, purgeErr := mutations.PurgeManagementBatch(ctx, command)
		if purgeErr != nil {
			return operation, purgeErr
		}
		u.cleanupPurgedVectors(vectorIDs, preview.Selection.TargetIDs, now)
		return operation, nil
	}
	return mutations.ApplyManagementRemoval(ctx, command)
}

// cleanupPurgedVectors deletes quarantined sidecar vectors after the relational purge commits and queues transient failures.
// cleanupPurgedVectors 在关系侧永久清理提交后删除隔离的旁路向量，并将瞬时失败加入补偿队列。
func (u *ManagementUseCase) cleanupPurgedVectors(vectorIDs []string, batchIDs []uint64, now time.Time) {
	vectorIDs = normalizeVectorGCIDs(vectorIDs)
	if len(vectorIDs) == 0 || u.vector == nil {
		return
	}
	cleanupCtx, cleanupCancel := newPostCommitVectorCleanupContext()
	defer cleanupCancel()
	if _, err := u.vector.DeleteByIDs(cleanupCtx, vectorIDs); err != nil {
		enqueueVectorGCCompensation(
			cleanupCtx,
			u.store,
			nil,
			logicdomain.VectorGCJobTypeManagementPurge,
			vectorIDs,
			now,
			"batch_ids",
			batchIDs,
			"err",
			err,
		)
	}
}

// RestoreRecycleBatch restores one explicitly restorable manual batch using an idempotent operation.
// RestoreRecycleBatch 用于通过幂等操作恢复一个明确可恢复的人工批次。
func (u *ManagementUseCase) RestoreRecycleBatch(ctx context.Context, batchID uint64, idempotencyKey string) (logicdomain.ManagementOperation, error) {
	if batchID == 0 {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "batch_id", Message: "must be positive"}
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "Idempotency-Key", Message: "is required"}
	}
	if len(idempotencyKey) > ManagementMaxOpaqueIDBytes {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "Idempotency-Key", Message: "is too long"}
	}
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	selection := logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetRecycleBatch,
		TargetIDs:  []uint64{batchID},
		Action:     logicdomain.ManagementActionRestore,
	}
	if existing, lookupErr := mutations.GetManagementOperationByIdempotencyKey(ctx, idempotencyKey); lookupErr == nil {
		if !selection.MatchesOperation(existing) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return existing, nil
	} else if !logicdomain.IsNotFoundError(lookupErr) {
		return logicdomain.ManagementOperation{}, lookupErr
	}
	operationID, err := randomManagementID("operation")
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	return mutations.RestoreManagementBatch(ctx, logicdomain.ManagementRestoreCommand{
		OperationID: operationID, IdempotencyKey: idempotencyKey,
		BatchID: batchID, Now: time.Now().UTC(),
	})
}

// GetOperation returns one persisted management-operation status.
// GetOperation 用于返回一条已持久化管理操作状态。
func (u *ManagementUseCase) GetOperation(ctx context.Context, operationID string) (logicdomain.ManagementOperation, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "operation_id", Message: "is required"}
	}
	if len(operationID) > ManagementMaxOpaqueIDBytes {
		return logicdomain.ManagementOperation{}, logicdomain.ValidationError{Field: "operation_id", Message: "is too long"}
	}
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	return mutations.GetManagementOperation(ctx, operationID)
}

// ListRecycleBatches returns one bounded recycle-bin page.
// ListRecycleBatches 用于返回一页有界回收站批次。
func (u *ManagementUseCase) ListRecycleBatches(ctx context.Context, cursorID uint64, limit int) (logicdomain.ManagementRecycleBatchPage, error) {
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementRecycleBatchPage{}, err
	}
	return mutations.ListManagementRecycleBatches(ctx, cursorID, u.normalizeLimit(limit))
}

// GetRecycleBatch returns one recycle batch with its exact recoverability state.
// GetRecycleBatch 用于返回一个带精确可恢复状态的回收批次。
func (u *ManagementUseCase) GetRecycleBatch(ctx context.Context, batchID uint64) (logicdomain.ManagementRecycleBatch, error) {
	if batchID == 0 {
		return logicdomain.ManagementRecycleBatch{}, logicdomain.ValidationError{Field: "batch_id", Message: "must be positive"}
	}
	mutations, err := u.mutationStore()
	if err != nil {
		return logicdomain.ManagementRecycleBatch{}, err
	}
	return mutations.GetManagementRecycleBatch(ctx, batchID)
}

// normalizeManagementSelection validates actions and returns sorted unique target identifiers.
// normalizeManagementSelection 用于校验动作并返回排序去重后的目标标识。
func normalizeManagementSelection(selection logicdomain.ManagementRemovalSelection) (logicdomain.ManagementRemovalSelection, error) {
	selection.TargetType = strings.TrimSpace(selection.TargetType)
	selection.Action = strings.TrimSpace(selection.Action)
	selection.Source = strings.TrimSpace(selection.Source)
	if selection.Source == "" {
		selection.Source = "vulcan-code"
	}
	if len(selection.Source) > ManagementMaxSourceBytes {
		return selection, logicdomain.ValidationError{Field: "source", Message: "must not exceed 128 bytes"}
	}
	allowedTarget := selection.TargetType == logicdomain.ManagementTargetSession || selection.TargetType == logicdomain.ManagementTargetTurn
	if selection.Action == logicdomain.ManagementActionPurge {
		allowedTarget = selection.TargetType == logicdomain.ManagementTargetRecycleBatch
	}
	if !allowedTarget {
		return selection, logicdomain.ValidationError{Field: "target_type", Message: "is not valid for the requested action"}
	}
	if selection.Action != logicdomain.ManagementActionArchive && selection.Action != logicdomain.ManagementActionUnarchive && selection.Action != logicdomain.ManagementActionRecycle && selection.Action != logicdomain.ManagementActionRestart && selection.Action != logicdomain.ManagementActionPurge {
		return selection, logicdomain.ValidationError{Field: "action", Message: "must be archive, unarchive, recycle, restart, or purge"}
	}
	if selection.TargetType == logicdomain.ManagementTargetTurn && (selection.Action == logicdomain.ManagementActionArchive || selection.Action == logicdomain.ManagementActionUnarchive || selection.Action == logicdomain.ManagementActionRestart) {
		return selection, logicdomain.ValidationError{Field: "action", Message: "turns cannot be archived or restarted"}
	}
	seen := make(map[uint64]struct{}, len(selection.TargetIDs))
	normalized := make([]uint64, 0, len(selection.TargetIDs))
	for _, targetID := range selection.TargetIDs {
		if targetID == 0 {
			return selection, logicdomain.ValidationError{Field: "target_ids", Message: "must contain positive identifiers"}
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		normalized = append(normalized, targetID)
	}
	if len(normalized) == 0 || len(normalized) > 100 {
		return selection, logicdomain.ValidationError{Field: "target_ids", Message: "must contain between 1 and 100 unique identifiers"}
	}
	slices.Sort(normalized)
	selection.TargetIDs = normalized
	return selection, nil
}

// randomManagementID returns one opaque high-entropy operation or preview identifier.
// randomManagementID 用于返回一个高熵不透明操作或预览标识。
func randomManagementID(prefix string) (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate management identifier: %w", err)
	}
	return strings.TrimSpace(prefix) + "_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

// ManagementSessionFilters contains caller-visible session filters before normalization.
// ManagementSessionFilters 用于保存规范化前调用方可见的会话筛选条件。
type ManagementSessionFilters struct {
	UserID      uint64
	ProjectID   uint64
	Query       string
	Status      string
	CreatedFrom time.Time
	CreatedTo   time.Time
	UpdatedFrom time.Time
	UpdatedTo   time.Time
	Cursor      string
	Limit       int
	Sort        string
}

// ManagementSessionResult returns one session page and its opaque continuation token.
// ManagementSessionResult 用于返回一页会话以及不透明的后续令牌。
type ManagementSessionResult struct {
	Items      []logicdomain.ManagementSessionRecord
	NextCursor string
	HasMore    bool
}

// ListSessions validates filters and returns one deterministic cursor page.
// ListSessions 用于校验筛选条件并返回一页确定性游标结果。
func (u *ManagementUseCase) ListSessions(ctx context.Context, filters ManagementSessionFilters) (ManagementSessionResult, error) {
	if utf8.RuneCountInString(strings.TrimSpace(filters.Query)) > ManagementMaxQueryRunes {
		return ManagementSessionResult{}, logicdomain.ValidationError{Field: "query", Message: "must not exceed 512 characters"}
	}
	if len(strings.TrimSpace(filters.Cursor)) > ManagementMaxOpaqueIDBytes {
		return ManagementSessionResult{}, logicdomain.ValidationError{Field: "cursor", Message: "is too long"}
	}
	query := logicdomain.ManagementSessionQuery{
		UserID: filters.UserID, ProjectID: filters.ProjectID, Query: strings.TrimSpace(filters.Query),
		Status: strings.TrimSpace(filters.Status), CreatedFrom: filters.CreatedFrom, CreatedTo: filters.CreatedTo,
		UpdatedFrom: filters.UpdatedFrom, UpdatedTo: filters.UpdatedTo, Limit: u.normalizeLimit(filters.Limit),
		Sort: strings.TrimSpace(filters.Sort),
	}
	if query.Status == "" {
		query.Status = "active"
	}
	if query.Status != "active" && query.Status != "archived" && query.Status != "recycled" && query.Status != "forgotten" && query.Status != "all" {
		return ManagementSessionResult{}, logicdomain.ValidationError{Field: "status", Message: "must be active, archived, recycled, forgotten, or all"}
	}
	if query.Sort == "" {
		query.Sort = "updated_desc"
	}
	if query.Sort != "updated_desc" && query.Sort != "updated_asc" {
		return ManagementSessionResult{}, logicdomain.ValidationError{Field: "sort", Message: "must be updated_desc or updated_asc"}
	}
	if strings.TrimSpace(filters.Cursor) != "" {
		cursor, err := decodeManagementSessionCursor(filters.Cursor, query.Sort)
		if err != nil {
			return ManagementSessionResult{}, err
		}
		query.Cursor = cursor
	}
	page, err := u.store.ListManagementSessions(ctx, query)
	if err != nil {
		return ManagementSessionResult{}, err
	}
	next := ""
	if page.HasMore {
		next = encodeManagementSessionCursor(page.NextCursor, query.Sort)
	}
	return ManagementSessionResult{Items: page.Items, NextCursor: next, HasMore: page.HasMore}, nil
}

// GetSession returns one session-management projection by stable internal identifier.
// GetSession 用于按稳定内部标识返回单条会话管理投影。
func (u *ManagementUseCase) GetSession(ctx context.Context, sessionID uint64) (logicdomain.ManagementSessionRecord, error) {
	if sessionID == 0 {
		return logicdomain.ManagementSessionRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must be positive"}
	}
	return u.store.GetManagementSession(ctx, sessionID)
}

// ManagementTurnFilters contains caller-visible turn filters before normalization.
// ManagementTurnFilters 用于保存规范化前调用方可见的回合筛选条件。
type ManagementTurnFilters struct {
	SessionID   uint64
	UserID      uint64
	ProjectID   uint64
	Query       string
	Status      string
	CreatedFrom time.Time
	CreatedTo   time.Time
	HasMemory   *bool
	HasProfile  *bool
	Cursor      string
	Limit       int
	Sort        string
}

// ManagementTurnResult returns one turn page and its opaque continuation token.
// ManagementTurnResult 用于返回一页回合以及不透明的后续令牌。
type ManagementTurnResult struct {
	Items      []logicdomain.ManagementTurnRecord
	NextCursor string
	HasMore    bool
}

// ListTurns validates filters and returns one ascending stable turn page.
// ListTurns 用于校验筛选条件并返回一页按升序稳定排列的回合。
func (u *ManagementUseCase) ListTurns(ctx context.Context, filters ManagementTurnFilters) (ManagementTurnResult, error) {
	if utf8.RuneCountInString(strings.TrimSpace(filters.Query)) > ManagementMaxQueryRunes {
		return ManagementTurnResult{}, logicdomain.ValidationError{Field: "query", Message: "must not exceed 512 characters"}
	}
	if len(strings.TrimSpace(filters.Cursor)) > ManagementMaxOpaqueIDBytes {
		return ManagementTurnResult{}, logicdomain.ValidationError{Field: "cursor", Message: "is too long"}
	}
	status := strings.TrimSpace(filters.Status)
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "pending" && status != "extracted" && status != "passed" {
		return ManagementTurnResult{}, logicdomain.ValidationError{Field: "status", Message: "must be all, pending, extracted, or passed"}
	}
	sort := strings.TrimSpace(filters.Sort)
	if sort == "" {
		sort = "id_asc"
	}
	if sort != "id_asc" && sort != "id_desc" {
		return ManagementTurnResult{}, logicdomain.ValidationError{Field: "sort", Message: "must be id_asc or id_desc"}
	}
	cursorID, err := decodeManagementTurnCursor(filters.Cursor)
	if err != nil {
		return ManagementTurnResult{}, err
	}
	page, err := u.store.ListManagementTurns(ctx, logicdomain.ManagementTurnQuery{
		SessionID: filters.SessionID, UserID: filters.UserID, ProjectID: filters.ProjectID,
		Query: strings.TrimSpace(filters.Query), Status: status, CreatedFrom: filters.CreatedFrom,
		CreatedTo: filters.CreatedTo, HasMemory: filters.HasMemory, HasProfile: filters.HasProfile,
		CursorID: cursorID, Limit: u.normalizeLimit(filters.Limit), Sort: sort,
	})
	if err != nil {
		return ManagementTurnResult{}, err
	}
	next := ""
	if page.HasMore {
		next = encodeManagementTurnCursor(page.NextCursorID)
	}
	return ManagementTurnResult{Items: page.Items, NextCursor: next, HasMore: page.HasMore}, nil
}

// GetTurn returns one persisted turn by stable internal identifier.
// GetTurn 用于按稳定内部标识返回单条持久化回合。
func (u *ManagementUseCase) GetTurn(ctx context.Context, turnID uint64) (logicdomain.ManagementTurnRecord, error) {
	if turnID == 0 {
		return logicdomain.ManagementTurnRecord{}, logicdomain.ValidationError{Field: "turn_id", Message: "must be positive"}
	}
	return u.store.GetManagementTurn(ctx, turnID)
}

// ParseTurnContent decodes the durable turn payload through the same parser used by runtime turn-detail reads.
// ParseTurnContent 使用运行时回合详情读取共用的解析器解码持久化回合载荷。
func (u *ManagementUseCase) ParseTurnContent(turn logicdomain.ManagementTurnRecord) (string, []logicdomain.TurnDetailTimelineItem, string, error) {
	return parseDehydratedTurnContent(turn.SessionTurnRecord.DehydratedContent)
}

// SessionDisplayTitle derives a safe human-facing title from the first user message without exposing the external session key.
// SessionDisplayTitle 用于从首条用户消息推导安全的人类可读标题，同时不暴露外部会话键。
func (u *ManagementUseCase) SessionDisplayTitle(record logicdomain.ManagementSessionRecord) string {
	userContent, _, _, err := parseDehydratedTurnContent(record.FirstTurnContent)
	if err == nil {
		if excerpt := truncateManagementText(userContent, 120); excerpt != "" {
			return excerpt
		}
	}
	return "Untitled session · " + record.CreatedAt.UTC().Format("2006-01-02")
}

// TurnDisplayExcerpt derives one compact human-facing excerpt from a durable turn.
// TurnDisplayExcerpt 用于从持久化回合推导紧凑的人类可读摘要。
func (u *ManagementUseCase) TurnDisplayExcerpt(turn logicdomain.ManagementTurnRecord) string {
	userContent, _, assistantContent, err := parseDehydratedTurnContent(turn.SessionTurnRecord.DehydratedContent)
	if err != nil {
		return "Content unavailable"
	}
	if excerpt := truncateManagementText(userContent, 120); excerpt != "" {
		return excerpt
	}
	if excerpt := truncateManagementText(assistantContent, 120); excerpt != "" {
		return excerpt
	}
	return "Empty turn"
}

// truncateManagementText normalizes whitespace and truncates one display string by Unicode code points.
// truncateManagementText 用于规范化空白并按 Unicode 码点截断显示文本。
func truncateManagementText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

// SliceUTF8Content returns one byte-addressed but UTF-8-safe content chunk.
// SliceUTF8Content 用于返回按字节寻址但保持 UTF-8 安全边界的内容分片。
func SliceUTF8Content(content string, offset, limit int) (string, int, bool, error) {
	if offset < 0 {
		return "", 0, false, logicdomain.ValidationError{Field: "offset", Message: "must not be negative"}
	}
	if limit <= 0 || limit > ManagementContentChunkBytes {
		return "", 0, false, logicdomain.ValidationError{Field: "limit", Message: fmt.Sprintf("must be between 1 and %d", ManagementContentChunkBytes)}
	}
	data := []byte(content)
	if offset > len(data) || (offset < len(data) && !utf8.RuneStart(data[offset])) {
		return "", 0, false, logicdomain.ValidationError{Field: "offset", Message: "must point to a UTF-8 boundary"}
	}
	end := offset + limit
	if end > len(data) {
		end = len(data)
	}
	for end > offset && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	return string(data[offset:end]), end, end < len(data), nil
}

// ListUsers returns durable memory users for human-readable management filters.
// ListUsers 用于返回供人类可读管理筛选使用的长期记忆用户。
func (u *ManagementUseCase) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	return u.store.ListUsers(ctx)
}

// ListProjects returns durable memory projects with canonical display paths.
// ListProjects 用于返回带规范显示路径的长期记忆项目。
func (u *ManagementUseCase) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return u.store.ListProjects(ctx)
}

// normalizeLimit applies configured management pagination bounds.
// normalizeLimit 用于应用已配置的管理分页边界。
func (u *ManagementUseCase) normalizeLimit(limit int) int {
	if limit <= 0 {
		return u.defaultPageSize
	}
	if limit > u.maxPageSize {
		return u.maxPageSize
	}
	return limit
}

type managementSessionCursorEnvelope struct {
	UpdatedAt int64  `json:"updated_at"`
	ID        uint64 `json:"id"`
	Sort      string `json:"sort"`
}

// encodeManagementSessionCursor serializes one session cursor into an opaque URL-safe token.
// encodeManagementSessionCursor 用于把会话游标序列化为不透明的 URL 安全令牌。
func encodeManagementSessionCursor(cursor logicdomain.ManagementCursor, sort string) string {
	payload, _ := json.Marshal(managementSessionCursorEnvelope{UpdatedAt: cursor.UpdatedAt.UTC().UnixMilli(), ID: cursor.ID, Sort: sort})
	return base64.RawURLEncoding.EncodeToString(payload)
}

// decodeManagementSessionCursor validates and decodes one opaque session cursor.
// decodeManagementSessionCursor 用于校验并解码一个不透明会话游标。
func decodeManagementSessionCursor(raw, sort string) (logicdomain.ManagementCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return logicdomain.ManagementCursor{}, logicdomain.ValidationError{Field: "cursor", Message: "is invalid"}
	}
	envelope := managementSessionCursorEnvelope{}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.ID == 0 || envelope.UpdatedAt <= 0 || envelope.Sort != sort {
		return logicdomain.ManagementCursor{}, logicdomain.ValidationError{Field: "cursor", Message: "is invalid for the selected sort"}
	}
	return logicdomain.ManagementCursor{UpdatedAt: time.UnixMilli(envelope.UpdatedAt).UTC(), ID: envelope.ID}, nil
}

// encodeManagementTurnCursor serializes one turn cursor into an opaque URL-safe token.
// encodeManagementTurnCursor 用于把回合游标序列化为不透明的 URL 安全令牌。
func encodeManagementTurnCursor(turnID uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d", turnID)))
}

// decodeManagementTurnCursor validates and decodes one opaque turn cursor.
// decodeManagementTurnCursor 用于校验并解码一个不透明回合游标。
func decodeManagementTurnCursor(raw string) (uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return 0, logicdomain.ValidationError{Field: "cursor", Message: "is invalid"}
	}
	var turnID uint64
	if _, err := fmt.Sscanf(string(payload), "%d", &turnID); err != nil || turnID == 0 {
		return 0, logicdomain.ValidationError{Field: "cursor", Message: "is invalid"}
	}
	return turnID, nil
}
