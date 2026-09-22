# GitHub 手动标签发行

当前版本为 `v0.1.0`。根目录 `VERSION` 是正式构建版本来源，`vmm-local -version-json` 和 `vmm-migrate -version-json` 输出相同版本及源码提交。

## 触发方式

1. 将待发布修改提交到 `main`，更新 `VERSION`，例如 `v0.1.0`。
2. 为该提交创建并推送同名标签。标签必须属于 `main` 历史。
3. 在 GitHub 的 **Actions → Tagged release → Run workflow** 中选择 `main`，填写已有标签。
4. 等待五个构建任务与最终发布任务全部成功。在 Releases 下载发行包。

也可以使用 GitHub CLI：

```powershell
git tag -a v0.1.0 -m "发布零点一初始版本"
git push origin main
git push origin v0.1.0
gh workflow run release.yml --ref main -f tag=v0.1.0
```

推送标签不会自动发行，必须手动触发。工作流先解析标签对应的完整提交，并要求该提交的 `VERSION` 与标签一致；构建阶段固定使用解析后的提交。发布前再次校验远程标签，防止编译过程中被移动。

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
  configs/...
  libs/<平台的 LanceDB 动态库>
  libs/manifest.json
  libs/native_lancedb-support/...
  VERSION
  LICENSE
  README.md
  NATIVE_STORAGE.md
  RELEASE_GUIDE.md
  release-manifest.json
```

发行包使用 `native` 存储，包含中文 GSE 分词与 FTS5，以及官方 LanceDB `0.39.0` 动态库。SQLite 不需要额外 DLL。不会带入测试数据库、用户覆盖配置或旧 VLDB/Controller 文件。

解压时保持整个目录结构。配置自己的模型端点与凭据后，从 `bin/` 目录启动 `vmm-local`；用户覆盖仍来自 `~/.vmm` 或 `-config` 指定的覆盖根。模型配置、PII、Noise 和 Prompt 覆盖规则与源码运行一致。详情见包内 `NATIVE_STORAGE.md`。

## 构建与失败处理

- Go 固定为 `1.27.0`，Rust 使用 `native/lancedb/rust-toolchain.toml`，Rust 依赖使用 `Cargo.lock`。
- 原生库缓存按平台、原生源码和制备脚本摘要隔离。缓存恢复后仍执行正式清单校验；日常 Go 构建不会自动编译 Rust。
- 五个平台全部完成后才进入发布任务。先上传草稿，下载并核对所有文件的 SHA-256，然后转为正式 Release。
- Release 附带五个压缩包、五份外部平台清单及 `SHA256SUMS`。包内清单还记录各文件摘要。
- 已公开发布的同名版本拒绝覆盖。上传中断时保留草稿，可重新运行同一版本；源码或依赖问题须修复后重新选择正确标签，已发布标签不应移动。
- 普通测试中需要真实云凭据或旧 VLDB 产物的测试可能跳过；四项强制原生验收明确拒绝跳过，缺库不得视为成功。

runner 标签依据 [GitHub 官方支持列表](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)。工作流使用仓库提供的 `GITHUB_TOKEN`，无需额外发布令牌；私有仓库需要可用的 GitHub Actions 配额。
