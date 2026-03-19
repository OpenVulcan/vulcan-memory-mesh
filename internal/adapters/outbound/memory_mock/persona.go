package memory_mock

import (
	"context"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type PersonaProvider struct{}

func NewPersonaProvider() *PersonaProvider { return &PersonaProvider{} }

func (p *PersonaProvider) Load(ctx context.Context, session domain.SessionRef) (domain.PersonaContext, error) {
	select { case <-ctx.Done(): return domain.PersonaContext{}, ctx.Err(); default: }
	if session.UserID != "usr_8899" { return domain.PersonaContext{}, nil }
	return domain.PersonaContext{
		ProjectConstraints: []string{"当前项目默认使用 Go，强调清晰边界与可测试性。", "实现时需要保留充分注释，避免隐式魔法逻辑。"},
		Profile:            []string{"用户是后端工程师，偏好六边形架构与模块化分层。"},
		Preferences:        []string{"喜欢用 Go。", "强制写注释。"},
	}, nil
}
