// profile_test.go verifies the profile query and manual profile-instruction use cases.
// profile_test.go 用于验证画像查询与手工画像指令用例。
package usecase

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/testutil"
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

// TestProfileUseCaseGetBundleBuildsCombinedPrompt verifies the bundle flow rebuilds scope profiles from active nodes,
// ignores stale stored rendered blobs, and emits the deterministic TEAM/PROJECT/USER combined text.
// TestProfileUseCaseGetBundleBuildsCombinedPrompt 用于验证 bundle 流程会基于 active 节点重建 scope 画像，
// 忽略陈旧的已存 rendered blob，并输出确定性的 TEAM/PROJECT/USER 组合文本。
func TestProfileUseCaseGetBundleBuildsCombinedPrompt(t *testing.T) {
	store := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
				UserName:    "alice",
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
				TeamName:    "TeamA",
				SpaceName:   "SpaceA",
				ProjectName: "ProjectA",
			},
		},
		renderedProfiles: map[string]string{
			profileRenderKey(logicdomain.ProfileTypeTeam, 3): `[Profile Legend]
- P = Priority

[Profile Timeline]
2025-01-01:
[P0][L3][W9] 过期的旧渲染，不应该再被读取。`,
		},
		nodesByTarget: map[string][]logicdomain.ProfileNodeRecord{
			profileRenderKey(logicdomain.ProfileTypeTeam, 3): {{
				ID:            11,
				ProfileType:   logicdomain.ProfileTypeTeam,
				BindID:        3,
				Content:       "团队统一使用英文提交信息。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 1,
				ProfileDate:   "2026-03-29",
			}},
			profileRenderKey(logicdomain.ProfileTypeProject, 9): {{
				ID:            21,
				ProfileType:   logicdomain.ProfileTypeProject,
				BindID:        9,
				Content:       "项目必须支持多种 AI 编程工具。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 0,
				ProfileDate:   "2026-03-30",
			}},
			profileRenderKey(logicdomain.ProfileTypeUser, 7): {{
				ID:            31,
				ProfileType:   logicdomain.ProfileTypeUser,
				BindID:        7,
				Content:       "用户偏好使用 Rust。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				ProfileDate:   "2026-03-31",
			}},
		},
	}
	uc := NewProfileUseCase(store, nil, nil)

	result, err := uc.GetBundle(context.Background(), ProfileBundleCommand{
		UserID:             7,
		ProjectID:          9,
		Mode:               ProfileBundleModeFull,
		IncludeExplanation: true,
	})
	if err != nil {
		t.Fatalf("get profile bundle: %v", err)
	}
	if result.CombinedText == "" {
		t.Fatal("expected combined bundle text")
	}
	if !strings.Contains(result.CombinedText, "P/L/W 说明") || !strings.Contains(result.CombinedText, "结构说明") {
		t.Fatalf("expected explanation text in combined bundle, got %q", result.CombinedText)
	}
	if strings.Contains(result.CombinedText, "[SPACE]：") {
		t.Fatalf("did not expect explanation text for empty space scope, got %q", result.CombinedText)
	}
	if strings.Contains(result.CombinedText, "过期的旧渲染") {
		t.Fatalf("expected stale stored rendered blob to be ignored, got %q", result.CombinedText)
	}
	if !strings.Contains(result.CombinedText, "[TEAM]\n2026-03-29:\n[P0][L3][W1] 团队统一使用英文提交信息。") {
		t.Fatalf("expected team section in combined bundle, got %q", result.CombinedText)
	}
	if strings.Contains(result.CombinedText, "[Profile Legend]") {
		t.Fatalf("expected legacy legend to be stripped, got %q", result.CombinedText)
	}
	if strings.Contains(result.CombinedText, "\n[SPACE]\n2026-") {
		t.Fatalf("did not expect empty space section, got %q", result.CombinedText)
	}
	if !strings.Contains(result.CombinedText, "[PROJECT]\n2026-03-30:\n[P0][L3][W0] 项目必须支持多种 AI 编程工具。") {
		t.Fatalf("expected project section in combined bundle, got %q", result.CombinedText)
	}
	if !strings.Contains(result.CombinedText, "[USER]\n2026-03-31:\n[P1][L2][W2] 用户偏好使用 Rust。") {
		t.Fatalf("expected user section in combined bundle, got %q", result.CombinedText)
	}
	if result.ExplanationText != "" || result.EnvironmentPriority != "" {
		t.Fatalf("expected helper fields to stay empty in full mode, got %+v", result)
	}
	if result.TeamProfile != "" || result.SpaceProfile != "" || result.ProjectProfile != "" || result.UserProfile != "" {
		t.Fatalf("expected split sections to stay empty in full mode, got %+v", result)
	}
}

// TestProfileUseCaseGetBundleReturnsEmptyWhenAllScopesMissing verifies full mode returns an empty string when every scope has no active nodes,
// even if stale rendered blobs still exist in storage.
// TestProfileUseCaseGetBundleReturnsEmptyWhenAllScopesMissing 用于验证当所有 scope 都没有 active 节点时，full 模式会直接返回空字符串，
// 即使存储里还残留陈旧 rendered blob 也不会被复用。
func TestProfileUseCaseGetBundleReturnsEmptyWhenAllScopesMissing(t *testing.T) {
	store := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
		renderedProfiles: map[string]string{
			profileRenderKey(logicdomain.ProfileTypeProject, 9): "2025-01-01:\n[P0][L3][W9] 过期的旧项目画像。",
			profileRenderKey(logicdomain.ProfileTypeUser, 7):    "2025-01-02:\n[P1][L2][W9] 过期的旧用户画像。",
		},
	}
	uc := NewProfileUseCase(store, nil, nil)

	result, err := uc.GetBundle(context.Background(), ProfileBundleCommand{
		UserID:             7,
		ProjectID:          9,
		Mode:               ProfileBundleModeFull,
		IncludeExplanation: true,
	})
	if err != nil {
		t.Fatalf("get empty profile bundle: %v", err)
	}
	if result.CombinedText != "" {
		t.Fatalf("expected empty combined text when all scopes are empty, got %q", result.CombinedText)
	}
	if result.ExplanationText != "" || result.EnvironmentPriority != "" {
		t.Fatalf("expected helper fields to stay empty in full mode, got %+v", result)
	}
	if result.TeamProfile != "" || result.SpaceProfile != "" || result.ProjectProfile != "" || result.UserProfile != "" {
		t.Fatalf("expected split sections to stay empty in full mode, got %+v", result)
	}
}

// TestProfileUseCaseGetBundleReturnsSplitSections verifies split mode returns the body-only texts rebuilt from active nodes without reusing stale stored blobs.
// TestProfileUseCaseGetBundleReturnsSplitSections 用于验证 split 模式会返回基于 active 节点重建的正文文本，而不会复用陈旧的已存 blob。
func TestProfileUseCaseGetBundleReturnsSplitSections(t *testing.T) {
	store := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
		renderedProfiles: map[string]string{
			profileRenderKey(logicdomain.ProfileTypeTeam, 3):    "2025-01-01:\n[P0][L3][W9] 旧团队画像。",
			profileRenderKey(logicdomain.ProfileTypeSpace, 5):   "2025-01-01:\n[P0][L3][W9] 旧空间画像。",
			profileRenderKey(logicdomain.ProfileTypeProject, 9): "2025-01-01:\n[P0][L3][W9] 旧项目画像。",
			profileRenderKey(logicdomain.ProfileTypeUser, 7):    "2025-01-01:\n[P1][L2][W9] 旧用户画像。",
		},
		nodesByTarget: map[string][]logicdomain.ProfileNodeRecord{
			profileRenderKey(logicdomain.ProfileTypeTeam, 3): {{
				ID:            41,
				ProfileType:   logicdomain.ProfileTypeTeam,
				BindID:        3,
				Content:       "团队统一使用英文提交信息。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 1,
				ProfileDate:   "2026-03-29",
			}},
			profileRenderKey(logicdomain.ProfileTypeSpace, 5): {{
				ID:            51,
				ProfileType:   logicdomain.ProfileTypeSpace,
				BindID:        5,
				Content:       "空间默认开启严格审查。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 0,
				ProfileDate:   "2026-03-29",
			}},
			profileRenderKey(logicdomain.ProfileTypeProject, 9): {{
				ID:            61,
				ProfileType:   logicdomain.ProfileTypeProject,
				BindID:        9,
				Content:       "项目必须支持多种 AI 编程工具。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 0,
				ProfileDate:   "2026-03-30",
			}},
			profileRenderKey(logicdomain.ProfileTypeUser, 7): {{
				ID:            71,
				ProfileType:   logicdomain.ProfileTypeUser,
				BindID:        7,
				Content:       "用户偏好使用 Rust。",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				ProfileDate:   "2026-03-31",
			}},
		},
	}
	uc := NewProfileUseCase(store, nil, nil)

	result, err := uc.GetBundle(context.Background(), ProfileBundleCommand{
		UserID:             7,
		ProjectID:          9,
		Mode:               ProfileBundleModeSplit,
		IncludeExplanation: true,
	})
	if err != nil {
		t.Fatalf("get split profile bundle: %v", err)
	}
	if result.CombinedText != "" {
		t.Fatalf("expected empty combined text in split mode, got %q", result.CombinedText)
	}
	if result.IncludeExplanation {
		t.Fatalf("expected split mode to suppress explanation flag, got %+v", result)
	}
	if result.ExplanationText != "" || result.EnvironmentPriority != "" {
		t.Fatalf("expected split mode to keep helper texts empty, got %+v", result)
	}
	if result.TeamProfile == "" || result.SpaceProfile == "" || result.ProjectProfile == "" || result.UserProfile == "" {
		t.Fatalf("expected all split scope texts, got %+v", result)
	}
	if strings.Contains(result.TeamProfile, "旧团队画像") || strings.Contains(result.ProjectProfile, "旧项目画像") || strings.Contains(result.UserProfile, "旧用户画像") {
		t.Fatalf("expected split bundle to ignore stale stored rendered blobs, got %+v", result)
	}
}

// TestProfileUseCaseGetBundleRequestsFullActiveSnapshots verifies bundle reconstruction requests one unlimited active-node snapshot per scope instead of silently applying the legacy 256 cap.
// TestProfileUseCaseGetBundleRequestsFullActiveSnapshots 用于验证 bundle 重建会为每个 scope 请求一次不受限的 active 节点快照，而不是继续静默套用旧的 256 上限。
func TestProfileUseCaseGetBundleRequestsFullActiveSnapshots(t *testing.T) {
	store := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
		nodesByTarget: map[string][]logicdomain.ProfileNodeRecord{
			profileRenderKey(logicdomain.ProfileTypeTeam, 3):    {{ID: 11, ProfileType: logicdomain.ProfileTypeTeam, BindID: 3, Content: "team", Status: logicdomain.ProfileStatusActive, Priority: logicdomain.ProfilePriorityP0, ProfileLevel: logicdomain.ProfileLevelPersistent}},
			profileRenderKey(logicdomain.ProfileTypeSpace, 5):   {{ID: 21, ProfileType: logicdomain.ProfileTypeSpace, BindID: 5, Content: "space", Status: logicdomain.ProfileStatusActive, Priority: logicdomain.ProfilePriorityP0, ProfileLevel: logicdomain.ProfileLevelPersistent}},
			profileRenderKey(logicdomain.ProfileTypeProject, 9): {{ID: 31, ProfileType: logicdomain.ProfileTypeProject, BindID: 9, Content: "project", Status: logicdomain.ProfileStatusActive, Priority: logicdomain.ProfilePriorityP0, ProfileLevel: logicdomain.ProfileLevelPersistent}},
			profileRenderKey(logicdomain.ProfileTypeUser, 7):    {{ID: 41, ProfileType: logicdomain.ProfileTypeUser, BindID: 7, Content: "user", Status: logicdomain.ProfileStatusActive, Priority: logicdomain.ProfilePriorityP1, ProfileLevel: logicdomain.ProfileLevelStable}},
		},
	}
	uc := NewProfileUseCase(store, nil, nil)

	_, err := uc.GetBundle(context.Background(), ProfileBundleCommand{
		UserID:    7,
		ProjectID: 9,
		Mode:      ProfileBundleModeFull,
	})
	if err != nil {
		t.Fatalf("get profile bundle: %v", err)
	}
	for _, key := range []string{
		profileRenderKey(logicdomain.ProfileTypeTeam, 3),
		profileRenderKey(logicdomain.ProfileTypeSpace, 5),
		profileRenderKey(logicdomain.ProfileTypeProject, 9),
		profileRenderKey(logicdomain.ProfileTypeUser, 7),
	} {
		limits := store.requestedLimitsForTarget(key)
		if len(limits) != 1 || limits[0] != 0 {
			t.Fatalf("expected bundle scope %s to request unlimited active snapshot, got %+v", key, limits)
		}
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
	limits := store.requestedLimitsForTarget(profileRenderKey(logicdomain.ProfileTypeTeam, 3))
	if len(limits) != 1 || limits[0] != 0 {
		t.Fatalf("expected manual instruction review to request unlimited active snapshot, got %+v", limits)
	}
}

// TestManualInstructionProfileDateUsesLocalCalendarDay verifies manual profile-instruction nodes derive their profile_date from the shared local calendar day instead of a UTC-truncated day.
// TestManualInstructionProfileDateUsesLocalCalendarDay 用于验证手工画像指令节点会从共享本地自然日推导 profile_date，而不是使用 UTC 截断后的日期。
func TestManualInstructionProfileDateUsesLocalCalendarDay(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	date := manualInstructionProfileDate(time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC))
	if date != "2026-04-02" {
		t.Fatalf("expected local calendar day 2026-04-02, got %q", date)
	}
}

// TestProfileUseCaseApplyInstructionInitializesStateForPartialConstruction verifies direct tests or manual integrations that bypass NewProfileUseCase still get lazily initialized dedupe state instead of crashing on nil maps.
// TestProfileUseCaseApplyInstructionInitializesStateForPartialConstruction 用于验证当直接测试或手工集成绕过 NewProfileUseCase 时，画像用例仍会懒初始化去重状态，而不是因为 nil map 直接崩溃。
func TestProfileUseCaseApplyInstructionInitializesStateForPartialConstruction(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeProject,
			BindID:      9,
			ProjectID:   9,
		},
	}
	reviewer := &stubManualProfileReviewer{
		review: logicdomain.ManualProfileInstructionReview{
			AcceptedNodes: []logicdomain.ManualProfileAcceptedNode{
				{
					NormalizedContent: "项目统一使用 Go 语言实现。",
					Priority:          logicdomain.ProfilePriorityP0,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "显式项目指令。",
				},
			},
			Reason: "部分装配实例也应保持稳定。",
		},
	}
	uc := &ProfileUseCase{
		store:    store,
		reviewer: reviewer,
	}

	result, err := uc.ApplyInstruction(context.Background(), ProfileInstructionCommand{
		ProfileType: logicdomain.ProfileTypeProject,
		ProjectID:   9,
		Instruction: "项目统一使用 Go 语言实现。",
	})
	if err != nil {
		t.Fatalf("apply profile instruction on partial use case: %v", err)
	}
	if result.InstructionID == 0 {
		t.Fatalf("expected persisted instruction result, got %+v", result)
	}
	if store.createInstructionCount() != 1 || store.applyInstructionCount() != 1 {
		t.Fatalf("expected one create/apply pair, got create=%d apply=%d", store.createInstructionCount(), store.applyInstructionCount())
	}
}

// TestProfileUseCaseApplyInstructionReusesSharedFlightWithNilContext verifies direct callers that accidentally pass a nil context still reuse an identical in-flight manual instruction instead of panicking on ctx.Done().
// TestProfileUseCaseApplyInstructionReusesSharedFlightWithNilContext 用于验证直接调用方即使误传 nil context，也能复用相同的进行中手工画像指令，而不会因为 ctx.Done() 触发 panic。
func TestProfileUseCaseApplyInstructionReusesSharedFlightWithNilContext(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeUser,
			BindID:      7,
			UserID:      7,
			ProjectID:   9,
		},
	}
	reviewer := &stubManualProfileReviewer{}
	uc := NewProfileUseCase(store, reviewer, nil)
	cmd := ProfileInstructionCommand{
		ProfileType: logicdomain.ProfileTypeUser,
		UserID:      7,
		ProjectID:   9,
		Instruction: "以后默认优先给出 Rust 方案。",
	}
	flightKey := uc.profileInstructionFlightKey(store.target, cmd.Instruction)
	flight := &profileInstructionFlight{
		done: make(chan struct{}),
		result: ProfileInstructionResult{
			Target:        store.target,
			InstructionID: 88,
			ReviewReason:  "reuse existing in-flight result",
		},
	}
	close(flight.done)
	uc.flights[flightKey] = flight

	result, err := uc.ApplyInstruction(nil, cmd)
	if err != nil {
		t.Fatalf("apply instruction with nil context should reuse shared flight: %v", err)
	}
	if result.InstructionID != 88 || result.ReviewReason != "reuse existing in-flight result" {
		t.Fatalf("unexpected shared-flight result: %+v", result)
	}
	if store.createInstructionCount() != 0 || store.applyInstructionCount() != 0 {
		t.Fatalf("expected shared flight reuse to skip persistence work, got create=%d apply=%d", store.createInstructionCount(), store.applyInstructionCount())
	}
}

// TestProfileUseCaseApplyInstructionDedupesIdenticalConcurrentCalls verifies identical concurrent manual instructions
// on the same target reuse one in-flight LLM review instead of creating duplicate instruction rows and duplicate node writes.
// TestProfileUseCaseApplyInstructionDedupesIdenticalConcurrentCalls 用于验证同一目标上的相同手工画像指令在并发时会复用同一条进行中的 LLM 评审，
// 而不会创建重复 instruction 记录或重复写入节点。
func TestProfileUseCaseApplyInstructionDedupesIdenticalConcurrentCalls(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeProject,
			BindID:      9,
			ProjectID:   9,
		},
	}
	reviewer := &blockingManualProfileReviewer{
		review: logicdomain.ManualProfileInstructionReview{
			AcceptedNodes: []logicdomain.ManualProfileAcceptedNode{
				{
					NormalizedContent: "项目统一使用 Go 语言实现。",
					Priority:          logicdomain.ProfilePriorityP0,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "显式项目指令。",
				},
			},
			Reason: "相同并发指令应只评审一次。",
		},
		entered: make(chan int, 2),
		release: make(chan struct{}),
	}
	uc := NewProfileUseCase(store, reviewer, nil)
	cmd := ProfileInstructionCommand{
		ProfileType: logicdomain.ProfileTypeProject,
		ProjectID:   9,
		Instruction: "项目统一使用 Go 语言实现。",
	}

	type callResult struct {
		result ProfileInstructionResult
		err    error
	}
	firstDone := make(chan callResult, 1)
	secondDone := make(chan callResult, 1)
	go func() {
		result, err := uc.ApplyInstruction(context.Background(), cmd)
		firstDone <- callResult{result: result, err: err}
	}()
	<-reviewer.entered
	go func() {
		result, err := uc.ApplyInstruction(context.Background(), cmd)
		secondDone <- callResult{result: result, err: err}
	}()
	time.Sleep(120 * time.Millisecond)

	if reviewer.callCount() != 1 {
		t.Fatalf("expected one reviewer call while identical request is in flight, got %d", reviewer.callCount())
	}
	if store.createInstructionCount() != 1 {
		t.Fatalf("expected one instruction insert while identical request is in flight, got %d", store.createInstructionCount())
	}
	if store.applyInstructionCount() != 0 {
		t.Fatalf("did not expect final writeback before releasing reviewer, got %d", store.applyInstructionCount())
	}

	close(reviewer.release)
	first := <-firstDone
	second := <-secondDone
	if first.err != nil || second.err != nil {
		t.Fatalf("expected both deduped calls to succeed, got first=%v second=%v", first.err, second.err)
	}
	if first.result.InstructionID != second.result.InstructionID {
		t.Fatalf("expected deduped calls to reuse one instruction result, got %+v vs %+v", first.result, second.result)
	}
	if reviewer.callCount() != 1 {
		t.Fatalf("expected reviewer to run once for identical concurrent calls, got %d", reviewer.callCount())
	}
	if store.createInstructionCount() != 1 || store.applyInstructionCount() != 1 {
		t.Fatalf("expected one create/apply pair, got create=%d apply=%d", store.createInstructionCount(), store.applyInstructionCount())
	}
}

// TestProfileUseCaseApplyInstructionSerializesDifferentCallsSameTarget verifies different instructions
// for the same target do not overlap; the second one waits until the first target-scoped review and writeback completes.
// TestProfileUseCaseApplyInstructionSerializesDifferentCallsSameTarget 用于验证同一目标上的不同手工指令不会并发执行；
// 第二条指令必须等待第一条目标级评审和写回完成后才会继续。
func TestProfileUseCaseApplyInstructionSerializesDifferentCallsSameTarget(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeProject,
			BindID:      9,
			ProjectID:   9,
		},
	}
	reviewer := &blockingManualProfileReviewer{
		review: logicdomain.ManualProfileInstructionReview{
			AcceptedNodes: []logicdomain.ManualProfileAcceptedNode{
				{
					NormalizedContent: "项目统一使用 Go 语言实现。",
					Priority:          logicdomain.ProfilePriorityP0,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "显式项目指令。",
				},
			},
			Reason: "同一目标需要串行评审。",
		},
		entered: make(chan int, 4),
		release: make(chan struct{}),
	}
	uc := NewProfileUseCase(store, reviewer, nil)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := uc.ApplyInstruction(context.Background(), ProfileInstructionCommand{
			ProfileType: logicdomain.ProfileTypeProject,
			ProjectID:   9,
			Instruction: "项目统一使用 Go 语言实现。",
		})
		firstDone <- err
	}()
	<-reviewer.entered
	go func() {
		_, err := uc.ApplyInstruction(context.Background(), ProfileInstructionCommand{
			ProfileType: logicdomain.ProfileTypeProject,
			ProjectID:   9,
			Instruction: "项目统一使用 Rust 语言实现。",
		})
		secondDone <- err
	}()
	time.Sleep(120 * time.Millisecond)

	if reviewer.callCount() != 1 {
		t.Fatalf("expected second instruction to stay blocked behind the same target gate, got %d reviewer calls", reviewer.callCount())
	}
	if store.createInstructionCount() != 1 {
		t.Fatalf("expected only the first instruction row before releasing the gate, got %d", store.createInstructionCount())
	}

	close(reviewer.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first serialized instruction failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second serialized instruction failed: %v", err)
	}
	if reviewer.callCount() != 2 {
		t.Fatalf("expected two reviewer calls after both different instructions finish, got %d", reviewer.callCount())
	}
	if store.createInstructionCount() != 2 || store.applyInstructionCount() != 2 {
		t.Fatalf("expected two create/apply pairs after serialized execution, got create=%d apply=%d", store.createInstructionCount(), store.applyInstructionCount())
	}
}

// TestProfileUseCaseApplyInstructionSkipsFailureWritebackOnOutcomeUncertain verifies the use case does not
// append an extra "failed" update when the store reports one storage outcome as uncertain.
// TestProfileUseCaseApplyInstructionSkipsFailureWritebackOnOutcomeUncertain 用于验证当存储层报告“结果不确定”时，
// 用例层不会再追加一条失败回写，避免在坏连接上继续放大状态污染。
func TestProfileUseCaseApplyInstructionSkipsFailureWritebackOnOutcomeUncertain(t *testing.T) {
	store := &stubProfileStore{
		target: logicdomain.ProfileTargetRef{
			ProfileType: logicdomain.ProfileTypeProject,
			BindID:      9,
			ProjectID:   9,
		},
		applyErr: logicdomain.OutcomeUncertainError{
			Operation: "apply manual profile instruction",
			Message:   "gateway returned deadlock after commit result became ambiguous",
		},
	}
	reviewer := &stubManualProfileReviewer{
		review: logicdomain.ManualProfileInstructionReview{
			AcceptedNodes: []logicdomain.ManualProfileAcceptedNode{
				{
					NormalizedContent: "项目统一使用 Go 语言实现。",
					Priority:          logicdomain.ProfilePriorityP0,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "显式项目指令。",
				},
			},
			Reason: "先完成评审，再由存储层回写。",
		},
	}
	uc := NewProfileUseCase(store, reviewer, nil)

	_, err := uc.ApplyInstruction(context.Background(), ProfileInstructionCommand{
		ProfileType: logicdomain.ProfileTypeProject,
		ProjectID:   9,
		Instruction: "项目统一使用 Go 语言实现。",
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain error, got %v", err)
	}
	if store.failInstructionCount() != 0 {
		t.Fatalf("did not expect failure writeback for uncertain outcome, got %d fail calls", store.failInstructionCount())
	}
}

// stubProfileStore supplies the profile store behavior needed by profile use case tests.
// stubProfileStore 用于为画像用例测试提供所需的画像存储行为。
type stubProfileStore struct {
	mu                 sync.Mutex
	target             logicdomain.ProfileTargetRef
	targets            map[int]logicdomain.ProfileTargetRef
	nodes              []logicdomain.ProfileNodeRecord
	nodesByTarget      map[string][]logicdomain.ProfileNodeRecord
	requestedLimits    map[string][]int
	renderedProfiles   map[string]string
	createdInstruction logicdomain.ProfileInstructionRecord
	nextInstructionID  uint64
	appliedNodes       []logicdomain.ProfileNodeCandidate
	appliedRetired     []logicdomain.ProfileRetireDecision
	appliedProfile     string
	applyErr           error
	createCalls        int
	applyCalls         int
	failCalls          int
}

// ResolveProfileTarget returns the canned target binding for deterministic test assertions.
// ResolveProfileTarget 用于返回预设目标绑定，保证测试断言稳定。
func (s *stubProfileStore) ResolveProfileTarget(_ context.Context, profileType int, _ uint64, _ uint64) (logicdomain.ProfileTargetRef, error) {
	if s.targets != nil {
		if target, ok := s.targets[profileType]; ok {
			return target, nil
		}
	}
	return s.target, nil
}

// ListActiveProfileNodes returns the canned active node slice for deterministic test assertions.
// ListActiveProfileNodes 用于返回预设 active 节点切片，保证测试断言稳定。
func (s *stubProfileStore) ListActiveProfileNodes(_ context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestedLimits == nil {
		s.requestedLimits = map[string][]int{}
	}
	key := profileRenderKey(target.ProfileType, target.BindID)
	s.requestedLimits[key] = append(s.requestedLimits[key], limit)
	if s.nodesByTarget != nil {
		if nodes, ok := s.nodesByTarget[key]; ok {
			return append([]logicdomain.ProfileNodeRecord(nil), nodes...), nil
		}
	}
	return append([]logicdomain.ProfileNodeRecord(nil), s.nodes...), nil
}

// requestedLimitsForTarget returns the captured ListActiveProfileNodes limits for one target key so tests can assert whether a caller requested a bounded or unbounded snapshot.
// requestedLimitsForTarget 用于返回某个目标 key 捕获到的 ListActiveProfileNodes limit，方便测试断言调用方请求的是受限还是不受限快照。
func (s *stubProfileStore) requestedLimitsForTarget(key string) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.requestedLimits[key]...)
}

// LoadRenderedProfile returns the canned rendered scope text so bundle tests can assert composition behavior without touching SQL adapters.
// LoadRenderedProfile 用于返回预设的 scope 渲染文本，让 bundle 测试无需依赖 SQL 适配器也能断言组合行为。
func (s *stubProfileStore) LoadRenderedProfile(_ context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renderedProfiles == nil {
		return "", nil
	}
	return s.renderedProfiles[profileRenderKey(target.ProfileType, target.BindID)], nil
}

// CreateProfileInstruction records the inserted instruction and returns a deterministic instruction id.
// CreateProfileInstruction 用于记录插入的指令，并返回确定性的 instruction id。
func (s *stubProfileStore) CreateProfileInstruction(_ context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	if s.nextInstructionID == 0 {
		s.nextInstructionID = 77
	}
	record.ID = s.nextInstructionID
	s.nextInstructionID++
	s.createdInstruction = record
	return record, nil
}

// FailProfileInstruction keeps the stub interface-complete while the success-path tests do not exercise failure persistence.
// FailProfileInstruction 用于补齐测试替身接口，而当前成功路径测试不会走到失败持久化。
func (s *stubProfileStore) FailProfileInstruction(context.Context, uint64, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCalls++
	return nil
}

// ApplyManualProfileInstruction captures the final writeback payload so tests can assert floors and source binding.
// ApplyManualProfileInstruction 用于捕获最终写回载荷，让测试可以断言地板规则和来源绑定。
func (s *stubProfileStore) ApplyManualProfileInstruction(_ context.Context, _ logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, _ string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyCalls++
	if s.applyErr != nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, s.applyErr
	}
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

// createInstructionCount returns how many instruction rows the stub has been asked to create.
// createInstructionCount 用于返回测试替身被请求创建 instruction 行的次数。
func (s *stubProfileStore) createInstructionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCalls
}

// applyInstructionCount returns how many final manual-instruction writebacks the stub has observed.
// applyInstructionCount 用于返回测试替身观察到的最终手工画像写回次数。
func (s *stubProfileStore) applyInstructionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyCalls
}

// failInstructionCount returns how many times the use case tried to mark one instruction as failed.
// failInstructionCount 用于返回用例层尝试把 instruction 标记为失败的次数。
func (s *stubProfileStore) failInstructionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failCalls
}

// profileRenderKey builds the deterministic scope key used by the rendered-profile stub map.
// profileRenderKey 用于构建测试替身渲染画像映射使用的确定性 scope 键。
func profileRenderKey(profileType int, bindID uint64) string {
	return strings.Join([]string{strconv.Itoa(profileType), strconv.FormatUint(bindID, 10)}, ":")
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

// blockingManualProfileReviewer lets concurrency tests pause one in-flight review so they can observe whether
// identical or conflicting manual instructions get deduped or serialized before a second LLM call begins.
// blockingManualProfileReviewer 用于让并发测试暂停一条进行中的评审，
// 以便观察相同或冲突的手工画像指令在第二次 LLM 调用开始前是否被去重或串行化。
type blockingManualProfileReviewer struct {
	mu      sync.Mutex
	review  logicdomain.ManualProfileInstructionReview
	calls   int
	entered chan int
	release chan struct{}
}

// Review records the call count, notifies the test that one LLM review has begun, then waits for the shared release gate.
// Review 用于记录调用次数、通知测试一条 LLM 评审已经开始，然后等待共享释放闸门。
func (s *blockingManualProfileReviewer) Review(_ context.Context, _ logicdomain.ProfileTargetRef, _ []logicdomain.ProfileNodeRecord, _ string, _ int, _ int) (logicdomain.ManualProfileInstructionReview, error) {
	s.mu.Lock()
	s.calls++
	callIndex := s.calls
	s.mu.Unlock()
	s.entered <- callIndex
	<-s.release
	return logicdomain.ManualProfileInstructionReview{
		AcceptedNodes: append([]logicdomain.ManualProfileAcceptedNode(nil), s.review.AcceptedNodes...),
		RetiredNodes:  append([]logicdomain.ProfileRetireDecision(nil), s.review.RetiredNodes...),
		Reason:        s.review.Reason,
	}, nil
}

// callCount returns how many reviewer invocations have started so far.
// callCount 用于返回当前已经开始的评审调用次数。
func (s *blockingManualProfileReviewer) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

var _ appports.ProfileStore = (*stubProfileStore)(nil)
