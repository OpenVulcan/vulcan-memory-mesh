# VMM PII Validator Configuration Guide

## Purpose

This document is for users who only need to configure PII rules and use the tester, without learning the internal implementation details.

If you want the internal architecture, VM, and developer-oriented details, read:

- [pii-validator-developer_EN.md](./pii-validator-developer_EN.md)

## Fixed Rule Directories

PII rule paths are fixed and are not configured by a path setting.

### System bundle location

- `SystemDir/pii_rules`

Typical packaged build location:

- `output/configs/pii_rules`

Typical `go run` development location:

- `configs/pii_rules`

### User bundle location

- `UserDir/pii_rules`

`UserDir` is resolved from:

- `~/.vmm` by default
- the directory passed to `-config`
- the parent directory of the `-config` file

## Required File Types

PII currently supports two bundle types:

- `common.json`
  - for non-language-specific rules
- `<lang>.json`
  - for locale-specific rules such as `zh-CN.json`

Example:

```text
configs/
  pii_rules/
    common.json
    zh-CN.json
```

```text
~/.vmm/
  pii_rules/
    common.json
    zh-CN.json
```

## Which Rules Belong Where

Put these in `common.json`:

- generic API keys
- bearer tokens
- AWS access key IDs
- Aliyun access key IDs
- Tencent secret IDs
- BTC WIF-like keys

Put these in language-specific files:

- national ID cards
- mobile phone numbers
- local tax IDs
- local account formats

## Selection and Override Rules

System and user bundles do not merge with each other.

The runtime chooses bundles like this:

1. choose one common bundle
   - user `common.json` if present
   - otherwise system `common.json`
2. choose one language bundle
   - user `<lang>.json` if present
   - otherwise system `<lang>.json`
3. build final rules
   - apply common rules first
   - then apply language rules

Conflict behavior:

- language rules override common rules with the same `name`
- different rule names are preserved

Important consequence:

- if you create `~/.vmm/pii_rules/common.json`, the system `common.json` is ignored
- if you create `~/.vmm/pii_rules/zh-CN.json`, the system `zh-CN.json` is ignored

This is intentional so users can fully control their own rule bundle.

## Rule File Format

Each rule file is JSON.

### Common example

```json
{
  "language": "common",
  "version": "1.0.0",
  "rules": [
    {
      "name": "OpenAI_Key",
      "pattern": "sk-[A-Za-z0-9]{20,}",
      "replacement": "[SK_MASKED]"
    },
    {
      "name": "Bearer_Token",
      "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})",
      "replacement": "Bearer [TOKEN_MASKED]"
    }
  ]
}
```

### Language example

```json
{
  "language": "zh-CN",
  "version": "1.1.0",
  "rules": [
    {
      "name": "CN_ID_PRO",
      "pattern": "\\b(\\d{17})([0-9Xx])\\b",
      "replacement": "[ID_VERIFIED]",
      "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)"
    },
    {
      "name": "Mobile",
      "pattern": "\\b1[3-9]\\d{9}\\b",
      "replacement": "[MOBILE_MASKED]"
    }
  ],
  "excludes": ["session_id", "request_id", "max_tokens"]
}
```

### Field meanings

- `language`
  - bundle key used at runtime
- `version`
  - optional bundle version
- `rules`
  - ordered rule list
- `name`
  - stable rule identity
- `pattern`
  - Go regexp string
- `replacement`
  - text used when the match is masked
- `condition`
  - optional extra validation expression
- `excludes`
  - reserved metadata for future use

## Configuration Fields

Relevant runtime config fields live in:

- [base.yaml](../configs/base.yaml)
- [config.yaml](../configs/config.yaml)

Minimal relevant configuration:

```yaml
pii:
  default_language: zh-CN
```

- `pii.default_language`
  - fallback language when the caller does not provide one or the requested language bundle is unavailable

Configuration override order:

1. system `configs/base.yaml`
2. system `configs/config.yaml`
3. user `~/.vmm/config.yaml` or the directory/file passed via `-config`
4. `.env` and process environment variables

## What the Rule System Can Do

From the configuration side, the current rule system can do all of the following:

- load one shared `common.json` bundle
- load one language-specific bundle such as `zh-CN.json`
- let user bundles fully replace system bundles
- combine the selected common bundle with the selected language bundle
- use regex for first-pass matching
- use `condition` for second-pass validation
- override common rules from the language bundle by rule name
- mask verified matches while keeping false positives unchanged
- provide safe production logs without leaking raw secrets
- support a standalone tester with verbose trace output
- support ad-hoc one-off rule testing without editing JSON files

This means the configuration files are not passive data only. They define the actual behavior of the runtime scrubber.

## Condition DSL Capabilities

The following functions are currently supported in `condition`:

- `weight_sum(s, weights[])`
- `cn_check(char)`
- `len(s)`
- `is_luhn(s)`
- `is_base64(s)`

Common variables:

- `$0` full match
- `$1`, `$2`, ... capture groups

Supported operators:

- `%`
- `==`
- `!=`
- `>`
- `>=`
- `<`
- `<=`
- `&&`
- `||`

Supported literals:

- integers such as `9`, `11`, `18`
- integer arrays such as `[7,9,10,5]`

### Typical examples

Mainland-China ID validation:

```text
(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)
```

Ad-hoc string length check:

```text
len($0) == 9
```

Base64 or Luhn gate:

```text
is_base64($0) || is_luhn($0)
```

## Startup Validation Rules

Every rule is validated before the app starts.

The runtime refuses to start when a bundle contains:

- unknown function names
- out-of-range capture references like `$9`
- malformed arrays
- unmatched parentheses
- invalid operators
- wrong function arity
- duplicate rule names inside one JSON file
- conditions that compile to more than 64 opcodes

This is important for rule authors because configuration mistakes fail fast instead of becoming silent runtime bugs.

## Rule Writing Tips

### Use `common.json` for shared secret formats

Good examples:

- `sk-...`
- `Bearer ...`
- cloud access key IDs
- wallet key formats

### Use language files for locale-specific formats

Good examples:

- Chinese ID cards
- Chinese mobile numbers
- local document numbers

### Add `condition` for risky numeric formats

Recommended for:

- ID cards
- bank cards
- long structured identifiers

### Use stable rule names

Rule names control override behavior.

If a replacement rule uses a new name, it is treated as a new rule, not an override.

### Use `condition` for risky numeric formats

Regex alone is often too broad for:

- ID cards
- bank-card-like values
- long structured identifiers

For those patterns, add a second-pass `condition` whenever possible.

### Understand the final layering model

There are two separate decisions:

1. which common bundle is selected
2. which language bundle is selected

After that, the selected language bundle can override same-name rules from the selected common bundle.

This is the only merge behavior.

### Use careful boundaries

Real pitfall observed in VMM:

- a mobile regex without boundaries matched an 11-digit substring inside an 18-digit identifier

Bad:

```json
{ "pattern": "1[3-9]\\d{9}" }
```

Safer:

```json
{ "pattern": "\\b1[3-9]\\d{9}\\b" }
```

This specific phone-number pitfall was seen during real VMM validation work, not only as a theoretical concern.

## Using the PII Tester

The standalone tester is:

- [main.go](../cmd/vmm-pii-tester/main.go)

### Build tester only

```powershell
.\make.ps1 tester
```

### Build all binaries

```powershell
.\make.ps1 build
```

### File mode

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

### Ad-hoc mode

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

Ad-hoc mode is useful when:

- you want to test one regex quickly
- you want to inspect `$0/$1/$2` capture results
- you want to validate one `condition` without editing JSON
- you want to reproduce a false positive in isolation

### Ad-hoc failure examples

Broken regex:

```powershell
go run ./cmd/vmm-pii-tester -text 'test' -pattern '[a-' -condition 'len($0) == 4'
```

Unknown DSL function:

```powershell
go run ./cmd/vmm-pii-tester -text 'test' -pattern '\d+' -condition 'unknown_func($0)'
```

### PowerShell note

Use single quotes when the expression contains `$0`, `$1`, and similar variables.

## How to Use the Validator

There are three practical ways to use the validator today.

### 1. File-based rule testing

Use this when you want to validate the currently installed `common.json` and language bundles.

Example:

```powershell
go run ./cmd/vmm-pii-tester -lang zh-CN -text '我的身份证是 11010519491231002X，手机号是 13812345678'
```

This mode shows:

- the original text
- which rule matched
- `$0/$1/$2/...`
- the VM result
- the final scrubbed text

### 2. Ad-hoc one-off rule testing

Use this when you want to test one rule immediately without editing JSON files.

Example:

```powershell
go run ./cmd/vmm-pii-tester -text '我的代号是 8888-9999' -pattern '\b\d{4}-\d{4}\b' -condition 'len($0) == 9' -replace '[SECRET]'
```

This mode is ideal for:

- drafting a new regex
- debugging capture groups
- validating one `condition`
- reproducing a false positive quickly

### 3. Programmatic usage

If you are integrating the validator in Go code, the main runtime entry is:

```go
engine, err := pii.NewEngine(systemDir, userDir, "zh-CN")
if err != nil {
    // handle error
}
scrubbed := engine.Scrub(text, lang)
```

Where:

- `text` is the raw string to scrub
- `lang` is a language tag such as `zh-CN`

For one-off in-memory testing, the tester uses:

```go
engine, err := pii.NewEngineWithRules("adhoc", logger, rules)
```

This is useful for internal tools, offline validation, or generated rule experiments.

Important note:

- The current gRPC mainline does not auto-wire this scrubber into existing service handlers.
- `vmm-pii-tester` and `internal/platform/pii` are the stable entry points for validation and rule development today.

## Example Local Override Workflow

### Replace system common rules locally

1. create `~/.vmm/pii_rules/common.json`
2. put your own common rules there
3. restart the app or tester

### Replace system Chinese rules locally

1. create `~/.vmm/pii_rules/zh-CN.json`
2. put your own Chinese rules there
3. restart the app or tester

## Validation Checklist

Before shipping a new rule file:

- regex matches only intended samples
- false positives were tested
- rule names are unique inside the file
- replacement text is explicit
- `condition` uses valid capture groups
- common rules are in `common.json`
- locale rules are in the right language file
- you understand that user bundles replace system bundles

Also verify:

- any risky numeric pattern has a second-pass `condition`
- rule order is intentional
- no system bundle is accidentally expected to survive once a user bundle exists
- the tester has been used on both positive and negative samples

## Current Limitations

- `excludes` does not yet actively filter fields
- no user-defined functions
- no string concatenation in DSL
- overrides depend on stable rule names

## Safe Logging and Production Behavior

Production logs do not print raw secrets.

The runtime logs only safe metadata such as:

- `rule`
- `result`
- `match_len`
- `match_hash`
- `reason`

If you need raw match and capture-group visibility, use `vmm-pii-tester` instead of production logs.
