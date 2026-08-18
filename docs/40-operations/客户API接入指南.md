---
status: current
owner: Dev Team
last-reviewed: 2026-08-14
---

# 客户 API 接入指南

本指南面向使用 eLight 模型 API 的客户开发者，帮助您从创建 API Key 到完成第一次模型调用。平台提供 OpenAI 兼容接口，也支持 Anthropic 和 Gemini 格式的部分接口。具体可用模型和接口以平台页面展示为准。

## 1. 服务地址

| 用途 | 地址 |
| --- | --- |
| 平台入口 | [http://lightingtheword.com/](http://lightingtheword.com/) |
| 平台安全入口 | [https://lightingtheword.com/](https://lightingtheword.com/) |
| OpenAI 兼容 API Base URL | `https://lightingtheword.com/v1` |
| Anthropic 兼容 API 根地址 | `https://lightingtheword.com` |
| Gemini 兼容 API 根地址 | `https://lightingtheword.com` |

`http://lightingtheword.com/` 当前会重定向到 HTTPS。浏览器可以直接打开 HTTP 入口，但 SDK、命令行和生产代码应直接使用上表中的 HTTPS 地址，不要把携带 API Key 的请求发送到明文 HTTP 地址。

> 注意：OpenAI SDK 的 `base_url` / `baseURL` 要以 `/v1` 结尾。不要配置成单个具体接口，例如 `/v1/chat/completions` 或 `/v1/responses`。

## 2. 接入前准备

### 2.1 登录平台

打开 [https://lightingtheword.com/](https://lightingtheword.com/) 并登录。如果您还没有账号，请先按页面提示完成注册，或联系为您提供服务的客户经理开通账号。

### 2.2 确认余额和模型

1. 在“钱包”页面确认账户有可用余额。
2. 在“模型定价”页面查看可用模型、计费方式和支持的接口。
3. 记录要调用的完整模型 ID。请按页面展示原样填写，注意大小写、连字符和版本后缀。

### 2.3 创建 API Key

1. 进入“API Keys”页面：[https://lightingtheword.com/keys](https://lightingtheword.com/keys)。
2. 点击“创建 API Key”。
3. 填写一个容易识别的名称，例如 `production-order-service`。
4. 根据需要设置额度、到期时间、可用模型和允许访问的 IP。首次测试可保持管理员提供的默认分组。
5. 创建后立即复制 API Key，并存入密钥管理工具或安全的环境变量。

API Key 是调用模型的凭据，不是网页登录密码。请不要将它写入前端代码、Git 仓库、截图、日志或客服工单。

## 3. 完成第一次调用

### 3.1 配置环境变量

开发环境可先在当前终端设置：

```bash
export LIGHTING_API_KEY='sk-替换为您的APIKey'
```

生产环境请使用部署平台的 Secret 或专用密钥管理服务，不要在启动脚本中明文保存 API Key。

### 3.2 查询可用模型

```bash
curl --fail-with-body --silent --show-error \
  'https://lightingtheword.com/v1/models' \
  -H "Authorization: Bearer ${LIGHTING_API_KEY}"
```

请从返回结果的 `data[].id` 中选择模型，并在后续示例中用真实模型 ID 替换 `<MODEL_ID>`。如果结果为空或缺少所需模型，请检查 API Key 的模型限制和分组。

### 3.3 调用对话接口

```bash
curl --fail-with-body --silent --show-error \
  'https://lightingtheword.com/v1/chat/completions' \
  -H "Authorization: Bearer ${LIGHTING_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "<MODEL_ID>",
    "messages": [
      {"role": "user", "content": "请用一句话介绍你自己。"}
    ],
    "stream": false
  }'
```

成功时会返回 OpenAI 兼容的 JSON。对话文本通常位于：

```text
choices[0].message.content
```

## 4. 使用 OpenAI SDK

### 4.1 Python

安装 SDK：

```bash
python -m pip install openai
```

示例：

```python
import os

from openai import OpenAI

client = OpenAI(
    api_key=os.environ["LIGHTING_API_KEY"],
    base_url="https://lightingtheword.com/v1",
)

response = client.chat.completions.create(
    model="<MODEL_ID>",
    messages=[
        {"role": "user", "content": "请用一句话介绍你自己。"},
    ],
)

print(response.choices[0].message.content)
```

### 4.2 Node.js / TypeScript

安装 SDK：

```bash
npm install openai
```

示例：

```typescript
import OpenAI from 'openai'

const client = new OpenAI({
  apiKey: process.env.LIGHTING_API_KEY,
  baseURL: 'https://lightingtheword.com/v1',
})

const response = await client.chat.completions.create({
  model: '<MODEL_ID>',
  messages: [
    { role: 'user', content: '请用一句话介绍你自己。' },
  ],
})

console.log(response.choices[0].message.content)
```

## 5. 常用 OpenAI 兼容接口

| 场景 | 方法 | 路径 |
| --- | --- | --- |
| 查询模型 | `GET` | `/v1/models` |
| Chat Completions | `POST` | `/v1/chat/completions` |
| Responses API | `POST` | `/v1/responses` |
| 文本向量 | `POST` | `/v1/embeddings` |
| 图像生成 | `POST` | `/v1/images/generations` |
| 语音转文字 | `POST` | `/v1/audio/transcriptions` |
| 文本转语音 | `POST` | `/v1/audio/speech` |

不同模型支持的接口和参数不同。在开发前，请先在“模型定价”的模型详情中确认“支持的接口”，不要将 Chat Completions 参数直接用于 Responses、图像或音频接口。

Responses API 的最小请求示例：

```bash
curl --fail-with-body --silent --show-error \
  'https://lightingtheword.com/v1/responses' \
  -H "Authorization: Bearer ${LIGHTING_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "<MODEL_ID>",
    "input": "请返回 OK"
  }'
```

## 6. 在兼容 OpenAI 的客户端中配置

对于支持“自定义 OpenAI”或“OpenAI Compatible”的工具，通常只需填写：

| 配置项 | 配置值 |
| --- | --- |
| Provider / 提供商 | OpenAI Compatible / 自定义 OpenAI |
| API Base URL | `https://lightingtheword.com/v1` |
| API Key | “API Keys”页面创建的密钥 |
| Model / 模型 | `/v1/models` 返回的完整模型 ID |

如果工具要求分开填写“Host”和“API Version”，请分别使用 `https://lightingtheword.com` 和 `v1`。如果工具会自动追加 `/v1`，则只填写 `https://lightingtheword.com`，避免生成 `/v1/v1/...` 路径。

## 7. Anthropic 和 Gemini 格式

只有当目标模型页面明确显示支持对应接口时，才使用以下格式。

### 7.1 Anthropic Messages

```bash
curl --fail-with-body --silent --show-error \
  'https://lightingtheword.com/v1/messages' \
  -H "x-api-key: ${LIGHTING_API_KEY}" \
  -H 'anthropic-version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "<MODEL_ID>",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "请返回 OK"}
    ]
  }'
```

### 7.2 Gemini generateContent

```bash
curl --fail-with-body --silent --show-error \
  'https://lightingtheword.com/v1beta/models/<MODEL_ID>:generateContent' \
  -H "x-goog-api-key: ${LIGHTING_API_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{
    "contents": [
      {"parts": [{"text": "请返回 OK"}]}
    ]
  }'
```

## 8. 流式输出

需要边生成边展示时，可将 `stream` 设为 `true`：

```json
{
  "model": "<MODEL_ID>",
  "messages": [
    {"role": "user", "content": "请详细介绍你自己。"}
  ],
  "stream": true
}
```

客户端应按 Server-Sent Events 逐条处理数据，并正确处理连接中断。不要将“已收到部分文本”当作请求已完整结束；对结果完整性有强要求的业务，应在客户端保存业务请求标识，并对中断结果进行明确标记。

## 9. 上线建议

- 为开发、测试和生产环境分别创建 API Key，不要共用同一把密钥。
- 按业务需要设置 Key 的额度、到期时间、模型白名单和 IP 白名单，遵循最小权限原则。
- 在服务端调用 API，不要让浏览器、移动端安装包或其他不可信客户端持有长期 API Key。
- 设置合理的连接超时和读取超时。推理、图像、音频等请求可能比普通对话需要更长的读取时间。
- 遇到 `429` 时优先遵循响应头中的 `Retry-After`；遇到可恢复的 `5xx` 时使用带随机抖动的指数退避。
- 不要自动重试 `400`、`401` 或 `403`；应先修正请求、密钥或权限。对可能产生费用的请求，重试前还要评估重复执行的业务影响。
- 在自己的日志中记录请求时间、模型、HTTP 状态码和网关返回的 `X-Oneapi-Request-Id`，但不记录 API Key 和完整敏感提示词。

## 10. 常见问题

| 现象 | 常见原因 | 处理建议 |
| --- | --- | --- |
| `401 Unauthorized` | API Key 缺失、错误、失效或已禁用 | 确认使用 `Authorization: Bearer <API_KEY>`，并在“API Keys”页检查状态 |
| `403 Forbidden` | IP、模型、分组或账号权限限制 | 检查 Key 的 IP/模型限制和账号权限；确认出口 IP 与白名单一致 |
| `404 Not Found` | Base URL 重复了 `/v1`、路径错误，或模型不存在 | 打印 SDK 的最终请求地址，并通过 `/v1/models` 核对模型 ID |
| `429 Too Many Requests` | 触发频率限制或当前请求量较高 | 遵循 `Retry-After`，降低并发并使用退避重试 |
| 提示余额或额度不足 | 账户余额不足，或 API Key 的独立额度已用完 | 查看“钱包”和“API Keys”页面，充值或调整 Key 额度 |
| `5xx` 或连接中断 | 网络、网关或上游模型暂时异常 | 记录请求 ID 和发生时间，使用有上限的退避重试；持续失败时提供脱敏信息排查 |
| 调用成功但返回格式不符 | 选错接口协议，或使用了模型不支持的参数 | 确认当前使用的是 Chat Completions、Responses、Anthropic 还是 Gemini 格式 |

排查时以实际响应体中的错误信息为准。如需服务支持，请提供：

- 问题发生时间，包含时区；
- 请求的接口路径和模型 ID；
- HTTP 状态码和脱敏后的错误信息；
- `X-Oneapi-Request-Id`，或“使用日志”页面中的请求 ID。

请勿提供完整 API Key。如果 API Key 已通过代码、日志、截图或聊天泄露，应立即在“API Keys”页禁用或删除旧 Key，然后创建新 Key。
