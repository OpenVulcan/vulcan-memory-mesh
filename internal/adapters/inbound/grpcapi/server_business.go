// server_business.go keeps the main business-chain RPC handlers and transport logging helpers for the inbound gRPC adapter.
// server_business.go 用于承载入站 gRPC 适配层中的主业务链路 RPC 处理器与传输日志辅助函数。
package grpcapi

import (
	"context"
	"encoding/json"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// ChatCompact validates the request, consumes the resolved session scope, and records the latest persisted turn as the active compact boundary.
// ChatCompact 用于校验请求、消费已解析 session 范围，并把最新持久化 turn 记录为当前 compact 边界。
func (s *Server) ChatCompact(ctx context.Context, req *vmmv1.ChatCompactRequest) (*vmmv1.ChatCompactResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.chatCompact == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeChatCompactRequest(req)
	if err := s.validator().ValidateChatCompact(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()
	result, err := s.chatCompact.Execute(ctx, usecase.ChatCompactCommand{Session: session})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ChatCompactResponse{
		Accepted:        result.Accepted,
		Updated:         result.Updated,
		CompactedTurnId: result.CompactedTurnID,
		TraceId:         trace.IDFromContext(ctx),
	}, nil
}

// PreCheck validates the request, consumes the scope resolved by the interceptor, and returns the live assembled pre-check context.
// PreCheck 用于校验请求、消费拦截器解析出的范围，并返回实时组装完成的 pre-check 上下文。
func (s *Server) PreCheck(ctx context.Context, req *vmmv1.PreCheckRequest) (*vmmv1.PreCheckResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.preCheck == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePreCheckRequest(req)
	if err := s.validator().ValidatePreCheck(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.preTimeout)
	defer cancel()
	s.logPreCheckReceipt(trace.IDFromContext(ctx), "pre-check received", req)
	result, err := s.preCheck.Execute(ctx, usecase.PreCheckCommand{
		Session:     session,
		UserContent: req.GetUserContent(),
		RecallMode:  usecase.PreCheckRecallMode(req.GetRecallMode()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	s.logPreCheckResult(trace.IDFromContext(ctx), "pre-check returned", req, result)
	items := make([]*vmmv1.ContextItem, 0, len(result.ContextItems))
	for _, item := range result.ContextItems {
		items = append(items, &vmmv1.ContextItem{
			Text:             item.Text,
			Score:            item.Score,
			TurnId:           item.TurnID,
			HasDialogue:      item.TurnID > 0,
			CreatedTimestamp: item.CreatedTimestamp,
			CreatedDatetime:  item.CreatedDateTime,
		})
	}
	return &vmmv1.PreCheckResponse{
		ShouldInject: result.ShouldInject,
		ContextText:  "",
		ContextItems: items,
		Degraded:     result.Degraded,
		TraceId:      trace.IDFromContext(ctx),
	}, nil
}

// logPreCheckReceipt writes one accepted pre-check request snapshot into the runtime logger, defaulting to redacted metadata and optionally appending plaintext or protected payload snapshots depending on the shared logger configuration.
// logPreCheckReceipt 用于把已接收的 pre-check 请求快照写入运行时日志；默认只输出脱敏元信息，并根据共享日志配置选择追加明文或受保护的载荷快照。
func (s *Server) logPreCheckReceipt(traceID, message string, req *vmmv1.PreCheckRequest) {
	if s == nil || s.logger == nil || req == nil {
		return
	}
	fields := []any{
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"user_content_present", req.GetUserContent() != "",
		"recall_mode", int32(req.GetRecallMode()),
	}
	fields = s.logger.AppendPayloadFields(fields, "request_payload", map[string]any{
		"session_id":   req.GetSessionId(),
		"user_content": req.GetUserContent(),
		"recall_mode":  int32(req.GetRecallMode()),
		"recall_label": req.GetRecallMode().String(),
	})
	s.logger.Info(message, fields...)
}

// logPreCheckResult writes one pre-check response snapshot into the runtime logger, defaulting to safe execution metadata and optionally appending plaintext or protected assembled-context snapshots depending on the shared logger configuration.
// logPreCheckResult 用于把 pre-check 返回结果快照写入运行时日志；默认只输出安全执行元信息，并根据共享日志配置选择追加明文或受保护的组装上下文快照。
func (s *Server) logPreCheckResult(traceID, message string, req *vmmv1.PreCheckRequest, result usecase.PreCheckResult) {
	if s == nil || s.logger == nil {
		return
	}
	nonEmptyContextItems := 0
	for _, item := range result.ContextItems {
		if item.Text != "" || item.Score != 0 || item.TurnID != 0 {
			nonEmptyContextItems++
		}
	}
	fields := []any{
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"should_inject", result.ShouldInject,
		"degraded", result.Degraded,
		"context_text_present", false,
		"context_items", len(result.ContextItems),
		"context_nonempty_items", nonEmptyContextItems,
	}
	fields = s.logger.AppendPayloadFields(fields, "response_payload", map[string]any{
		"should_inject": result.ShouldInject,
		"degraded":      result.Degraded,
		"context_items": summarizePreCheckContextItemsForTransportLog(result.ContextItems),
	})
	s.logger.Info(message, fields...)
}

// preCheckContextItemTransportLogPayload keeps pre-check response payload logs aligned with the trimmed gRPC response contract so operators see the same fields as callers.
// preCheckContextItemTransportLogPayload 用于让 pre-check 响应载荷日志与裁剪后的 gRPC 返回契约保持一致，确保运维看到的字段和调用方一致。
type preCheckContextItemTransportLogPayload struct {
	Text             string  `json:"text"`
	Score            float64 `json:"score"`
	HasDialogue      bool    `json:"has_dialogue"`
	TurnID           uint64  `json:"turn_id"`
	CreatedTimestamp int64   `json:"created_timestamp,omitempty"`
	CreatedDateTime  string  `json:"created_datetime,omitempty"`
}

// summarizePreCheckContextItemsForTransportLog projects internal context items into the minimal transport shape expected by the updated pre-check response contract.
// summarizePreCheckContextItemsForTransportLog 用于把内部 context item 投影成更新后 pre-check 传输契约所需的最小结构。
func summarizePreCheckContextItemsForTransportLog(items []logicdomain.ContextItem) []preCheckContextItemTransportLogPayload {
	out := make([]preCheckContextItemTransportLogPayload, 0, len(items))
	for _, item := range items {
		out = append(out, preCheckContextItemTransportLogPayload{
			Text:             item.Text,
			Score:            item.Score,
			HasDialogue:      item.TurnID > 0,
			TurnID:           item.TurnID,
			CreatedTimestamp: item.CreatedTimestamp,
			CreatedDateTime:  item.CreatedDateTime,
		})
	}
	return out
}

// PostAction validates the request, logs raw and cleaned payloads, then completes synchronous persistence before returning.
// PostAction 用于校验请求、记录原始与清洗后载荷，并在返回前完成同步持久化。
func (s *Server) PostAction(ctx context.Context, req *vmmv1.PostActionRequest) (*vmmv1.PostActionResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.postAction == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePostActionRequest(req)
	if err := s.validator().ValidatePostAction(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	traceID := trace.IDFromContext(ctx)
	rawReq := clonePostActionRequest(req)
	cleanedReq := s.sanitizePostActionRequest(req)
	cmd := toPostActionCommand(session, rawReq, cleanedReq)
	s.logPostActionReceipt(traceID, "post-action received raw", rawReq)
	s.logPostActionReceipt(traceID, "post-action received cleaned", cleanedReq)

	// Run the post-action flow synchronously so the transport contract matches the immediate extraction mainline.
	// 同步执行 post-action 流程，让传输契约和即时提炼主链保持一致。
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, traceID)
	result, err := s.postAction.Execute(ctx, cmd)
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.PostActionResponse{
		Accepted: result.Accepted,
		TraceId:  traceID,
	}, nil
}

// sanitizePostActionRequest clones the validated request into one storage-ready copy so raw logs and persisted text can diverge safely.
// sanitizePostActionRequest 用于把已校验请求复制成一份面向存储的版本，让原始日志与入库文本可以安全分离。
func (s *Server) sanitizePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	cloned := clonePostActionRequest(req)
	if cloned == nil {
		return cloned
	}

	// Reuse the default storage sanitizer for partially constructed servers so direct tests and manual integrations still see the same cleaned payload contract as the main runtime path.
	// 对于部分装配的服务实例，这里仍复用默认的存储型清洗器，保证直接测试和手工集成拿到与主运行时一致的清洗后载荷契约。
	sanitizer := s.postActionSanitizer()
	cloned.UserContent = sanitizer.Sanitize(cloned.GetUserContent())
	cloned.AssistantContent = sanitizer.Sanitize(cloned.GetAssistantContent())
	for _, item := range cloned.GetTimeline() {
		item.Content = sanitizer.Sanitize(item.GetContent())
	}
	return cloned
}

// toPostActionCommand converts both raw and cleaned transport requests plus the resolved session scope into the use-case command shape.
// toPostActionCommand 用于把原始/清洗后的传输层请求，以及已解析的 session 范围，一起转换成用例层命令。
func toPostActionCommand(session logicdomain.SessionRef, rawReq, cleanedReq *vmmv1.PostActionRequest) usecase.PostActionCommand {
	items := make([]usecase.PostActionTimelineItem, 0, len(cleanedReq.GetTimeline()))
	for _, item := range cleanedReq.GetTimeline() {
		items = append(items, usecase.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	rawItems := make([]usecase.PostActionTimelineItem, 0, len(rawReq.GetTimeline()))
	for _, item := range rawReq.GetTimeline() {
		rawItems = append(rawItems, usecase.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	return usecase.PostActionCommand{
		Session:             session,
		UserContent:         cleanedReq.GetUserContent(),
		AssistantContent:    cleanedReq.GetAssistantContent(),
		Timeline:            items,
		RawUserContent:      rawReq.GetUserContent(),
		RawAssistantContent: rawReq.GetAssistantContent(),
		RawTimeline:         rawItems,
	}
}

// clonePostActionRequest copies the PostAction request so normalization, cleaning, and logging can operate on independent instances.
// clonePostActionRequest 用于复制 PostAction 请求，让规范化、清洗和日志输出可以在独立实例上进行。
func clonePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	if req == nil {
		return nil
	}
	cloned := &vmmv1.PostActionRequest{
		SessionId:        req.GetSessionId(),
		UserId:           req.GetUserId(),
		ProjectId:        req.GetProjectId(),
		UserContent:      req.GetUserContent(),
		AssistantContent: req.GetAssistantContent(),
		Timeline:         make([]*vmmv1.PostActionTimelineItem, 0, len(req.GetTimeline())),
	}
	for _, item := range req.GetTimeline() {
		cloned.Timeline = append(cloned.Timeline, &vmmv1.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	return cloned
}

// logPostActionReceipt writes one accepted payload snapshot into the runtime logger, defaulting to redacted metadata and only emitting full payload text when the explicit RPC payload debug switch is enabled.
// logPostActionReceipt 用于把已接收载荷快照写入运行时日志；默认只输出脱敏元信息，只有显式 RPC 载荷调试开关开启时才会输出完整正文。
func (s *Server) logPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
	if s == nil || s.logger == nil || req == nil {
		return
	}
	nonEmptyTimelineItems := 0
	for _, item := range req.GetTimeline() {
		if item == nil {
			continue
		}
		if item.GetType() != "" || item.GetContent() != "" {
			nonEmptyTimelineItems++
		}
	}

	// Keep raw payload text behind one explicit debug-only switch so production-safe logs
	// remain redacted by default while local troubleshooting can still opt into full receipts.
	// 把原始载荷正文放到显式调试开关之后，让默认日志继续保持安全脱敏，
	// 同时在本地排障场景下仍然可以选择输出完整收据。
	if s.debugRPCPayloads {
		timelineJSON, err := json.Marshal(req.GetTimeline())
		if err != nil {
			s.logger.Warn(message+" timeline marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
			s.logger.Info(
				message,
				"trace_id", traceID,
				"session_id", req.GetSessionId(),
				"user_content", req.GetUserContent(),
				"assistant_content", req.GetAssistantContent(),
				"timeline_items", len(req.GetTimeline()),
				"timeline_nonempty_items", nonEmptyTimelineItems,
			)
			return
		}
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"user_content", req.GetUserContent(),
			"assistant_content", req.GetAssistantContent(),
			"timeline_items", len(req.GetTimeline()),
			"timeline_nonempty_items", nonEmptyTimelineItems,
			"timeline_json", string(timelineJSON),
		)
		return
	}
	timelineJSON, err := json.Marshal(req.GetTimeline())
	if err != nil {
		s.logger.Warn(message+" timeline marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"user_content_present", req.GetUserContent() != "",
			"assistant_content_present", req.GetAssistantContent() != "",
			"timeline_items", len(req.GetTimeline()),
			"timeline_nonempty_items", nonEmptyTimelineItems,
		)
		return
	}
	s.logger.Info(
		message,
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"user_content_present", req.GetUserContent() != "",
		"assistant_content_present", req.GetAssistantContent() != "",
		"timeline_items", len(req.GetTimeline()),
		"timeline_nonempty_items", nonEmptyTimelineItems,
		"timeline_marshaled", len(timelineJSON) > 0,
	)
}
