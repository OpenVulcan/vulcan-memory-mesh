# SQLite FTS5 + GSE Hybrid Retrieval Plan
# SQLite FTS5 + GSE 混合检索改造计划

## Task Goal
## 任务目标

- Introduce pure-Go Chinese pre-tokenization on top of the existing SQLite FTS retrieval path so local lexical search keeps CGO-free portability while fixing BM25 recall and ranking for Chinese text.
- 在现有 SQLite FTS 检索链路上引入纯 Go 中文预分词能力，在保持 CGO-free 可移植性的前提下，修复中文文本在 BM25 检索中的召回与排序问题。
- Preserve the current repository layering and runtime packaging rules, and add a configuration switch so Chinese pre-tokenization is enabled by default but can be manually turned off for English-centric deployments.
- 保持当前仓库分层与运行时打包规则不变，并补充配置开关，使中文预分词默认开启，同时允许纯英文场景手动关闭。

## Execution Steps
## 执行步骤

1. Inspect the current FTS schema, write path, lexical search flow, and configuration loading so the implementation matches the repository reality instead of the external proposal.
1. 先检查当前 FTS 表结构、写入链路、词法检索流程与配置加载方式，确保方案以仓库真实实现为准，而不是直接套用外部提案。
2. Design a reusable tokenizer abstraction with a long-lived GSE-backed implementation and a configuration gate that does not break non-Chinese deployments.
2. 设计可复用的分词抽象，并提供长生命周期的 GSE 实现与配置开关，避免破坏非中文部署场景。
3. Upgrade or align the SQLite FTS schema and write synchronization logic so indexed text can store application-side tokenized content without corrupting existing fact tables.
3. 升级或对齐 SQLite FTS 表结构与写入同步逻辑，使索引文本以应用层预分词方式落库，同时不破坏现有事实表。
4. Update the lexical search path so raw queries are tokenized according to configuration and translated into safe FTS5 MATCH expressions with BM25-based ordering.
4. 改造词法检索链路，使原始查询按配置进行分词并被组装成安全的 FTS5 MATCH 表达式，同时保持 BM25 排序。
5. Add or update tests and user-facing documentation for configuration, write/read behavior, and Chinese lexical search expectations.
5. 补充或更新测试与用户文档，覆盖配置说明、写入/读取行为以及中文词法检索预期。
6. Run the required Go test suites and perform plan-based verification before recording the execution summary and moving this file to `docs/completed/`.
6. 运行规定的 Go 测试集，并在基于计划完成自检后补写执行总结，再将本文件迁移到 `docs/completed/`。

## Technical Choices
## 技术选型

- Prefer a single shared tokenizer service owned by application startup or store initialization so dictionary loading cost is paid once and reuse stays explicit.
- 优先采用由应用启动或 Store 初始化持有的共享分词服务，让词典加载成本只支付一次，并让复用关系保持清晰。
- Keep SQLite FTS5 on built-in `unicode61`; use pre-tokenized text with spaces so the solution remains pure Go and compatible with the current SQLite driver constraints.
- 保持 SQLite FTS5 使用内置 `unicode61`；通过插入带空格的预分词文本实现倒排索引，以维持纯 Go 方案并兼容当前 SQLite 驱动约束。
- Reuse existing repositories, transaction boundaries, and configuration patterns instead of introducing a new cross-layer shortcut.
- 复用现有仓储、事务边界与配置模式，不额外引入破坏分层的新捷径。

## Acceptance Criteria
## 验收标准

- The repository contains a configuration option for application-side lexical tokenization, enabled by default for Chinese tokenization scenarios and documented for manual override.
- 仓库中新增应用层词法分词配置项，默认满足中文分词场景开启，并提供可手动覆盖的文档说明。
- SQLite lexical indexing/search uses application-side tokenized content when the feature is enabled, and can gracefully fall back when disabled.
- SQLite 词法索引与检索在开关开启时使用应用层预分词内容，在关闭时能够平滑回退。
- Chinese queries can retrieve and rank relevant memory records through the lexical search path with BM25 ordering.
- 中文查询能够通过词法检索链路召回并按 BM25 排序返回相关记忆记录。
- Required tests pass, and any changed behavior is reflected in the relevant documentation.
- 规定测试通过，且所有受影响行为均已同步到相关文档。

## Verification Checklist
## 验证清单

- [x] FTS schema and synchronization logic updated against the real repository structure.
- [x] 已按仓库真实结构完成 FTS 表结构与同步逻辑改造。
- [x] Configuration loading, defaults, and docs updated consistently.
- [x] 配置加载、默认值与文档已保持一致。
- [x] Lexical write path and search path both honor the tokenizer switch.
- [x] 词法写入链路与检索链路均正确遵循分词开关。
- [x] Required Go test suites executed successfully.
- [x] 已成功执行要求的 Go 测试集。

## Execution Change Summary
## 执行变更总结

### 1. Core Fixes And Adjustments
### 1. 核心修复与调整概述

- Added one application-side lexical tokenizer built on pure-Go GSE so SQLite FTS5 can keep `unicode61` while storing Chinese-friendly tokenized text for BM25 retrieval.
- 新增基于纯 Go GSE 的应用层词法分词器，让 SQLite FTS5 在继续使用 `unicode61` 的前提下，写入适合中文 BM25 的预分词文本。
- Added the `memory_pipeline.lexical_pre_tokenize` switch, enabled by default, and wired it through application startup into the SQLite store so English-only deployments can manually fall back to the legacy regex path.
- 新增默认开启的 `memory_pipeline.lexical_pre_tokenize` 开关，并把它从应用启动层传递到 SQLite Store，使纯英文部署可以手动回退到旧的正则路径。
- Updated the FTS schema to use `unicode61 remove_diacritics 2`, and changed both memory write paths plus lexical search query assembly to honor the tokenizer switch.
- 将 FTS schema 更新为 `unicode61 remove_diacritics 2`，并改造了两条记忆写入路径与 lexical 查询组装逻辑，使其共同遵循分词开关。

### 2. File Change List
### 2. 📂文件变更清单

- New / 新增
  - `internal/platform/textutil/lexical_tokenizer.go`
  - `internal/platform/textutil/lexical_tokenizer_test.go`
- Modified / 修改
  - `internal/adapters/outbound/vldb_sqlite/store.go`
  - `internal/adapters/outbound/vldb_sqlite/store_test.go`
  - `internal/app/app.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `configs/local.json`
  - `configs/.env.example`
  - `configs/vmm_config_readme.md`
  - `README.md`
  - `go.mod`
  - `go.sum`
- Deleted / 删除
  - None in this task.
  - 本任务未删除文件。

### 3. Key Code Adjustments
### 3. 💻关键代码调整详情

- `LexicalTokenizer` now encapsulates one startup-loaded GSE segmenter, protects shared segmentation with a mutex, and exposes separate helpers for FTS index text and MATCH expression generation.
- `LexicalTokenizer` 现在封装了一个启动时加载的 GSE 分词器，通过互斥锁保护共享分词调用，并分别提供 FTS 索引文本与 MATCH 表达式构造能力。
- `Store.NewStore` now accepts lexical options, builds the tokenizer once, bumps schema version to 14, and rewrites FTS rows through `buildMemoryNodeFTSUpsertSQL`.
- `Store.NewStore` 现在接收 lexical 选项，只初始化一次分词器，将 schema 版本提升到 14，并通过 `buildMemoryNodeFTSUpsertSQL` 重写 FTS 行写入。
- `SearchLexicalMemory` now delegates query assembly to the tokenizer so Chinese queries become `tokenized phrase + OR tokens`, while the disabled mode preserves the previous regex-only behavior.
- `SearchLexicalMemory` 现在把查询组装委托给分词器，使中文查询转成“分词短语 + OR 词项”；当开关关闭时则保持此前的纯正则行为。
- Added regression tests for tokenizer output, Store MATCH params, FTS upsert content, and config default/env override behavior.
- 新增了针对分词器输出、Store MATCH 参数、FTS upsert 内容以及配置默认值/环境变量覆盖行为的回归测试。

### 4. Remaining Issues And Notes
### 4. ⚠️遗留问题与注意事项

- The SQLite adapter still follows the repository’s existing schema-reset strategy on schema-version changes, so upgrading from older debug data will recreate managed tables.
- SQLite 适配器仍沿用仓库当前“schema 版本变更即重建受管表”的策略，因此从旧调试数据升级时会重建受管表。
- The tokenizer deliberately appends ASCII identifier fallback terms such as `vmm-local` so mixed-language memory text remains searchable even when GSE splits punctuation.
- 分词器会刻意补充 `vmm-local` 这类 ASCII 标识符回退词，以保证中英混合记忆文本在 GSE 按标点切分后仍然可检索。
- Verification completed with `go test ./...`.
- 已使用 `go test ./...` 完成验证。
