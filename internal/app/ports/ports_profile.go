// ports_profile.go keeps profile retrieval and profile-instruction related contracts used by profile and pre-check workflows.
// ports_profile.go 用于承载画像查询、画像指令以及 pre-check 相关工作流依赖的画像契约。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ProfileStore is the port used by profile-query and manual profile-instruction RPCs to resolve targets, inspect active nodes, and persist reviewed updates.
// ProfileStore 用于让画像查询与手工画像指令 RPC 解析目标、查看 active 节点并持久化评审后的更新结果。
type ProfileStore interface {
	ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error)
	ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error)
	LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error)
	CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error)
	FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error
	ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error)
}

// ContextPersonaProvider is the port used by pre-check flows to load stable persona and project context.
// ContextPersonaProvider 用于给 pre-check 流程加载稳定的画像和项目上下文。
type ContextPersonaProvider interface {
	Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error)
}
