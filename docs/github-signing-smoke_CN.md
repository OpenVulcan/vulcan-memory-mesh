# GitHub 托管 Runner 签名测试

测试日期：2026-09-24。

用户已授权使用组织 Actions Secrets 测试 Windows SimplySign 和 Linux GPG；本次不测试 macOS 签名。

## 范围与入口

工作流：`.github/workflows/signing-smoke.yml`。

使用专门的 Go 测试程序验证凭据、客户端启动、签名与验签。程序只输出固定文本，不调用 VMM/VMMM 运行时。测试证据不能替代正式产品完整安装包的构建与验收。

工作流只具有仓库只读权限，不创建或修改 Release。它支持手动触发；在 `codex/github-signing-smoke` 分支修改测试脚本时也会触发，方便在合并前实测。

## 凭据契约

| Secret | 用途 |
| --- | --- |
| `CERTUM_TOTP_EMAIL` | SimplySign 登录邮箱 |
| `CERTUM_TOTP_SECRET` | 原始 Base32 TOTP 种子，采用 CodeSignAuto 查证的 SHA-256、30 秒、6 位参数 |
| `GPG_PRIVATE_KEY` | ASCII 装甲格式的 OpenPGP 私钥，必须恰好包含一个可签名的主密钥 |
| `GPG_PASSPHRASE` | 可选；私钥有口令时必须配置 |

组织 Secret 必须对运行测试的仓库可见。测试只报告是否可读取，绝不打印其值。动态 OTP 在调用客户端前额外脱敏；私钥和口令经标准输入传给 GnuPG，原生子进程不继承这些环境变量。

## Windows 实现依据

官方 MSI：

`https://files.certum.eu/software/SimplySignDesktop/Windows/9.4.4.92/SimplySignDesktop-9.4.4.92-64-bit-en.msi`

- 官方下载页面：<https://support.certum.eu/en/cert-offer-software-and-libraries/>
- SHA-256：`8ec420fc27798b86078b7bd02fe7152097e1b3005bab51820eaca8e57df84da3`。
- 安装包签名证书指纹：`AC9C643063CD501E851A7B6A9762E295FBABB012`；本地校验状态为有效，主体为 Asseco Data Systems S.A.。
- MSI 目录表确认客户端安装在 `%ProgramFiles%\Certum\SimplySign Desktop\SimplySignDesktop.exe`。
- 自动登录参数依据 CodeSignAuto 的 `SimplySignController.StartLoginAsync`，固定参考提交 `f312d76e4edbf5c3328a0fa01db9533de84c92ce`：<https://github.com/zhuxbo/CodeSignAuto/blob/f312d76e4edbf5c3328a0fa01db9533de84c92ce/src/CodeSignAuto.Agent/SimplySign/SimplySignController.cs>。
- `/autologin 邮箱 OTP` 是已查证的第三方调用方式；本测试用于确认它能否在 GitHub 临时 Runner 上工作，不将其描述为 Certum 官方无人值守 API。
- OTP 算法依据同一固定提交中的 `OtpauthProfile` 和 `TotpGenerator`：它们明确要求 SHA-256、30 秒、6 位；不能套用常见的 SHA-1 默认值。

每次只尝试一次登录。必须发现恰好一张有效且关联私钥的 Certum 代码签名证书，才使用其确切指纹调用 SignTool。签名限时执行；如果客户端要求额外的 PIN 或交互，测试会失败而不会记录为通过。

验收要求：SignTool 验签通过，PowerShell 返回有效签名，发布者指纹匹配，包含时间戳，修改 PE 节内数据后返回 `HashMismatch`。

## Linux 实现依据

使用临时隔离密钥环导入私钥，为 ELF 测试程序生成独立的 ASCII 装甲签名。另建仅含公钥的密钥环完成验签，核对签名指纹，并确认修改文件后返回 `BADSIG`。退出时停止对应 GPG 代理并清理私钥目录。

此测试验证 OpenPGP 文件签名，不声称已经签署 APT 仓库元数据、RPM 或 AppImage；现有 Ed25519 发布清单机制继续用于当前产品分发。

## 证据与结果

工作流保存七天的公开测试证据：测试程序、独立签名、公钥、安装器校验报告和不含凭据的 JSON 结果。私钥目录、OTP、客户端日志和完整环境变量不会上传。

本地已通过：RFC 6238 六组标准测试向量、非法种子拒绝、Python 语法、PowerShell 语法、工作流权限和事件结构检查、Windows 测试程序构建、PE 节篡改生成。

首轮 VMMM 测试：<https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35983290135>。

- Linux GPG 导入、签名、独立公钥验签、篡改拒绝均通过；下载产物后在本机再次独立验签通过。
- Windows 两个 Secret 可读取，官方 MSI 验签与安装通过，Runner 具有会话 2 且支持交互；登录阶段未发现证书，未进入签名。
- 审查定位测试脚本错误使用 SHA-1，已依据源码修正为 SHA-256，并增加登录前关闭客户端和退出码诊断。该失败属于测试实现错误，不能作为托管 Runner 不支持 SimplySign 的证据。

修正后的 VMMM 测试全部通过：<https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35983876589>。

- 被测提交：`e78e74f0761faf8628574b5c3fc22e7ff3fb6592`。
- Windows：官方客户端安装、SHA-256 TOTP 自动登录、云端私钥签名、RFC 3161 时间戳、SignTool 验签、PowerShell 验签和篡改拒绝全部通过。下载后的签名程序在本机再次验签，状态为 `Valid`。
- Windows 证书指纹：`19E4E2A3EFAECB6BE966D4C89B28C2C6F5C8DEA9`；有效期截至 `2027-09-24T06:04:54Z`。
- Windows 已签名测试程序 SHA-256：`BBFF538227A5ED9D1A15F4AD310E5CF5ACF51DC356BEB18DD8AE5A03F8F6599D`。
- Linux：私钥导入、独立签名、公钥隔离验签和篡改拒绝全部通过，下载产物后的本机复验也通过。
- Linux 公钥主指纹：`6C0B6CA76104CB7B04C2829886D1113F5325ECAC`。
- Linux 测试程序 SHA-256：`bd935d5e92462577234cd6ca88d8d6493c19699f8d02021735749115270988a4`。
- 同提交 VMMM 原有五平台 CI 通过：<https://github.com/OpenVulcan/vulcan-memory-mesh-manager/actions/runs/35983876501>。

VMM 独立仓库测试也全部通过：<https://github.com/OpenVulcan/vulcan-memory-mesh/actions/runs/35984267211>。

- 被测提交：`7990cfcf2f72046f00a3506600105fa2a6c3973d`。
- Windows 和 Linux 都完成与 VMMM 相同的签名、验签及篡改拒绝检查；两个下载产物均在本机再次独立验签通过。
- Windows 证书和 Linux 公钥指纹与 VMMM 相同，证明组织凭据已对两个仓库生效。
- Windows 已签名测试程序 SHA-256：`A0FE0ED83516FAE9DA9E2AC37A1D8AA8C2DF7CCA3E16C99F9C85BE4F18C86528`。
- Linux 测试程序 SHA-256：`19647ff6bfc32d90a51449923375e11224d66827e557a3f0521102ad6b7de7d3`。

复现时在对应仓库的 Actions 页面查看 `Signing credential smoke test`；工作流合并到默认分支后可直接手动触发。在合并前，测试分支中修改工作流或测试脚本会触发实测。组织凭据不复制到本机，两仓库分别保留同样的测试入口和说明。

## 实测结论与边界

当前 Certum 证书、组织凭据及固定版本 SimplySign Desktop 已在 GitHub 托管 Windows 临时运行器完成无人值守签名，过程中没有额外人工输入 PIN。本次链路不需要个人电脑、固定 IP 或常驻 Windows 服务器。

该结果验证了当前组合的可行性，不是 Certum 对第三方自动登录参数的长期接口保证。测试没有发布正式产品、改动已有发行工作流或为 macOS 签名；正式发行接入仍需覆盖实际产品文件、打包顺序和全部发布产物的校验。

同一个 Certum 账户的跨仓库签名测试按顺序运行，避免同时登录。工作流中的并发组只在单仓库内生效；多个项目未来同时发行时，还需要单独验证会话并发或统一排队。
