// validators_test.go implements unit tests for the atomic validation primitives.
// validators_test.go 用于实现原子校验函数的单元测试。
package pii

import "testing"

// TestWeightSumRejectsNonDigits verifies the TestWeightSumRejectsNonDigits behavior.
// TestWeightSumRejectsNonDigits 用于验证 TestWeightSumRejectsNonDigits 行为。
func TestWeightSumRejectsNonDigits(t *testing.T) {
	sum, ok := WeightSum("12A4", []int{1, 2, 3, 4})
	if ok || sum != 0 {
		t.Fatalf("weight sum = (%d, %v), want (0, false)", sum, ok)
	}
}

// TestWeightSumMatchesCNIDExample verifies the TestWeightSumMatchesCNIDExample behavior.
// TestWeightSumMatchesCNIDExample 用于验证 TestWeightSumMatchesCNIDExample 行为。
func TestWeightSumMatchesCNIDExample(t *testing.T) {
	sum, ok := WeightSum("11010519491231002", []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2})
	if !ok {
		t.Fatal("expected weight sum to succeed")
	}
	if sum%11 != 2 {
		t.Fatalf("weight sum mod 11 = %d, want 2", sum%11)
	}
}

// TestCNCheckMapsExpectedCharacters verifies the TestCNCheckMapsExpectedCharacters behavior.
// TestCNCheckMapsExpectedCharacters 用于验证 TestCNCheckMapsExpectedCharacters 行为。
func TestCNCheckMapsExpectedCharacters(t *testing.T) {
	value, ok := CNCheck("X")
	if !ok || value != 2 {
		t.Fatalf("cn check = (%d, %v), want (2, true)", value, ok)
	}
	value, ok = CNCheck("2")
	if !ok || value != 10 {
		t.Fatalf("cn check = (%d, %v), want (10, true)", value, ok)
	}
}

// TestLuhnFunctions verifies the TestLuhnFunctions behavior.
// TestLuhnFunctions 用于验证 TestLuhnFunctions 行为。
func TestLuhnFunctions(t *testing.T) {
	sum, ok := LuhnSum("79927398713")
	if !ok || sum%10 != 0 {
		t.Fatalf("luhn sum = (%d, %v), want valid checksum", sum, ok)
	}
	if !IsLuhn("79927398713") {
		t.Fatal("expected known luhn sample to pass")
	}
	if IsLuhn("79927398710") {
		t.Fatal("expected invalid luhn sample to fail")
	}
	if IsLuhn("79927A98713") {
		t.Fatal("expected non-digit input to fail")
	}
}

// TestIsBase64Strict verifies the TestIsBase64Strict behavior.
// TestIsBase64Strict 用于验证 TestIsBase64Strict 行为。
func TestIsBase64Strict(t *testing.T) {
	if !IsBase64("SGVsbG8=") {
		t.Fatal("expected valid base64 to pass")
	}
	if IsBase64("SGVsbG8") {
		t.Fatal("expected missing padding/length mismatch to fail")
	}
	if IsBase64("SGVs=b8=") {
		t.Fatal("expected invalid interior padding to fail")
	}
	if IsBase64("SGVs*b8=") {
		t.Fatal("expected invalid alphabet to fail")
	}
}
