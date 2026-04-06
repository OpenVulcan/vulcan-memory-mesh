// evaluator.go implements the lightweight opcode compiler and VM used by conditional PII rules.
// evaluator.go 用于实现条件型 PII 规则复用的轻量级指令编译器和虚拟机。
package pii

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const maxProgramSteps = 64

// reasonCode categorizes one evaluator outcome without leaking the matched sensitive text.
// reasonCode 用于给一次求值结果分类，同时避免泄露匹配到的敏感原文。
type reasonCode string

const (
	reasonEvalOK            reasonCode = "EVAL_OK"
	reasonCondFalse         reasonCode = "COND_FALSE"
	reasonPanicMasked       reasonCode = "PANIC_MASKED"
	reasonStepLimitMasked   reasonCode = "STEP_LIMIT_MASKED"
	reasonBadGroupRefMasked reasonCode = "BAD_GROUP_REF_MASKED"
)

// tokenKind describes one lexical token used while compiling a condition into opcodes.
// tokenKind 用于描述把条件表达式编译成指令时使用的一种词法单元。
type tokenKind uint8

const (
	tokenInvalid tokenKind = iota
	tokenVariable
	tokenInteger
	tokenArray
	tokenFunction
	tokenOperator
	tokenLeftParen
	tokenRightParen
	tokenComma
)

// token carries one parsed lexical unit.
// token 用于承载一个已解析完成的词法单元。
type token struct {
	kind       tokenKind
	text       string
	groupIndex int
	intValue   int
	ints       []int
	argCount   int
}

// astNodeKind groups the node shapes used during compile-time expression lowering.
// astNodeKind 用于归类编译阶段表达式节点的不同形态。
type astNodeKind uint8

const (
	astVariable astNodeKind = iota
	astInteger
	astArray
	astFunction
	astBinary
)

// astNode represents one compile-time expression tree node.
// astNode 用于表示一棵编译阶段表达式树中的单个节点。
type astNode struct {
	kind       astNodeKind
	name       string
	groupIndex int
	intValue   int
	ints       []int
	args       []*astNode
	left       *astNode
	right      *astNode
}

// opKind defines one VM instruction.
// opKind 用于定义一条虚拟机指令。
type opKind uint8

const (
	opPushGroup opKind = iota
	opPushInt
	opPushArray
	opCallWeightSum
	opCallCNCheck
	opCallLen
	opCallIsLuhn
	opCallIsBase64
	opModulo
	opEqual
	opNotEqual
	opGreater
	opGreaterOrEqual
	opLess
	opLessOrEqual
	opJumpIfFalse
	opJumpIfTrue
	opPop
)

// opCode stores one precompiled instruction plus any small inline operands.
// opCode 用于保存一条预编译后的指令及其内联小型操作数。
type opCode struct {
	kind       opKind
	groupIndex int
	intValue   int
	ints       []int
	jump       int
}

// program is the immutable opcode sequence executed by the evaluator VM.
// program 用于表示由求值虚拟机执行的不可变指令序列。
type program struct {
	ops []opCode
}

// valueKind tracks the concrete runtime type carried on the evaluator stack.
// valueKind 用于跟踪求值栈上存放的具体运行时类型。
type valueKind uint8

const (
	valueInvalid valueKind = iota
	valueString
	valueInt
	valueBool
	valueIntSlice
)

// value stores one VM stack entry in a tagged-union style to avoid interface allocations.
// value 用于以带标签联合体的方式存放一项虚拟机栈值，避免 interface 分配。
type value struct {
	kind valueKind
	s    string
	i    int
	b    bool
	ints []int
}

// evaluator executes precompiled PII condition programs against one regex match.
// evaluator 用于针对单条正则匹配执行预编译好的 PII 条件程序。
type evaluator struct{}

// compileCondition tokenizes, validates, and lowers one DSL condition into VM bytecode.
// compileCondition 用于把一条 DSL 条件表达式完成词法分析、校验和字节码降级。
func compileCondition(condition string, numSubexp int) (program, error) {
	trimmed := strings.TrimSpace(condition)
	if trimmed == "" {
		return program{}, nil
	}
	tokens, err := tokenizeCondition(trimmed, numSubexp)
	if err != nil {
		return program{}, err
	}
	rpn, err := shuntingYard(tokens)
	if err != nil {
		return program{}, err
	}
	root, err := buildAST(rpn)
	if err != nil {
		return program{}, err
	}
	prog := program{ops: make([]opCode, 0, len(rpn)*2)}
	if err := compileNode(root, &prog); err != nil {
		return program{}, err
	}
	if len(prog.ops) == 0 {
		return program{}, errors.New("empty condition program")
	}
	if len(prog.ops) > maxProgramSteps {
		return program{}, fmt.Errorf("condition generates %d opcodes, exceeds max %d", len(prog.ops), maxProgramSteps)
	}
	return prog, nil
}

// Run executes one precompiled program against the source text and match indexes.
// Run 用于针对原始文本和当前匹配索引执行一段预编译程序。
func (vm evaluator) Run(prog program, text string, match []int) (result bool, reason reasonCode) {
	defer func() {
		if recover() != nil {
			result = true
			reason = reasonPanicMasked
		}
	}()
	if len(prog.ops) == 0 {
		return true, reasonEvalOK
	}
	var stack [maxProgramSteps]value
	sp := 0
	steps := 0
	for ip := 0; ip < len(prog.ops); ip++ {
		steps++
		if steps > maxProgramSteps {
			return true, reasonStepLimitMasked
		}
		op := prog.ops[ip]
		switch op.kind {
		case opPushGroup:
			groupValue, ok := readGroup(text, match, op.groupIndex)
			if !ok {
				return true, reasonBadGroupRefMasked
			}
			stack[sp] = value{kind: valueString, s: groupValue}
			sp++
		case opPushInt:
			stack[sp] = value{kind: valueInt, i: op.intValue}
			sp++
		case opPushArray:
			stack[sp] = value{kind: valueIntSlice, ints: op.ints}
			sp++
		case opPop:
			if sp < 1 {
				return true, reasonPanicMasked
			}
			sp--
		case opJumpIfFalse:
			if sp < 1 || stack[sp-1].kind != valueBool {
				return true, reasonPanicMasked
			}
			if !stack[sp-1].b {
				ip = op.jump - 1
			}
		case opJumpIfTrue:
			if sp < 1 || stack[sp-1].kind != valueBool {
				return true, reasonPanicMasked
			}
			if stack[sp-1].b {
				ip = op.jump - 1
			}
		case opCallWeightSum:
			if sp < 2 || stack[sp-2].kind != valueString || stack[sp-1].kind != valueIntSlice {
				return true, reasonPanicMasked
			}
			sum, ok := WeightSum(stack[sp-2].s, stack[sp-1].ints)
			if !ok {
				return true, reasonPanicMasked
			}
			sp -= 2
			stack[sp] = value{kind: valueInt, i: sum}
			sp++
		case opCallCNCheck:
			if sp < 1 || stack[sp-1].kind != valueString {
				return true, reasonPanicMasked
			}
			check, ok := CNCheck(stack[sp-1].s)
			if !ok {
				return true, reasonPanicMasked
			}
			stack[sp-1] = value{kind: valueInt, i: check}
		case opCallLen:
			if sp < 1 || stack[sp-1].kind != valueString {
				return true, reasonPanicMasked
			}
			stack[sp-1] = value{kind: valueInt, i: len(stack[sp-1].s)}
		case opCallIsLuhn:
			if sp < 1 || stack[sp-1].kind != valueString {
				return true, reasonPanicMasked
			}
			stack[sp-1] = value{kind: valueBool, b: IsLuhn(stack[sp-1].s)}
		case opCallIsBase64:
			if sp < 1 || stack[sp-1].kind != valueString {
				return true, reasonPanicMasked
			}
			stack[sp-1] = value{kind: valueBool, b: IsBase64(stack[sp-1].s)}
		case opModulo:
			if sp < 2 || stack[sp-2].kind != valueInt || stack[sp-1].kind != valueInt || stack[sp-1].i == 0 {
				return true, reasonPanicMasked
			}
			left := stack[sp-2].i
			right := stack[sp-1].i
			sp -= 2
			stack[sp] = value{kind: valueInt, i: left % right}
			sp++
		case opEqual, opNotEqual:
			if sp < 2 {
				return true, reasonPanicMasked
			}
			ok, equal := compareEqual(stack[sp-2], stack[sp-1])
			if !ok {
				return true, reasonPanicMasked
			}
			if op.kind == opNotEqual {
				equal = !equal
			}
			sp -= 2
			stack[sp] = value{kind: valueBool, b: equal}
			sp++
		case opGreater, opGreaterOrEqual, opLess, opLessOrEqual:
			if sp < 2 || stack[sp-2].kind != valueInt || stack[sp-1].kind != valueInt {
				return true, reasonPanicMasked
			}
			left := stack[sp-2].i
			right := stack[sp-1].i
			var cmp bool
			switch op.kind {
			case opGreater:
				cmp = left > right
			case opGreaterOrEqual:
				cmp = left >= right
			case opLess:
				cmp = left < right
			case opLessOrEqual:
				cmp = left <= right
			}
			sp -= 2
			stack[sp] = value{kind: valueBool, b: cmp}
			sp++
		default:
			return true, reasonPanicMasked
		}
	}
	if sp != 1 || stack[0].kind != valueBool {
		return true, reasonPanicMasked
	}
	if stack[0].b {
		return true, reasonEvalOK
	}
	return false, reasonCondFalse
}

// readGroup resolves one $n capture group from the regex submatch index array without allocating slices.
// readGroup 用于在不分配额外切片的前提下，从正则子匹配索引数组中解析一个 $n 捕获组。
func readGroup(text string, match []int, groupIndex int) (string, bool) {
	offset := groupIndex * 2
	if groupIndex < 0 || offset+1 >= len(match) {
		return "", false
	}
	start := match[offset]
	end := match[offset+1]
	if start < 0 || end < start || end > len(text) {
		return "", false
	}
	return text[start:end], true
}

// compareEqual performs strict typed equality checks for VM values.
// compareEqual 用于对虚拟机值执行严格的同类型相等比较。
func compareEqual(left, right value) (bool, bool) {
	if left.kind != right.kind {
		return false, false
	}
	switch left.kind {
	case valueInt:
		return true, left.i == right.i
	case valueBool:
		return true, left.b == right.b
	case valueString:
		return true, left.s == right.s
	default:
		return false, false
	}
}

// tokenizeCondition scans the DSL source into tokens while enforcing group bounds and literal validity.
// tokenizeCondition 用于把 DSL 源串扫描成词法单元，并同时校验捕获组边界和字面量合法性。
func tokenizeCondition(expr string, numSubexp int) ([]token, error) {
	tokens := make([]token, 0, 16)
	for i := 0; i < len(expr); {
		switch ch := expr[i]; {
		case isSpace(ch):
			i++
		case ch == '$':
			start := i + 1
			i++
			for i < len(expr) && isDigit(expr[i]) {
				i++
			}
			if start == i {
				return nil, errors.New("group reference is missing an index")
			}
			index, err := strconv.Atoi(expr[start:i])
			if err != nil {
				return nil, err
			}
			if index > numSubexp {
				return nil, fmt.Errorf("group reference $%d exceeds regex capture count %d", index, numSubexp)
			}
			tokens = append(tokens, token{kind: tokenVariable, groupIndex: index, text: expr[start-1 : i]})
		case isDigit(ch):
			start := i
			i++
			for i < len(expr) && isDigit(expr[i]) {
				i++
			}
			value, err := strconv.Atoi(expr[start:i])
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokenInteger, intValue: value, text: expr[start:i]})
		case isIdentifierStart(ch):
			start := i
			i++
			for i < len(expr) && isIdentifierPart(expr[i]) {
				i++
			}
			name := expr[start:i]
			if !isBuiltinFunction(name) {
				return nil, fmt.Errorf("unknown function: %s", name)
			}
			tokens = append(tokens, token{kind: tokenFunction, text: name})
		case ch == '[':
			arrayToken, next, err := readArrayToken(expr, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, arrayToken)
			i = next
		case ch == '(':
			tokens = append(tokens, token{kind: tokenLeftParen, text: "("})
			i++
		case ch == ')':
			tokens = append(tokens, token{kind: tokenRightParen, text: ")"})
			i++
		case ch == ',':
			tokens = append(tokens, token{kind: tokenComma, text: ","})
			i++
		default:
			if op, width := readOperator(expr[i:]); width > 0 {
				tokens = append(tokens, token{kind: tokenOperator, text: op})
				i += width
				continue
			}
			return nil, fmt.Errorf("unexpected character %q in condition", ch)
		}
	}
	return tokens, nil
}

// shuntingYard converts the flat token stream into reverse-polish notation while preserving function arity.
// shuntingYard 用于把扁平词法流转换成逆波兰序列，并保留函数参数个数。
func shuntingYard(tokens []token) ([]token, error) {
	output := make([]token, 0, len(tokens))
	stack := make([]token, 0, len(tokens))
	callFrames := make([]callFrame, 0, 4)
	var prev token
	prevSet := false

	for _, tok := range tokens {
		switch tok.kind {
		case tokenVariable, tokenInteger, tokenArray:
			output = append(output, tok)
			if len(callFrames) > 0 {
				callFrames[len(callFrames)-1].sawValue = true
			}
		case tokenFunction:
			stack = append(stack, tok)
		case tokenLeftParen:
			if prevSet && prev.kind == tokenFunction {
				callFrames = append(callFrames, callFrame{})
			}
			stack = append(stack, tok)
		case tokenComma:
			if len(callFrames) == 0 {
				return nil, errors.New("comma outside function call")
			}
			for len(stack) > 0 && stack[len(stack)-1].kind != tokenLeftParen {
				output = append(output, stack[len(stack)-1])
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				return nil, errors.New("comma without matching parenthesis")
			}
			frame := &callFrames[len(callFrames)-1]
			if !frame.sawValue {
				return nil, errors.New("empty function argument")
			}
			frame.argCount++
			frame.sawValue = false
		case tokenOperator:
			for len(stack) > 0 && stack[len(stack)-1].kind == tokenOperator {
				top := stack[len(stack)-1]
				if precedence(top.text) < precedence(tok.text) {
					break
				}
				output = append(output, top)
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, tok)
		case tokenRightParen:
			for len(stack) > 0 && stack[len(stack)-1].kind != tokenLeftParen {
				output = append(output, stack[len(stack)-1])
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				return nil, errors.New("unmatched closing parenthesis")
			}
			stack = stack[:len(stack)-1]
			if len(stack) > 0 && stack[len(stack)-1].kind == tokenFunction {
				fn := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if len(callFrames) == 0 {
					return nil, errors.New("missing function frame")
				}
				frame := callFrames[len(callFrames)-1]
				callFrames = callFrames[:len(callFrames)-1]
				if frame.sawValue {
					frame.argCount++
				}
				fn.argCount = frame.argCount
				output = append(output, fn)
				if len(callFrames) > 0 {
					callFrames[len(callFrames)-1].sawValue = true
				}
			} else if len(callFrames) > 0 {
				callFrames[len(callFrames)-1].sawValue = true
			}
		default:
			return nil, errors.New("unexpected token while parsing condition")
		}
		prev = tok
		prevSet = true
	}

	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].kind == tokenLeftParen || stack[i].kind == tokenRightParen {
			return nil, errors.New("unmatched parenthesis in condition")
		}
		output = append(output, stack[i])
	}
	return output, nil
}

// buildAST turns one reverse-polish program into a compact expression tree for opcode lowering.
// buildAST 用于把一段逆波兰程序转换成紧凑表达式树，以便进一步降级成指令。
func buildAST(rpn []token) (*astNode, error) {
	stack := make([]*astNode, 0, len(rpn))
	for _, tok := range rpn {
		switch tok.kind {
		case tokenVariable:
			stack = append(stack, &astNode{kind: astVariable, groupIndex: tok.groupIndex})
		case tokenInteger:
			stack = append(stack, &astNode{kind: astInteger, intValue: tok.intValue})
		case tokenArray:
			stack = append(stack, &astNode{kind: astArray, ints: tok.ints})
		case tokenFunction:
			spec, ok := builtinFunctions[tok.text]
			if !ok {
				return nil, fmt.Errorf("unknown function: %s", tok.text)
			}
			if tok.argCount != spec.arity {
				return nil, fmt.Errorf("function %s expects %d args, got %d", tok.text, spec.arity, tok.argCount)
			}
			if len(stack) < tok.argCount {
				return nil, fmt.Errorf("function %s is missing arguments", tok.text)
			}
			args := make([]*astNode, tok.argCount)
			copy(args, stack[len(stack)-tok.argCount:])
			stack = stack[:len(stack)-tok.argCount]
			stack = append(stack, &astNode{kind: astFunction, name: tok.text, args: args})
		case tokenOperator:
			if len(stack) < 2 {
				return nil, fmt.Errorf("operator %s is missing operands", tok.text)
			}
			right := stack[len(stack)-1]
			left := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, &astNode{kind: astBinary, name: tok.text, left: left, right: right})
		default:
			return nil, errors.New("unexpected token in ast builder")
		}
	}
	if len(stack) != 1 {
		return nil, errors.New("condition did not compile into a single expression")
	}
	return stack[0], nil
}

// compileNode lowers one expression tree into VM opcodes while emitting real short-circuit jumps.
// compileNode 用于把单棵表达式树降级成虚拟机指令，并生成真正的短路跳转。
func compileNode(node *astNode, prog *program) error {
	if node == nil {
		return errors.New("nil condition node")
	}
	switch node.kind {
	case astVariable:
		prog.ops = append(prog.ops, opCode{kind: opPushGroup, groupIndex: node.groupIndex})
	case astInteger:
		prog.ops = append(prog.ops, opCode{kind: opPushInt, intValue: node.intValue})
	case astArray:
		prog.ops = append(prog.ops, opCode{kind: opPushArray, ints: node.ints})
	case astFunction:
		for _, arg := range node.args {
			if err := compileNode(arg, prog); err != nil {
				return err
			}
		}
		switch node.name {
		case "weight_sum":
			prog.ops = append(prog.ops, opCode{kind: opCallWeightSum})
		case "cn_check":
			prog.ops = append(prog.ops, opCode{kind: opCallCNCheck})
		case "len":
			prog.ops = append(prog.ops, opCode{kind: opCallLen})
		case "is_luhn":
			prog.ops = append(prog.ops, opCode{kind: opCallIsLuhn})
		case "is_base64":
			prog.ops = append(prog.ops, opCode{kind: opCallIsBase64})
		default:
			return fmt.Errorf("unsupported function node: %s", node.name)
		}
	case astBinary:
		switch node.name {
		case "&&":
			if err := compileNode(node.left, prog); err != nil {
				return err
			}
			jumpPos := len(prog.ops)
			prog.ops = append(prog.ops, opCode{kind: opJumpIfFalse})
			prog.ops = append(prog.ops, opCode{kind: opPop})
			if err := compileNode(node.right, prog); err != nil {
				return err
			}
			prog.ops[jumpPos].jump = len(prog.ops)
		case "||":
			if err := compileNode(node.left, prog); err != nil {
				return err
			}
			jumpPos := len(prog.ops)
			prog.ops = append(prog.ops, opCode{kind: opJumpIfTrue})
			prog.ops = append(prog.ops, opCode{kind: opPop})
			if err := compileNode(node.right, prog); err != nil {
				return err
			}
			prog.ops[jumpPos].jump = len(prog.ops)
		default:
			if err := compileNode(node.left, prog); err != nil {
				return err
			}
			if err := compileNode(node.right, prog); err != nil {
				return err
			}
			switch node.name {
			case "%":
				prog.ops = append(prog.ops, opCode{kind: opModulo})
			case "==":
				prog.ops = append(prog.ops, opCode{kind: opEqual})
			case "!=":
				prog.ops = append(prog.ops, opCode{kind: opNotEqual})
			case ">":
				prog.ops = append(prog.ops, opCode{kind: opGreater})
			case ">=":
				prog.ops = append(prog.ops, opCode{kind: opGreaterOrEqual})
			case "<":
				prog.ops = append(prog.ops, opCode{kind: opLess})
			case "<=":
				prog.ops = append(prog.ops, opCode{kind: opLessOrEqual})
			default:
				return fmt.Errorf("unsupported operator node: %s", node.name)
			}
		}
	default:
		return errors.New("unknown condition node kind")
	}
	return nil
}

// callFrame tracks function argument parsing state during the shunting-yard pass.
// callFrame 用于在 shunting-yard 过程中跟踪函数参数的解析状态。
type callFrame struct {
	argCount int
	sawValue bool
}

// functionSpec describes one builtin function signature accepted by the compiler.
// functionSpec 用于描述编译器接受的一条内建函数签名。
type functionSpec struct {
	arity int
}

var builtinFunctions = map[string]functionSpec{
	"weight_sum": {arity: 2},
	"cn_check":   {arity: 1},
	"len":        {arity: 1},
	"is_luhn":    {arity: 1},
	"is_base64":  {arity: 1},
}

// isBuiltinFunction reports whether one identifier is an allowed builtin function.
// isBuiltinFunction 用于判断某个标识符是否为允许使用的内建函数。
func isBuiltinFunction(name string) bool {
	_, ok := builtinFunctions[name]
	return ok
}

// precedence returns the operator precedence used by the shunting-yard algorithm.
// precedence 用于返回 shunting-yard 算法所使用的操作符优先级。
func precedence(op string) int {
	switch op {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=", "<", "<=", ">", ">=":
		return 3
	case "%":
		return 4
	default:
		return 0
	}
}

// readOperator scans one supported operator and reports the token width.
// readOperator 用于扫描一条受支持的操作符，并返回其长度。
func readOperator(s string) (string, int) {
	if strings.HasPrefix(s, "&&") {
		return "&&", 2
	}
	if strings.HasPrefix(s, "||") {
		return "||", 2
	}
	if strings.HasPrefix(s, "==") {
		return "==", 2
	}
	if strings.HasPrefix(s, "!=") {
		return "!=", 2
	}
	if strings.HasPrefix(s, ">=") {
		return ">=", 2
	}
	if strings.HasPrefix(s, "<=") {
		return "<=", 2
	}
	switch {
	case strings.HasPrefix(s, "%"):
		return "%", 1
	case strings.HasPrefix(s, ">"):
		return ">", 1
	case strings.HasPrefix(s, "<"):
		return "<", 1
	default:
		return "", 0
	}
}

// readArrayToken parses one integer array literal such as [1,2,3].
// readArrayToken 用于解析形如 [1,2,3] 的整数数组字面量。
func readArrayToken(expr string, start int) (token, int, error) {
	i := start + 1
	values := make([]int, 0, 8)
	for {
		for i < len(expr) && isSpace(expr[i]) {
			i++
		}
		if i >= len(expr) {
			return token{}, 0, errors.New("unterminated array literal")
		}
		if expr[i] == ']' {
			i++
			break
		}
		valueStart := i
		for i < len(expr) && isDigit(expr[i]) {
			i++
		}
		if valueStart == i {
			return token{}, 0, errors.New("array literal expects integer values")
		}
		value, err := strconv.Atoi(expr[valueStart:i])
		if err != nil {
			return token{}, 0, err
		}
		values = append(values, value)
		for i < len(expr) && isSpace(expr[i]) {
			i++
		}
		if i >= len(expr) {
			return token{}, 0, errors.New("unterminated array literal")
		}
		if expr[i] == ',' {
			i++
			continue
		}
		if expr[i] == ']' {
			i++
			break
		}
		return token{}, 0, errors.New("array literal expects ',' or ']'")
	}
	if len(values) == 0 {
		return token{}, 0, errors.New("array literal must not be empty")
	}
	return token{kind: tokenArray, ints: values, text: expr[start:i]}, i, nil
}

// isSpace reports whether one byte is a supported ASCII whitespace character.
// isSpace 用于判断某个字节是否为受支持的 ASCII 空白字符。
func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

// isDigit reports whether one byte is an ASCII digit.
// isDigit 用于判断某个字节是否为 ASCII 数字字符。
func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

// isIdentifierStart reports whether one byte can start a builtin function name.
// isIdentifierStart 用于判断某个字节是否可以作为内建函数名的起始字符。
func isIdentifierStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

// isIdentifierPart reports whether one byte can continue a builtin function name.
// isIdentifierPart 用于判断某个字节是否可以继续内建函数名。
func isIdentifierPart(ch byte) bool {
	return isIdentifierStart(ch) || isDigit(ch)
}
