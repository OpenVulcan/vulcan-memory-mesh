# 任务目标

修复独立维护工具 `vmm-migrate` 在执行向量重建等长耗时破坏性操作时缺少可取消上下文的问题，确保操作者通过 `Ctrl+C` 或终止信号中断命令时，业务逻辑能够感知取消并进入既有的回滚或有序退出路径。

# 执行步骤

1. 复核当前 `vmm-migrate` 入口的上下文来源与信号处理方式，确认 `main` 是否始终把不可取消的 `context.Background()` 传入执行链路。
2. 在 `cmd/vmm-migrate/main.go` 中接入基于系统信号的可取消上下文，让 `run` 与其下游维护流程共享同一取消源。
3. 保持现有命令行参数语义与输出格式不变，只补充中断感知，不扩大到其余维护逻辑或数据库超时策略。
4. 为新增行为补充或调整测试，覆盖“信号上下文可取消地传入执行链路”的关键路径。
5. 复核用户提出的另外两条审查意见，明确说明 `30` 秒在不同位置的实际语义，避免误改已经符合预期的预算等待与超时策略。

# 技术选型

- 采用 Go 标准库 `os/signal` 与 `signal.NotifyContext` 生成可取消上下文。
- 保持取消信号范围聚焦在 CLI 入口层，不修改 `RunVectorRebuild` 既有回滚编排。
- 测试优先使用可控上下文与轻量断言，避免引入不稳定的真实信号依赖。

# 验收标准

1. `vmm-migrate` 入口不再直接把裸 `context.Background()` 传入 `run`。
2. 当维护命令执行期间收到中断信号时，下游流程能够收到取消上下文。
3. 不修改第 2、3 条审查意见对应的数据库超时与预算等待逻辑，且能给出准确解释。
4. 相关定向测试通过，现有核心测试不因本次修改回归。

---

# 执行变更总结

## 1. 核心修复与调整概述

- 已在 `vmm-migrate` 入口补充基于系统信号的可取消上下文，不再把裸 `context.Background()` 直接传入维护执行链路。
- 当操作者通过 `Ctrl+C` 或终止信号中断维护命令时，下游向量重建流程现在可以收到上下文取消，从而进入既有的回滚或有序退出路径。
- 已复核另外两条审查意见中的 `30` 秒语义，并确认本次不修改对应超时与预算等待逻辑。

## 2. 📂文件变更清单

- 新增：`cmd/vmm-migrate/main_test.go`
- 修改：`cmd/vmm-migrate/main.go`
- 新增：`docs/plan/20260407-07-MAINTENANCE_CLI_SIGNAL_CANCELLATION.md`

## 3. 💻关键代码调整详情

- 在 `cmd/vmm-migrate/main.go` 中新增 `buildSignalAwareMainContext`，使用 `signal.NotifyContext` 绑定 `os.Interrupt` 与 `SIGTERM`。
- `main` 入口改为先创建信号感知上下文，再把该上下文传入 `run`，确保维护子流程共享同一取消源。
- 在 `cmd/vmm-migrate/main_test.go` 中补充基础测试，验证入口上下文具备可取消能力。
- 复核结果：
  - `internal/adapters/outbound/vldb_postgres/vector_dimension_migration.go` 里的 `30` 秒来自 `bootstrapContext`，表示 PostgreSQL 维度迁移事务的总上下文上限，不是 embedding 预算耗尽后的等待重试。
  - embedding 预算耗尽后的 `30` 秒等待位于 `internal/app/vector_rebuild.go` 的 `vectorRebuildBudgetRetryDelay`，两者职责不同。
  - `internal/adapters/outbound/vldb_postgres/memory_store.go` 的 `ListProjectMemories` 当前仍走普通查询超时，默认并不是固定 `30` 秒，而是跟随 `postgres.query_timeout`，当前仓库默认值仍是 `5s`。

## 4. ⚠️遗留问题与注意事项

- 本次只按要求修复第 1 条审查意见，没有调整 PostgreSQL 维护查询或维度迁移的超时策略。
- 如果后续决定把维护读取链路统一抬到固定 `30` 秒，建议仅对维护路径单独加超时地板，避免影响在线请求的普通查询超时语义。
- 当前只执行了 `go test ./cmd/vmm-migrate` 定向验证，未额外重跑全量测试。
