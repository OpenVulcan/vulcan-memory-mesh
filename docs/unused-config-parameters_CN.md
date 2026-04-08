# 当前未接入主运行时的配置参数清单（中文）

## 文档目标

这份文档专门记录当前仓库里“已经出现在主配置里，但还没有真正进入主运行时业务链”的参数，方便后续统一决定：

- 保留并继续接线
- 改名
- 下沉为测试专用参数
- 直接删除

## 统计范围

这份清单只覆盖 `internal/config.Config` 这套主配置。

这里的“主运行时”特指：

- `cmd/vmm-local/main.go`
- `internal/app/app.go`
- 正常 gRPC 启动链路

不包含：

- `internal/testutil/realruntime.go` 这类测试基座
- 仅配置归一化、默认值补齐、启动时校验用到的参数
- 未来规划但当前还没接线的处理器

## 判定说明

本文把当前参数分成 3 类：

- 主运行时未消费
  - 正常服务启动后，不会在业务链里读取它
- 仅测试基座使用
  - 正常服务不用，但测试辅助代码会读取
- 仅配置桥接或启动校验使用
  - 只在 `Normalize()` / `Validate()` 这类配置阶段生效，不进入业务执行

## 清单

| 参数 | 当前状态 | 当前唯一用途 | 备注 |
| --- | --- | --- | --- |
| `post_action.input_mode` | 仅配置桥接或启动校验使用 | 只在配置加载阶段做默认值归一化和合法值校验 | `compat` / `strict` 当前没有运行时差异 |
| `pre_check.intent_timeout` | 主运行时已消费 | 第一层 `precheck_l1_main` 的内部超时预算 | 仍会在启动时与 `grpc.request_timeout.pre_check` 做大小关系校验 |
| `pre_check.top_k` | 主运行时已消费 | 控制每个 pre-check 检索语句的向量召回数量 | 直接进入实时 pre-check 工作流 |
| `pre_check.similarity_threshold` | 仅配置桥接或启动校验使用 | 当 `memory_pipeline.min_similarity_score` 为空时，在 `Normalize()` 中回填过去 | 运行时实际读取的是 `memory_pipeline.min_similarity_score` |
| `memory_pipeline.max_search_keywords` | 主运行时已消费 | 限制第一层意图提取最多返回多少条检索语句 | 已进入实时 pre-check 工作流 |
| `memory_pipeline.min_similarity_score` | 主运行时已消费 | 控制 pre-check 过滤低相似度记忆候选的阈值 | 已进入实时 pre-check 工作流 |

## 关联代码位置

如果后续要继续清理或接线，优先从这些位置开始看：

- `internal/config/config.go`
  - 参数声明、默认值、环境变量映射、合法性校验
- `internal/app/app.go`
  - 当前主运行时真正装配了哪些依赖
- `internal/app/usecase/precheck.go`
  - 当前 `PreCheck` 如何执行两层召回与采纳

## 当前结论

按 2026-04-06 的主线状态看，最需要后续统一决策的一组参数是：

- `post_action.input_mode`

其中：

- `pre_check.*`、`memory_pipeline.*` 与 `pii.default_language` 已进入主运行时，不再属于“未接入”参数
