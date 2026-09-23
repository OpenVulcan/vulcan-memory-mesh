# VMM Release Public Key / VMM 发行公钥

VMM release manifests use a dedicated Ed25519 key. The private key is stored only in the VMM repository GitHub Actions Secret `VMM_RELEASE_ED25519_PRIVATE_KEY`; this document contains public verification material only.

VMM 发行清单使用独立的 Ed25519 密钥。私钥仅存储在 VMM 仓库的 GitHub Actions Secret `VMM_RELEASE_ED25519_PRIVATE_KEY` 中；本文档只包含公开校验材料。

| Product / 产品 | Key ID / 密钥标识 | Base64 public key / Base64 公钥 |
| --- | --- | --- |
| VMM | `vmm-2026-09-23-01` | `h2906GgOkZSmkYGWy3tV6/sH0+zyCcnhTZKjKx06cpM=` |

The release workflow must fail when either `VMM_RELEASE_ED25519_PRIVATE_KEY` or `VMM_RELEASE_ED25519_KEY_ID` is missing, and the manager must verify `manifest.sig` against this key before accepting any VMM artifact. The workflow creates verified drafts only; public publication remains blocked while the Certum certificate is pending.

发行工作流在 `VMM_RELEASE_ED25519_PRIVATE_KEY` 或 `VMM_RELEASE_ED25519_KEY_ID` 缺失时必须失败；管理器在接受任何 VMM 资产前必须使用此公钥验证 `manifest.sig`。工作流只创建已校验草稿；Certum 证书办理期间保持公开发行阻断。
