// read_handlers.go exposes bounded session, turn, user, and project management resources.
// read_handlers.go 用于暴露有界会话、回合、用户与项目管理资源。
package managementapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// managementIdentityResponse is one human-readable selectable memory identity.
// managementIdentityResponse 是一条人类可读且可选择的记忆身份。
type managementIdentityResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// managementProjectResponse is one selectable memory project with its canonical path.
// managementProjectResponse 是一个带规范路径的可选择记忆项目。
type managementProjectResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// managementPageResponse wraps one cursor page without exposing database pagination details.
// managementPageResponse 用于包装游标分页，同时不暴露数据库分页细节。
type managementPageResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

// managementSessionResponse is the bounded HTTP projection for one session summary or detail.
// managementSessionResponse 是单条会话摘要或详情的有界 HTTP 投影。
type managementSessionResponse struct {
	SessionID             string                     `json:"session_id"`
	SessionKey            string                     `json:"session_key"`
	DisplayTitle          string                     `json:"display_title"`
	User                  managementIdentityResponse `json:"user"`
	Project               managementProjectResponse  `json:"project"`
	Status                string                     `json:"status"`
	CreatedAt             string                     `json:"created_at"`
	UpdatedAt             string                     `json:"updated_at"`
	HistoricalTurnCount   int                        `json:"historical_turn_count"`
	VisibleTurnCount      int                        `json:"visible_turn_count"`
	PendingTurnCount      int                        `json:"pending_turn_count"`
	ActiveMemoryCount     int                        `json:"active_memory_count"`
	ProfileReferenceCount int                        `json:"profile_reference_count"`
	LastSummarizedTurnID  string                     `json:"last_summarized_turn_id,omitempty"`
	LastCompactedTurnID   string                     `json:"last_compacted_turn_id,omitempty"`
	LastCompactedAt       string                     `json:"last_compacted_at,omitempty"`
}

// managementTurnResponse is the bounded HTTP projection for one persisted turn.
// managementTurnResponse 是单条持久化回合的有界 HTTP 投影。
type managementTurnResponse struct {
	TurnID               string                     `json:"turn_id"`
	SessionID            string                     `json:"session_id"`
	ProjectID            string                     `json:"project_id"`
	User                 managementIdentityResponse `json:"user"`
	Project              managementProjectResponse  `json:"project"`
	DisplayExcerpt       string                     `json:"display_excerpt"`
	ExtractionStatus     string                     `json:"extraction_status"`
	CreatedAt            string                     `json:"created_at"`
	UpdatedAt            string                     `json:"updated_at"`
	ContentBytes         int                        `json:"content_bytes"`
	ExtractedDetailBytes int                        `json:"extracted_detail_bytes"`
	DerivedMemories      int                        `json:"derived_memories"`
	ProfileReferences    int                        `json:"profile_references"`
}

// managementTurnContentResponse returns one UTF-8-safe section chunk.
// managementTurnContentResponse 用于返回一段 UTF-8 安全的内容分片。
type managementTurnContentResponse struct {
	TurnID     string `json:"turn_id"`
	Section    string `json:"section"`
	Index      int    `json:"index,omitempty"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	HasMore    bool   `json:"has_more"`
	Chunk      string `json:"chunk"`
}

// registerManagementReadRoutes binds every phase-two read endpoint to one management use case.
// registerManagementReadRoutes 用于把第二阶段全部读取端点绑定到同一个管理用例。
func registerManagementReadRoutes(mux *http.ServeMux, management *usecase.ManagementUseCase) {
	if management == nil {
		return
	}
	mux.HandleFunc("GET /management/v1/users", func(writer http.ResponseWriter, request *http.Request) {
		users, err := management.ListUsers(request.Context())
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementIdentityResponse, 0, len(users))
		for _, user := range users {
			items = append(items, managementIdentityResponse{ID: strconv.FormatUint(user.ID, 10), Name: user.Name})
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
	mux.HandleFunc("GET /management/v1/projects", func(writer http.ResponseWriter, request *http.Request) {
		projects, err := management.ListProjects(request.Context())
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementProjectResponse, 0, len(projects))
		for _, project := range projects {
			items = append(items, managementProjectResponse{ID: strconv.FormatUint(project.ID, 10), Name: project.Name, Path: project.Path()})
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	})
	mux.HandleFunc("GET /management/v1/sessions", func(writer http.ResponseWriter, request *http.Request) {
		filters, err := parseManagementSessionFilters(request)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		result, err := management.ListSessions(request.Context(), filters)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementSessionResponse, 0, len(result.Items))
		for _, record := range result.Items {
			items = append(items, projectManagementSession(management, record))
		}
		writeJSON(writer, http.StatusOK, managementPageResponse[managementSessionResponse]{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore})
	})
	mux.HandleFunc("GET /management/v1/sessions/{session_id}", func(writer http.ResponseWriter, request *http.Request) {
		sessionID, err := parseManagementID("session_id", request.PathValue("session_id"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		record, err := management.GetSession(request.Context(), sessionID)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementSession(management, record))
	})
	mux.HandleFunc("GET /management/v1/sessions/{session_id}/turns", func(writer http.ResponseWriter, request *http.Request) {
		sessionID, err := parseManagementID("session_id", request.PathValue("session_id"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		limit, err := parseOptionalManagementInt("limit", request.URL.Query().Get("limit"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		result, err := management.ListTurns(request.Context(), usecase.ManagementTurnFilters{
			SessionID: sessionID, Status: request.URL.Query().Get("status"), Cursor: request.URL.Query().Get("cursor"), Limit: limit,
		})
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementTurnResponse, 0, len(result.Items))
		for _, turn := range result.Items {
			items = append(items, projectManagementTurn(management, turn))
		}
		writeJSON(writer, http.StatusOK, managementPageResponse[managementTurnResponse]{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore})
	})
	mux.HandleFunc("GET /management/v1/turns", func(writer http.ResponseWriter, request *http.Request) {
		filters, err := parseManagementTurnFilters(request)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		result, err := management.ListTurns(request.Context(), filters)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		items := make([]managementTurnResponse, 0, len(result.Items))
		for _, turn := range result.Items {
			items = append(items, projectManagementTurn(management, turn))
		}
		writeJSON(writer, http.StatusOK, managementPageResponse[managementTurnResponse]{Items: items, NextCursor: result.NextCursor, HasMore: result.HasMore})
	})
	mux.HandleFunc("GET /management/v1/turns/{turn_id}", func(writer http.ResponseWriter, request *http.Request) {
		turnID, err := parseManagementID("turn_id", request.PathValue("turn_id"))
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		turn, err := management.GetTurn(request.Context(), turnID)
		if err != nil {
			writeManagementError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, projectManagementTurn(management, turn))
	})
	mux.HandleFunc("GET /management/v1/turns/{turn_id}/content", func(writer http.ResponseWriter, request *http.Request) {
		handleManagementTurnContent(writer, request, management)
	})
}

// parseManagementSessionFilters validates query parameters before they reach storage.
// parseManagementSessionFilters 用于在查询参数进入存储前完成校验。
func parseManagementSessionFilters(request *http.Request) (usecase.ManagementSessionFilters, error) {
	query := request.URL.Query()
	userID, err := parseOptionalManagementID("user_id", query.Get("user_id"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	projectID, err := parseOptionalManagementID("project_id", query.Get("project_id"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	limit, err := parseOptionalManagementInt("limit", query.Get("limit"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	createdFrom, err := parseOptionalManagementTime("created_from", query.Get("created_from"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	createdTo, err := parseOptionalManagementTime("created_to", query.Get("created_to"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	updatedFrom, err := parseOptionalManagementTime("updated_from", query.Get("updated_from"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	updatedTo, err := parseOptionalManagementTime("updated_to", query.Get("updated_to"))
	if err != nil {
		return usecase.ManagementSessionFilters{}, err
	}
	return usecase.ManagementSessionFilters{
		UserID: userID, ProjectID: projectID, Query: query.Get("query"), Status: query.Get("status"),
		CreatedFrom: createdFrom, CreatedTo: createdTo, UpdatedFrom: updatedFrom, UpdatedTo: updatedTo,
		Cursor: query.Get("cursor"), Limit: limit, Sort: query.Get("sort"),
	}, nil
}

// parseManagementTurnFilters validates global turn-list query parameters.
// parseManagementTurnFilters 用于校验全局回合列表查询参数。
func parseManagementTurnFilters(request *http.Request) (usecase.ManagementTurnFilters, error) {
	query := request.URL.Query()
	sessionID, err := parseOptionalManagementID("session_id", query.Get("session_id"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	userID, err := parseOptionalManagementID("user_id", query.Get("user_id"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	projectID, err := parseOptionalManagementID("project_id", query.Get("project_id"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	limit, err := parseOptionalManagementInt("limit", query.Get("limit"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	createdFrom, err := parseOptionalManagementTime("created_from", query.Get("created_from"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	createdTo, err := parseOptionalManagementTime("created_to", query.Get("created_to"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	hasMemory, err := parseOptionalManagementBool("has_memory", query.Get("has_memory"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	hasProfile, err := parseOptionalManagementBool("has_profile", query.Get("has_profile"))
	if err != nil {
		return usecase.ManagementTurnFilters{}, err
	}
	return usecase.ManagementTurnFilters{
		SessionID: sessionID, UserID: userID, ProjectID: projectID, Query: query.Get("query"),
		Status: query.Get("status"), CreatedFrom: createdFrom, CreatedTo: createdTo,
		HasMemory: hasMemory, HasProfile: hasProfile, Cursor: query.Get("cursor"), Limit: limit, Sort: query.Get("sort"),
	}, nil
}

// handleManagementTurnContent reads one decoded section and returns a bounded UTF-8-safe chunk.
// handleManagementTurnContent 用于读取一个解码后的区段并返回有界 UTF-8 安全分片。
func handleManagementTurnContent(writer http.ResponseWriter, request *http.Request, management *usecase.ManagementUseCase) {
	turnID, err := parseManagementID("turn_id", request.PathValue("turn_id"))
	if err != nil {
		writeManagementError(writer, request, err)
		return
	}
	offset, err := parseOptionalManagementInt("offset", request.URL.Query().Get("offset"))
	if err != nil {
		writeManagementError(writer, request, err)
		return
	}
	limit, err := parseOptionalManagementInt("limit", request.URL.Query().Get("limit"))
	if err != nil {
		writeManagementError(writer, request, err)
		return
	}
	if limit == 0 {
		limit = usecase.ManagementContentChunkBytes
	}
	turn, err := management.GetTurn(request.Context(), turnID)
	if err != nil {
		writeManagementError(writer, request, err)
		return
	}
	userContent, timeline, assistantContent, err := management.ParseTurnContent(turn)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "turn-content-unavailable", "Turn content unavailable", "The stored turn content could not be decoded.")
		return
	}
	section := strings.TrimSpace(request.URL.Query().Get("section"))
	content := ""
	index := 0
	switch section {
	case "user":
		content = userContent
	case "assistant":
		content = assistantContent
	case "timeline":
		index, err = parseOptionalManagementInt("index", request.URL.Query().Get("index"))
		if err != nil || index < 0 || index >= len(timeline) {
			writeManagementError(writer, request, logicdomain.ValidationError{Field: "index", Message: "must identify an existing timeline item"})
			return
		}
		content = timeline[index].Content
	default:
		writeManagementError(writer, request, logicdomain.ValidationError{Field: "section", Message: "must be user, assistant, or timeline"})
		return
	}
	chunk, nextOffset, hasMore, err := usecase.SliceUTF8Content(content, offset, limit)
	if err != nil {
		writeManagementError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, managementTurnContentResponse{
		TurnID: strconv.FormatUint(turnID, 10), Section: section, Index: index, Offset: offset,
		NextOffset: nextOffset, HasMore: hasMore, Chunk: chunk,
	})
}

// projectManagementSession converts one internal session record into string-safe HTTP identifiers.
// projectManagementSession 用于把内部会话记录转换为字符串安全的 HTTP 标识。
func projectManagementSession(management *usecase.ManagementUseCase, record logicdomain.ManagementSessionRecord) managementSessionResponse {
	response := managementSessionResponse{
		SessionID: strconv.FormatUint(record.SessionID, 10), SessionKey: record.SessionKey, DisplayTitle: management.SessionDisplayTitle(record),
		User:    managementIdentityResponse{ID: strconv.FormatUint(record.UserID, 10), Name: record.UserName},
		Project: managementProjectResponse{ID: strconv.FormatUint(record.ProjectID, 10), Name: record.ProjectName, Path: strings.Join([]string{record.TeamName, record.SpaceName, record.ProjectName}, "/")},
		Status:  record.Status, CreatedAt: formatManagementTime(record.CreatedAt), UpdatedAt: formatManagementTime(record.UpdatedAt),
		HistoricalTurnCount: record.TurnCount, VisibleTurnCount: record.VisibleTurnCount,
		PendingTurnCount: record.PendingTurnCount, ActiveMemoryCount: record.ActiveMemoryCount,
		ProfileReferenceCount: record.ProfileReferenceCount,
	}
	if record.LastSummarizedID != 0 {
		response.LastSummarizedTurnID = strconv.FormatUint(record.LastSummarizedID, 10)
	}
	if record.LastCompactedTurnID != 0 {
		response.LastCompactedTurnID = strconv.FormatUint(record.LastCompactedTurnID, 10)
	}
	if !record.LastCompactedAt.IsZero() {
		response.LastCompactedAt = formatManagementTime(record.LastCompactedAt)
	}
	return response
}

// projectManagementTurn converts one internal turn record into one bounded HTTP summary.
// projectManagementTurn 用于把内部回合记录转换为一条有界 HTTP 摘要。
func projectManagementTurn(management *usecase.ManagementUseCase, turn logicdomain.ManagementTurnRecord) managementTurnResponse {
	status := "pending"
	if turn.ExtractedStatus == logicdomain.TurnExtractedStatusPassed {
		status = "passed"
	} else if turn.ExtractedStatus == logicdomain.TurnExtractedStatusDone {
		status = "extracted"
	}
	return managementTurnResponse{
		TurnID: strconv.FormatUint(turn.ID, 10), SessionID: strconv.FormatUint(turn.SessionID, 10),
		ProjectID: strconv.FormatUint(turn.ProjectID, 10), DisplayExcerpt: management.TurnDisplayExcerpt(turn),
		User:             managementIdentityResponse{ID: strconv.FormatUint(turn.UserID, 10), Name: turn.UserName},
		Project:          managementProjectResponse{ID: strconv.FormatUint(turn.ProjectID, 10), Name: turn.ProjectName, Path: strings.Join([]string{turn.TeamName, turn.SpaceName, turn.ProjectName}, "/")},
		ExtractionStatus: status, CreatedAt: formatManagementTime(turn.CreatedAt), UpdatedAt: formatManagementTime(turn.UpdatedAt),
		ContentBytes: len([]byte(turn.DehydratedContent)), ExtractedDetailBytes: len([]byte(turn.Details)),
		DerivedMemories: turn.DerivedMemories, ProfileReferences: turn.ProfileReferences,
	}
}

// writeManagementError maps domain validation and lookup failures into uniform safe problems.
// writeManagementError 用于把领域校验与查找失败映射为统一安全问题响应。
func writeManagementError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, errManagementBodyTooLarge):
		writeProblem(writer, request, http.StatusRequestEntityTooLarge, "request-too-large", "Request too large", "The management request body exceeds the configured limit.")
	case logicdomain.IsValidationError(err):
		writeProblem(writer, request, http.StatusBadRequest, "invalid-request", "Invalid request", err.Error())
	case logicdomain.IsNotFoundError(err):
		writeProblem(writer, request, http.StatusNotFound, "not-found", "Resource not found", err.Error())
	case logicdomain.IsProtectedResourceError(err):
		writeProblem(writer, request, http.StatusConflict, "resource-protected", "Resource is protected", err.Error())
	case logicdomain.IsConfirmationRequired(err):
		writeProblem(writer, request, http.StatusConflict, "confirmation-required", "Confirmation required", err.Error())
	case logicdomain.IsConflictError(err):
		code := "conflict"
		if strings.Contains(strings.ToLower(err.Error()), "stale") {
			code = "preview-stale"
		}
		writeProblem(writer, request, http.StatusConflict, code, "Request conflict", err.Error())
	case logicdomain.IsOutcomeUncertain(err):
		writeProblem(writer, request, http.StatusServiceUnavailable, "outcome-uncertain", "Operation outcome is uncertain", err.Error())
	default:
		writeProblem(writer, request, http.StatusInternalServerError, "internal-error", "Internal error", "The management request could not be completed.")
	}
}

// parseManagementID parses one required positive decimal identifier.
// parseManagementID 用于解析一个必填的正十进制标识。
func parseManagementID(field, raw string) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, logicdomain.ValidationError{Field: field, Message: "must be a positive decimal string"}
	}
	return value, nil
}

// parseOptionalManagementID parses one optional positive decimal identifier.
// parseOptionalManagementID 用于解析一个可选的正十进制标识。
func parseOptionalManagementID(field, raw string) (uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseManagementID(field, raw)
}

// parseOptionalManagementInt parses one optional non-negative decimal integer.
// parseOptionalManagementInt 用于解析一个可选的非负十进制整数。
func parseOptionalManagementInt(field, raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, logicdomain.ValidationError{Field: field, Message: "must be a non-negative integer"}
	}
	return value, nil
}

// parseOptionalManagementBool parses one optional strict boolean query value.
// parseOptionalManagementBool 用于解析一个可选的严格布尔查询值。
func parseOptionalManagementBool(field, raw string) (*bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return nil, logicdomain.ValidationError{Field: field, Message: "must be true or false"}
	}
	return &value, nil
}

// parseOptionalManagementTime parses one optional RFC 3339 timestamp.
// parseOptionalManagementTime 用于解析一个可选的 RFC 3339 时间戳。
func parseOptionalManagementTime(field, raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, logicdomain.ValidationError{Field: field, Message: "must be an RFC 3339 timestamp"}
	}
	return value.UTC(), nil
}

// formatManagementTime formats one non-zero timestamp for stable JSON output.
// formatManagementTime 用于把非零时间戳格式化为稳定 JSON 输出。
func formatManagementTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
