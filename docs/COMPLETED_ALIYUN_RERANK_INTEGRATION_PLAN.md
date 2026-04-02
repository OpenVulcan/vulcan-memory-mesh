# 阿里云 DashScope Rerank 接入计划

更新时间：2026-04-02

## 1. 任务目标

- 在当前 VMM 项目中接入阿里云 DashScope 文本重排序能力。
- 使用用户指定的接口地址与模型：
  - `https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank`
  - `qwen3-vl-rerank`
- 优先复用项目当前已有的 API Key 配置方式，避免引入第二套凭证管理。
- 将 rerank 能力接入当前检索链，并补充必要测试与文档。

## 2. 执行步骤

1. 检查当前配置结构、LLM/Embedding 适配器与检索链实现，确认 rerank 最合适的装配点。
2. 设计并新增应用层 `RerankerClient` 端口，保证 `adapters -> app -> logic/domain` 依赖方向不被破坏。
3. 新增阿里云 DashScope rerank 出站适配器，复用现有 API Key / HTTP 客户端风格。
4. 在当前检索链中接入 rerank，并保证失败时可降级，不影响主流程可用性。
5. 补充配置说明、测试用例和必要文档。
6. 执行至少相关包测试；若改动范围较大，再补 `go test ./...`。

## 3. 技术选型

- Rerank provider：阿里云 DashScope `text-rerank`
- 模型：`qwen3-vl-rerank`
- 接口风格：独立 HTTP 适配器，不混入现有聊天模型调用路径
- 接入策略：默认可选启用，失败时回退到原始排序结果

## 4. 验收标准

- 项目内存在可复用的 rerank 端口与 DashScope 实现。
- 当前检索链能够在启用配置后调用 rerank。
- API Key 复用现有方式，不新增混乱配置。
- 相关测试通过，且至少覆盖：
  - 请求构造
  - 响应解析
  - 降级行为
  - 检索链接入行为
- 文档已同步更新。
