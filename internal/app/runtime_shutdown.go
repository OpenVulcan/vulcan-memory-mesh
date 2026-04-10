// runtime_shutdown.go implements runtime shutdown deduplication helpers shared by startup cleanup and graceful stop paths.
// runtime_shutdown.go 用于实现启动清理与优雅停机共用的运行时 shutdown 去重辅助逻辑。
package app

import (
	"fmt"
	"reflect"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// buildUniqueShutdownSequence preserves construction order while collapsing duplicate shutdown hooks that point at the same runtime dependency.
// buildUniqueShutdownSequence 用于在保留构建顺序的同时折叠指向同一运行时依赖的重复 shutdown 钩子。
func buildUniqueShutdownSequence(shutdowners ...appports.Shutdowner) []appports.Shutdowner {
	unique := make([]appports.Shutdowner, 0, len(shutdowners))
	for _, shutdowner := range shutdowners {
		unique = appendUniqueShutdowner(unique, shutdowner)
	}
	return unique
}

// appendUniqueShutdowner appends one shutdown hook only when it does not already exist in the current construction list.
// appendUniqueShutdowner 用于仅在当前构建列表里尚不存在时追加一条 shutdown 钩子。
func appendUniqueShutdowner(shutdowners []appports.Shutdowner, shutdowner appports.Shutdowner) []appports.Shutdowner {
	if shutdowner == nil {
		return shutdowners
	}
	id := shutdownerIdentity(shutdowner)
	for _, existing := range shutdowners {
		if shutdownerIdentity(existing) == id {
			return shutdowners
		}
	}
	return append(shutdowners, shutdowner)
}

// shutdownerIdentity derives one stable in-process identity for duplicate-suppression across startup cleanup and graceful shutdown.
// shutdownerIdentity 用于为启动清理与优雅停机的重复抑制推导一条稳定的进程内标识。
func shutdownerIdentity(shutdowner appports.Shutdowner) string {
	if shutdowner == nil {
		return ""
	}
	value := reflect.ValueOf(shutdowner)
	typeName := reflect.TypeOf(shutdowner).String()
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		if value.IsNil() {
			return typeName + ":nil"
		}
		return fmt.Sprintf("%s:%x", typeName, value.Pointer())
	default:
		return fmt.Sprintf("%s:%#v", typeName, shutdowner)
	}
}
