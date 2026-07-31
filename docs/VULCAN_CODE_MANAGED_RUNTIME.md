# Vulcan Code 托管运行模式

## 入口

VMM 保留原有独立运行方式，并新增严格互斥的托管入口：

```text
vmm-local --vulcan-managed-config <绝对路径>
```

托管入口只读取这一份 JSON/YAML 清单，不读取 `base.yaml`、`config.yaml`、用户目录配置、`.env` 或供应商密钥。清单缺失、字段未知、版本不匹配、路径不是绝对路径、地址不是显式回环地址时直接失败，不回退到独立模式。

## 安全边界

- 清单所有者固定为 `vulcan-code`，契约版本当前为 `1`。
- 存储固定连接 Vulcan Code 已启动的 VLDB Controller，禁止自动启动第二个 Controller。
- VMM 使用独立的 `vmm-memory` Space 和独立数据根。
- LLM、Embedding、Rerank 全部通过 Vulcan Code 受控推理发现文件调用；VMM 不持有供应商凭据。
- gRPC 全部方法（包括 `Healthz`）要求进程级随机 Bearer Token。
- VMM 监视精确父进程和宿主关闭标记；宿主消失或请求关闭时执行应用级优雅释放。
- 状态文件只包含非敏感进程身份、协议版本、清单摘要和实际回环监听地址。

## 生命周期

1. Vulcan Code 先启动并发布共享 Controller 与受控推理服务。
2. Vulcan Code 原子生成托管清单并启动 `vmm-local`。
3. VMM 校验清单，连接外部 Controller，装配 Vulcan 推理客户端并绑定 gRPC。
4. VMM 原子写入状态文件。
5. Vulcan Code 校验父子 PID、实例、代次、摘要、协议版本和回环地址，再使用 Bearer Token 调用 `Healthz`。
6. 只有验证通过后，Vulcan Code 才把实际地址投影到 VMM Hook。
7. 正常关闭时宿主写入关闭标记，等待 VMM 释放资源；超时后才强制终止。

## 托管维护入口

Vulcan Code 需要处理已有数据、向量模型变化或标记损坏时，使用同一份严格托管清单调用正式维护程序：

```text
vmm-migrate -vulcan-managed-config <绝对路径> -vector-rebuild -confirm-vector-rebuild
```

- `-config` 与 `-vulcan-managed-config` 严格互斥，不会合并或回退读取普通配置。
- `-confirm-vector-rebuild` 只供已经完成界面确认与安全备份的宿主非交互编排使用。
- 维护程序仍会检查运行时端口必须处于停服状态。
- 取消或失败由宿主保留原始数据与已完成备份；只有正式重建成功后宿主才更新向量空间标记。

## 构建验证

Windows 标准构建：

```powershell
.\make.bat build
```

该命令必须生成 `output/bin/vmm-local.exe`、`output/bin/vmm-migrate.exe` 和 `output/configs/` 资产。Vulcan Code 的打包脚本复制两个正式程序以及 `noise_rules`、`pii_rules`、`prompts`，不会把普通 VMM 配置文件作为托管配置来源。
