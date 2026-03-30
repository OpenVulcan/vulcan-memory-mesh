// profile_test.go verifies the profile query and manual profile-instruction use cases.
// profile_test.go 用于验证画像查询与手工画像指令用例。
package usecase

import (
	"context"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestProfileUseCaseGetNodesReturnsActiveSlice verifies the query flow resolves the requested target and returns the active node slice unchanged.
// TestProfileUseCaseGetNodesReturnsActiveSlice 用于验证查询流程会解析请求目标，并原样返回 active 节点切片。
func TestProfileUseCaseGetNodesReturnsActiveSlice(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeUser,
			BindID:      7,
			UserID:      7,
		},
		nodes: []logicdomain.ProfileNodeRecord{
			{ID: 11, ProfileType: logicdomain.ProfileTypeUser, BindID: 7, Content: "偏好使用 Rust。", Priority: logicdomain.ProfilePriorityP1, ProfileLevel: logicdomain.ProfileLevelStable},
		},
	}
	uc := NewProfileUseCase(store, nil, nil)

	result, err := uc.GetNodes(context.Background(), ProfileQueryCommand{
		ProfileType: logicdomain.ProfileTypeUser,
		UserID:      7,
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("get profile nodes: %v", err)
	}
	if result.Target.BindID != 7 || len(result.Nodes) != 1 || result.Nodes[0].ID != 11 {
		t.Fatalf("unexpected query result: %+v", result)
	}
}

// TestProfileUseCaseApplyInstructionRaisesTeamAuthority verifies team manual instructions are promoted to the highest authority floor before persistence.
// TestProfileUseCaseApplyInstructionRaisesTeamAuthority 用于验证 team 手工画像指令在持久化前会被提升到最高权限地板。
func TestProfileUseCaseApplyInstructionRaisesTeamAuthority(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeTeam,
			BindID:      3,
			TeamID:      3,
			ProjectID:   9,
		},
		nodes: []logicdomain.ProfileNodeRecord{
			{
				ID:            41,
				ProfileType:   logicdomain.ProfileTypeTeam,
				BindID:        3,
				Content:       "团队默认使用 Rust。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				ProfileDate:   "2026-03-29",
				SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
				SourceID:      5,
				CreatedAt:     time.Date(2026, 3, 29, 8, 0, 0, 0, time.UTC),
			},
		},
	}
	reviewer := &stubManualProfileReviewer{
		review: logicdomain.ManualProfileInstructionReview{
			AcceptedNodes: []logicdomain.ManualProfileAcceptedNode{
				{
					NormalizedContent: "团队服务端统一使用 Go 语言实现。",
					Priority:          logicdomain.ProfilePriorityP2,
					ProfileLevel:      logicdomain.ProfileLevelSituational,
					LevelReason:       "模型给了较低等级，但后端应抬高。",
					SupersedeNodes: []logicdomain.ProfileRetireDecision{
						{NodeID: 41, Reason: "新的团队级规范覆盖旧语言约定。"},
					},
				},
			},
			Reason: "团队显式指令应升级为最高权威规则。",
		},
	}
	uc := NewProfileUseCase(store, reviewer, nil)

	result, err := uc.ApplyInstruction(context.Background(), ProfileInstructionCommand{
		ProfileType: logicdomain.ProfileTypeTeam,
		ProjectID:   9,
		Instruction: "以后团队服务端统一使用 Go。",
	})
	if err != nil {
		t.Fatalf("apply profile instruction: %v", err)
	}
	if reviewer.floorPriority != logicdomain.ProfilePriorityP0 || reviewer.floorLevel != logicdomain.ProfileLevelPersistent {
		t.Fatalf("unexpected reviewer floors: %+v", reviewer)
	}
	if len(store.appliedNodes) != 1 {
		t.Fatalf("expected one applied node, got %+v", store.appliedNodes)
	}
	if store.appliedNodes[0].Priority != logicdomain.ProfilePriorityP0 || store.appliedNodes[0].ProfileLevel != logicdomain.ProfileLevelPersistent {
		t.Fatalf("expected team manual node to be raised to highest authority, got %+v", store.appliedNodes[0])
	}
	if store.appliedNodes[0].SourceKind != logicdomain.ProfileSourceKindManualInstruction || store.appliedNodes[0].SourceID != store.createdInstruction.ID {
		t.Fatalf("unexpected node source binding: %+v", store.appliedNodes[0])
	}
	if len(store.appliedRetired) != 1 || store.appliedRetired[0].NodeID != 41 {
		t.Fatalf("unexpected retired nodes: %+v", store.appliedRetired)
	}
	if result.InstructionID == 0 || result.ReviewReason == "" {
		t.Fatalf("unexpected apply result: %+v", result)
	}
}

// stubProfileStore supplies the profile store behavior needed by profile use case tests.
// stubProfileStore 用于为画像用例测试提供所需的画像存储行为。
type stubProfileStore struct {
	target             logicdomain.ProfileTargetRef
	nodes              []logicdomain.ProfileNodeRecord
	createdInstruction logicdomain.ProfileInstructionRecord
	appliedNodes       []logicdomain.ProfileNodeCandidate
	appliedRetired     []logicdomain.ProfileRetireDecision
	appliedProfile     string
}

// ResolveProfileTarget returns the canned target binding for deterministic test assertions.
// ResolveProfileTarget 用于返回预设目标绑定，保证测试断言稳定。
func (s *stubProfileStore) ResolveProfileTarget(context.Context, int, uint64, uint64) (logicdomain.ProfileTargetRef, error) {
	return s.target, nil
}

// ListActiveProfileNodes returns the canned active node slice for deterministic test assertions.
// ListActiveProfileNodes 用于返回预设 active 节点切片，保证测试断言稳定。
func (s *stubProfileStore) ListActiveProfileNodes(context.Context, logicdomain.ProfileTargetRef, int) ([]logicdomain.ProfileNodeRecord, error) {
	return append([]logicdomain.ProfileNodeRecord(nil), s.nodes...), nil
}

// CreateProfileInstruction records the inserted instruction and returns a deterministic instruction id.
// CreateProfileInstruction 用于记录插入的指令，并返回确定性的 instruction id。
func (s *stubProfileStore) CreateProfileInstruction(_ context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	record.ID = 77
	s.createdInstruction = record
	return record, nil
}

// FailProfileInstruction keeps the stub interface-complete while the success-path tests do not exercise failure persistence.
// FailProfileInstruction 用于补齐测试替身接口，而当前成功路径测试不会走到失败持久化。
func (s *stubProfileStore) FailProfileInstruction(context.Context, uint64, string, string) error {
	return nil
}

// ApplyManualProfileInstruction captures the final writeback payload so tests can assert floors and source binding.
// ApplyManualProfileInstruction 用于捕获最终写回载荷，让测试可以断言地板规则和来源绑定。
func (s *stubProfileStore) ApplyManualProfileInstruction(_ context.Context, _ logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, _ string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	s.appliedNodes = append([]logicdomain.ProfileNodeCandidate(nil), nodes...)
	s.appliedRetired = append([]logicdomain.ProfileRetireDecision(nil), retired...)
	s.appliedProfile = renderedProfile
	accepted := make([]logicdomain.ProfileNodeRecord, 0, len(nodes))
	for idx, node := range nodes {
		accepted = append(accepted, logicdomain.ProfileNodeRecord{
			ID:            uint64(idx + 100),
			ProfileType:   s.target.ProfileType,
			BindID:        s.target.BindID,
			Content:       node.Content,
			Status:        node.Status,
			Priority:      node.Priority,
			ProfileLevel:  node.ProfileLevel,
			LevelReason:   node.LevelReason,
			RefreshWeight: node.RefreshWeight,
			ProfileDate:   node.ProfileDate,
			SourceKind:    node.SourceKind,
			SourceID:      node.SourceID,
		})
	}
	return logicdomain.ManualProfileInstructionApplyResult{
		InstructionID: instruction.ID,
		AcceptedNodes: accepted,
		RetiredNodes:  append([]logicdomain.ProfileRetireDecision(nil), retired...),
	}, nil
}

// stubManualProfileReviewer returns a canned manual review result while recording the enforced floors.
// stubManualProfileReviewer 用于回放预设的手工画像评审结果，并记录传入的权限地板。
type stubManualProfileReviewer struct {
	review        logicdomain.ManualProfileInstructionReview
	floorPriority int
	floorLevel    int
}

// Review captures the enforced floors and returns the canned manual review result.
// Review 用于记录传入的权限地板，并返回预设的手工评审结果。
func (s *stubManualProfileReviewer) Review(_ context.Context, _ logicdomain.ProfileTargetRef, _ []logicdomain.ProfileNodeRecord, _ string, floorPriority, floorLevel int) (logicdomain.ManualProfileInstructionReview, error) {
	s.floorPriority = floorPriority
	s.floorLevel = floorLevel
	return logicdomain.ManualProfileInstructionReview{
		AcceptedNodes: append([]logicdomain.ManualProfileAcceptedNode(nil), s.review.AcceptedNodes...),
		RetiredNodes:  append([]logicdomain.ProfileRetireDecision(nil), s.review.RetiredNodes...),
		Reason:        s.review.Reason,
	}, nil
}

var _ appports.ProfileStore = (*stubProfileStore)(nil)
