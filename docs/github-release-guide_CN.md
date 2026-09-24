# GitHub 手动标签发行

当前版本为 `v0.1.0`。根目录 `VERSION` 是正式构建版本来源，`vmm-local -version-json` 和 `vmm-migrate -version-json` 输出相同版本及源码提交。

## 触发方式

1. 将待发布修改提交到 `main`，更新 `VERSION`，例如 `v0.1.0`。
2. 为该提交创建并推送同名标签。标签必须属于 `main` 历史。
3. 在 GitHub 的 **Actions → Tagged release → Run workflow** 中选择 `main`，填写已有标签，取消 `verify_only`。
4. 等待五个构建任务与最终签名验证全部成功。在 Releases 的草稿中检查发行包，再按发行审批公开。

也可以使用 GitHub CLI：

```powershell
git tag -a v0.1.0 -m "发布零点一初始版本"
git push origin main
git push origin v0.1.0
gh workflow run release.yml --ref main -f tag=v0.1.0 -f verify_only=false
```

推送标签不会自动发行，必须手动触发。工作流先解析标签对应的完整提交，并要求该提交的 `VERSION` 与标签一致；构建阶段固定使用解析后的提交。发布前再次校验远程标签，防止编译过程中被移动。

默认 `verify_only=true` 用当前工作流提交及其 `VERSION` 执行相同构建、真实签名和验签，将最终产物保存为 `vmm-release-verified` Actions artifact，不创建或覆盖标签、草稿或公开 Release。`codex/github-release-signing` 分支推送也只进入此模式。已公开的 `v0.1.0` 不可重复发行。

## 发行平台

| 平台 | GitHub runner | 原生库目标 | 压缩格式 |
| --- | --- | --- | --- |
| Windows x64 | windows-2025 | x86_64-pc-windows-msvc | zip |
| Linux x64 | ubuntu-24.04 | x86_64-unknown-linux-gnu | tar.gz |
| Linux ARM64 | ubuntu-24.04-arm | aarch64-unknown-linux-gnu | tar.gz |
| macOS Intel | macos-15-intel | x86_64-apple-darwin | tar.gz |
| macOS ARM64 | macos-15 | aarch64-apple-darwin | tar.gz |

每个平台在对应架构运行 Go 测试、真实 LanceDB 动态库验收和成品 gRPC 验收。Linux 包采用 GNU libc，面向 Ubuntu 24.04 及兼容环境；不声明兼容旧版 glibc 或 Alpine/musl。macOS 包未配置开发者证书签名与公证。

## 包内容与运行

```text
vulcan-memory-mesh-v0.1.0-<平台>/
  bin/vmm-local[.exe]
  bin/vmm-migrate[.exe]
  bin/vmm-pii-tester[.exe]
  bin/vldb-controller[.exe]
  configs/...
  libs/<平台的 legacy SQLite/LanceDB 动态库>
  libs/<平台的 native LanceDB 动态库>
  libs/manifest.json
  libs/native_lancedb-support/...
  VERSION
  LICENSE
  README.md
  NATIVE_STORAGE.md
  RELEASE_GUIDE.md
  release-manifest.json
```

每个平台发行包使用 `all` 依赖配置：同时包含 split 所需的 SQLite/LanceDB 动态库、controller 模式所需的 `vldb-controller`、native LanceDB ABI 及支持文件。解压后的 `configs/base.yaml` 默认仍为 `native`，安装器可以在下载并校验完整包后切换到 `split`、`controller` 或 `combined`。

包内 `release-manifest.json` 的 `capabilities.schema_version` 为 `1`，明确记录 `split`、`controller`、`native`、`combined` 四种模式；`combined` 的 provider 为 `postgres`，检索 flavor 支持 `standard` 与 `paradedb`。PostgreSQL 服务端和扩展由用户按部署环境提供，不会伪装成平台本地依赖。

不会带入测试数据库、用户覆盖配置或源码开发覆盖层 `configs/config.yaml`。

### 配置资产边界

发行脚本使用受控配置清单：`base.yaml`、`.env.example`、供应商示例、native 示例、配置模板，以及完整的 `noise_rules`、`pii_rules`、`prompts` 目录会进入发行包。源码中的 `configs/config.yaml` 是开发与测试覆盖层，可能引用 `DEEPSEEK_API_KEY`、`BAILIAN_API_KEY` 等环境变量，明确不会进入公开发行包。未来新增 `configs/` 文件时，必须先登记到 `scripts/release.py` 的发行清单；未登记文件会使打包失败，避免未知覆盖层或凭据配置被发布。

解压时保持整个目录结构。配置自己的模型端点与凭据后，从 `bin/` 目录启动 `vmm-local`；用户覆盖仍来自 `~/.vmm` 或 `-config` 指定的覆盖根。模型配置、PII、Noise 和 Prompt 覆盖规则与源码运行一致。详情见包内 `NATIVE_STORAGE.md`。

## 构建与失败处理

- Go 固定为 `1.27.0`，Rust 使用 `native/lancedb/rust-toolchain.toml`，Rust 依赖使用 `Cargo.lock`。
- `all` 发行构建还会安装并校验固定版本的 `vldb_sqlite`、`vldb_lancedb` 与 `vldb-controller` 宿主依赖；这些文件必须和 native manifest 一起通过平台级 staging 校验。
- 原生库缓存按平台、原生源码和制备脚本摘要隔离。缓存恢复后仍执行正式清单校验；日常 Go 构建不会自动编译 Rust。
- 五个平台全部完成后才进入草稿创建任务。上传草稿后重新下载并核对所有文件的 SHA-256；当前工作流不会转为公开 Release。
- Release 附带五个压缩包、五份外部平台清单、两份 Linux `.tar.gz.asc` 签名、`manifest.json`、`manifest.sig`、`windows-authenticode.json` 及 `SHA256SUMS`，共十六项资产。
- 已公开发布的同名版本拒绝覆盖。上传中断时保留草稿，可重新运行同一版本。若首次构建因源码或测试问题失败且尚未公开发布，先等待旧任务结束并提交修复，再以限定旧标签对象的 `--force-with-lease` 更新该标签，随后重新手动触发工作流；已发布标签不应移动。草稿创建命令为 `python scripts/release.py draft`。
- 普通测试中需要真实云凭据或旧 VLDB 产物的测试可能跳过；四项强制原生验收明确拒绝跳过，缺库不得视为成功。

runner 标签依据 [GitHub 官方支持列表](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)。工作流使用仓库提供的 `GITHUB_TOKEN`，无需额外发布令牌；私有仓库需要可用的 GitHub Actions 配额。
## 签名清单与正式发布门禁

仓库中的 `v0.1.0` 已发布包仍是 native-only 版本。`all` 依赖包需要使用新的发行构建流程重新构建，不能把旧包当作支持 split、controller 或 combined 的完整包。

每个平台的 all-profile 压缩包会在 Release 外层配套以下公开元数据：

- `manifest.json`：protocol v1 清单，记录产品 `vmm`、发行标签、完整提交 SHA，以及 Windows x64、Linux x64、Linux ARM64、macOS Intel、macOS ARM64 五个压缩包的精确字节数和 SHA-256。
- `manifest.sig`：对 `manifest.json` 原始字节的 Ed25519 分离式签名，封装字段为 `version`、`key_id`、`signature`。
- `windows-authenticode.json`：Windows 四个可执行文件和三个 DLL 的 Authenticode 校验结果、Certum 证书主体、签发者、指纹、时间戳状态，以及与 Windows 压缩包绑定的文件摘要。
- `SHA256SUMS`：包含五个平台压缩包、平台元数据、`manifest.json`、`manifest.sig` 和 Windows 签名证明的摘要。

VMM 的公开信任根位于 [docs/release-public-keys.md](release-public-keys.md)：`key_id` 为 `vmm-2026-09-23-01`，公钥为该文档中的 Base64 值。VMM GitHub Actions 只从以下 Secrets 读取发行密钥，私钥不得写入仓库：

- `VMM_RELEASE_ED25519_PRIVATE_KEY`
- `VMM_RELEASE_ED25519_KEY_ID`

Windows Certum 与 Linux GPG 使用以下组织 Secrets：

- `CERTUM_TOTP_EMAIL`
- `CERTUM_TOTP_SECRET`
- `GPG_PRIVATE_KEY`
- `GPG_PASSPHRASE`：仅加密的 GPG 私钥需要。

发布工作流在临时 Windows Runner 安装固定哈希和签发者的 SimplySign Desktop，通过邮箱和 SHA-256、30 秒、6 位动态口令登录云证书，签名 `vmm-local.exe`、`vmm-migrate.exe`、`vmm-pii-tester.exe`、`vldb-controller.exe`、`vmm_lancedb_native.dll`、`vldb_sqlite.dll` 和 `vldb_lancedb.dll`。SignTool 和 PowerShell 必须同时确认可信签名、固定证书身份和 RFC 3161 时间戳。证书公开身份固定在 `scripts/signing-policy.json`，不再使用 PFX Secrets。原生 DLL 签名后只刷新 `libs/manifest.json` 的 `library_sha256`，保留其余身份字段，再重新验收并打包。

两份最终 Linux 压缩包使用 GPG 生成分离式 `.asc` 签名，然后用隔离的纯公钥密钥环复验。公开信任根为 `docs/release-gpg-public.asc`，主指纹固定在 `scripts/signing-policy.json`。缺少凭据、错误证书或 GPG 密钥、无时间戳、缺签名或产物篡改都会阻止发行。macOS 不进行平台代码签名；五个平台仍全部受 Ed25519 发行清单保护。

真实发行模式通过校验后只创建 GitHub Draft Release，上传全部资产，重新下载并逐文件核对摘要，最后再次确认仍是草稿。Certum 证书已到位并接入云签名；公开 Release 仍是单独的发行操作。管理器安装前继续使用内置 VMM 公钥验证 `manifest.sig`。完整运行说明见 [正式发行签名](release-signing_CN.md)。

### 宿主 VLDB 依赖的固定摘要

`all` profile 使用的 legacy SQLite、LanceDB 和 `vldb-controller` 压缩包由 `scripts/host_deps_sha256.tsv` 固定记录。清单覆盖 Windows x64、Linux x64、Linux ARM64、macOS Intel 和 macOS ARM64 共五个平台，每个平台包含三类依赖。

`scripts/install_host_deps.sh` 与 `scripts/install_host_deps.ps1` 只接受清单中精确的仓库、标签和资产名。下载完成后直接对压缩包计算 SHA-256；不会把同一 Release 中可替换的 `.sha256` 资产当作信任根。本地缓存压缩包也必须匹配仓库清单，缺少、重复、格式错误或摘要不匹配时立即失败。

已安装缓存的 marker 同时记录压缩包摘要和解压后文件摘要。旧格式或不完整 marker 会被视为未验证缓存并重新校验。变更 VLDB 版本或资产时，必须先从对应官方 GitHub Release 获取实际资产摘要，再成对更新两个宿主脚本和 `host_deps_sha256.tsv`，不能填写占位摘要。
