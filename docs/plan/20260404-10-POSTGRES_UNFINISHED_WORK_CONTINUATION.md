# PostgreSQL 组合库未完成项续作计划

## 1. 任务目标

在当前已完成 PostgreSQL 组合库基础骨架、配置接入、方言路由与最小统一记忆读写能力的基础上：

1. 先整理并提交当前所有应纳入版本控制的代码与文档变更
2. 分析后续多轮评审产生的计划与完成记录，识别仍未关闭的缺口
3. 继续实现当前任务中尚未完成、且已具备明确设计输入的关键链路
4. 保持 `split` 模式零回归，不引入破坏现有行为的临时兼容分支

## 2. 当前已知背景

- 已存在 PostgreSQL Dialect Pattern 总体方案计划
- 当前仓库已新增 `internal/adapters/outbound/vldb_postgres`
- 当前已支持：
  - `storage.mode=combined`
  - `storage.combined_provider=postgres`
  - `postgres.flavor=paradedb|standard`
  - 共享 schema 初始化
  - 基础向量检索
  - 基础 lexical 检索
  - 统一记忆最小读写
  - 基础 scope/user/project 解析

## 3. 本轮执行步骤

### 3.1 提交前整理

1. 检查工作区状态
2. 清理 `.gocache`、`.gomodcache` 等不应提交的临时产物
3. 确认需要提交的代码与文档范围

### 3.2 评审结果分析

1. 阅读本轮之后新增的 `docs/completed/20260404-05` 到 `20260404-09` 文档
2. 归纳这些评审和修复记录中已经关闭的问题
3. 识别仍然未关闭、且会阻塞组合库继续推进的缺口

### 3.3 当前代码提交

1. 将当前应纳入版本控制的代码与文档加入暂存区
2. 生成一次清晰的阶段性提交
3. 保留后续实现空间，不在本次提交中夹带缓存或无关文件

### 3.4 未完成项续作

基于评审分析结果，优先补齐以下尚未完成且影响主链路的部分：

1. PostgreSQL 组合库仍未实现的关键关系工作流
2. 直接阻塞运行时使用的核心端口实现
3. 与当前架构计划不一致的缺失或半实现行为

优先级原则：

- 先主链路
- 先运行时阻塞项
- 先已有清晰设计输入的部分

### 3.5 验证

至少执行：

1. `go test ./internal/adapters/outbound/vldb_postgres ./internal/config ./internal/app ./internal/app/usecase`
2. `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`
3. `go test ./...`

## 4. 技术策略

### 4.1 提交策略

- 提交当前真实代码成果
- 不提交缓存目录
- 文档状态与代码状态保持一致

### 4.2 续作策略

- 严格沿用统一 `postgres` 适配器 + `flavor` 路由设计
- 不回退到双存储兼容式查询
- 不重新引入 `vector_json` 目标库存储
- 尽量优先实现共享层逻辑，再在必要点做 flavor 分叉

## 5. 验收标准

### 5.1 提交验收

- 当前代码已形成一次干净提交
- 无缓存目录进入版本控制

### 5.2 分析验收

- 能明确说明多轮评审后剩余未完成项
- 能区分“已完成修复”和“仍待实现能力”

### 5.3 实现验收

- 至少关闭一批当前仍未完成的核心 PostgreSQL 组合库阻塞项
- 所有新增改动通过相关测试
- 计划文档与实际状态保持同步
