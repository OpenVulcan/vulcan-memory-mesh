# 循环审核与自动修复记录（2026-09-26）

## 完成条件

在已有全仓库审计修复基础上，逐轮检查改动、真实调用链、边界和回归证据。发现可确认问题即修复，并将连续无问题轮数清零；达到连续五轮未发现新增可确认问题后结束。轮次是人工源码审核结合针对性验证，不把重复运行同一测试当作独立审核。

## 轮次记录

| 轮次 | 核查范围与证据 | 发现及处理 | 连续无问题轮数 |
| --- | --- | --- | ---: |
| 1 | 配置加载、时长解码、路由覆盖；先运行失败回归，再运行配置、命令及应用测试，531 项通过 | 数值毫秒乘法溢出；LLM/rerank 路由元素继承旧端点、密钥和参数。增加溢出边界校验，显式路由数组整体替换；修正四处依赖隐式参数继承的测试配置 | 0 |
| 2 | SQLite 写入、提交不明对账、向量回滚、回收恢复、LanceDB 行解码；相关三个包 489 项测试通过 | 统一存储接口及 SQLite 注释错误承诺原子提交。按实际逐语句提交与部分失败契约纠正注释和原修复记录，保留必要补偿 | 0 |
| 3 | 继续核查所有 AI 凭据数组的覆盖来源；新回归先失败，修复后配置、命令及应用测试 534 项通过 | embedding 节点数组也会继承旧节点密钥和配额。显式节点列表整体替换；缺少自身密钥必须失败；同步 README | 0 |
| 4 | 再查配置、gRPC 装配、LLM 必填字段、提示词包及清洗链路；六包 562 项通过，`go vet ./...` 通过 | 未发现新增可确认问题 | 1 |
| 5 | AST 提取与词法对比 1,376 个原函数：1,238 个未改变，其中 706 个仅搬迁；逐项核对 41 个删除/重命名函数及 97 个变动函数；核查构建条件、依赖方向和格式，Linux AMD64 / Darwin ARM64 命令入口交叉编译通过 | 未发现新增可确认问题；删除项对应已记录的移除或替换 | 2 |
| 6 | AI 错误类型、冷却时长和 Rust FFI/marker/Arrow 边界；响应头与 Google 结构化错误回归先失败，修复后相关三包 63 项通过；Rust 2 项测试及格式检查通过 | `Retry-After` 和 Google 数值重试提示溢出会产生负数或极短冷却时长。加入可表示范围验证并共用浮点时长转换，保留有效日期、小数秒及既有配置冷却策略 | 0 |
| 7 | 搜索全部生产 Go 时长乘法，沿画像自动提炼和手工替换追踪刷新权重来源；新回归先失败，修复后用例、处理器、AI 容灾三包 373 项通过 | 画像续期先乘后限幅会溢出，例如稳定画像权重 4,096 时得到过去的到期日期。改为乘法前判断生命周期上限 | 0 |
| 8 | 对新增配置及冷却修复做边界复审，核对有效零值、缺省数组、节点密钥和日期语义；真实原生库 ABI、读写往返、待恢复 schema 三项验收通过 | 未发现新增可确认问题 | 1 |
| 9 | 标准 all 模式构建成功；154 个变更文件与隔离目录逐字节一致；全量 Go 1,380 个测试/子测试通过，5 跳过，0 失败；检查打包保护及测试质量 | 删除一个只检查文档固定句子的 Python 测试及其专用路径常量。保留摘要、签名、目录所有权和实际包验证；Python 剩余 22 项中 19 通过、3 跳过 | 0 |
| 10 | 核对三个新增回归文件的输入、错误及实际状态断言；区分夹具内容传递测试与固定文档措辞测试；最新 AST 对比 1,392 个原函数，706 个搬迁函数保持一致，删除/重命名列表没有遗漏 | 未发现新增可确认问题 | 1 |
| 11 | 再查 SQLite 向量写入、查询、去重、缓存和恢复；确认批量向量在写入前完成序列化校验，错误带记录定位，提交不明根据真实引用状态处理 | 未发现新增可确认问题 | 2 |
| 12 | 核对关闭与调用锁、FFI 线程绑定和缓冲区生命周期、提交后独立清理上下文；在全量日志中核验取消、对账、部分提交及补偿测试实际通过 | 未发现新增可确认问题 | 3 |
| 13 | 再核对标准打包路径与配置说明，147 个 Go/Rust 变更文件与构建目录逐字节一致；发行脚本的强制原生验收检查通过，141 个 Go 文件格式检查通过 | 未发现新增可确认问题 | 4 |
| 14 | 最终核对命令加载入口、接口、测试删除清单、全部跳过原因和审核记录；最新源码重新完成 Linux AMD64 / Darwin ARM64 入口交叉编译，差异格式检查通过 | 未发现新增可确认问题 | 5 |

已完成 14 轮；第 10—14 轮连续五轮未发现新增可确认问题，达到本次循环结束条件。末五轮分别重点检查测试质量、持久化、并发与 FFI、打包验收、最终一致性，具体覆盖及证据见上表。

## 本轮新增修复

1. 数值时长在乘以毫秒前检查 `time.Duration` 可表示范围，错误不改变接收对象。
2. `llm.routes`、`rerank.routes`、`embedding.nodes` 按显式数组整体替换，禁止按位置继承旧条目的凭据和供应商参数。未声明整个数组时仍保留既有分层行为。
3. 存储接口说明准确区分统一协调、逐语句提交和部分写入；未移除真实故障路径的向量保护。
4. 供应商重试提示在转换为冷却时长前检查溢出、非有限值及截断为零，响应头和 Google 结构化提示使用一致的范围规则。
5. 画像续期根据已定义的基础时长、步长和最大值，在乘法前判断是否达到上限，保持既有生命周期策略。
6. 删除仅限制发行文档措辞、无法验证真实信任链的测试，保留能验证实际依赖信任链的测试。

新增回归位于 `internal/config/config_boundary_review_test.go`、`internal/adapters/outbound/ai_key_failover/retry_after_test.go` 和 `internal/app/usecase/profile_expiry_test.go`。其他代码与文档修改均保留在原工作区，分支为 `codex/repository-audit-repair`，尚未提交。

## 本次循环文件变更

- 新增：本文及上述三个回归文件。
- 修改配置：`internal/config/config.go`、`config_load.go`、`config_env_loading_test.go`、`config_layers_test.go`、`effective_sources_test.go`，以及 `cmd/vmm-local/config_provider_test.go`。
- 修改存储契约注释：`internal/app/ports/ports_memory.go`、`internal/app/usecase/memory_query_write.go`、`internal/adapters/outbound/vldb_sqlite/memory_writes.go`。
- 修改时长算法：`internal/adapters/outbound/ai_key_failover/classifier.go`、`internal/app/usecase/postaction_profiles.go`。
- 修改测试清理：`scripts/test_install_host_deps.py`，移除一个文档措辞测试和专用常量，未删除文件。
- 修改文档：`README.md`、`docs/REPOSITORY_AUDIT_REPAIRS_20260926_CN.md`。

此前全仓库审计的拆分与清理清单见 [原修复记录](REPOSITORY_AUDIT_REPAIRS_20260926_CN.md)。

## 最终验证

| 验证 | 结果 |
| --- | --- |
| 主模块 `go test ./... -count=1 -json -timeout=20m`，启用真实原生库及 all 打包依赖 | 1,380 个测试/子测试通过事件，5 个测试跳过，0 失败；38 个测试包通过，7 个包无测试 |
| `go vet ./...` | 通过 |
| 嵌套 Go 模块 `third_party/vldb-controller-client-go` | 5 项通过，2 个包 |
| Python 脚本测试 | 22 项：19 通过，3 跳过 |
| Rust `cargo test --locked` 及 `cargo fmt -- --check` | 2 项测试通过，格式通过 |
| 标准 `make.ps1 build`，`VMM_BUILD_STORAGE_PROFILE=all` | 隔离构建目录构建成功，配置与依赖同步完成 |
| 实际原生 ABI、读写往返、恢复，以及 native / legacy-split / legacy-controller 打包集成 | 全部通过，发行脚本 `check_tests` 校验通过 |
| 最新 Linux AMD64 / Darwin ARM64 命令入口测试交叉编译 | 通过 |
| `git diff --check` 与变更 Go 文件 `gofmt -l` | 通过 |

完整 Go JSON 日志保留在本机 `F:\packages\temp\vmm-review-loop-tests-utf8.jsonl`。

标准验收包保留在 `C:\Users\lingeasy\.codex\worktrees\repository-audit-validation\VulcanMemoryMesh\output`，入口为 `bin/vmm-local.exe`。原工作区的未归属 `output/configs/base.yaml` 保持原内容；使用隔离目录完成构建验收。

五个 Go 跳过项分别依赖真实 DashScope 凭据、Windows 目录符号链接权限（两项）、Unix 权限位（两项）；Python 的三个跳过项依赖 Unix Bash/GnuPG 环境。Linux/macOS 此次只有交叉编译证据，真实 PostgreSQL 服务及系统服务安装未做运行验收。
