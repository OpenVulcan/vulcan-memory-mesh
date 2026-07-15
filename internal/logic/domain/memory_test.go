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

// TestValidTurnAnalysisAdmissionReasonForAdmission verifies the first-pass admission contract keeps keep/drop decisions and rejection reasons semantically aligned.
// TestValidTurnAnalysisAdmissionReasonForAdmission 用于验证首轮准入契约会让 keep/drop 结论与拒绝原因保持语义一致。
func TestValidTurnAnalysisAdmissionReasonForAdmission(t *testing.T) {
	tests := []struct {
		name      string
		admission string
		reason    string
		want      bool
	}{
		{
			name:      "keep requires empty reason",
			admission: TurnAnalysisAdmissionKeep,
			reason:    "",
			want:      true,
		},
		{
			name:      "keep rejects rejection reason",
			admission: TurnAnalysisAdmissionKeep,
			reason:    TurnAnalysisAdmissionReasonNonDurable,
			want:      false,
		},
		{
			name:      "drop requires supported reason",
			admission: TurnAnalysisAdmissionDrop,
			reason:    TurnAnalysisAdmissionReasonQAAnswerOnly,
			want:      true,
		},
		{
			name:      "drop rejects empty reason",
			admission: TurnAnalysisAdmissionDrop,
			reason:    "",
			want:      false,
		},
		{
			name:      "unknown admission rejects reason",
			admission: "archive",
			reason:    TurnAnalysisAdmissionReasonNonDurable,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidTurnAnalysisAdmissionReasonForAdmission(tt.admission, tt.reason); got != tt.want {
				t.Fatalf("ValidTurnAnalysisAdmissionReasonForAdmission(%q, %q) = %v, want %v", tt.admission, tt.reason, got, tt.want)
			}
		})
	}
}

// TestNormalizeMemoryContextKey verifies context dimensions collapse punctuation and repeated separators into one stable lower snake-style key.
// TestNormalizeMemoryContextKey 用于验证情境维度会把标点和重复分隔符折叠成稳定的小写 snake 风格 key。
func TestNormalizeMemoryContextKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "space separated key",
			raw:  " Deployment Mode ",
			want: "deployment_mode",
		},
		{
			name: "dot separated key",
			raw:  "deployment.mode",
			want: "deployment_mode",
		},
		{
			name: "repeated separators collapse",
			raw:  "deployment__mode\tcurrent",
			want: "deployment_mode_current",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMemoryContextKey(tt.raw); got != tt.want {
				t.Fatalf("NormalizeMemoryContextKey(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestNormalizeMemoryContextEvidenceLabel verifies rendered context-evidence labels collapse harmless formatting drift onto one canonical key=value surface before they are reused by retrieval or reviewer prompts.
// TestNormalizeMemoryContextEvidenceLabel 用于验证已渲染的 context evidence 标签会在被检索链路或 reviewer 提示词复用前，折叠到统一的 canonical key=value 表面。
func TestNormalizeMemoryContextEvidenceLabel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "key value pair keeps canonical surface",
			raw:  " deployment mode = LOCAL_OSS ",
			want: "deployment_mode=local oss",
		},
		{
			name: "value-only evidence keeps normalized value",
			raw:  " Local-OSS  Runtime ",
			want: "local oss runtime",
		},
		{
			name: "empty normalized value drops evidence",
			raw:  "deployment_mode=   ",
			want: "",
		},
		{
			name: "key punctuation drift collapses",
			raw:  " deployment.mode__current = Local-OSS ",
			want: "deployment_mode_current=local oss",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMemoryContextEvidenceLabel(tt.raw); got != tt.want {
				t.Fatalf("NormalizeMemoryContextEvidenceLabel(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestNormalizeMemoryContextEdgesAggregatesCounts verifies repeated context labels collapse into deterministic support/rebuttal counters.
// TestNormalizeMemoryContextEdgesAggregatesCounts 用于验证重复情境标签会折叠成确定性的支持/反驳统计。
func TestNormalizeMemoryContextEdgesAggregatesCounts(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	edges := NormalizeMemoryContextEdges(201, []MemoryContextEdgeCandidate{
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: MemoryContextRelationSupport},
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: MemoryContextRelationSupport},
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: MemoryContextRelationRebuttal},
		{ContextKey: "deployment.mode", ContextValue: "local_oss", Relation: MemoryContextRelationSupport},
	}, now)

	if len(edges) != 2 {
		t.Fatalf("expected two aggregated context edges, got %+v", edges)
	}
	if edges[0].MemoryID != 201 || edges[0].ContextKey != "deployment_mode" || edges[0].ContextValue != "local oss" || edges[0].SupportCount != 1 || edges[0].RebuttalCount != 0 {
		t.Fatalf("unexpected first edge aggregation: %+v", edges[0])
	}
	if edges[1].ContextKey != "task_stage" || edges[1].SupportCount != 2 || edges[1].RebuttalCount != 1 {
		t.Fatalf("unexpected second edge aggregation: %+v", edges[1])
	}
	supportCount, rebuttalCount := SummarizeMemoryContextEdges(edges)
	if supportCount != 3 || rebuttalCount != 1 {
		t.Fatalf("unexpected memory-level evidence summary: support=%d rebuttal=%d", supportCount, rebuttalCount)
	}
}
