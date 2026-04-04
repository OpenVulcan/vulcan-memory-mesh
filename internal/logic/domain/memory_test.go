// memory_test.go verifies shared durable-memory lifecycle helpers so retrieval and lifecycle write-backs keep using the same active+unexpired contract.
// memory_test.go 用于验证共享的长期记忆生命周期辅助逻辑，确保检索与生命周期回写持续使用同一套 active+unexpired 契约。
package domain

import (
	"testing"
	"time"
)

// TestMemoryNodeRecordIsActiveUnexpiredAt verifies the shared hot-path lifecycle predicate accepts only active rows whose expiry has not elapsed at the observation time.
// TestMemoryNodeRecordIsActiveUnexpiredAt 用于验证共享热路径生命周期谓词只接受在观察时刻仍为 active 且尚未过期的记录。
func TestMemoryNodeRecordIsActiveUnexpiredAt(t *testing.T) {
	now := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		row  MemoryNodeRecord
		want bool
	}{
		{
			name: "active without explicit expiry stays hot",
			row:  MemoryNodeRecord{ID: 1, Status: MemoryStatusActive},
			want: true,
		},
		{
			name: "active future expiry stays hot",
			row:  MemoryNodeRecord{ID: 2, Status: MemoryStatusActive, ExpiresAt: now.Add(time.Hour)},
			want: true,
		},
		{
			name: "active row at expiry boundary is cold",
			row:  MemoryNodeRecord{ID: 3, Status: MemoryStatusActive, ExpiresAt: now},
			want: false,
		},
		{
			name: "active expired row is cold",
			row:  MemoryNodeRecord{ID: 4, Status: MemoryStatusActive, ExpiresAt: now.Add(-time.Minute)},
			want: false,
		},
		{
			name: "superseded row is cold",
			row:  MemoryNodeRecord{ID: 5, Status: MemoryStatusSuperseded, ExpiresAt: now.Add(time.Hour)},
			want: false,
		},
		{
			name: "zero id row is cold",
			row:  MemoryNodeRecord{ID: 0, Status: MemoryStatusActive, ExpiresAt: now.Add(time.Hour)},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MemoryNodeRecordIsActiveUnexpiredAt(tt.row, now); got != tt.want {
				t.Fatalf("MemoryNodeRecordIsActiveUnexpiredAt(%+v, %v) = %v, want %v", tt.row, now, got, tt.want)
			}
		})
	}
}
