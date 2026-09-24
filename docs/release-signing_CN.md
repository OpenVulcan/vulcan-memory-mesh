# 正式发行签名

两个仓库的 `.github/workflows/release.yml` 已接入 GitHub 托管运行器上的实际产品签名。Windows 使用 SimplySign 云证书和 SignTool；Linux 对最终下载文件生成 GPG 分离式签名；macOS 不进行代码签名或公证。所有平台保留既有 Ed25519 发行清单验证。

## 凭据与公开信任

组织 Secrets：`CERTUM_TOTP_EMAIL`、`CERTUM_TOTP_SECRET`、`GPG_PRIVATE_KEY`；GPG 私钥有口令时增加 `GPG_PASSPHRASE`。所有需要签名的仓库都必须在这些组织 Secrets 的可用范围内。

各仓库原有的 `VMM_RELEASE_ED25519_PRIVATE_KEY` / `VMMM_RELEASE_ED25519_PRIVATE_KEY` 与对应 `*_KEY_ID` 继续使用。

公开身份固定在 `scripts/signing-policy.json`：

- Certum 证书指纹：`19E4E2A3EFAECB6BE966D4C89B28C2C6F5C8DEA9`。
- GPG 主公钥指纹：`6C0B6CA76104CB7B04C2829886D1113F5325ECAC`。
- GPG 公钥文件：`docs/release-gpg-public.asc`。

证书续期或 GPG 密钥轮换时，需要审核并更新上述公开信任文件。SimplySign 客户端固定版本、下载摘要和安装包签名身份；自动登录参数沿用已实测的第三方调用方式，不将其描述为 Certum 官方无人值守 API。

## 打包顺序

VMM：正式构建与原生存储验收 → 签名四个 Windows 可执行文件和三个 DLL → 刷新原生 DLL 摘要 → 重新验收并打包 → 生成绑定压缩包的 Windows 签名证明 → 签名两个 Linux 压缩包 → Ed25519 清单 → 全部资产验证。

VMMM：构建五个平台程序 → 在 Windows 原生运行器签名管理器 → 汇总文件并生成摘要清单 → 在独立 Windows 任务重新验证实际程序签名 → 签名两个 Linux 程序 → Ed25519 清单 → 按已签名程序的最终摘要生成引导脚本。

Linux 的 `.asc` 是 OpenPGP 文件签名，不表示已经签署 APT 仓库、RPM 或 AppImage。发行时 `.asc` 与对应 Linux 下载文件一同提供；公钥来自受信仓库，不能仅信任下载站点同时给出的任意公钥。

## 运行方式

在 Actions 中运行对应的正式发行工作流。默认 `verify_only=true`：使用当前工作流提交完整构建、签名和验证，将实际最终资产保存为 `vmm-release-verified` 或 `vmmm-release-verified`，不创建标签或 Release。`codex/github-release-signing` 分支推送固定使用验证模式。

合并到 `main` 后，正式创建草稿时显式设置 `verify_only=false`。VMM 要求已有标签属于 `main` 且与 `VERSION` 一致；VMMM 按既有规则核对或绑定标签，源码提交必须属于 `main`。已公开的 VMM `v0.1.0` 不可覆盖。

实际发行模式上传后会重新下载所有资产并核对字节；任何签名、身份、摘要、文件集合或标签竞态错误都会阻断任务。流程最终仍保留 Draft Release，公开版本是单独操作。不得把仅有 Actions 验证产物的运行说成已经公开发行。

共用同一账户的各仓库请顺序运行签名任务；GitHub 的并发组不提供跨仓库锁，本次没有引入额外的排队服务。

## 验证记录

VMMM 正式产品验证已通过：[发行工作流 35986546656](https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35986546656)，源码提交 `62749422077b5e574c89a794747ead3d58bc7cd4`。五个平台构建、实际 Windows 云签名、独立 Windows 任务复验、两份 Linux GPG 签名、Ed25519 清单和引导脚本生成全部通过，最终包含十一项资产。下载后的本机 Authenticode、时间戳、两个 GPG 签名、Ed25519 清单及全部文件摘要复验通过，实际 Windows 程序可执行并返回版本 `v0.1.0`。

- Windows 程序摘要：`50c6f4ddebcb3f4f2ebc969a2273181ddc9f8fffbb697775d6440402238c2307`。
- Linux x64 摘要：`a7255fdb6f5f8fcc00519afd0efebfbd1d51b1fd33e41cc752d04550b556b5f8`。
- Linux ARM64 摘要：`a8999413d77700e1559cbd7e297b1f896333f4eac38b9acd0cfe1fc408f2512c`。
- 同提交五平台持续集成：[35986546482](https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35986546482)，包括真实临时 GPG 密钥的缺签名、篡改和错误身份拒绝测试。

首轮 VMMM 实测暴露原有版本检查步骤的 Bash 内嵌文本缩进和缺少条件结束标记问题，已修复并增加真实脚本语法回归。不能把首次失败的运行当成签名成功证据。

VMM 首轮正式产品验证 [35986388344](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/35986388344) 在 Linux 两个平台与 macOS 两个平台的真实依赖全量测试中失败，没有达到最终签名汇总阶段。Windows 的七文件实际签名通过。固定依赖 `vldb-sqlite v0.1.6` 的 `DatabaseFileLock::acquire` 忽略 `fs4 0.13.1` 返回的 `Ok(false)`，导致第二个运行时未被拒绝；另一个快照测试夹具未显式创建权限为 `0700` 的私有目标目录。

普通 CI 原先先运行全量测试、再安装真实依赖并仅运行指定集成用例，未覆盖依赖安装后的文件锁测试；现已补上安装依赖后的全量测试。不能将此前普通持续集成成功或 VMMM 签名成功当成 VMM 正式产品验证成功。

经用户授权，上游修复与发行已完成：

- [SQLite v0.1.7](https://github.com/OpenVulcan/vldb-sqlite/releases/tag/v0.1.7)：显式拒绝未取得锁的竞争者，五平台库测试、二十项下载资产摘要及 VMM 实际 Windows 动态库回归通过。
- [控制器 v0.2.4](https://github.com/OpenVulcan/vldb-controller/releases/tag/v0.2.4)：固定修复后的 SQLite 完整提交，五平台运行 [35992536927](https://github.com/OpenVulcan/vldb-controller/actions/runs/35992536927) 通过。二十四项资产下载核验、两份 Windows Authenticode 签名与四份 Linux GPG 签名复验通过。本机控制器工作区五十八项测试通过。

控制器从隔离工作区构建，不包含原工作目录中的未提交功能。上述 GitHub 原生发行不代表已经发布 crates.io 包。SQLite 上游包本身未接入 Certum；VMM 的 Windows 完整包继续对其中的 SQLite DLL 签名。

VMM 已在提交 `e707ae4f9a97870da685a2de4c0ca6449c5223f2` 同步更新四个安装与打包入口、十项已核验资产摘要和回归测试。运行 [35998448056](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/35998448056) 的四个非 Windows 平台通过，Windows 暂存包测试失败且旧打包工具隐藏了失败诊断。同提交在本机隔离工作区的标准构建、全量测试、静态检查和暂存包验收通过，但不足以判断该次 CI 失败根因。提交 `813aff7` 让该测试直接输出诊断，未放宽断言和超时；运行 [36000078004](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/36000078004) 随后完整通过。该次 Windows 失败未再复现，不将后续通过解释为已确认其根因。

下载复核发现上述通过产物仍带有暂存运行测试生成的日志。提交 `fa0eb0d0558795186e6d1126f53906534b818faf` 修复包内容边界：验收前固定文件与摘要，验收后拒绝原输入变更；ZIP 与 TAR 都只打包固定集合。回归在修复前复现日志混入与修改输入未被拒绝，修复后通过。运行 [36001845374](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/36001845374) 又发现新增测试夹具未解析 macOS 临时目录别名与 Windows 短路径；提交 `cb3e653` 统一夹具规范路径，产品校验未放宽。同提交的一次普通 ARM64 CI 曾因 PowerShell 启动超过三十秒失败，其余两次通过；后续没有修改该超时。

### 最终 VMM 产物验收

[正式流水 36002737086](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/36002737086) 的七个任务全部通过，源码提交为 `cb3e653359e759fea51dfeb030a81ba8d267fb81`。五个平台均完成标准完整构建、真实依赖全量测试、强制原生与暂存运行验收；Unix 原生任务验证了 ZIP/TAR 内容边界与执行位。Windows 七文件真实云签名、Linux 两份 GPG 签名、Ed25519 清单和十六项资产汇总全部通过。同提交三次五平台普通 CI（`36002743861`、`36002737211`、`36002738135`）均成功。

十六项最终资产已下载，并在本机使用受信仓库的公开密钥与证书身份独立复验：全部摘要与长度、Ed25519、两份 GPG 签名、七个 Windows 文件的 Authenticode 与可信时间戳均通过。逐项检查五个压缩包中的八十九项负载文件与包内收据，共九十个普通文件；未包含运行日志、数据库或开发覆盖配置。实际 Windows 程序返回正确版本、平台和源码提交。

管理器的生产 `archive.Extract` 随后在本机成功解包这五份真实压缩包，逐一验证八十九项负载摘要和存储能力清单。该接收验收不等同于在本机运行 Linux/macOS 程序；对应平台的运行证据来自上述原生工作流。

| 平台 | 最终压缩包 SHA-256 |
| --- | --- |
| Windows x64 | `200dffbbf28fe6020fde6e8644e82481f720cc62824b9649d11bc55cb7567f72` |
| Linux x64 | `397287220705277e1989b41245405aeeb5afd6c61cc70bb5eb86af9037cab55b` |
| Linux ARM64 | `de50c2eb8d0d934b0c41a8103189b5e53da8934130b9138b174fa1f79d43500d` |
| macOS Intel | `11064262604e2f6b12346f4dc2b744ff8a06ca0f8825dd08fca272011de049eb` |
| macOS ARM64 | `ab54209fd246dee5ff2f6f9b68e5723c4bbcc987ef656fdc19100b03be5539de` |

上述 VMM / VMMM 产品结果来自仅验证模式，仍为 Actions 产物。两个产品 PR 尚未合并或公开新版本；已公开的 VMM `v0.1.0` 仍是旧的 native-only 包，不得覆盖。公开引导脚本的首次安装验收留待完整产品发行后执行。

早期凭据与独立测试程序验证见 [签名冒烟测试](github-signing-smoke_CN.md)。
