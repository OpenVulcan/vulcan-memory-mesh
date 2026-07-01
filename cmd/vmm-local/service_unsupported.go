//go:build !windows && !linux && !darwin

// service_unsupported.go provides clear service-management errors on platforms outside the supported desktop/server set.
// service_unsupported.go 用于在不属于受支持桌面或服务器集合的平台上提供清晰的服务管理错误。
package main

import (
	"context"
	"fmt"
)

// runServiceRuntime runs the normal foreground runtime on unsupported service platforms for local debugging.
// runServiceRuntime 用于在不支持服务注册的平台上以前台方式运行普通运行时，便于本地调试。
func runServiceRuntime(_ string, run func(context.Context, func()) error) error {
	return run(context.Background(), nil)
}

// applyServiceCommand reports that service registration is intentionally limited to Windows, Linux, and macOS.
// applyServiceCommand 用于报告服务注册当前仅限 Windows、Linux 和 macOS。
func applyServiceCommand(command serviceCommand, _ string, _ string) error {
	return fmt.Errorf("service action %q is only supported on Windows, Linux, and macOS", command.action)
}
