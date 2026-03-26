# vmm-token-estimator

一个纯 Go、无词表、无 CGO 的 Token 启发式估算器，适合在 AI Agent / gRPC Gateway / Prompt Budgeting 场景中做**上下文窗口预估与拦截**。

## 设计目标

- **O(N)** 复杂度，按 UTF-8 rune 扫描。
- **纯 Go 内存实现**，不引入 `tiktoken` / `sentencepiece` / CGO。
- **线程安全**，`Estimator` 初始化后只读，可直接复用到高并发服务。
- **可调系数**，通过不同 `Config` 适配海外模型与国产模型。
- **更贴近真实成本**，重点修复“英文按词算、中文按字算”的估算偏差。

## 核心思路

1. **字符分类**
   - CJK：Han / Hiragana / Katakana / Hangul
   - Latin letters
   - Numbers
   - Punctuation & Symbols
   - Whitespaces
   - 额外提供 `OtherLetterWeight`，兜底处理非 Latin / 非 CJK 的其它字母脚本

2. **英文按词估算**
   连续 Latin 字母不会按字符逐个累加，而是按**连续 Latin 词段**计算：

   ```text
   wordCost = LatinWordWeight
            + max(0, letters-LatinShortWordThreshold)/LatinCharsPerExtraToken
            + caseTransitions * LatinCaseBoundaryWeight
   ```

   因此：
   - `hello world` 更接近按 2 个词估算，而不是按 10 个字母估算。
   - `tokenEstimatorHTTPServer` 这类 camelCase / PascalCase 标识符会因为大小写跳变获得额外成本，更适合代码场景。

3. **全局安全边际**

   ```text
   estimated = ceil(rawEstimate * SafeMargin)
   ```

   这样可优先保证**宁多勿少**，避免上下文实际超窗。

## 目录结构

```text
vmm-token-estimator/
├── go.mod
├── README.md
└── pkg/
    └── token_estimator/
        ├── estimator.go
        └── estimator_test.go
```

## 快速使用

```go
package main

import (
    "fmt"
    token "vmm-token-estimator/pkg/token_estimator"
)

func main() {
    estimator := token.NewEstimator(token.OverseasModelConfig)
    text := "你好，world 2026!"
    budget := estimator.Estimate(text)

    report := estimator.EstimateDetailed(text)
    fmt.Println("budget:", budget)
    fmt.Printf("buckets: %+v\n", report.Buckets)
    fmt.Printf("contrib: %+v\n", report.Contributions)
}
```

## 预置配置

### OverseasModelConfig
适合模拟“中文更贵、英文更便宜”的模型。

### DomesticModelConfig
适合模拟“中文更便宜、英文正常”的模型。

你也可以自行定义：

```go
cfg := token.Config{
    CJKWeight:               2.10,
    LatinWordWeight:         0.90,
    LatinShortWordThreshold: 6,
    LatinCharsPerExtraToken: 4.0,
    LatinCaseBoundaryWeight: 0.12,
    NumberWeight:            0.55,
    SymbolWeight:            0.90,
    WhitespaceWeight:        0.08,
    OtherLetterWeight:       1.20,
    SafeMargin:              1.08,
    MinimumTokens:           1,
}
```

## 单测与基准

执行测试：

```bash
go test ./...
```

执行基准：

```bash
go test -bench=. -benchmem ./pkg/token_estimator
```

本次容器内一次基准结果：

```text
BenchmarkEstimatorEstimate/overseas/english_long-56    194845   6215 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/overseas/cjk_long-56        667075   1582 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/overseas/mixed_cn_en-56     474639   2505 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/overseas/dense_json-56      476803   2564 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/domestic/mixed_cn_en-56     441846   2521 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/domestic/dense_json-56      465270   2569 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/domestic/english_long-56    199314   6125 ns/op   0 B/op   0 allocs/op
BenchmarkEstimatorEstimate/domestic/cjk_long-56        845294   1560 ns/op   0 B/op   0 allocs/op
```

## 调参建议

- **中文总是偏低**：增大 `CJKWeight` 或 `SafeMargin`
- **英文长词偏低**：减小 `LatinCharsPerExtraToken`
- **代码片段偏低**：增大 `SymbolWeight`
- **camelCase / 标识符偏低**：增大 `LatinCaseBoundaryWeight`
- **数字串偏高或偏低**：调整 `NumberWeight`

## 线程安全说明

`Estimator` 在 `NewEstimator` 时会对配置做归一化，并将其作为只读值保存。`Estimate` / `EstimateDetailed` 只使用栈上局部变量，不修改共享状态，因此适合多 goroutine 直接并发调用。
