// evaluator_test.go implements unit tests for the opcode compiler and evaluator VM.
// evaluator_test.go 用于实现指令编译器和求值虚拟机的单元测试。
package pii

import (
	"strings"
	"testing"
)

// TestCompileConditionRejectsUnknownFunction verifies the TestCompileConditionRejectsUnknownFunction behavior.
// TestCompileConditionRejectsUnknownFunction 用于验证 TestCompileConditionRejectsUnknownFunction 行为。
func TestCompileConditionRejectsUnknownFunction(t *testing.T) {
	_, err := compileCondition("unknown($1)", 1)
	if err == nil || !strings.Contains(err.Error(), "unknown function") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestCompileConditionRejectsOutOfRangeGroup verifies the TestCompileConditionRejectsOutOfRangeGroup behavior.
// TestCompileConditionRejectsOutOfRangeGroup 用于验证 TestCompileConditionRejectsOutOfRangeGroup 行为。
func TestCompileConditionRejectsOutOfRangeGroup(t *testing.T) {
	_, err := compileCondition("cn_check($2) == 2", 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds regex capture count") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestCompileConditionRejectsOverComplexProgram verifies the TestCompileConditionRejectsOverComplexProgram behavior.
// TestCompileConditionRejectsOverComplexProgram 用于验证 TestCompileConditionRejectsOverComplexProgram 行为。
func TestCompileConditionRejectsOverComplexProgram(t *testing.T) {
	parts := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		parts = append(parts, "is_base64($0)")
	}
	_, err := compileCondition(strings.Join(parts, " || "), 0)
	if err == nil || !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEvaluatorReturnsConditionFalse verifies the TestEvaluatorReturnsConditionFalse behavior.
// TestEvaluatorReturnsConditionFalse 用于验证 TestEvaluatorReturnsConditionFalse 行为。
func TestEvaluatorReturnsConditionFalse(t *testing.T) {
	prog, err := compileCondition("(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)", 2)
	if err != nil {
		t.Fatal(err)
	}
	match := []int{0, 18, 0, 17, 17, 18}
	ok, reason := evaluator{}.Run(prog, "110105194912310021", match)
	if ok || reason != reasonCondFalse {
		t.Fatalf("run result = (%v, %s), want (false, %s)", ok, reason, reasonCondFalse)
	}
}

// TestEvaluatorReturnsEvalOK verifies the TestEvaluatorReturnsEvalOK behavior.
// TestEvaluatorReturnsEvalOK 用于验证 TestEvaluatorReturnsEvalOK 行为。
func TestEvaluatorReturnsEvalOK(t *testing.T) {
	prog, err := compileCondition("(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)", 2)
	if err != nil {
		t.Fatal(err)
	}
	match := []int{0, 18, 0, 17, 17, 18}
	ok, reason := evaluator{}.Run(prog, "11010519491231002X", match)
	if !ok || reason != reasonEvalOK {
		t.Fatalf("run result = (%v, %s), want (true, %s)", ok, reason, reasonEvalOK)
	}
}

// TestEvaluatorSupportsLenFunction verifies that len($0) style conditions are compiled and evaluated correctly.
// TestEvaluatorSupportsLenFunction 用于验证 len($0) 这类条件能够被正确编译和执行。
func TestEvaluatorSupportsLenFunction(t *testing.T) {
	prog, err := compileCondition("len($0) == 9", 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, reason := evaluator{}.Run(prog, "8888-9999", []int{0, 9})
	if !ok || reason != reasonEvalOK {
		t.Fatalf("run result = (%v, %s), want (true, %s)", ok, reason, reasonEvalOK)
	}
}

// TestEvaluatorShortCircuitsOr verifies the TestEvaluatorShortCircuitsOr behavior.
// TestEvaluatorShortCircuitsOr 用于验证 TestEvaluatorShortCircuitsOr 行为。
func TestEvaluatorShortCircuitsOr(t *testing.T) {
	prog, err := compileCondition("is_base64($0) || cn_check($9) == 1", 0)
	if err == nil {
		t.Fatal("expected invalid group reference to fail at compile time")
	}

	prog, err = compileCondition("is_base64($0) || is_luhn($0)", 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, reason := evaluator{}.Run(prog, "SGVsbG8=", []int{0, 8})
	if !ok || reason != reasonEvalOK {
		t.Fatalf("run result = (%v, %s)", ok, reason)
	}
}

// TestEvaluatorMasksBadGroupRefs verifies the TestEvaluatorMasksBadGroupRefs behavior.
// TestEvaluatorMasksBadGroupRefs 用于验证 TestEvaluatorMasksBadGroupRefs 行为。
func TestEvaluatorMasksBadGroupRefs(t *testing.T) {
	prog := program{ops: []opCode{{kind: opPushGroup, groupIndex: 99}}}
	ok, reason := evaluator{}.Run(prog, "abc", []int{0, 3})
	if !ok || reason != reasonBadGroupRefMasked {
		t.Fatalf("run result = (%v, %s), want bad-group masked", ok, reason)
	}
}

// TestEvaluatorMasksStepLimit verifies the TestEvaluatorMasksStepLimit behavior.
// TestEvaluatorMasksStepLimit 用于验证 TestEvaluatorMasksStepLimit 行为。
func TestEvaluatorMasksStepLimit(t *testing.T) {
	ops := make([]opCode, maxProgramSteps+1)
	for i := range ops {
		ops[i] = opCode{kind: opPushInt, intValue: i}
	}
	ok, reason := evaluator{}.Run(program{ops: ops}, "abc", []int{0, 3})
	if !ok || reason != reasonStepLimitMasked {
		t.Fatalf("run result = (%v, %s), want step-limit masked", ok, reason)
	}
}

// TestEvaluatorMasksInvalidNumericFunctionInput verifies the TestEvaluatorMasksInvalidNumericFunctionInput behavior.
// TestEvaluatorMasksInvalidNumericFunctionInput 用于验证 TestEvaluatorMasksInvalidNumericFunctionInput 行为。
func TestEvaluatorMasksInvalidNumericFunctionInput(t *testing.T) {
	prog, err := compileCondition("weight_sum($1, [1,2,3]) == 6", 1)
	if err != nil {
		t.Fatal(err)
	}
	match := []int{0, 3, 0, 3}
	ok, reason := evaluator{}.Run(prog, "12A", match)
	if !ok || reason != reasonPanicMasked {
		t.Fatalf("run result = (%v, %s), want panic-masked", ok, reason)
	}
}
