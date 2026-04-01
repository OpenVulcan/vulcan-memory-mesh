// profile.go implements the profile query and manual profile-instruction use cases used by the gRPC profile surface.
// profile.go 用于实现 gRPC 画像接口使用的画像查询和手工画像指令用例。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// ManualProfileInstructionReviewer is the tiny port used by the profile use case to review one explicit instruction against the current active nodes of one target.
// ManualProfileInstructionReviewer 用于让画像用例把一条显式指令与单个目标当前 active 节点做对照评审。
type ManualProfileInstructionReviewer interface {
	Review(ctx context.Context, target logicdomain.ProfileTargetRef, activeNodes []logicdomain.ProfileNodeRecord, instruction string, floorPriority, floorLevel int) (logicdomain.ManualProfileInstructionReview, error)
}

// ProfileQueryCommand carries one target selector for the active-profile query RPC.
// ProfileQueryCommand 用于承载 active 画像查询 RPC 所需的单目标选择参数。
type ProfileQueryCommand struct {
	ProfileType int
	UserID      uint64
	ProjectID   uint64
	Limit       int
}

// ProfileQueryResult returns the resolved target plus the active node slice for the requested profile scope.
// ProfileQueryResult 用于返回请求画像范围的已解析目标及其 active 节点切片。
type ProfileQueryResult struct {
	Target logicdomain.ProfileTargetRef
	Nodes  []logicdomain.ProfileNodeRecord
}

// ProfileInstructionCommand carries one explicit manual instruction for a single target.
// ProfileInstructionCommand 用于承载面向单个目标的一条显式手工画像指令。
type ProfileInstructionCommand struct {
	ProfileType int
	UserID      uint64
	ProjectID   uint64
	Instruction string
}

// ProfileInstructionResult returns the persisted node mutations produced by one manual instruction review.
// ProfileInstructionResult 用于返回单次手工画像指令评审后持久化得到的节点变更结果。
type ProfileInstructionResult struct {
	Target        logicdomain.ProfileTargetRef
	InstructionID uint64
	AcceptedNodes []logicdomain.ProfileNodeRecord
	RetiredNodes  []logicdomain.ProfileRetireDecision
	ReviewReason  string
}

// ProfileExecutor groups the profile query and manual-instruction flows exposed by the inbound gRPC adapter.
// ProfileExecutor 用于聚合入站 gRPC 适配层对外暴露的画像查询和手工画像指令流程。
type ProfileExecutor interface {
	GetNodes(ctx context.Context, cmd ProfileQueryCommand) (ProfileQueryResult, error)
	GetBundle(ctx context.Context, cmd ProfileBundleCommand) (ProfileBundleResult, error)
	ApplyInstruction(ctx context.Context, cmd ProfileInstructionCommand) (ProfileInstructionResult, error)
}

// ProfileUseCase orchestrates profile-node queries plus explicit manual profile instructions on top of the DuckDB profile store and LLM reviewer.
// ProfileUseCase 用于在 DuckDB 画像存储和 LLM 评审器之上编排画像节点查询与显式手工画像指令流程。
type ProfileUseCase struct {
	store    appports.ProfileStore
	reviewer ManualProfileInstructionReviewer
	logger   *logx.Logger
	mu       sync.Mutex
	flights  map[string]*profileInstructionFlight
	gates    map[string]*profileInstructionGate
}

// NewProfileUseCase creates a ProfileUseCase instance.
// NewProfileUseCase 用于创建 ProfileUseCase 实例。
func NewProfileUseCase(store appports.ProfileStore, reviewer ManualProfileInstructionReviewer, logger *logx.Logger) *ProfileUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &ProfileUseCase{
		store:    store,
		reviewer: reviewer,
		logger:   logger,
		flights:  map[string]*profileInstructionFlight{},
		gates:    map[string]*profileInstructionGate{},
	}
}

// profileInstructionFlight stores one in-flight ApplyProfileInstruction result so duplicate concurrent gRPC calls
// can reuse the first LLM review instead of triggering the same reviewer flow twice.
// profileInstructionFlight 用于保存一条正在进行中的 ApplyProfileInstruction 结果，
// 让重复的并发 gRPC 调用能够复用第一次 LLM 评审，而不是把同样的流程触发两次。
type profileInstructionFlight struct {
	done   chan struct{}
	result ProfileInstructionResult
	err    error
}

// profileInstructionGate serializes manual profile instructions per concrete target so different instructions
// never inspect and mutate the same active-node set concurrently.
// profileInstructionGate 用于按具体目标串行化手工画像指令，避免不同指令并发读取和改写同一批 active 节点。
type profileInstructionGate struct {
	mu sync.Mutex
}

// GetNodes resolves the requested target and returns only its current active nodes in a bounded deterministic order.
// GetNodes 用于解析请求目标，并按受限且确定性的顺序返回它当前 active 的画像节点。
func (u *ProfileUseCase) GetNodes(ctx context.Context, cmd ProfileQueryCommand) (ProfileQueryResult, error) {
	if u == nil || u.store == nil {
		return ProfileQueryResult{}, fmt.Errorf("profile store is nil")
	}
	if err := validateProfileQueryCommand(cmd); err != nil {
		return ProfileQueryResult{}, err
	}
	target, err := u.store.ResolveProfileTarget(ctx, cmd.ProfileType, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return ProfileQueryResult{}, err
	}
	nodes, err := u.store.ListActiveProfileNodes(ctx, target, cmd.Limit)
	if err != nil {
		return ProfileQueryResult{}, err
	}
	return ProfileQueryResult{Target: target, Nodes: nodes}, nil
}

// ApplyInstruction persists one explicit manual profile instruction by reviewing it against active nodes, enforcing scope-specific floors, and rebuilding the durable profile blob.
// ApplyInstruction 用于把一条显式手工画像指令与 active 节点进行评审、施加 scope 级地板规则，并重建长期画像 Blob 后再持久化。
func (u *ProfileUseCase) ApplyInstruction(ctx context.Context, cmd ProfileInstructionCommand) (ProfileInstructionResult, error) {
	if u == nil || u.store == nil {
		return ProfileInstructionResult{}, fmt.Errorf("profile store is nil")
	}
	if u.reviewer == nil {
		return ProfileInstructionResult{}, fmt.Errorf("manual profile reviewer is nil")
	}
	if err := validateProfileInstructionCommand(cmd); err != nil {
		return ProfileInstructionResult{}, err
	}
	target, err := u.store.ResolveProfileTarget(ctx, cmd.ProfileType, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return ProfileInstructionResult{}, err
	}
	instruction := strings.TrimSpace(cmd.Instruction)
	flightKey := u.profileInstructionFlightKey(target, instruction)
	if flight, shared := u.loadOrCreateProfileInstructionFlight(flightKey); shared {
		return u.waitProfileInstructionFlight(ctx, flight)
	} else {
		gate := u.profileInstructionGate(target)
		gate.mu.Lock()
		defer gate.mu.Unlock()

		// Serialize per-target manual instructions and reuse identical in-flight calls so overlapping plugin retries
		// do not launch duplicate LLM reviews or race on the same DuckDB profile rows.
		// 按目标串行化手工画像指令，并复用相同的并发调用结果，避免插件重试时重复触发 LLM 评审，
		// 或在同一批 DuckDB 画像行上发生竞争。
		result, err := u.applyInstructionLocked(ctx, target, instruction)
		u.finishProfileInstructionFlight(flightKey, flight, result, err)
		return result, err
	}
}

// applyInstructionLocked runs the reviewed manual profile-instruction writeback while the caller already holds
// the per-target gate, ensuring the active-node snapshot stays stable for this target during one review.
// applyInstructionLocked 用于在调用方已经持有目标级串行闸门后执行手工画像指令写回，
// 确保该目标在一次评审期间看到的 active 节点快照保持稳定。
func (u *ProfileUseCase) applyInstructionLocked(ctx context.Context, target logicdomain.ProfileTargetRef, instruction string) (ProfileInstructionResult, error) {
	activeNodes, err := u.store.ListActiveProfileNodes(ctx, target, 256)
	if err != nil {
		return ProfileInstructionResult{}, err
	}

	// Persist the incoming manual instruction first so every later reviewer decision and durable node update can reference one stable source id.
	// 先持久化这条手工画像指令，让后续评审结果和长期节点更新都能引用稳定的 source id。
	instructionRecord, err := u.store.CreateProfileInstruction(ctx, logicdomain.ProfileInstructionRecord{
		ProfileType: target.ProfileType,
		BindID:      target.BindID,
		Instruction: instruction,
		Status:      logicdomain.ProfileInstructionStatusPending,
	})
	if err != nil {
		return ProfileInstructionResult{}, err
	}

	floorPriority, floorLevel := manualInstructionFloors(target.ProfileType, instruction)
	review, err := u.reviewer.Review(ctx, target, activeNodes, instruction, floorPriority, floorLevel)
	if err != nil {
		u.failProfileInstruction(ctx, instructionRecord.ID, err.Error(), "")
		return ProfileInstructionResult{}, err
	}
	reviewJSON := marshalProfileInstructionReview(review)

	// Translate the reviewer output into durable node candidates, enforcing target-specific authority floors before any SQL write happens.
	// 把评审结果翻译成长期节点候选，并在任何 SQL 写入前先施加目标级别的权限地板规则。
	candidates, retired, renderedProfile, err := u.materializeManualInstructionReview(target, instructionRecord.ID, activeNodes, review, floorPriority, floorLevel)
	if err != nil {
		u.failProfileInstruction(ctx, instructionRecord.ID, err.Error(), reviewJSON)
		return ProfileInstructionResult{}, err
	}
	applied, err := u.store.ApplyManualProfileInstruction(ctx, target, instructionRecord, candidates, retired, renderedProfile, reviewJSON)
	if err != nil {
		if !logicdomain.IsOutcomeUncertain(err) {
			u.failProfileInstruction(ctx, instructionRecord.ID, err.Error(), reviewJSON)
		} else if u.logger != nil {
			u.logger.Warn("manual profile instruction outcome uncertain", "instruction_id", instructionRecord.ID, "err", err)
		}
		return ProfileInstructionResult{}, err
	}
	return ProfileInstructionResult{
		Target:        target,
		InstructionID: applied.InstructionID,
		AcceptedNodes: applied.AcceptedNodes,
		RetiredNodes:  applied.RetiredNodes,
		ReviewReason:  strings.TrimSpace(review.Reason),
	}, nil
}

// profileInstructionFlightKey builds the dedupe key for one explicit instruction against one resolved target.
// profileInstructionFlightKey 用于构建单个已解析目标上的显式画像指令去重键。
func (u *ProfileUseCase) profileInstructionFlightKey(target logicdomain.ProfileTargetRef, instruction string) string {
	return fmt.Sprintf("%d:%d:%s", target.ProfileType, target.BindID, strings.TrimSpace(instruction))
}

// profileInstructionGate returns the stable per-target mutex used to serialize manual instructions for that target.
// profileInstructionGate 用于返回某个目标对应的稳定互斥锁，以串行化该目标上的手工画像指令。
func (u *ProfileUseCase) profileInstructionGate(target logicdomain.ProfileTargetRef) *profileInstructionGate {
	u.mu.Lock()
	defer u.mu.Unlock()
	key := fmt.Sprintf("%d:%d", target.ProfileType, target.BindID)
	gate, ok := u.gates[key]
	if ok {
		return gate
	}
	gate = &profileInstructionGate{}
	u.gates[key] = gate
	return gate
}

// loadOrCreateProfileInstructionFlight registers one in-flight instruction call or returns the existing
// shared flight when an identical target+instruction call is already running.
// loadOrCreateProfileInstructionFlight 用于登记一条正在执行中的画像指令调用；
// 如果相同目标+相同指令已经在运行，则直接返回现有共享 flight。
func (u *ProfileUseCase) loadOrCreateProfileInstructionFlight(key string) (*profileInstructionFlight, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if flight, ok := u.flights[key]; ok {
		return flight, true
	}
	flight := &profileInstructionFlight{done: make(chan struct{})}
	u.flights[key] = flight
	return flight, false
}

// waitProfileInstructionFlight waits for one identical in-flight instruction call to finish and reuses its result.
// waitProfileInstructionFlight 用于等待一条相同的进行中画像指令完成，并复用它的结果。
func (u *ProfileUseCase) waitProfileInstructionFlight(ctx context.Context, flight *profileInstructionFlight) (ProfileInstructionResult, error) {
	select {
	case <-ctx.Done():
		return ProfileInstructionResult{}, ctx.Err()
	case <-flight.done:
		return flight.result, flight.err
	}
}

// finishProfileInstructionFlight publishes the final result for one in-flight instruction and removes the dedupe slot.
// finishProfileInstructionFlight 用于发布一条进行中画像指令的最终结果，并移除对应的去重槽位。
func (u *ProfileUseCase) finishProfileInstructionFlight(key string, flight *profileInstructionFlight, result ProfileInstructionResult, err error) {
	u.mu.Lock()
	if current, ok := u.flights[key]; ok && current == flight {
		delete(u.flights, key)
	}
	flight.result = result
	flight.err = err
	close(flight.done)
	u.mu.Unlock()
}

// materializeManualInstructionReview enforces authority floors, derives refresh/expiry metadata, and renders the updated profile text from active plus fresh nodes.
// materializeManualInstructionReview 用于施加权限地板、推导 refresh/expiry 元数据，并基于 active 与新节点一起渲染更新后的画像文本。
func (u *ProfileUseCase) materializeManualInstructionReview(target logicdomain.ProfileTargetRef, instructionID uint64, activeNodes []logicdomain.ProfileNodeRecord, review logicdomain.ManualProfileInstructionReview, floorPriority, floorLevel int) ([]logicdomain.ProfileNodeCandidate, []logicdomain.ProfileRetireDecision, string, error) {
	activeByID := make(map[uint64]logicdomain.ProfileActiveNodeRecord, len(activeNodes))
	existing := make([]logicdomain.ProfileActiveNodeRecord, 0, len(activeNodes))
	for _, node := range activeNodes {
		activeRecord := toActiveProfileNodeRecord(node)
		activeByID[node.ID] = activeRecord
		existing = append(existing, activeRecord)
	}

	now := time.Now().UTC()
	profileDate := now.Format("2006-01-02")
	candidates := make([]logicdomain.ProfileNodeCandidate, 0, len(review.AcceptedNodes))
	retired := make([]logicdomain.ProfileRetireDecision, 0, len(review.RetiredNodes))
	retiredSet := map[uint64]struct{}{}

	for idx, accepted := range review.AcceptedNodes {
		content := strings.TrimSpace(accepted.NormalizedContent)
		if content == "" {
			return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("accepted_nodes[%d].normalized_content is required", idx)}
		}
		priority, level, levelReason := enforceManualInstructionFloor(target.ProfileType, accepted.Priority, accepted.ProfileLevel, strings.TrimSpace(accepted.LevelReason), floorPriority, floorLevel)
		supersedeIDs := make([]uint64, 0, len(accepted.SupersedeNodes))
		statusReason := ""
		for supIdx, decision := range accepted.SupersedeNodes {
			if decision.NodeID == 0 {
				return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("accepted_nodes[%d].supersede_nodes[%d].node_id is required", idx, supIdx)}
			}
			if _, ok := activeByID[decision.NodeID]; !ok {
				return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("accepted_nodes[%d].supersede_nodes[%d].node_id %d was not present in active nodes", idx, supIdx, decision.NodeID)}
			}
			if _, exists := retiredSet[decision.NodeID]; exists {
				return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("node_id %d is retired multiple times", decision.NodeID)}
			}
			retiredSet[decision.NodeID] = struct{}{}
			supersedeIDs = append(supersedeIDs, decision.NodeID)
			if statusReason == "" && strings.TrimSpace(decision.Reason) != "" {
				statusReason = strings.TrimSpace(decision.Reason)
			}
			retired = append(retired, logicdomain.ProfileRetireDecision{
				NodeID: decision.NodeID,
				Reason: strings.TrimSpace(decision.Reason),
			})
		}
		refreshWeight := computeProfileRefreshWeight(activeByID, supersedeIDs)
		candidates = append(candidates, logicdomain.ProfileNodeCandidate{
			ProfileType:      target.ProfileType,
			Content:          content,
			Status:           logicdomain.ProfileStatusActive,
			Priority:         priority,
			ProfileLevel:     level,
			LevelReason:      levelReason,
			RefreshWeight:    refreshWeight,
			ProfileDate:      profileDate,
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         instructionID,
			StatusReason:     statusReason,
			ExpiresAt:        computeProfileExpiry(now, level, refreshWeight),
			SupersedeNodeIDs: supersedeIDs,
		})
	}

	for idx, decision := range review.RetiredNodes {
		if decision.NodeID == 0 {
			return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("retired_nodes[%d].node_id is required", idx)}
		}
		if _, ok := activeByID[decision.NodeID]; !ok {
			return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("retired_nodes[%d].node_id %d was not present in active nodes", idx, decision.NodeID)}
		}
		if _, exists := retiredSet[decision.NodeID]; exists {
			return nil, nil, "", logicdomain.InvalidLLMOutputError{Scene: "review_profile_instruction", Message: fmt.Sprintf("node_id %d is retired multiple times", decision.NodeID)}
		}
		retiredSet[decision.NodeID] = struct{}{}
		retired = append(retired, logicdomain.ProfileRetireDecision{
			NodeID: decision.NodeID,
			Reason: strings.TrimSpace(decision.Reason),
		})
	}

	renderedProfile := renderProfileTimeline(existing, candidates, extractRetiredProfileNodeIDs(retired))
	return candidates, retired, renderedProfile, nil
}

// failProfileInstruction best-effort marks one manual instruction as failed without hiding the original reviewer or persistence error.
// failProfileInstruction 用于以尽力而为的方式把一条手工画像指令标记为失败，同时不掩盖原始评审或持久化错误。
func (u *ProfileUseCase) failProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) {
	if u == nil || u.store == nil || instructionID == 0 {
		return
	}
	if err := u.store.FailProfileInstruction(ctx, instructionID, failureReason, reviewResult); err != nil && u.logger != nil {
		u.logger.Error("profile instruction failure persistence failed", "instruction_id", instructionID, "err", err)
	}
}

// validateProfileQueryCommand checks the requested target selector before the query hits DuckDB.
// validateProfileQueryCommand 用于在查询命中 DuckDB 前校验请求目标选择参数。
func validateProfileQueryCommand(cmd ProfileQueryCommand) error {
	if !logicdomain.ValidProfileType(cmd.ProfileType) {
		return logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	switch cmd.ProfileType {
	case logicdomain.ProfileTypeUser:
		if cmd.UserID == 0 {
			return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
		}
	default:
		if cmd.ProjectID == 0 {
			return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
		}
	}
	return nil
}

// validateProfileInstructionCommand checks the target selector and explicit manual instruction before LLM review begins.
// validateProfileInstructionCommand 用于在 LLM 评审开始前校验目标选择参数和显式手工画像指令。
func validateProfileInstructionCommand(cmd ProfileInstructionCommand) error {
	if err := validateProfileQueryCommand(ProfileQueryCommand{
		ProfileType: cmd.ProfileType,
		UserID:      cmd.UserID,
		ProjectID:   cmd.ProjectID,
		Limit:       1,
	}); err != nil {
		return err
	}
	if strings.TrimSpace(cmd.Instruction) == "" {
		return logicdomain.ValidationError{Field: "instruction", Message: "is required"}
	}
	return nil
}

// manualInstructionFloors derives the minimum priority/level that one explicit manual instruction must preserve after review.
// manualInstructionFloors 用于推导一条显式手工画像指令在评审后必须保留的最低 priority/level。
func manualInstructionFloors(profileType int, instruction string) (int, int) {
	if profileType == logicdomain.ProfileTypeTeam || profileType == logicdomain.ProfileTypeSpace {
		return logicdomain.ProfilePriorityP0, logicdomain.ProfileLevelPersistent
	}
	if looksLikeHardManualDirective(instruction) {
		return logicdomain.ProfilePriorityP0, logicdomain.ProfileLevelStable
	}
	return logicdomain.ProfilePriorityP1, logicdomain.ProfileLevelStable
}

// looksLikeHardManualDirective detects explicit must/ban/switch instructions so user/project manual edits start from a stronger baseline.
// looksLikeHardManualDirective 用于识别明确的必须/禁止/切换类指令，让 user/project 的手工编辑从更强的基线开始。
func looksLikeHardManualDirective(instruction string) bool {
	lowered := strings.ToLower(strings.TrimSpace(instruction))
	if lowered == "" {
		return false
	}
	keywords := []string{
		"必须", "不要", "不需要", "禁止", "统一", "改为", "改成", "务必", "只能", "不得", "现在开始", "以后",
		"must", "do not", "don't", "never", "forbid", "forbidden", "ban", "switch to", "replace with", "use only",
	}
	for _, keyword := range keywords {
		if strings.Contains(lowered, keyword) {
			return true
		}
	}
	return false
}

// enforceManualInstructionFloor clamps the reviewer output upward so explicit manual instructions never fall below their target-specific authority floor.
// enforceManualInstructionFloor 用于把评审结果向上钳制，确保显式手工画像指令永远不会低于目标特定的权限地板。
func enforceManualInstructionFloor(profileType, priority, level int, levelReason string, floorPriority, floorLevel int) (int, int, string) {
	adjusted := false
	if priority > floorPriority {
		priority = floorPriority
		adjusted = true
	}
	if level < floorLevel {
		level = floorLevel
		adjusted = true
	}
	if !adjusted {
		return priority, level, levelReason
	}
	floorNote := fmt.Sprintf("Raised to manual authority floor [%s][%s].", profilePriorityLabel(floorPriority), profileLevelLabel(floorLevel))
	if profileType == logicdomain.ProfileTypeTeam || profileType == logicdomain.ProfileTypeSpace {
		floorNote = "Raised to highest-authority organizational floor for team/space manual instructions."
	}
	levelReason = strings.TrimSpace(levelReason)
	if levelReason == "" {
		return priority, level, floorNote
	}
	if strings.Contains(levelReason, floorNote) {
		return priority, level, levelReason
	}
	return priority, level, levelReason + " " + floorNote
}

// toActiveProfileNodeRecord converts one query-facing profile node into the active-node shape reused by refresh and render helpers.
// toActiveProfileNodeRecord 用于把查询返回的画像节点转换成 refresh 与渲染辅助逻辑复用的 active-node 结构。
func toActiveProfileNodeRecord(node logicdomain.ProfileNodeRecord) logicdomain.ProfileActiveNodeRecord {
	return logicdomain.ProfileActiveNodeRecord{
		ID:             node.ID,
		TurnID:         node.TurnID,
		ProfileType:    node.ProfileType,
		BindID:         node.BindID,
		Content:        node.Content,
		Status:         node.Status,
		Priority:       node.Priority,
		ProfileLevel:   node.ProfileLevel,
		LevelReason:    node.LevelReason,
		RefreshWeight:  node.RefreshWeight,
		ProfileDate:    node.ProfileDate,
		SourceKind:     node.SourceKind,
		SourceID:       node.SourceID,
		StatusReason:   node.StatusReason,
		ExpiresAt:      node.ExpiresAt,
		SupersededByID: node.SupersededByID,
		CreatedAt:      node.CreatedAt,
		UpdatedAt:      node.UpdatedAt,
	}
}

// extractRetiredProfileNodeIDs flattens the retire decisions into a deterministic node-id slice for the shared renderer.
// extractRetiredProfileNodeIDs 用于把退役决策扁平化成共享渲染器使用的确定性节点 ID 切片。
func extractRetiredProfileNodeIDs(retired []logicdomain.ProfileRetireDecision) []uint64 {
	ids := make([]uint64, 0, len(retired))
	for _, decision := range retired {
		if decision.NodeID == 0 {
			continue
		}
		ids = append(ids, decision.NodeID)
	}
	return normalizeProfileNodeIDs(ids)
}

// marshalProfileInstructionReview stores the structured reviewer result in a stable JSON form so manual instruction rows remain debuggable later.
// marshalProfileInstructionReview 用于把结构化评审结果保存成稳定 JSON，确保手工画像指令记录后续仍然可调试。
func marshalProfileInstructionReview(review logicdomain.ManualProfileInstructionReview) string {
	body, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return ""
	}
	return string(body)
}
