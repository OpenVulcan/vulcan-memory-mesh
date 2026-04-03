# PreCheck Scope Config Plan

## 任务目标 / Goal

- 为 `PreCheck` 增加可配置的长期记忆检索作用域。
- 配置项支持：`team / space / project`，默认值为 `space`。
- 保持现有统一检索链路、排序链路和日志链路不被破坏。
- 明确记录本次配置对 `PreCheck` 检索过滤条件的实际影响，并补齐测试与文档。

## 执行步骤 / Steps

1. 审计当前 `PreCheck -> MemoryUseCase.Search(...)` 的 scope 过滤构造位置，确认现状确为 `team + space + project`。
   - 结果：确认当前统一检索在 [memory_query.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/memory_query.go) 中固定构造 `team + space + project + user` 过滤。
2. 设计并实现新的配置项，使 `PreCheck` 可按 `team / space / project` 三种作用域控制长期记忆检索边界，默认 `space`。
   - 已在 [config.go](D:/projects/VulcanMemoryMesh/internal/config/config.go) 为 `pre_check.search_scope` 增加配置、默认值、环境变量覆盖与合法性校验。
   - 已在 [precheck.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/precheck.go) 和 [precheck_scope.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/precheck_scope.go) 增加 pre-check 作用域透传与过滤构造逻辑。
3. 确保该配置只影响 `PreCheck` 这条链路，不误伤其他通用 `MemoryQuery` 场景。
   - 已在 [memory_query.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/memory_query.go) 增加可选 `ScopeOverride`。
   - 结果：`ScopeOverride=""` 时，通用 `MemoryQuery` 仍保持默认项目级过滤；只有 `PreCheck` 会把 `space/team/project` 显式传进去。
4. 补充回归测试，覆盖：
   - 默认值为 `space`
   - `team / space / project` 三种过滤效果
   - 配置非法值时的归一或校验行为
   - 已新增：
     - [config_test.go](D:/projects/VulcanMemoryMesh/internal/config/config_test.go)
     - [memory_query_test.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/memory_query_test.go)
     - [precheck_test.go](D:/projects/VulcanMemoryMesh/internal/app/usecase/precheck_test.go)
5. 同步更新文档与示例配置。
   - 已更新：
     - [README.md](D:/projects/VulcanMemoryMesh/README.md)
     - [grpc-integration-guide_CN.md](D:/projects/VulcanMemoryMesh/docs/grpc-integration-guide_CN.md)
   - 说明：`configs/.env.example` 与 `configs/openai.local.example.json` 当前工作区已有未提交改动，本次未继续改写，以避免混入无关变更；文档中已给出新增配置键名与取值说明。
6. 对照计划完成自检，并将计划文件改名为 `COMPLETED_` 前缀。
   - 已完成。

## 验收标准 / Acceptance Criteria

- `PreCheck` 的检索作用域可通过配置显式控制。
- 默认行为切换为 `space`。
- `team / space / project` 都有测试覆盖。
- 已通过：
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config ./internal/platform/logx -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 实际结论 / Actual Findings

- 问题真实存在：
  - 是。`PreCheck` 之前没有独立作用域配置，固定使用项目级过滤。
- 实际修改：
  - 新增 `pre_check.search_scope`
  - 支持 `team / space / project`
  - 默认值改为 `space`
  - 新增环境变量：`VMM_PRE_CHECK_SEARCH_SCOPE`
- 生效语义：
  - `team`：保留 `team_id`，去掉 `space_id / project_id`
  - `space`：保留 `team_id + space_id`，去掉 `project_id`
  - `project`：保留 `team_id + space_id + project_id`
- 影响范围：
  - 只影响 `PreCheck`
  - 不影响普通 `SearchMemoryEvents` / `MemoryQuery` 的默认项目级过滤行为
