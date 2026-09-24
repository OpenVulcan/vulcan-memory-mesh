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

修正后的远程结果将在实测后补充。
