---
status: current
owner: Dev Team
last-reviewed: 2026-07-19
---

# Azure 渠道启用 Responses 兼容配置

## 1. 发布版本

- 部署版本必须包含提交：`1086038f`
- 更新代码后重新构建服务或镜像
- 重启所有网关实例

## 2. 客户端配置

```text
Base URL: https://lightingtheword.com/v1
```

禁止配置：

```text
https://lightingtheword.com
https://lightingtheword.com/v1/response
https://lightingtheword.com/v1/responses
```

## 3. Azure 渠道配置

进入：

```text
渠道管理 → 编辑 Azure 渠道
```

按以下内容配置：

| 配置项 | 配置值 |
|---|---|
| 渠道类型 | Azure |
| API 地址 | Azure 资源根地址 |
| API 密钥 | Azure API Key |
| 默认 API 版本 | `2025-04-01-preview`，或资源实际支持版本 |
| 响应 API 版本 | 默认留空；资源有明确要求时填写其支持版本 |
| 模型 | 包含 `gpt-5.4`、`gpt-5.5` |
| 模型映射 | 映射到 Azure 中实际存在的 Deployment |
| 请求 Body 透传 | 关闭 |

API 地址示例：

```text
https://example.openai.azure.com
```

或：

```text
https://example.cognitiveservices.azure.com
```

API 地址中禁止追加：

```text
/openai/v1
/responses
/chat/completions
```

## 4. 系统配置

进入：

```text
系统设置 → 模型相关设置
```

确认系统级“请求透传”为关闭状态。

进入：

```text
系统设置 → 模型相关设置 → ChatCompletions → 响应兼容
```

在“策略 JSON”中保存：

```json
{
  "enabled": true,
  "all_channels": false,
  "channel_types": [3],
  "model_patterns": [
    "(?i)^gpt-5\\.(4|5)($|-.*)"
  ]
}
```

如只对指定 Azure 渠道启用，改用：

```json
{
  "enabled": true,
  "all_channels": false,
  "channel_ids": [实际Azure渠道ID],
  "model_patterns": [
    "(?i)^gpt-5\\.(4|5)($|-.*)"
  ]
}
```

## 5. 多实例配置

- 所有实例连接同一个配置数据库
- 确认所有实例已部署相同版本
- 保存策略后逐实例确认配置已同步
- 配置未同步的实例执行滚动重启

## 6. 验证

使用包含以下参数的业务请求验证：

```text
model: gpt-5.4 或 gpt-5.5
tools: 至少一个 function tool
reasoning_effort: medium
请求路径: /v1/chat/completions
```

验收结果：

- 请求不再返回以下错误：

```text
Function tools with reasoning_effort are not supported for gpt-5.5 in /v1/chat/completions.
```

- DEBUG 日志中的 Azure 上游地址包含：

```text
/openai/v1/responses
```

或：

```text
/openai/responses
```

- Azure 上游地址不得包含：

```text
/deployments/{model}/chat/completions
```

- 返回正常文本或 `tool_calls`
- 使用日志中的渠道类型为 Azure

## 7. 未生效检查

依次确认：

1. 生产实例已经重新构建并重启。
2. 策略 JSON 的 `enabled` 为 `true`。
3. 实际模型名匹配 `model_patterns`。
4. 实际渠道类型为 Azure。
5. `channel_ids` 使用生产数据库中的真实渠道 ID。
6. 系统级请求透传已关闭。
7. Azure 渠道请求 Body 透传已关闭。
8. Azure Base URL 只包含资源根地址。
9. 默认 API 版本或响应 API 版本受 Azure 资源支持。
10. 模型映射指向真实存在的 Azure Deployment。

## 8. 回滚

在“策略 JSON”中保存：

```json
{
  "enabled": false,
  "all_channels": false,
  "channel_types": [3],
  "model_patterns": [
    "(?i)^gpt-5\\.(4|5)($|-.*)"
  ]
}
```

不要通过把 HTTP 400 加入自动重试范围进行回滚或规避。
