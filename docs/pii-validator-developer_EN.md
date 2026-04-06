# VMM PII Validator Developer Guide

## Purpose

This document is for developers, contributors, and AI assistants who need to understand how the VMM PII validator works internally.

It focuses on:

- runtime architecture
- rule compilation
- the condition DSL
- override behavior
- debug tooling
- implementation pitfalls

If you only want to know where to place files or how to write JSON bundles, read:

- [pii-validator-config_EN.md](./pii-validator-config_EN.md)

## Core Architecture

The validator has three core runtime parts:

1. Rule loader and compiler

- [engine.go](../internal/platform/pii/engine.go)
- [evaluator.go](../internal/platform/pii/evaluator.go)
- Responsibilities:
  - load JSON bundles
  - compile regexes
  - compile `condition` expressions into opcodes
  - reject invalid rules at startup

2. Atomic validation primitives

- [validators.go](../internal/platform/pii/validators.go)
- Responsibilities:
  - checksum validation
  - weighted numeric validation
  - strict format validation

3. Runtime scrub engine

- [engine.go](../internal/platform/pii/engine.go)
- Responsibilities:
  - scan with `FindAllStringSubmatchIndex`
  - evaluate compiled conditions
  - replace only accepted matches

## Runtime Entry Points

The main application-facing runtime entry is:

```go
Scrub(text string, lang string) string
```

In the current mainline repository, this capability is preserved primarily as a reusable platform component plus a standalone tester:

- platform implementation:
  - [engine.go](../internal/platform/pii/engine.go)
- standalone debug entry:
  - [main.go](../cmd/vmm-pii-tester/main.go)

The standalone local debug tool is:

- [main.go](../cmd/vmm-pii-tester/main.go)

Important note:

- The current gRPC mainline does not auto-wire this scrubber into existing service handlers.
- If we reintroduce a runtime integration later, README, flow documentation, and test guidance must be updated together so offline validation capabilities are not mistaken for live production wiring.

## Rule Selection Model

Rule selection is intentionally simple and explicit.

There are two bundle types:

- `common.json`
- one language file such as `zh-CN.json`

System and user bundles are not merged with each other.

Selection order:

1. pick one common bundle
   - use user `common.json` if present
   - otherwise use system `common.json`
2. pick one language bundle
   - use user `<lang>.json` if present
   - otherwise use system `<lang>.json`
3. build the final runtime rules
   - apply selected common rules
   - then apply selected language rules

Conflict rule:

- language rules override common rules by `name`
- non-conflicting rules are appended

This means:

- users can completely replace the system common bundle
- users can completely replace the system language bundle
- only the final selected common bundle and final selected language bundle are combined

## Condition DSL

The `condition` field is compiled at startup into a compact opcode program.

No external expression engine is used.

### Supported variables

- `$0`
  - full regex match
- `$1`, `$2`, ...
  - capture groups

### Supported operators

- `%`
- `==`
- `!=`
- `>`
- `>=`
- `<`
- `<=`
- `&&`
- `||`

### Supported literals

- integers such as `9`, `11`, `18`
- integer arrays such as `[7,9,10,5]`

### Built-in functions

- `weight_sum(s, weights[])`
- `cn_check(char)`
- `len(s)`
- `is_luhn(s)`
- `is_base64(s)`

### Examples

Mainland-China ID validation:

```text
(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)
```

Ad-hoc tester example:

```text
len($0) == 9
```

## Compile-Time Safety Checks

Every rule is compiled and validated before the app starts.

Rejected cases include:

- unknown functions
- out-of-range `$n` capture references
- malformed arrays
- unmatched parentheses
- invalid operators
- wrong function arity
- duplicate rule names inside one JSON file
- opcode count greater than 64

This keeps runtime execution fast and deterministic.

## Runtime Safety Model

The evaluator is fail-safe.

If evaluation fails unexpectedly, the engine masks the match instead of letting sensitive data through.

Reason codes:

- `EVAL_OK`
- `COND_FALSE`
- `PANIC_MASKED`
- `STEP_LIMIT_MASKED`
- `BAD_GROUP_REF_MASKED`

## Logging Model

Production evaluator logs must never contain raw matched text.

Logged fields:

- `rule`
- `result`
- `match_len`
- `match_hash`
- `reason`

`match_hash` is the first 12 hex characters of the SHA256 hash of the matched text.

## Debug Mode

`Engine.DebugMode` exists only for local debugging and rule development.

Default:

- `DebugMode == false`

That means production code stays silent and does not leak raw matches.

When `DebugMode == true`, the engine prints a detailed trace for each match:

- rule name
- full match
- `$0`
- `$1/$2/...`
- VM result
- final action

This is used by:

- [vmm-pii-tester](../cmd/vmm-pii-tester/main.go)

### File mode

Loads the fixed system/user rule directories and evaluates real bundles.

Example:

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

### Ad-hoc mode

Compiles one in-memory rule without touching JSON files.

Example:

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

This mode is ideal for:

- testing a new regex quickly
- validating one DSL condition
- checking capture groups
- reproducing false positives without editing bundle files

### PowerShell warning

Use single quotes for expressions containing `$0`, `$1`, and so on.

Correct:

```powershell
-condition 'len($0) == 9'
```

Wrong:

```powershell
-condition "len($0) == 9"
```

PowerShell will expand `$0` inside double quotes before the tester sees it.

## Performance Model

The engine does not use `ReplaceAllStringFunc`.

Instead it uses:

1. `FindAllStringSubmatchIndex`
2. one `strings.Builder`
3. manual reconstruction of the output string

Benefits:

- zero repeated regex execution
- direct access to capture groups
- predictable runtime behavior

The evaluator uses jump instructions for true short-circuit handling of `&&` and `||`.

The implementation intentionally does not pool:

- regex match result slices
- `strings.Builder`

## Common Pitfalls

### 1. Using `$0` for digit-only functions

Bad:

```text
weight_sum($0, [...])
```

If `$0` contains checksum characters like `X`, this is invalid.

Use:

```text
weight_sum($1, [...]) == cn_check($2)
```

### 2. Mobile regex false positives inside larger numeric strings

VMM hit a real case where a phone-number regex matched the middle 11 digits of an 18-digit identifier.

Bad:

```json
{ "pattern": "1[3-9]\\d{9}" }
```

Safer:

```json
{ "pattern": "\\b1[3-9]\\d{9}\\b" }
```

Always test:

- true positives
- near misses
- enclosing larger strings
- mixed punctuation and Unicode

### 3. Assuming `excludes` already works as field-level filtering

It does not.

`excludes` currently exists as metadata only.

### 4. Assuming user bundles merge with system bundles

They do not.

If a user `common.json` exists, system `common.json` is ignored.

If a user `zh-CN.json` exists, system `zh-CN.json` is ignored.

Only the selected common bundle and selected language bundle are combined.

## Test Coverage

Current tests cover:

- atomic validator functions
- compile-time DSL rejection
- true short-circuit VM behavior
- valid and invalid China ID cases
- safe logging
- deadlock-safe concurrent scrub and reload
- common-rule loading
- language-overrides-common behavior
- user-common replacing system-common
- user-language replacing system-language
- duplicate rule-name rejection
- debug trace output
- in-memory ad-hoc rule injection
- ad-hoc compile failure handling

Files:

- [validators_test.go](../internal/platform/pii/validators_test.go)
- [evaluator_test.go](../internal/platform/pii/evaluator_test.go)
- [engine_test.go](../internal/platform/pii/engine_test.go)

## Contributor Checklist

Before submitting changes:

- regex matches only the intended shape
- `condition` uses valid capture groups
- replacement text is explicit and safe
- no raw secrets are added to production logs
- negative samples are covered
- rule names are stable and unique
- common rules are placed in `common.json`
- locale-specific rules are placed in the correct language file
- override semantics are understood

If AI generates a rule, manually review:

- boundaries
- capture groups
- digit-only assumptions
- replacement wording
- false-positive samples
- whether the rule belongs in `common.json` or a language file

## Current Limitations

- no user-defined functions
- no string concatenation in DSL
- no active field-aware `excludes` enforcement yet
- override still depends on stable rule names

These limits are intentional to keep runtime small, predictable, and safe.
