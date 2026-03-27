// persona.go implements the persona-loading side of the DockDB gateway adapter.
// persona.go 用于实现 DockDB 网关适配器中的画像加载能力。
package vldg_dockdb

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Load returns the current durable persona context for one session scope.
// Load 用于返回某个会话范围下当前可用的长期画像上下文。
func (s *Store) Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error) {
	// Keep the provider contract satisfied with a stable no-op result until DockDB-backed persona records are introduced.
	// 在 DockDB 画像记录尚未落地前，先用稳定的空结果满足 provider 契约，避免重新引入内存回退。
	select {
	case <-ctx.Done():
		return logicdomain.PersonaContext{}, ctx.Err()
	default:
	}
	return logicdomain.PersonaContext{}, nil
}
