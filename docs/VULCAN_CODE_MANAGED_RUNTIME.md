# Vulcan Code 托管运行退役与迁移说明

Vulcan Code 专用的托管运行入口已经从 VMM standalone 主线退役。当前 VMM 只接受独立分层配置，不再读取 Vulcan Code 生成的托管清单，也不再由托管宿主注入 Controller、推理发现文件或 Bearer Token 生命周期。

## 已退役的入口

以下参数会被 `vmm-local` 和 `vmm-migrate` 明确拒绝，并返回清楚的错误：

```text
-vulcan-managed-config <path>
--vulcan-managed-config=<path>
```

错误信息为：

```text
-vulcan-managed-config is no longer supported; use standalone layered configuration via -config
```

`-config` 现在表示独立配置覆盖根目录或该根目录下的 YAML 文件。标准加载顺序是随包 `output/configs/base.yaml`、项目覆盖层、`~/.vmm` 或 `-config` 指定覆盖层，再按现有规则读取同级 `.env`。旧托管清单不能与普通配置合并或作为静默回退来源。

## 独立存储迁移

托管运行时的旧数据不通过启动时自动转换迁移。需要把 legacy `split` 或 `controller` 数据转到 native 时，先停写并保留源快照，再写入新的空目标目录：

```powershell
.\output\bin\vmm-migrate.exe -migrate split-to-native -native-output <new-empty-dir> -confirm-migrate
.\output\bin\vmm-migrate.exe -migrate controller-to-native -native-output <new-empty-dir> -confirm-migrate
```

源模式必须与迁移目标匹配，`-native-output` 必须指向新建且为空的目录。迁移不会原地覆盖旧数据，也不会因为 native 库缺失而切回 legacy 模式。完成后应检查 `migration-report.json` 和 `native-storage-override.fragment.yaml`，确认事实、ID、向量维度及库 manifest 后，把配置片段合并到原配置覆盖根；该片段不能直接作为 `-config` 参数，且必须保留原有 embedding provider、model、params 以及 prompts、pii_rules、noise_rules 覆盖。

native SQLite 的 FTS 文本是从关系事实派生的索引数据，迁移时需要按当前 native tokenizer 重建。已有 LanceDB 向量只有在模型和 `embedding.dimension` 都一致时才可复用；迁移过程不自动调用 embedding 服务。迁移会记录关系库/向量库身份及 embedding provider、model、dimension、params 摘要，错配时拒绝启动。`sqlite.db.migration-incomplete` 会隔离未完成目标；失败目标保留供诊断，修复后必须重新迁移到新的空目录。`-fts-rebuild` 的参数和调度入口已接入，但完整重建实现、停写约束及跨平台验收仍待核，不能把命令出现视为已完成迁移验收。

## native 配置边界

native 的默认路径为：

- `database/native/sqlite.db`
- `database/native/lancedb`

这些相对路径按标准打包的 `output/` 根解析。`lancedb.native.library_path` 留空时由应用选择 `output/libs/` 下当前平台的 native LanceDB 动态库。具体字段、环境变量和缓存 manifest 规则见[独立原生存储说明](./native-storage-guide_CN.md)。

旧托管字段不能写入普通 YAML；未知配置字段在加载阶段直接失败。native 的模式、路径、tokenizer、ABI 或 manifest 不匹配也会直接失败，运行时不会通过隐式托管配置或旧后端兜底。

## 验证边界

本文件只记录托管入口退役和离线迁移约束。配置测试与源码检查不等于某个目标平台上的动态库加载或历史数据迁移验收；目标平台必须准备并验证对应 native 缓存、库 ABI 和迁移快照后，才能形成运行结论。
