// persona.go implements the in-memory mock outbound adapters.
// persona.go 用于实现内存版 mock 出站适配器。
package memory_mock

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// PersonaProvider is the local mock persona adapter used to inject fixed persona data during development.
// PersonaProvider 用于作为本地 mock 画像适配器，在开发阶段注入固定画像数据。
type PersonaProvider struct{}

// NewPersonaProvider creates a PersonaProvider instance.
// NewPersonaProvider 用于创建 PersonaProvider 实例。
func NewPersonaProvider() *PersonaProvider { return &PersonaProvider{} }

// Load loads related data.
// Load 用于加载相关数据。
func (p *PersonaProvider) Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error) {
	select {
	case <-ctx.Done():
		return logicdomain.PersonaContext{}, ctx.Err()
	default:
	}
	if session.UserID != "usr_8899" {
		return logicdomain.PersonaContext{}, nil
	}
	return logicdomain.PersonaContext{ProjectConstraints: []string{"当前项目默认使用 Go，强调清晰边界与可测试性。", "实现时需要保留充分注释，避免隐式魔法逻辑。"}, Profile: []string{"用户是后端工程师，偏好六边形架构与模块化分层。"}, Preferences: []string{"喜欢用 Go。", "强制写注释。"}}, nil
}
