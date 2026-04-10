// validators.go implements atomic validation primitives used by the PII evaluator VM.
// validators.go 用于实现 PII 计算虚拟机复用的原子校验函数。
package pii

import (
	"encoding/base64"
	"strings"
)

var cnCheckRemainderMap = map[byte]int{
	'1': 0,
	'0': 1,
	'X': 2,
	'x': 2,
	'9': 3,
	'8': 4,
	'7': 5,
	'6': 6,
	'5': 7,
	'4': 8,
	'3': 9,
	'2': 10,
}

// LuhnSum calculates the Luhn checksum sum and reports whether the input is pure digits.
// LuhnSum 用于计算 Luhn 校验和，并返回输入是否为纯数字。
func LuhnSum(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	sum := 0
	double := false
	for i := len(s) - 1; i >= 0; i-- {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		digit := int(ch - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum, true
}

// WeightSum multiplies each digit by its matching weight and rejects length mismatches or non-digits.
// WeightSum 用于将每一位数字与对应权重相乘求和，并拒绝长度不匹配或非数字输入。
func WeightSum(s string, weights []int) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) != len(weights) {
		return 0, false
	}
	sum := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		sum += int(ch-'0') * weights[i]
	}
	return sum, true
}

// CNCheck converts one mainland-China ID checksum character into its remainder index.
// CNCheck 用于把大陆身份证校验位字符转换成对应的余数索引。
func CNCheck(s string) (int, bool) {
	if len(s) != 1 {
		return 0, false
	}
	value, ok := cnCheckRemainderMap[s[0]]
	return value, ok
}

// IsLuhn reports whether a pure-digit string satisfies the Luhn checksum rule.
// IsLuhn 用于判断纯数字字符串是否满足 Luhn 校验规则。
func IsLuhn(s string) bool {
	sum, ok := LuhnSum(s)
	return ok && sum%10 == 0
}

// IsBase64 performs a strict Base64 validation including alphabet, length, padding, and decode checks.
// IsBase64 用于执行严格 Base64 校验，包括字符集、长度、填充和解码验证。
func IsBase64(s string) bool {
	if s == "" || len(s)%4 != 0 {
		return false
	}
	paddingStarted := false
	paddingCount := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '+' || ch == '/':
			if paddingStarted {
				return false
			}
		case ch == '=':
			paddingStarted = true
			paddingCount++
			if paddingCount > 2 {
				return false
			}
		default:
			return false
		}
	}
	if paddingStarted {
		if !(strings.HasSuffix(s, "=") || strings.HasSuffix(s, "==")) {
			return false
		}
	}
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}
