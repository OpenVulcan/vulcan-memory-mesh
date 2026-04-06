# PII 多国家规则矩阵（CN）

## 目标

本矩阵记录 `configs/pii_rules` 当前落地的多国家 PII 规则覆盖面，用于说明：

- 哪些 locale 已生成规则文件
- 每个 locale 实际做了哪些高置信规则
- 每个 locale 有哪些明确保留的限制

## 全局限制

以下限制适用于全部 locale：

- 不做姓名、公司名、机构名、学校名、医院名等实体型规则。
- 不做国家、省州、市区县等地名本身的屏蔽。
- 不做只有城市/街道名、没有门牌/楼栋/单元的泛地址。
- 纯数字规则优先使用边界；如条件允许，优先加二次复核。
- Go `regexp` 基于 RE2，不支持 lookbehind / lookahead，因此部分国家编号只能采用“保守正则 + 限制文档”而不是更激进的前瞻写法。
- 由于 VM 目前只支持 `len / is_luhn / weight_sum / cn_check / is_base64`，大部分国家编号暂未启用本国专属 checksum。

## common.json

| 文件 | 已做规则 | 主要限制 |
|---|---|---|
| `common.json` | PEM 私钥块、国际信用卡、BTC 地址、BTC WIF、ETH 地址、JWT、LLM API Key、AWS AK、阿里云 AK、腾讯 Secret ID、GitHub PAT、Email、公网 IPv4、公网 IPv6 | IPv4 仅保留公网段；内网/回环/链路本地地址不处理 |

## Locale 覆盖

| Locale | 国家/地区 | 已做规则 | 主要限制 |
|---|---|---|---|
| `zh-CN` | 中国大陆 | 身份证、银联借记卡、手机号、统一社会信用代码、护照、固话、车牌、门牌级地址 | 不做姓名；地址仅替换“街道/小区 + 门牌/楼栋/单元”片段 |
| `zh-TW` | 中国台湾 | 身分证字号、手机号、门牌级地址 | 不做姓名；未做统一编号与护照 |
| `zh-HK` | 中国香港 | HKID、门牌级地址 | 不做姓名；未做手机号，避免 8 位数字高误杀 |
| `ja-JP` | 日本 | My Number、电话、门牌级地址 | 地址规则偏向 `丁目` 结构；未做姓名与都道府县地名 |
| `ko-KR` | 韩国 | 주민등록번호、사업자등록번호、手机号、道路地址 | 不做姓名；未加韩国专属 checksum |
| `en-US` | 美国 | SSN、EIN、电话、门牌级地址 | SSN 因 RE2 限制未做前瞻过滤，属于保守版 |
| `en-CA` | 加拿大 | SIN、电话、门牌级地址 | SIN 仅做格式化版本，未做校验算法 |
| `es-MX` | 墨西哥 | CURP、RFC、电话、门牌级地址 | 不做姓名；地址仅从 `Calle/Av/... + 门牌` 开始抓取 |
| `pt-BR` | 巴西 | CPF、CNPJ、电话、门牌级地址 | CPF/CNPJ 当前仅做格式规则，未做 checksum |
| `es-AR` | 阿根廷 | DNI、CUIL/CUIT、电话、门牌级地址 | DNI 使用格式化版本，降低纯数字误杀 |
| `es-CL` | 智利 | RUT、电话、门牌级地址 | RUT 当前未做校验位计算 |
| `es-PE` | 秘鲁 | RUC、电话、门牌级地址 | 未做 DNI，避免 8 位纯数字误杀 |
| `en-GB` | 英国 | NINO、NHS Number、手机号、门牌级地址 | 未做 postcode 单独匹配 |
| `en-IE` | 爱尔兰 | PPSN、手机号、门牌级地址 | 未做 Eircode 单独匹配 |
| `fr-FR` | 法国 | NIR、SIREN、电话、门牌级地址 | NIR/SIREN 为保守格式版，未加本地 checksum |
| `de-DE` | 德国 | Steuer-ID、USt-IdNr、手机号、门牌级地址 | 未做邮编；税号仅做格式约束 |
| `it-IT` | 意大利 | Codice Fiscale、VAT、手机号、门牌级地址 | 未做 CAP 单独匹配 |
| `es-ES` | 西班牙 | DNI/NIE、CIF、手机号、门牌级地址 | 未做邮编；未加 DNI 字母校验 |
| `pt-PT` | 葡萄牙 | NIF、手机号、门牌级地址 | NIF 未做 checksum |
| `nl-NL` | 荷兰 | VAT、手机号、门牌级地址 | 未做 BSN，避免 9 位纯数字误杀 |
| `sv-SE` | 瑞典 | Personnummer、手机号、门牌级地址 | 未做邮编；未分离组织号与个人号 |
| `nb-NO` | 挪威 | VAT、手机号、门牌级地址 | 未做 Fødselsnummer，避免 11 位纯数字过宽 |
| `da-DK` | 丹麦 | CPR、电话、门牌级地址 | 电话为保守格式版，可能覆盖标准 8 位分组号码 |
| `fi-FI` | 芬兰 | HETU、手机号、门牌级地址 | 未做商业编号；未加 HETU 校验 |
| `pl-PL` | 波兰 | NIP、手机号、门牌级地址 | 未做 PESEL，避免 11 位纯数字误杀 |
| `tr-TR` | 土耳其 | T.C. Kimlik No、手机号、门牌级地址 | TCKN 未做 checksum；采用边界约束 |
| `en-IN` | 印度 | Aadhaar、PAN、GSTIN、手机号、门牌级地址 | Aadhaar 未做 Verhoeff 校验；地址只做门牌级片段 |
| `en-SG` | 新加坡 | NRIC/FIN、UEN、手机号、门牌级地址 | 不做邮编单独匹配 |
| `en-MY` | 马来西亚 | NRIC、手机号、门牌级地址 | 未做车牌；未做企业号 |
| `th-TH` | 泰国 | 身份证号、手机号、门牌级地址 | 身份证仅做格式版，未做 checksum |
| `vi-VN` | 越南 | 手机号、门牌级地址 | 暂未做 CCCD/税号，避免纯数字误杀 |
| `id-ID` | 印度尼西亚 | NPWP、手机号、门牌级地址 | 暂未做 NIK，避免与 16 位银行卡/流水号冲突 |
| `en-PH` | 菲律宾 | TIN、手机号、门牌级地址 | 暂未做 PhilSys/UMID |
| `en-AU` | 澳大利亚 | TFN、ABN、手机号、门牌级地址 | TFN/ABN 未做 checksum |
| `en-NZ` | 新西兰 | IRD、手机号、门牌级地址 | 未做 NZBN |
| `ar-AE` | 阿联酋 | Emirates ID、手机号、门牌级地址 | 地址为保守英文路名版，未做阿拉伯语楼宇名规则 |

## 当前总量

- `common.json`：1 份
- locale 文件：36 份
- 总文件数：37 份
- 已通过的 CLI 用例：37 组（每组包含正样本与反样本）

## 后续建议

如果下一阶段要继续提高精度，优先顺序建议为：

1. 为高价值国家加入本地 checksum VM 扩展（如 CPF、CNPJ、RUT、Aadhaar、TCKN 等）。
2. 为地址规则补充更多本地街道后缀词表，但继续坚持“必须带门牌/楼栋”。
3. 对纯数字身份证类规则逐步改为“格式化优先、纯数字次之”，减少日志、订单号、流水号误杀。
4. 如果引擎后续支持前瞻/后顾或自定义函数，可回补美国 SSN、台湾统一编号、波兰 PESEL 等更精细约束。
