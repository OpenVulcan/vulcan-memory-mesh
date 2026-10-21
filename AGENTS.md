# VMM 仓库协作说明

## 项目范围

本仓库是 VulcanMemoryMesh 的 OSS 本地版。

- 不要重新引入仅 SaaS 才需要的运行时路径。
- 当前支持 `split`、`controller`、`native` 和 `combined` 四种存储模式；`combined` 使用 PostgreSQL。审计和重构必须保留这些已装配的生产能力及其测试。
- 必须保持依赖方向清晰：`adapters -> app -> logic/domain`。

## 构建规则

标准构建入口只有：

- `.\make.ps1 build`
- `.\make.bat build`
- `.\make.ps1 build release`
- `.\make.bat build release`

不要把根目录直接执行 `go build -o vmm-local ./cmd/vmm-local` 产生的产物，当成正式可运行交付物。

原因如下：

- 运行时会根据可执行文件位置推导系统配置目录。
- 当前支持的标准目录结构是：
  - 二进制：`output/bin/vmm-local(.exe)`
  - 配置：`output/configs/...`
- `make.ps1` / `make.bat` 会负责把 `configs/` 同步到 `output/configs/`。

这意味着：

- Windows 下正确的打包运行产物是 `output/bin/vmm-local.exe`。
- 根目录下的 [vmm-local](./vmm-local) 不是按照标准打包流程生成的，不应作为正式运行产物使用。

## 运行规则

- 正式运行时必须使用 `output/bin/` 目录中的可执行文件。
- 如果需要手工启动，请先进入 `output/bin/`，再运行：
  - `.\vmm-local.exe`
- 不要从仓库根目录直接运行临时产物。
- 不要把根目录下的可执行文件或其他非 `output/bin/` 产物，当成正式运行入口。

## 配置规则

- 系统配置底座通过可执行文件旁边的 `../configs` 结构解析。
- 用户覆盖来自：
  - `~/.vmm`
  - 或 `-config` 指向的目录 / 文件所在目录
- `-config` 现在表示“覆盖根目录”，不是旧式的“仅指定单个 app 配置文件”参数。
- 配置加载从 `config.DefaultBase()` 开始，再合并文件与环境覆盖；显式非法数值必须由校验报错，不得在 `Normalize` 中钳位或改回默认值。动态路由中已有契约规定的零值缺省语义保留。

## 规则目录规则

### Prompt 规则

- 系统目录：`configs/prompts/...`
- 用户覆盖：`~/.vmm/prompts/...` 或 `<config-root>/prompts/...`
- 启动时选择并校验一套完整提示词包：用户包不存在时选系统包；用户包存在但不完整、不可访问或不是目录时直接报错。
- 运行时固定已选包目录，读取失败必须返回场景和路径，不得按文件切换到另一层。新增覆盖包后需重启才能切换来源。

### PII 规则

- 系统目录：`configs/pii_rules/...`
- 用户覆盖：`~/.vmm/pii_rules/...` 或 `<config-root>/pii_rules/...`

### Noise 规则

- 系统目录：`configs/noise_rules/...`
- 用户覆盖：`~/.vmm/noise_rules/...` 或 `<config-root>/noise_rules/...`

对于 `pii_rules` 和 `noise_rules`，当前代码已经实现了“用户层优先、系统层兜底”的文件选择逻辑。不要在不更新文档和测试的情况下，悄悄修改这些优先级规则。

## gRPC 与持久化规则

- `post-action` 支持 `compat` 和 `strict` 两种模式。
- 入站清洗层负责处理媒体/base64/`<think>` 标签等文本净化。
- `MessageNormalizer` 只负责把已经清洗过的文本归一成标准轮次。
- `NoiseGate` 在关系存储写入前执行，用于判断一条标准化轮次是否值得进入长期记忆。
- 业务接口使用 gRPC；`management.enabled=true` 时另外装配人工管理 HTTP 服务。旧业务 HTTP 接口已移除，不能据此删除当前管理服务。
- 管理 HTTP 的令牌认证、请求体限制与作用域校验属于有效边界，重构时必须保留。
- 主动记忆写入通过 `MemoryStore.ApplyDirectMemoryWrite` 原子提交；所有当前后端均实现该契约，不再保留非原子备用路径。
- 读取持久化向量或检索结果时必须校验真实字段。损坏数据应报出记录和字段，不得伪装成空向量或满分命中。
- 当前运行时不再内建 TLS；如果需要 TLS，请在前面使用 Caddy。

## 测试规则

当你修改以下任一内容时，至少运行：

- 请求校验
- `post-action`
- `noise_rules`
- `pii_rules`
- 文本净化

最少要跑：

- `go test ./internal/adapters/inbound/grpcapi ./internal/app/usecase ./internal/logic/processor ./internal/platform/textutil ./internal/platform/pii ./internal/config`

在完成较大改动前，还必须跑：

- `go test ./...`

如果你修改了运行时打包路径或配置目录规则，还要额外跑：

- `.\make.ps1 build`
  或
- `.\make.bat build`

## 文档同步规则

如果你修改了以下任一内容，必须同步更新对应文档：

- `post-action` 契约或清洗行为
- `pii_rules` 加载规则
- `noise_rules` 加载规则
- 构建 / 打包目录结构

至少检查这些文件：

- [README.md](./README.md)
- [docs/post-action-guide_CN.md](./docs/post-action-guide_CN.md)
- [docs/noise-gate-guide_CN.md](./docs/noise-gate-guide_CN.md)

## 注释规范

新增代码必须遵守当前仓库的双语注释规范：

- 英文在上，中文在下。
- 注释应说明“这个功能用在什么地方、作用是什么”，不要只写空泛的“用于定义 XXX”。

### 文件头注释

每个新增源码文件都应有文件头注释，说明：

- 这个文件负责什么
- 它属于哪一层
- 它通常被哪条链路使用

### 类型与函数注释

每个新增的：

- `type`
- `interface`
- `const` / `var`（成组的重要配置或原因码）
- 函数 / 方法

都要有中英文双语注释。

### 功能区域注释

不要求给每一行代码都写注释，但要求给“每个实现功能的区域”写清楚说明。

这里的“功能区域”指的是：

- 一个函数内部的关键阶段
- 一段独立职责的逻辑块
- 一组连续语句完成的一个明确子功能

例如：

- 输入校验阶段
- 规则编译阶段
- 清洗与裁剪阶段
- 写库前的过滤阶段
- 排序与截断阶段

这些区域都应在代码块上方补中英文双语注释，帮助后续维护者快速理解流程。

### 不推荐的写法

- 不要把注释写成逐行翻译代码。
- 不要写“给变量赋值”“执行循环”这类没有信息量的注释。
- 不要只说明“是什么”，要说明“为什么在这里做、对哪条链路生效”。

### 适用范围

这条规则重点适用于：

- 新增文件
- 重写的大函数
- 新增处理流程
- 复杂的条件分支、清洗逻辑、编译逻辑、持久化逻辑

如果只是很小的局部修复，不要求为了补注释而把每一行都变得臃肿，但新增的完整功能块仍然必须满足上述规范。
