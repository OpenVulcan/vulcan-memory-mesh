# VMM gRPC 接口测试说明（中文）

## 文档目标

这份文档面向联调和测试同学，给出当前主线版本可直接执行的 `grpcurl` 示例。

当前服务：

- `vmm.v1.VMMService`

默认地址示例：

- `127.0.0.1:17625`

## 一、Healthz

```powershell
grpcurl -plaintext `
  -d '{}' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/Healthz
```

## 二、ListProjects

```powershell
grpcurl -plaintext `
  -d '{}' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ListProjects
```

预期返回里的 `displayPath` 形如：

```text
[9]TeamA/SpaceA/ProjectA
```

## 三、ResolveProject

### 按数字 ID 解析

```powershell
grpcurl -plaintext `
  -d '{
    "projectRef": "9"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ResolveProject
```

### 按路径解析

```powershell
grpcurl -plaintext `
  -d '{
    "projectRef": "TeamA/SpaceA/ProjectA"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ResolveProject
```

## 四、EnsureProject

### 仅检查，不自动补建

```powershell
grpcurl -plaintext `
  -d '{
    "projectPath": "TeamA/SpaceA/ProjectB",
    "confirmCreate": false
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/EnsureProject
```

### 显式确认后创建缺失节点

```powershell
grpcurl -plaintext `
  -d '{
    "projectPath": "TeamA/SpaceA/ProjectB",
    "confirmCreate": true
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/EnsureProject
```

## 五、DeleteProject

### 先拿确认结果

```powershell
grpcurl -plaintext `
  -d '{
    "projectPath": "TeamA/SpaceA/ProjectB",
    "confirmDelete": false
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/DeleteProject
```

### 确认删除

```powershell
grpcurl -plaintext `
  -d '{
    "projectPath": "TeamA/SpaceA/ProjectB",
    "confirmDelete": true
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/DeleteProject
```

## 六、MigrateProject

### 先校验

```powershell
grpcurl -plaintext `
  -d '{
    "sourceProjectPath": "TeamA/SpaceA/ProjectOld",
    "targetProjectPath": "TeamA/SpaceA/ProjectNew",
    "confirmMigrate": false
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/MigrateProject
```

### 确认迁移

```powershell
grpcurl -plaintext `
  -d '{
    "sourceProjectPath": "TeamA/SpaceA/ProjectOld",
    "targetProjectPath": "TeamA/SpaceA/ProjectNew",
    "confirmMigrate": true
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/MigrateProject
```

## 七、ResolveUser

### 只解析用户

```powershell
grpcurl -plaintext `
  -d '{
    "userRef": "alice",
    "confirmCreate": false
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ResolveUser
```

### 不存在时创建用户

```powershell
grpcurl -plaintext `
  -d '{
    "userRef": "alice",
    "confirmCreate": true
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ResolveUser
```

## 八、ListUsers

```powershell
grpcurl -plaintext `
  -d '{}' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ListUsers
```

## 九、DeleteUser

### 第一次调用，获取确认码

```powershell
grpcurl -plaintext `
  -d '{
    "userRef": "alice",
    "confirmationCode": ""
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/DeleteUser
```

### 第二次调用，带确认码删除

```powershell
grpcurl -plaintext `
  -d '{
    "userRef": "alice",
    "confirmationCode": "返回里的32位确认码"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/DeleteUser
```

## 十、PreCheck

当前 `PreCheck` 只接受：

- `sessionId`
- `userId`
- `projectId`
- `userContent`
- `recallMode`

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": 7,
    "projectId": 9,
    "userContent": "请根据当前项目给我建议",
    "recallMode": "PRE_CHECK_RECALL_MODE_SESSION_COMPACT"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PreCheck
```

说明：

- 省略 `recallMode` 或传 `PRE_CHECK_RECALL_MODE_LEGACY` 时，服务端保持旧版召回行为
- 传 `PRE_CHECK_RECALL_MODE_SESSION_COMPACT` 时，服务端按当前 session 的 compact 边界过滤同 session 的 turn-extract 记忆
- 当前版本如果收到未来新增的非零枚举值，也会回退到 compact-aware 基线，而不是重新开放整个当前 session

## 十一、ChatCompact

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": 7,
    "projectId": 9
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ChatCompact
```

说明：

- 服务端会把该 session 当前最新已持久化 turn 记为 compact 边界
- 如果当前 session 没有 turn，会返回成功但 `compactedTurnId=0`

## 十二、PostAction

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": 7,
    "projectId": 9,
    "userContent": "最开始的问题",
    "assistantContent": "最后的回答",
    "timeline": [
      { "type": "assistant", "content": "中间回答" },
      { "type": "user", "content": "补充问题" }
    ]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/PostAction
```

说明：

- 服务端会先记录原始日志，再记录清洗后日志
- 当前同步阶段会先把 turn 落到关系库存储，并把 session 入异步分析队列
- 返回 `accepted=true` 只表示 turn 已稳定入库且异步提炼已入队，不表示 `analyze_turn` 已完成
- 当 `timeline` 为空时，才会走 `NoiseGate`

## 十三、GetProfileNodes

```powershell
grpcurl -plaintext `
  -d '{
    "target": "PROFILE_TARGET_USER",
    "userId": 7,
    "projectId": 9,
    "limit": 50
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/GetProfileNodes
```

说明：

- `USER` 目标必须传 `userId`
- `PROJECT / TEAM / SPACE` 目标必须传 `projectId`

## 十四、GetProfileBundle

### FULL 模式

```powershell
grpcurl -plaintext `
  -d '{
    "userId": 7,
    "projectId": 9,
    "mode": "PROFILE_BUNDLE_MODE_FULL",
    "includeExplanation": true
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/GetProfileBundle
```

### SPLIT 模式

```powershell
grpcurl -plaintext `
  -d '{
    "userId": 7,
    "projectId": 9,
    "mode": "PROFILE_BUNDLE_MODE_SPLIT"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/GetProfileBundle
```

## 十五、ApplyProfileInstruction

```powershell
grpcurl -plaintext `
  -d '{
    "target": "PROFILE_TARGET_PROJECT",
    "projectId": 9,
    "instruction": "这个项目统一使用 Go 1.24，并默认中文回复代码审查意见。"
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/ApplyProfileInstruction
```

## 十六、SearchMemoryEvents

```powershell
grpcurl -plaintext `
  -d '{
    "userId": 7,
    "projectId": 9,
    "queries": [
      "最近确认过的架构决策",
      "当前项目的持久化限制"
    ],
    "topK": 5
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/SearchMemoryEvents
```

说明：

- `queries` 是简单字符串数组
- 不再使用 `query_json`
- 不再使用 `background`

## 十七、GetTurnDetails

```powershell
grpcurl -plaintext `
  -d '{
    "turnIds": [12, 15]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/GetTurnDetails
```

## 十八、WriteMemories

```powershell
grpcurl -plaintext `
  -d '{
    "sessionId": "sess_001",
    "userId": 7,
    "projectId": 9,
    "items": [
      {
        "scopeLevel": 2,
        "abstract": "项目默认使用 gRPC 接口",
        "details": "当前 OSS 本地版只暴露 gRPC，不再内建 HTTP 服务。",
        "category": 5,
        "priority": 2,
        "memoryLevel": 3
      }
    ]
  }' `
  127.0.0.1:17625 `
  vmm.v1.VMMService/WriteMemories
```

说明：

- `scopeLevel` 当前语义：
  - `1 = session`
  - `2 = project`
  - `3 = user`
- 返回只保留 `memoryId` 与 `deduped`

## 十九、常见错误

### 参数错误

- gRPC code：`InvalidArgument`
- `ErrorInfo.reason`：`GRPC_VALIDATION_FAILED`

### 资源不存在

- gRPC code：`NotFound`
- `ErrorInfo.reason`：`RESOURCE_NOT_FOUND`

### 需要确认

- gRPC code：`FailedPrecondition`
- `ErrorInfo.reason`：`CONFIRMATION_REQUIRED`

### 请求过大

- gRPC code：`ResourceExhausted`

## 二十、推荐测试顺序

建议按这个顺序联调：

1. `Healthz`
2. `ListProjects`
3. `ResolveProject`
4. `ResolveUser`
5. `PostAction`
6. `ChatCompact`
7. `PreCheck`
8. `SearchMemoryEvents`
9. `GetTurnDetails`
10. `GetProfileNodes / GetProfileBundle`
11. `ApplyProfileInstruction`
12. `WriteMemories`
13. `DeleteProject / DeleteUser / MigrateProject`
