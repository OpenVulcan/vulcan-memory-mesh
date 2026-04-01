// profile_bundle.go implements the deterministic profile-bundle assembly flow used by gRPC callers that need one ready-to-inject scope combination.
// profile_bundle.go 用于实现确定性的画像组合流程，供需要“一次拿到可直接注入内容”的 gRPC 调用方使用。
package usecase

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// ProfileBundleModeFull returns one fully assembled prompt text that already contains the scope sections in injection order.
	// ProfileBundleModeFull 用于返回一段已经按注入顺序组装好的完整提示词文本。
	ProfileBundleModeFull = 1

	// ProfileBundleModeSplit returns the individual TEAM/SPACE/PROJECT/USER sections so callers can inject or post-process them themselves.
	// ProfileBundleModeSplit 用于返回拆分后的 TEAM/SPACE/PROJECT/USER 段落，便于调用方自行注入或二次处理。
	ProfileBundleModeSplit = 2
)

const (
	// profileBundleIntroText frames the combined bundle as one deterministic summary of the current environment plus user-serving preferences.
	// profileBundleIntroText 用于把组合结果固定描述为当前环境约束与服务对象偏好的确定性摘要。
	profileBundleIntroText = "以下内容是结合用户历史习惯、偏好、设定总结的画像。"

	// profileBundleEnvironmentHeader explains that environment constraints come from TEAM/SPACE/PROJECT and that project-level rules override broader scopes.
	// profileBundleEnvironmentHeader 用于说明环境约束来自 TEAM/SPACE/PROJECT，并强调项目级规则会覆盖更宽 scope。
	profileBundleEnvironmentHeader = "以下是你当前所处的项目环境约束（优先级：Project > Space > Team）："

	// profileBundleEnvironmentPriorityText keeps the raw precedence string available for split-mode callers that need metadata without a duplicated full sentence.
	// profileBundleEnvironmentPriorityText 用于为 split 模式提供原始优先级串，避免再返回一整句完整标题造成重复拼接。
	profileBundleEnvironmentPriorityText = "Project > Space > Team"

	// profileBundleUserHeader explains that USER preferences should be respected only after environment constraints have been satisfied.
	// profileBundleUserHeader 用于说明 USER 偏好需要在不违反环境约束的前提下尽量满足。
	profileBundleUserHeader = "以下是你当前正在服务的目标用户偏好（请在不违反环境约束的前提下，尽量迎合用户）："

	// profileBundleExplanationText keeps the optional P/L/W legend outside the stored scope profile body so callers can turn it on only when they need help text.
	// profileBundleExplanationText 用于把可选的 P/L/W 说明放在存储画像正文之外，让调用方只在需要帮助说明时才打开。
	profileBundleExplanationText = `以下是等级与偏好权重说明：
- P/L/W 说明：
- P = Priority（优先级）
  - P0：硬约束 / 不可违背的规则
  - P1：重要偏好 / 重要工作约定
  - P2：普通参考 / 低优先级偏好
- L = Lifetime Level（生命周期级别）
  - L0：短暂上下文
  - L1：阶段性偏好或语境
  - L2：稳定习惯或长期偏好
  - L3：持久规则 / 强约束 / 身份特征
- W = Refresh Weight（刷新权重）
  - 更高的 W 代表这条记忆被再次确认或续期的次数更多。

结构说明：
- [TEAM]：团队级画像，表示团队范围内共享的长期规则与约定。
- [SPACE]：空间级画像，表示当前空间内共享的规则与上下文。
- [PROJECT]：当前项目画像，表示当前项目的具体约束、目标与工程约定。
- [USER]：当前正在服务的目标用户偏好，需要在不违反环境约束的前提下尽量满足。`
)

// ProfileBundleCommand carries the user/project pair plus the output mode used to assemble one deterministic profile prompt bundle.
// ProfileBundleCommand 用于承载组装确定性画像提示词所需的 user/project 对，以及输出模式。
type ProfileBundleCommand struct {
	UserID             uint64
	ProjectID          uint64
	Mode               int
	IncludeExplanation bool
}

// ProfileBundleResult returns both the split scope bodies and the optional fully assembled prompt text for one user/project pair.
// ProfileBundleResult 用于返回某个 user/project 组合下的拆分 scope 正文，以及可选的完整拼装提示词文本。
type ProfileBundleResult struct {
	UserTarget          logicdomain.ProfileTargetRef
	ProjectTarget       logicdomain.ProfileTargetRef
	Mode                int
	IncludeExplanation  bool
	ExplanationText     string
	EnvironmentPriority string
	CombinedText        string
	TeamProfile         string
	SpaceProfile        string
	ProjectProfile      string
	UserProfile         string
}

// GetBundle resolves the user/project pair, loads the current rendered scope profiles, strips any legacy legend text, and returns either one full bundle or split sections.
// GetBundle 用于解析 user/project 组合、加载当前渲染后的 scope 画像、去掉遗留说明头，并按 full 或 split 形式返回组合结果。
func (u *ProfileUseCase) GetBundle(ctx context.Context, cmd ProfileBundleCommand) (ProfileBundleResult, error) {
	if u == nil || u.store == nil {
		return ProfileBundleResult{}, fmt.Errorf("profile store is nil")
	}
	if err := validateProfileBundleCommand(cmd); err != nil {
		return ProfileBundleResult{}, err
	}

	// Resolve the concrete USER and PROJECT scopes first, then derive TEAM/SPACE from the resolved project hierarchy
	// so bundle assembly stays deterministic without issuing redundant hierarchy queries.
	// 先解析具体的 USER 和 PROJECT 目标，再从已解析项目层级派生 TEAM/SPACE，
	// 这样既能保持组合结果确定性，也能避免重复发起层级查询。
	userTarget, err := u.store.ResolveProfileTarget(ctx, logicdomain.ProfileTypeUser, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return ProfileBundleResult{}, err
	}
	projectTarget, err := u.store.ResolveProfileTarget(ctx, logicdomain.ProfileTypeProject, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return ProfileBundleResult{}, err
	}
	teamTarget := logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeTeam,
		BindID:      projectTarget.TeamID,
		UserID:      userTarget.UserID,
		TeamID:      projectTarget.TeamID,
		SpaceID:     projectTarget.SpaceID,
		ProjectID:   projectTarget.ProjectID,
		UserName:    userTarget.UserName,
		TeamName:    projectTarget.TeamName,
		SpaceName:   projectTarget.SpaceName,
		ProjectName: projectTarget.ProjectName,
	}
	spaceTarget := logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeSpace,
		BindID:      projectTarget.SpaceID,
		UserID:      userTarget.UserID,
		TeamID:      projectTarget.TeamID,
		SpaceID:     projectTarget.SpaceID,
		ProjectID:   projectTarget.ProjectID,
		UserName:    userTarget.UserName,
		TeamName:    projectTarget.TeamName,
		SpaceName:   projectTarget.SpaceName,
		ProjectName: projectTarget.ProjectName,
	}

	teamProfile, err := u.loadBundleProfileBody(ctx, teamTarget)
	if err != nil {
		return ProfileBundleResult{}, fmt.Errorf("load team rendered profile: %w", err)
	}
	spaceProfile, err := u.loadBundleProfileBody(ctx, spaceTarget)
	if err != nil {
		return ProfileBundleResult{}, fmt.Errorf("load space rendered profile: %w", err)
	}
	projectProfile, err := u.loadBundleProfileBody(ctx, projectTarget)
	if err != nil {
		return ProfileBundleResult{}, fmt.Errorf("load project rendered profile: %w", err)
	}
	userProfile, err := u.loadBundleProfileBody(ctx, userTarget)
	if err != nil {
		return ProfileBundleResult{}, fmt.Errorf("load user rendered profile: %w", err)
	}

	result := ProfileBundleResult{
		UserTarget:          userTarget,
		ProjectTarget:       projectTarget,
		Mode:                cmd.Mode,
		IncludeExplanation:  cmd.IncludeExplanation,
		EnvironmentPriority: profileBundleEnvironmentPriorityText,
		TeamProfile:         teamProfile,
		SpaceProfile:        spaceProfile,
		ProjectProfile:      projectProfile,
		UserProfile:         userProfile,
	}
	if cmd.IncludeExplanation {
		result.ExplanationText = profileBundleExplanationText
	}
	if cmd.Mode == ProfileBundleModeFull {
		result.CombinedText = buildProfileBundleText(result)
		// Full mode is authoritative: callers should consume the merged bundle only,
		// so helper metadata and split sections are intentionally suppressed here to
		// avoid accidental double concatenation on the client side.
		// Full 模式下以 merged bundle 为唯一权威输出，因此这里主动清空辅助说明和拆分段落，
		// 避免客户端再把这些字段重复拼接进组合文本。
		result.ExplanationText = ""
		result.EnvironmentPriority = ""
		result.TeamProfile = ""
		result.SpaceProfile = ""
		result.ProjectProfile = ""
		result.UserProfile = ""
	}
	return result, nil
}

// loadBundleProfileBody reads one rendered scope profile and normalizes it into the body-only format expected by bundle assembly.
// loadBundleProfileBody 用于读取单个已渲染 scope 画像，并把它规范成组合接口所需的“仅正文”格式。
func (u *ProfileUseCase) loadBundleProfileBody(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	if target.BindID == 0 {
		return "", nil
	}
	stored, err := u.store.LoadRenderedProfile(ctx, target)
	if err != nil {
		return "", err
	}
	return stripLegacyProfileLegend(stored), nil
}

// validateProfileBundleCommand checks the bundle request before it resolves the hierarchy and rendered profile blobs.
// validateProfileBundleCommand 用于在解析层级和读取渲染画像之前校验组合请求。
func validateProfileBundleCommand(cmd ProfileBundleCommand) error {
	if cmd.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if cmd.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	switch cmd.Mode {
	case ProfileBundleModeFull, ProfileBundleModeSplit:
		return nil
	default:
		return logicdomain.ValidationError{Field: "mode", Message: "must be full or split"}
	}
}

// buildProfileBundleText assembles the final injection-ready prompt text with deterministic section ordering and optional explanation text.
// buildProfileBundleText 用于按确定性 section 顺序组装最终可注入提示词，并按需包含解释说明。
func buildProfileBundleText(result ProfileBundleResult) string {
	blocks := make([]string, 0, 4)
	blocks = append(blocks, profileBundleIntroText)
	if strings.TrimSpace(result.ExplanationText) != "" {
		blocks = append(blocks, strings.TrimSpace(result.ExplanationText))
	}

	environmentSections := make([]string, 0, 3)
	if strings.TrimSpace(result.TeamProfile) != "" {
		environmentSections = append(environmentSections, "[TEAM]\n"+strings.TrimSpace(result.TeamProfile))
	}
	if strings.TrimSpace(result.SpaceProfile) != "" {
		environmentSections = append(environmentSections, "[SPACE]\n"+strings.TrimSpace(result.SpaceProfile))
	}
	if strings.TrimSpace(result.ProjectProfile) != "" {
		environmentSections = append(environmentSections, "[PROJECT]\n"+strings.TrimSpace(result.ProjectProfile))
	}
	if len(environmentSections) > 0 {
		blocks = append(blocks, profileBundleEnvironmentHeader+"\n"+strings.Join(environmentSections, "\n\n"))
	}

	if strings.TrimSpace(result.UserProfile) != "" {
		blocks = append(blocks, profileBundleUserHeader+"\n[USER]\n"+strings.TrimSpace(result.UserProfile))
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

// stripLegacyProfileLegend removes the historical "[Profile Legend]" wrapper so old stored rows remain compatible after the renderer switches to body-only persistence.
// stripLegacyProfileLegend 用于去掉历史的 "[Profile Legend]" 包装，确保渲染器切到“仅正文”后，旧数据仍能兼容新 bundle 输出。
func stripLegacyProfileLegend(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return ""
	}
	if !strings.HasPrefix(profile, "[Profile Legend]") {
		return profile
	}
	marker := "[Profile Timeline]"
	idx := strings.Index(profile, marker)
	if idx < 0 {
		return profile
	}
	return strings.TrimSpace(profile[idx+len(marker):])
}
