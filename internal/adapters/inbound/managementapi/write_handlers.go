// write_handlers.go exposes preview-bound management mutations through the isolated HTTP surface.
// write_handlers.go 用于通过独立 HTTP 管理面暴露受预览约束的管理变更。
package managementapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// errManagementBodyTooLarge identifies a request rejected by the configured management body limit.
// errManagementBodyTooLarge 标识被管理请求体上限拒绝的请求。
var errManagementBodyTooLarge = errors.New("management request body exceeds configured limit")

// removalPreviewRequest is the strict HTTP request for one impact preview.
// removalPreviewRequest 是一次影响预览使用的严格 HTTP 请求。
type removalPreviewRequest struct {
	TargetType string   `json:"target_type"`
	TargetIDs  []string `json:"target_ids"`
	Action     string   `json:"action"`
	Source     string   `json:"source"`
}

// executePreviewRequest identifies one previously issued preview.
// executePreviewRequest 用于标识一个此前签发的预览。
type executePreviewRequest struct {
	PreviewToken string `json:"preview_token"`
	ConfirmText  string `json:"confirm_text,omitempty"`
}

// restoreBatchRequest identifies one restorable recycle batch.
// restoreBatchRequest 用于标识一个可恢复回收批次。
type restoreBatchRequest struct {
	BatchID string `json:"batch_id"`
}

// managementImpactResponse is the JSON projection of one exact impact preview.
// managementImpactResponse 是一次精确影响预览的 JSON 投影。
type managementImpactResponse struct {
	PreviewToken       string   `json:"preview_token"`
	ExpiresAt          string   `json:"expires_at"`
	ImpactRevision     string   `json:"impact_revision"`
	TargetType         string   `json:"target_type"`
	TargetIDs          []string `json:"target_ids"`
	Action             string   `json:"action"`
	SessionCount       int      `json:"session_count"`
	TurnCount          int      `json:"turn_count"`
	MemoryCount        int      `json:"memory_count"`
	ContextEdgeCount   int      `json:"context_edge_count"`
	VectorCount        int      `json:"vector_count"`
	ProfileNodeCount   int      `json:"profile_node_count"`
	PendingTurnCount   int      `json:"pending_turn_count"`
	HotWindowTurnCount int      `json:"hot_window_turn_count"`
	ProtectedCount     int      `json:"protected_count"`
	ConfirmText        string   `json:"required_confirmation,omitempty"`
}

// managementOperationResponse is the string-safe JSON projection of one mutation.
// managementOperationResponse 是一次变更的字符串安全 JSON 投影。
type managementOperationResponse struct {
	OperationID  string   `json:"operation_id"`
	Action       string   `json:"action"`
	TargetType   string   `json:"target_type"`
	TargetIDs    []string `json:"target_ids"`
	Status       string   `json:"status"`
	BatchID      string   `json:"batch_id,omitempty"`
	ErrorCode    string   `json:"error_code,omitempty"`
	ErrorMessage string   `json:"error_message,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	CompletedAt  string   `json:"completed_at,omitempty"`
}

// managementRecycleBatchResponse is the human-management projection of one recycle batch.
// managementRecycleBatchResponse 是一个回收批次的人工管理投影。
type managementRecycleBatchResponse struct {
	BatchID      string   `json:"batch_id"`
	Source       string   `json:"source"`
	TargetType   string   `json:"target_type,omitempty"`
	TargetIDs    []string `json:"target_ids"`
	Restorable   bool     `json:"restorable"`
	State        string   `json:"state"`
	Reason       string   `json:"reason"`
	SessionCount int      `json:"session_count"`
	TurnCount    int      `json:"turn_count"`
	MemoryCount  int      `json:"memory_count"`
	ProfileCount int      `json:"profile_count"`
	RecycledAt   string   `json:"recycled_at"`
	ExpiresAt    string   `json:"expires_at,omitempty"`
	RestoredAt   string   `json:"restored_at,omitempty"`
	PurgedAt     string   `json:"purged_at,omitempty"`
}

// registerManagementWriteRoutes binds previews, mutations, operations, and recycle-bin reads.
// registerManagementWriteRoutes 用于绑定预览、变更、操作状态及回收站读取。
func registerManagementWriteRoutes(mux *http.ServeMux, management *usecase.ManagementUseCase) {
	if management == nil {
		return
	}
	mux.HandleFunc("POST /management/v1/removal-previews", func(writer http.ResponseWriter, request *http.Request) {
		var payload removalPreviewRequest
		if err := decodeStrictManagementJSON(request, &payload); err != nil {
			writeManagementError(writer, request, err)
			return
		}
		targetIDs, err := parseManagementStringIDs(payload.TargetIDs)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		preview, err := management.CreateRemovalPreview(request.Context(), logicdomain.ManagementRemovalSelection{
			TargetType: payload.TargetType, TargetIDs: targetIDs, Action: payload.Action, Source: payload.Source,
		})
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusCreated, projectManagementPreview(preview))
	})
	execute := func(writer http.ResponseWriter, request *http.Request, requiredAction string) {
		var payload executePreviewRequest
		if err := decodeStrictManagementJSON(request, &payload); err != nil {
			writeManagementError(writer, request, err)
			return
		}
		operation, err := management.ExecuteRemovalForAction(request.Context(), payload.PreviewToken, request.Header.Get("Idempotency-Key"), payload.ConfirmText, requiredAction)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementOperation(operation))
	}
	mux.HandleFunc("POST /management/v1/removals", func(writer http.ResponseWriter, request *http.Request) {
		execute(writer, request, "non_purge")
	})
	mux.HandleFunc("POST /management/v1/purges", func(writer http.ResponseWriter, request *http.Request) {
		execute(writer, request, logicdomain.ManagementActionPurge)
	})
	mux.HandleFunc("POST /management/v1/restores", func(writer http.ResponseWriter, request *http.Request) {
		var payload restoreBatchRequest
		if err := decodeStrictManagementJSON(request, &payload); err != nil {
			writeManagementError(writer, request, err)
			return
		}
		batchID, err := parseManagementID("batch_id", payload.BatchID)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		operation, err := management.RestoreRecycleBatch(request.Context(), batchID, request.Header.Get("Idempotency-Key"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementOperation(operation))
	})
	mux.HandleFunc("GET /management/v1/operations/{operation_id}", func(writer http.ResponseWriter, request *http.Request) {
		operation, err := management.GetOperation(request.Context(), request.PathValue("operation_id"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementOperation(operation))
	})
	mux.HandleFunc("GET /management/v1/recycle-batches", func(writer http.ResponseWriter, request *http.Request) {
		cursorID, err := parseOptionalManagementID("cursor", request.URL.Query().Get("cursor"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		limit, err := parseOptionalManagementInt("limit", request.URL.Query().Get("limit"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		page, err := management.ListRecycleBatches(request.Context(), cursorID, limit)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementRecycleBatchResponse, 0, len(page.Items))
		for _, batch := range page.Items {
			items = append(items, projectManagementRecycleBatch(batch))
		}
		nextCursor := ""
		if page.NextCursorID != 0 {
			nextCursor = strconv.FormatUint(page.NextCursorID, 10)
		}
		writeJSON(writer, http.StatusOK, managementPageResponse[managementRecycleBatchResponse]{Items: items, NextCursor: nextCursor, HasMore: page.HasMore})
	})
	mux.HandleFunc("GET /management/v1/recycle-batches/{batch_id}", func(writer http.ResponseWriter, request *http.Request) {
		batchID, err := parseManagementID("batch_id", request.PathValue("batch_id"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		batch, err := management.GetRecycleBatch(request.Context(), batchID)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementRecycleBatch(batch))
	})
}

// decodeStrictManagementJSON rejects trailing values and unknown fields.
// decodeStrictManagementJSON 用于拒绝尾随值与未知字段。
func decodeStrictManagementJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errManagementBodyTooLarge
		}
		return logicdomain.ValidationError{Field: "body", Message: err.Error()}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return logicdomain.ValidationError{Field: "body", Message: "must contain one JSON value"}
	}
	return nil
}

// parseManagementStringIDs parses string-safe JSON identifiers.
// parseManagementStringIDs 用于解析 JSON 中字符串安全的标识。
func parseManagementStringIDs(values []string) ([]uint64, error) {
	result := make([]uint64, 0, len(values))
	for index, value := range values {
		parsed, err := parseManagementID(fmt.Sprintf("target_ids[%d]", index), value)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

// projectManagementPreview converts one preview to string-safe JSON.
// projectManagementPreview 用于把预览转换为字符串安全 JSON。
func projectManagementPreview(preview logicdomain.ManagementPreview) managementImpactResponse {
	return managementImpactResponse{
		PreviewToken: preview.Token, ExpiresAt: formatManagementTime(preview.ExpiresAt),
		ImpactRevision: preview.Impact.Revision, TargetType: preview.Selection.TargetType,
		TargetIDs: projectManagementIDs(preview.Selection.TargetIDs), Action: preview.Selection.Action,
		SessionCount: preview.Impact.SessionCount, TurnCount: preview.Impact.TurnCount,
		MemoryCount: preview.Impact.MemoryCount, ContextEdgeCount: preview.Impact.ContextEdgeCount,
		VectorCount: preview.Impact.VectorCount, ProfileNodeCount: preview.Impact.ProfileNodeCount,
		PendingTurnCount: preview.Impact.PendingTurnCount, HotWindowTurnCount: preview.Impact.HotWindowTurnCount,
		ProtectedCount: preview.Impact.ProtectedCount, ConfirmText: preview.ConfirmText,
	}
}

// projectManagementOperation converts one operation to string-safe JSON.
// projectManagementOperation 用于把操作转换为字符串安全 JSON。
func projectManagementOperation(operation logicdomain.ManagementOperation) managementOperationResponse {
	response := managementOperationResponse{
		OperationID: operation.ID, Action: operation.Action, TargetType: operation.TargetType,
		TargetIDs: projectManagementIDs(operation.TargetIDs), Status: operation.Status,
		ErrorCode: operation.ErrorCode, ErrorMessage: operation.ErrorMessage,
		CreatedAt: formatManagementTime(operation.CreatedAt), UpdatedAt: formatManagementTime(operation.UpdatedAt),
	}
	if operation.BatchID != 0 {
		response.BatchID = strconv.FormatUint(operation.BatchID, 10)
	}
	if !operation.CompletedAt.IsZero() {
		response.CompletedAt = formatManagementTime(operation.CompletedAt)
	}
	return response
}

// projectManagementRecycleBatch converts one batch to string-safe JSON.
// projectManagementRecycleBatch 用于把回收批次转换为字符串安全 JSON。
func projectManagementRecycleBatch(batch logicdomain.ManagementRecycleBatch) managementRecycleBatchResponse {
	response := managementRecycleBatchResponse{
		BatchID: strconv.FormatUint(batch.BatchID, 10), Source: batch.Source, TargetType: batch.TargetType,
		TargetIDs: projectManagementIDs(batch.TargetIDs), Restorable: batch.Restorable, State: batch.State,
		Reason: batch.Reason, SessionCount: batch.SessionCount, TurnCount: batch.TurnCount,
		MemoryCount: batch.MemoryCount, ProfileCount: batch.ProfileCount, RecycledAt: formatManagementTime(batch.RecycledAt),
	}
	if !batch.ExpiresAt.IsZero() {
		response.ExpiresAt = formatManagementTime(batch.ExpiresAt)
	}
	if !batch.RestoredAt.IsZero() {
		response.RestoredAt = formatManagementTime(batch.RestoredAt)
	}
	if !batch.PurgedAt.IsZero() {
		response.PurgedAt = formatManagementTime(batch.PurgedAt)
	}
	return response
}

// projectManagementIDs converts uint64 identifiers into exact decimal strings.
// projectManagementIDs 用于把 uint64 标识转换为精确十进制字符串。
func projectManagementIDs(ids []uint64) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, strconv.FormatUint(id, 10))
	}
	return result
}
