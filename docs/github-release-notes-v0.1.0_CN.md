# VulcanMemoryMesh v0.1.0

- 独立运行的本地记忆服务，提供 gRPC 接口。
- 原生 SQLite 关系存储，GSE 中文分词和可恢复 FTS5 全文索引。
- 官方 LanceDB 0.39.0 原生动态库，向量检索与原子更新。
- 配套配置覆盖、健康检查、离线迁移、索引维护和向量重建。
- Windows x64、Linux x64/ARM64、macOS Intel/ARM64 五种原生发行包。

下载对应平台压缩包，保持 `bin`、`configs`、`libs` 目录结构，配置模型服务后从 `bin` 启动。已经发布的 v0.1.0 平台包只包含 native 依赖，不能作为支持 split/controller 的完整安装器包使用。包含 split 动态库、`vldb-controller` 与 native ABI 的 `all` 完整包须由后续新标签发行；管理器会依据签名清单核验包的实际能力。v0.1.0 的 `SHA256SUMS` 可用于其原始下载校验。

Linux 包面向 Ubuntu 24.04 及兼容 GNU libc 环境；macOS 包未做开发者签名与公证。迁移原有数据前请阅读包内 `NATIVE_STORAGE.md`，不要直接复用旧数据库路径。
