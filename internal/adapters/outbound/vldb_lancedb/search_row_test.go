// search_row_test.go protects vector result scope identities from JSON number rounding.
// search_row_test.go 防止向量结果中的范围标识因 JSON 数字转换而丢失精度。
package vldb_lancedb

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"
)

// TestSearchAcceptsExplicitNullMetadata covers the JSON representation produced when an optional metadata map is absent.
// TestSearchAcceptsExplicitNullMetadata 覆盖可选元数据映射缺省时产生的 JSON null 表示。
func TestSearchAcceptsExplicitNullMetadata(t *testing.T) {
	engine := &fakeLanceDBEngine{vectorSearchFunc: func(string, []float32, uint32, string, string, lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
		return lancedbffi.SearchResult{Data: []byte(`[{"id":"without-metadata","metadata_json":"null","_distance":0.25}]`)}, nil
	}}
	store := &Store{engine: engine, tableName: "memory_3", vectorColumn: "vector", dimension: 3}
	hits, err := store.Search(context.Background(), []float32{1, 0, 0}, 1, logicdomain.SearchFilter{})
	if err != nil || len(hits) != 1 || hits[0].Metadata["origin"] != "vector_search" {
		t.Fatalf("null metadata was not preserved as an empty annotated map: hits=%+v err=%v", hits, err)
	}
}

// TestSearchValidatesDistance verifies malformed backend rows cannot become perfect matches or override a genuine zero distance.
// TestSearchValidatesDistance 验证异常后端行不会成为满分命中，也不会覆盖真正的零距离。
func TestSearchValidatesDistance(t *testing.T) {
	for _, field := range []string{"", `"_distance":null,`, `"_distance":"",`, `"_distance":"NaN",`, `"_distance":"Inf",`, `"_distance":-1,`, `"_distance":0,`} {
		t.Run(field, func(t *testing.T) {
			engine := &fakeLanceDBEngine{vectorSearchFunc: func(string, []float32, uint32, string, string, lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
				return lancedbffi.SearchResult{Data: []byte(`[{` + field + `"id":"match","score":0.9}]`)}, nil
			}}
			store := &Store{engine: engine, tableName: "memory_3", vectorColumn: "vector", dimension: 3}
			hits, err := store.Search(context.Background(), []float32{1, 0, 0}, 1, logicdomain.SearchFilter{})
			if field == `"_distance":0,` {
				if err != nil || len(hits) != 1 || hits[0].Score != 1 {
					t.Fatalf("zero distance must remain a perfect match: hits=%+v err=%v", hits, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "_distance") {
				t.Fatalf("expected distance error, got hits=%+v err=%v", hits, err)
			}
		})
	}
}

// TestSearchRejectsCorruptMetadata preserves the backend row identifier in decoding errors.
// TestSearchRejectsCorruptMetadata 验证元数据解码错误包含后端行标识。
func TestSearchRejectsCorruptMetadata(t *testing.T) {
	engine := &fakeLanceDBEngine{vectorSearchFunc: func(string, []float32, uint32, string, string, lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
		return lancedbffi.SearchResult{Data: []byte(`[{"id":"corrupt","metadata_json":"{","_distance":0}]`)}, nil
	}}
	store := &Store{engine: engine, tableName: "memory_3", vectorColumn: "vector", dimension: 3}
	_, err := store.Search(context.Background(), []float32{1, 0, 0}, 1, logicdomain.SearchFilter{})
	if err == nil || !strings.Contains(err.Error(), "corrupt") || !strings.Contains(err.Error(), "metadata_json") {
		t.Fatalf("expected identifiable metadata error, got %v", err)
	}
}

// TestSearchRowPreservesFullWidthScopeIDs checks the numeric and string forms returned by supported backends.
// TestSearchRowPreservesFullWidthScopeIDs 验证受支持后端返回的数字与字符串形式保留完整范围标识。
func TestSearchRowPreservesFullWidthScopeIDs(t *testing.T) {
	var row searchRow
	input := []byte(`{"team_id":9007199254740993,"space_id":"9007199254740993","project_id":18446744073709551615,"session_id":"18446744073709551615","user_id":9223372036854775808,"source_turn_id":9007199254740995,"_distance":0.25,"score":"0.5"}`)
	if err := json.Unmarshal(input, &row); err != nil {
		t.Fatal(err)
	}
	if row.TeamID != 9007199254740993 || row.SpaceID != row.TeamID || row.ProjectID != math.MaxUint64 || row.SessionID != math.MaxUint64 || row.UserID != 9223372036854775808 || row.SourceTurnID != 9007199254740995 {
		t.Fatalf("scope identifiers lost precision: %+v", row)
	}
	if row.Distance != 0.25 {
		t.Fatalf("search scores changed: %+v", row)
	}
}

// TestSearchRowRejectsMalformedScopeIDs prevents overflow, negative and fractional values from entering scope matching.
// TestSearchRowRejectsMalformedScopeIDs 防止溢出、负数和小数进入范围匹配。
func TestSearchRowRejectsMalformedScopeIDs(t *testing.T) {
	for _, value := range []string{`18446744073709551616`, `-1`, `1.5`, `"18446744073709551616"`, `"-1"`} {
		var row searchRow
		if err := json.Unmarshal([]byte(`{"team_id":`+value+`,"_distance":0}`), &row); err == nil || !strings.Contains(err.Error(), "team_id") {
			t.Fatalf("accepted invalid scope identifier %s", value)
		}
	}
}
