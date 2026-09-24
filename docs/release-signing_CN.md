# 正式发行签名

两个仓库的 `.github/workflows/release.yml` 已接入 GitHub 托管运行器上的实际产品签名。Windows 使用 SimplySign 云证书和 SignTool；Linux 对最终下载文件生成 GPG 分离式签名；macOS 不进行代码签名或公证。所有平台保留既有 Ed25519 发行清单验证。

## 凭据与公开信任

组织 Secrets：`CERTUM_TOTP_EMAIL`、`CERTUM_TOTP_SECRET`、`GPG_PRIVATE_KEY`；GPG 私钥有口令时增加 `GPG_PASSPHRASE`。两个仓库必须在这些组织 Secrets 的可用范围内。

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

同一账户的两个仓库请顺序运行签名任务；GitHub 的并发组不提供跨仓库锁，本次没有引入额外的排队服务。

## 验证记录

VMMM 正式产品验证已通过：[发行工作流 35986546656](https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35986546656)，源码提交 `62749422077b5e574c89a794747ead3d58bc7cd4`。五个平台构建、实际 Windows 云签名、独立 Windows 任务复验、两份 Linux GPG 签名、Ed25519 清单和引导脚本生成全部通过，最终包含十一项资产。下载后的本机 Authenticode、时间戳、两个 GPG 签名、Ed25519 清单及全部文件摘要复验通过，实际 Windows 程序可执行并返回版本 `v0.1.0`。

- Windows 程序摘要：`50c6f4ddebcb3f4f2ebc969a2273181ddc9f8fffbb697775d6440402238c2307`。
- Linux x64 摘要：`a7255fdb6f5f8fcc00519afd0efebfbd1d51b1fd33e41cc752d04550b556b5f8`。
- Linux ARM64 摘要：`a8999413d77700e1559cbd7e297b1f896333f4eac38b9acd0cfe1fc408f2512c`。
- 同提交五平台持续集成：[35986546482](https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35986546482)，包括真实临时 GPG 密钥的缺签名、篡改和错误身份拒绝测试。

首轮 VMMM 实测暴露原有版本检查步骤的 Bash 内嵌文本缩进和缺少条件结束标记问题，已修复并增加真实脚本语法回归。不能把首次失败的运行当成签名成功证据。

VMM 正式产品完整验证正在运行：[35986388344](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/35986388344)。同提交五平台普通持续集成 [35986388353](https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/35986388353) 已通过；完整原生发行验收待运行结束后登记。早期凭据与独立测试程序验证见 [签名冒烟测试](github-signing-smoke_CN.md)。
