---
status: current
owner: Dev Team
last-reviewed: 2026-10-18
---

# Lighting 与上游号池环境及测试基线

本资料保存 Claude 兼容性排查涉及的 Lighting 接入地址、上游路线和历史测试证据，供我方与号池提供方共同复测。验收要求见 [上游号池与 Claude Code 接口兼容要求](上游号池与ClaudeCode接口兼容要求.md)；既有问题明细见 [Claude 接口修复后的测试结果](Claude接口响应结构与停止标记验证.md)。

**第 4 节保留 2026 年 10 月 13–14 日的历史快照。最新结果见 [10 月 18 日两端复测](2026-10-18-两端Claude兼容复测报告.md)，不能将旧失败直接当作当前状态。** 10 月 18 日先完成 Lighting 复测及真实 Claude Code 最小工具流程；更换用户提供的新上游 Key 后，又在 15:49–15:53 完成报告列出的两端同批对照。用户确认暂无官方账户环境，所以使用官方文档作规范基线，供应方的 AWS 官转报告不替代本方官方直连。

## 1. 环境与调用关系

```text
客户程序或 Claude Code
  → Lighting：https://lightingtheword.com
  → 实际命中的网关渠道及协议适配器
  → 上游号池：https://api-zl.numspirit.com
  → 上游实际提供的模型服务
```

最后一跳的服务商、账户类型与部署拓扑尚未取得运营方证明。不得把上游域名返回的内容直接称为 Anthropic 官方直连结果。

| 项目 | Lighting 客户入口 | 上游直连对照 |
| --- | --- | --- |
| HTTPS 根地址 | `https://lightingtheword.com` | `https://api-zl.numspirit.com` |
| 已测试请求 | `POST /v1/messages` | `POST /v1/messages` |
| 完整测试地址 | `https://lightingtheword.com/v1/messages` | `https://api-zl.numspirit.com/v1/messages` |
| 客户请求模型 | `claude-fable-5-1` | 当前主对照为 `claude-fable-5-1`；`ccmax/claude-fable-5-1` 在新 Key 下探测为 503，历史曾可用 |
| 历史测试鉴权头 | `x-api-key: <Lighting Key>` | `Authorization: Bearer <上游 Key>`；新 Key 的 x-api-key 另测成功 |
| 请求版本头 | `anthropic-version: 2023-06-01` | 相同 |
| 请求内容类型 | `application/json` | 相同 |
| 返回方式 | JSON 与 SSE 均测过 | JSON 与 SSE 均测过 |
| 凭据关系 | 独立网关凭据 | 独立上游凭据；不是网关 Key |
| 模型映射证据 | 历史流式曾返回 `ccmax/` 前缀；10 月 18 日两种方式均为无前缀名 | 前缀线索只解释历史对照选择；不证明当前实际模型或渠道 |

根地址来源还包括 [客户 API 接入指南](../../40-operations/客户API接入指南.md)。Claude 客户端使用根地址，避免重复追加成 `/v1/v1/messages`。单数路径 `/v1/message` 不是 Messages API；10 月 13 日非流式、流式探测均返回 404。

### 待运营方补齐的环境信息

| 信息 | 用途与当前边界 |
| --- | --- |
| Lighting 实际部署版本、提交及发布时间 | 本地源码不能证明线上运行了同一版本 |
| 渠道 ID、渠道类型、Base URL、分组、路由及模型映射 | `/v1/messages` 只决定客户端协议，不能证明上游使用 Claude 原生协议 |
| 请求透传、流式 usage 补充、头部覆盖及重试配置 | 确认哪些字节或字段可能在网关内发生变化 |
| 上游号池版本、路线、账户类型、区域和实际模型版本 | 对齐不同前缀、账号和流量分支的能力；只记录脱敏账户标识 |
| Claude Code 客户端版本、SDK 版本及启用的 beta | 本轮冻结 Python `anthropic==1.13.0`；Claude Code 2.1.221 的 bare 最小工具流程已测，客户完整配置仍需补齐 |
| 官方对照账户及允许使用的测试模型 | 用户 10 月 18 日确认暂无官方环境；不把供应方 AWS 官转记录或其他渠道称为本方官方直连 |
| 超时、最大请求体、SSE 缓冲及代理配置 | 验证长思考、断流和长上下文行为；不猜测未提供的数值 |

以上未确认项保留为待补齐，不写入假定的 IP、服务器、管理员账号、端口或已上线能力。Key、Cookie、OAuth 凭据和完整认证头值不进入仓库；历史证据中的签名仅用于受控复测，不复制到本文。

## 2. 客户最初反馈

客户已明确调用 `POST /v1/messages`，反馈我方响应包含：

```json
{
  "id": "id",
  "type": "message",
  "role": "assistant",
  "model": "claude-fable-5-1",
  "content": [{"type": "text", "text": "hello"}],
  "stopReason": "end_turn",
  "usage": {"input_tokens": 14, "output_tokens": 4}
}
```

客户提供的另一渠道样本使用 `stop_reason`，并额外带有以下字段：

| 层级 | 对照样本包含的信息 |
| --- | --- |
| 消息 | `container: null`、`stop_reason: "end_turn"`、`stop_sequence: null`、`stop_details: null`、`diagnostics: null`、`context_management.applied_edits: []` |
| 缓存 usage | `cache_creation_input_tokens: 0`、`cache_read_input_tokens: 0`、`cache_creation.ephemeral_5m_input_tokens: 0`、`cache_creation.ephemeral_1h_input_tokens: 0` |
| 其他 usage | `iterations`、`output_tokens_details.thinking_tokens: 0`、`service_tier: "standard"`、`inference_geo: "global"` |

**`stopReason` 与 `stop_reason` 是协议字段名差异；其他字段须按版本、模型和能力判断必选性。** 不能要求所有请求固定填充另一渠道的全部字段，也不能删除上游已经真实返回的扩展字段。JSON 对象字段顺序不作为故障。

本会话没有客户这次请求的原始网络报文、实际渠道和逐跳关联记录，不能仅凭这两段展示 JSON 确认是客户端二次序列化、网关转换还是上游造成。

## 3. 本地代码核对与复现

10 月 17 日查阅的本地 HEAD 为 `ea7f21e8c`，工作区另有未提交改动；它仅作为本地检索起点，不是线上发布版本证明。

| 路径 | 已核对行为 | 定位意义 |
| --- | --- | --- |
| [Claude 入口](../../../router/relay-router.go) | `/v1/messages` 注册为 `RelayFormatClaude` | 只确定客户端请求和响应使用 Claude 协议 |
| [渠道适配选择](../../../relay/claude_handler.go) | `ClaudeHelper` 按 `info.ApiType` 取得适配器 | 同一入口仍可能命中不同上游协议 |
| [Claude 非流式响应](../../../relay/channel/claude/relay-claude.go) | 原生 Claude 分支 `responseData = data`，发送收到的原始响应字节 | 此分支不会因 DTO 缺少扩展字段而删减响应体 |
| 同文件中的原生流式分支 | 可补充 `message_delta.usage`，随后转发事件 | 流式不能概括成所有字段绝对逐字节不变，应单独比较改动范围 |
| [OpenAI 返回 Claude 的分支](../../../relay/channel/openai/relay-openai.go) | 调用 `ConvertResponse` 并重新序列化 | 跨协议转换可能缺少 Claude 扩展信息 |
| [Claude DTO](../../../relaykit/dto/claude.go) | `StopReason` 的 JSON tag 为 `stop_reason` | 当前 DTO 不会把该字段输出成 `stopReason` |
| [响应写出](../../../service/http.go) | `IOCopyBytesGracefully` 复制传入字节 | 不在这里重建 JSON 字段 |
| [Token 计数路由](../../../router/relay-router.go) | 10 月 17 日本地检查中 `/messages/count_tokens` 注册被注释 | 10 月 18 日两端线上该路径均实测 404，见最新复测 |

本会话曾用客户两份样本直接调用 `HandleClaudeResponseData` 的原生 Claude 非流式分支：两份写出结果均与输入字节完全一致。客户第一份的 `stopReason` 被原样保留，但 DTO 解析后的 `StopReason` 为空。这证明当前该分支不会自动修正非标准字段，也说明仅做成功转发不足以证明协议语义正确。该本地调用不等于完整 HTTP 链路或线上验收。

## 4. 历史线上测试快照

以下时间均为北京时间。数量直接来自本地汇总或既有测试记录，各批次独立，不能相加后称为不同用例数量。

| 批次 | 已记录结果 | 能说明什么 |
| --- | --- | --- |
| 10 月 13 日初测 | 标准入口 24 次、含 8 次流式，24 次 HTTP 200；另有单数路径 2 次 404；SDK 严格校验 23/24 通过 | 非流式工具 `input` 当时为字符串，严格类型校验失败；200 不等于兼容 |
| 10 月 14 日 10:40–10:44 修复前对照 | 上游 25 次、网关 19 次，合计 44 次 HTTP 200，含 13 次流式；SDK 41/44 通过，另记录 1 次早前传输失败 | 多个问题可在上游复现；19 组请求除模型名外保持一致，但不是同一次请求逐跳抓包 |
| 10 月 14 日 15:06 修复后网关复测 | 20 次 HTTP 200，含 6 次流式；SDK 20/20 通过 | 工具参数对象、已测 omitted 思考块保留问题修复；停止标记仍失败 |
| 10 月 14 日 15:14–15:15 停止标记对照 | 网关、上游前缀及无前缀路线各 3 次，共 9 次 HTTP 200，含 3 次流式 | 结束原因及命中标记仍有问题，无前缀非流式还未截断正文 |
| 10 月 14 日 19:37 签名直连对照 | 共 8 次请求；ccmax 起始 1 次成功、6 次回传均成功，无前缀起始 1 次 HTTP 502 | 原样签名可继续；单字符篡改和随机字符串在 JSON/SSE 下均被接受，负例检查失败 |

10 月 18 日新增两批：首轮保留 62 份 API 结果及 1 次 CLI 流程；新 Key 同批保留 107 份 API 结果（Lighting 51、上游主模型 55、ccmax 可用性探测 1）。旧 Key 的 401 已被新 Key 的正常调用解决。最新通过、未通过及未覆盖项目以 [两端复测报告](2026-10-18-两端Claude兼容复测报告.md) 为准，下面的 10 月 14 日缺陷表不代表当前故障清单。

### 现存缺陷与已修复范围

| 检查项 | 最新已有证据 | 后续验收 |
| --- | --- | --- |
| `stop_reason` 拼写 | 客户原始反馈为 `stopReason`；后续已测报文使用下划线格式 | 保留原始问题回归；不把后续样例当作该客户请求已修复证明 |
| 自定义停止 | 网关与 ccmax 正文为 `ALPHA`，却报 `end_turn`；`stop_sequence` 为空或缺失 | 必须同时核对正文、结束原因、命中标记 |
| 无前缀路线 | 停止标记非流式返回了完整 `ALPHA\|END\|OMEGA`；另一次思考工具起始 502 | 不得视为可替代的已通过路线 |
| 工具参数 | 修复后样例为对象，工具结果可回传继续 | 需保留 JSON/SSE 和完整工具循环回归 |
| 思考与签名 | omitted 保留已修复；错误签名仍能成功回传 | 保留有效签名正例，必须拒绝损坏签名；前缀绑定另按模型、账户和 beta 测试 |
| 缓存 | 已出现统计字段；尚未观察到足以确认缓存命中的证据 | 需要合格长度、相同前缀、同一缓存作用域内的真实写入及读取 |
| 截断与工具停止原因 | 已测样例能报告 `max_tokens`、`tool_use` | 扩展到号池切换和错误分支，不能推断其他原因也正确 |
| SSE 开始消息 | 缺 `stop_reason: null` 与 `stop_sequence: null` | 按冻结的官方协议版本复测 |
| 模型标识 | 非流式返回无前缀，流式曾返回 `ccmax/` 前缀 | 固定外部模型映射并验证两种模式一致 |
| 官方 SDK | 历史离线解析使用 `anthropic==1.13.0` | 增加冻结版本的实际 Claude Code；SDK 解析不是语义检查替代品 |

最小停止标记请求和逐条结果保留在 [现有测试记录的开发附录](Claude接口响应结构与停止标记验证.md#复现请求)。错误签名的请求 ID、有效签名来源及四次失败负例也保留在该记录中，不重复维护第二套明细。

修复后网关批次的 5m/1h 写入及重复请求共四份响应，`input_tokens` 均为 3294，`cache_creation_input_tokens` 和 `cache_read_input_tokens` 均为 0。这是尚未确认真实缓存生效的具体证据，不能据此推断缓存失效发生在哪一层。

## 5. 原始证据索引

测试机器上的资料根目录：

```text
/Users/randy/.codex/artifacts/claude-validation-20261013/
```

| 相对资料目录或文件 | 内容 |
| --- | --- |
| `summary.json`、`sdk_validation.json`、`*.request.json`、`*.result.json`、`*.raw` | 10 月 13 日矩阵、请求、响应与 SDK 检查 |
| `direct-20261014/summary.json` 及同目录报文 | 修复前直连上游与网关对照 |
| `gateway-retest-20261014-150600/summary.json` 及同目录报文 | 修复后 20 次网关复测 |
| `stop-attribution-20261014-151437/` | 三条路线的停止标记对照 |
| `signature-upstream-20261014-193732/summary.json` 及同目录报文 | 新鲜有效签名、篡改签名和随机签名对照 |

这些是本机定位路径，不是仓库内附件或保证长期可访问的下载地址。对外提供证据前须另行制作脱敏副本，不整包复制原始认证信息、会话和可回传的签名。

下一轮记录须把 `Lighting 请求 ID → 上游请求 ID → 脱敏号池账户/路线 ID` 关联起来。独立重放只能证明上游存在同类问题；归因某个客户请求仍需其实际路由及同次请求的逐跳证据。

10 月 18 日新 Key 同批证据目录：`/Users/randy/.codex/artifacts/claude-compat-20261018/paired-154921/`。其中上游直接复现了零上限、enabled、错误分类/流式错误包装、显式绑定及计数差异；模型列表另有格式差异。原始凭据不写入文档。
