# Log Directory File Output Plan

## 任务目标 / Goal

- 为当前运行时增加文件日志落盘能力。
- Make runtime logs persist to a `logs` directory in addition to the current console output.
- 日志目录按年月日分组，日志文件按小时滚动命名。
- Group log files by day and name each file by concrete year-month-day-hour.
- 保持现有 stdout 输出不丢失，同时确保标准打包产物路径下能稳定写日志。

## 执行步骤 / Steps

1. 审查当前日志初始化位置、输出 writer、以及运行时目录推导规则。
2. 设计日志目录和文件命名规则，并对齐标准打包结构。
3. 在日志层实现同时输出到 stdout 和文件。
4. 确保日志目录自动创建，且文件按日期/小时落盘。
5. 补充测试和文档说明。
6. 运行规定测试与构建。
7. 完成后重命名计划文件并提交。

## 技术原则 / Technical Principles

- 默认行为应保持兼容，现有 stdout 日志仍然保留。
- 文件日志路径必须可预测，且与标准运行产物结构一致。
- 目录与文件命名必须稳定，便于排障与归档。
- 不引入额外 SaaS/远程依赖。

## 验收标准 / Acceptance Criteria

- 运行时日志会同步输出到 `logs/<YYYYMMDD>/<YYYYMMDDHH>.log`。
- 标准打包运行路径下无需手工建目录即可正常落盘。
- 现有 stdout 日志仍然存在。
- 测试、文档、构建验证全部完成。
- 计划文件完成后改名为 `COMPLETED_` 前缀。

## 实际结果 / Actual Outcome

- 已新增运行时文件日志写入器 `HourlyFileWriter`，按本地时间写入：
  - `logs/<YYYYMMDD>/<YYYYMMDDHH>.log`
- 本地运行时现在会同时输出到：
  - stdout
  - 小时分片日志文件
- 日志根目录解析规则已落地：
  - 标准打包产物：`output/logs/`
  - 仓库内直接调试：仓库根 `logs/`
- 已补充初始化失败清理逻辑：
  - 如果日志文件已打开、但后续依赖装配失败，会主动关闭文件句柄，避免资源泄漏
- 已同步更新文档：
  - `README.md`
  - `docs/post-action-guide_CN.md`

## 验证结果 / Verification

- 已通过：
  - `go test ./internal/platform/logx ./internal/app -count=1`
  - `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config -count=1`
  - `go test ./... -count=1`
  - `.\make.ps1 build`

## 验收对照 / Acceptance Check

- `logs/<YYYYMMDD>/<YYYYMMDDHH>.log`：已实现
- 标准打包路径自动创建目录：已实现
- stdout 输出保留：已实现
- 测试、文档、构建验证：已完成
- 计划文件改名为 `COMPLETED_`：执行中
