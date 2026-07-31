// common_management_test.go verifies explicit management lifecycle transitions.
// common_management_test.go 用于验证显式管理生命周期转换。
package domain

import "testing"

// TestSessionMemoryStatusValidateManagementAction verifies restore and restart boundaries cannot be bypassed.
// TestSessionMemoryStatusValidateManagementAction 验证恢复与重新开始边界不能被绕过。
func TestSessionMemoryStatusValidateManagementAction(t *testing.T) {
	tests := []struct {
		name    string
		status  SessionMemoryStatus
		action  string
		allowed bool
	}{
		{name: "archive active", status: SessionMemoryStatusActive, action: ManagementActionArchive, allowed: true},
		{name: "unarchive archived", status: SessionMemoryStatusArchived, action: ManagementActionUnarchive, allowed: true},
		{name: "recycle active", status: SessionMemoryStatusActive, action: ManagementActionRecycle, allowed: true},
		{name: "recycle archived", status: SessionMemoryStatusArchived, action: ManagementActionRecycle, allowed: true},
		{name: "restart forgotten", status: SessionMemoryStatusForgotten, action: ManagementActionRestart, allowed: true},
		{name: "unarchive forgotten", status: SessionMemoryStatusForgotten, action: ManagementActionUnarchive, allowed: false},
		{name: "archive recycled", status: SessionMemoryStatusRecycled, action: ManagementActionArchive, allowed: false},
		{name: "restart recycled", status: SessionMemoryStatusRecycled, action: ManagementActionRestart, allowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.status.ValidateManagementAction(test.action)
			if test.allowed && err != nil {
				t.Fatalf("ValidateManagementAction() error = %v", err)
			}
			if !test.allowed && !IsConflictError(err) {
				t.Fatalf("ValidateManagementAction() error = %v, want conflict", err)
			}
		})
	}
}
