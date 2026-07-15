// hints_test.go characterizes shared provider-hint semantics before adapter-specific SDK mapping occurs.
// hints_test.go 用于刻画共享 provider hint 在进入各适配器 SDK 映射前的语义。
package providerhint

import "testing"

// TestCloneMapPreservesWritableEmptyContract verifies empty inputs remain writable and populated inputs do not alias the source map.
// TestCloneMapPreservesWritableEmptyContract 用于验证空输入仍可写，且非空输入不会与源 map 共享容器。
func TestCloneMapPreservesWritableEmptyContract(t *testing.T) {
	empty := CloneMap(nil)
	empty["new"] = true
	if !empty["new"].(bool) {
		t.Fatalf("expected writable non-nil empty clone, got %#v", empty)
	}

	source := map[string]any{"temperature": 0.2}
	cloned := CloneMap(source)
	cloned["temperature"] = 0.8
	if source["temperature"] != 0.2 {
		t.Fatalf("source map mutated through clone: %#v", source)
	}
}

// TestCloneNestedMapTrimsKeysAndClonesValues verifies model lookup normalization and inner-map isolation.
// TestCloneNestedMapTrimsKeysAndClonesValues 用于验证模型查询键规范化及内层 map 隔离。
func TestCloneNestedMapTrimsKeysAndClonesValues(t *testing.T) {
	source := map[string]map[string]any{" model-a ": {"temperature": 0.2}}
	cloned := CloneNestedMap(source)
	cloned["model-a"]["temperature"] = 0.8
	if source[" model-a "]["temperature"] != 0.2 {
		t.Fatalf("source nested map mutated through clone: %#v", source)
	}
	if empty := CloneNestedMap(nil); empty == nil {
		t.Fatal("expected non-nil empty nested clone")
	}
}

// TestMergePreservesPrecedenceAndBaseIsolation verifies request hints override model defaults without mutating configured base values.
// TestMergePreservesPrecedenceAndBaseIsolation 用于验证请求 hint 会覆盖模型默认值，且不会修改已配置的基础值。
func TestMergePreservesPrecedenceAndBaseIsolation(t *testing.T) {
	base := map[string]any{"temperature": 0.1, "top_p": 0.8}
	merged := Merge(base, map[string]any{"temperature": 0.2}, map[string]any{"temperature": 0.3})
	if merged["temperature"] != 0.3 || merged["top_p"] != 0.8 {
		t.Fatalf("unexpected merged hints: %#v", merged)
	}
	merged["top_p"] = 0.5
	if base["top_p"] != 0.8 {
		t.Fatalf("base hints mutated through merge: %#v", base)
	}
}

// TestStrictScalarConversions verifies accepted types and rejects string coercion retained only by provider-specific adapters.
// TestStrictScalarConversions 用于验证受支持类型，并拒绝仅由特定 provider 适配器保留的字符串强制转换。
func TestStrictScalarConversions(t *testing.T) {
	floatCases := []struct {
		input any
		want  float64
	}{{float64(1.5), 1.5}, {float32(2.5), 2.5}, {int(3), 3}, {int64(4), 4}, {int32(5), 5}}
	for _, tc := range floatCases {
		got, ok := Float64(tc.input)
		if !ok || got != tc.want {
			t.Fatalf("Float64(%T(%v)) = %v, %v; want %v, true", tc.input, tc.input, got, ok, tc.want)
		}
	}
	if _, ok := Float64("1.5"); ok {
		t.Fatal("expected Float64 to reject strings")
	}

	intCases := []struct {
		input any
		want  int64
	}{{int(1), 1}, {int64(2), 2}, {int32(3), 3}, {float64(4.9), 4}, {float32(5.9), 5}}
	for _, tc := range intCases {
		got, ok := Int64(tc.input)
		if !ok || got != tc.want {
			t.Fatalf("Int64(%T(%v)) = %v, %v; want %v, true", tc.input, tc.input, got, ok, tc.want)
		}
	}
	if _, ok := Int64("5"); ok {
		t.Fatal("expected Int64 to reject strings")
	}

	if got, ok := Bool(true); !ok || !got {
		t.Fatalf("Bool(true) = %v, %v", got, ok)
	}
	if _, ok := Bool("true"); ok {
		t.Fatal("expected Bool to reject strings")
	}
	if got, ok := String(" value "); !ok || got != "value" {
		t.Fatalf("String trim = %q, %v", got, ok)
	}
	if _, ok := String(" "); ok {
		t.Fatal("expected String to reject blank values")
	}
}
