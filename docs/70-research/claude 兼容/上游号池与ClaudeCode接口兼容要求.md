---
status: draft
owner: Dev Team
last-reviewed: 2026-10-18
---

# 上游号池与 Claude Code 接口兼容要求

**交付目标是：客户将 Claude Code 的 Anthropic 根地址切换到 Lighting 后，模型请求、响应结构及多轮工具行为符合对应 Claude API 合同。** 不能仅以“能聊天”“HTTP 200”或“官方 SDK 可解析”验收。

适用链路为 `Claude Code → Lighting → 当前上游号池 → 实际模型服务`。要求同时约束号池输出和 Lighting 对客户的最终输出。环境、历史结果和原始证据索引见 [Lighting 与上游号池环境及测试基线](Lighting与上游号池环境及测试基线.md)；本资料是本次对接的要求草案，不替代项目通用编码、安全或计费规范。

## 1. 一致性的范围与版本

Claude Code 是客户端；这里的“Claude Code API”指它调用的 Claude API，重点为 Anthropic Messages 的 JSON/SSE 及上下文、工具和思考语义，不把 claude.ai 登录、订阅后台、遥测等私有接口一并假定为号池必须实现的公开端点。

本次官方规范复核日期为 **2026-10-18**。“官网 API”在本文指 Anthropic 公开 Claude API；AWS Bedrock 使用其自身合同作为补充对照，不能把官转网站等同于官方直连。每次正式验收必须冻结以下组合：Claude Code 版本、Python/TypeScript SDK 版本、模型的实际版本和外部别名、`anthropic-version`、beta 集合、号池版本、Lighting 部署版本、账户/工作区及路由配置。官方资料会更新，文中的示例不是永久固定的 beta 白名单。

一致性按以下规则判定：

- 字段名、类型、必填字段、`null`、显式零值和内容块顺序按冻结合同检查；JSON 对象属性顺序和空白不影响一致性。
- 生成文本、响应 ID、签名字节、耗时和不同请求的 token 数不要求完全相同；停止、工具、签名验证、缓存和错误必须表达真实状态。
- 上游已经返回的扩展字段应保留。尚未支持或不适用的可选能力，按对应官方合同处理，禁止伪造 `iterations`、缓存命中、思考用量或区域来补齐外观。
- 无法等价实现的能力必须列为差异并阻止“完整一致”验收，不能静默移除参数、关闭思考、改用其他模型或把错误转成正常回复。

官方说明 `ANTHROPIC_BASE_URL` 接入会使 Claude Code 将网关视为 Anthropic 格式端点，因此实际后端是否需要跨协议转换由网关负责。参见 [官方网关概述](https://code.claude.com/docs/en/llm-gateway) 与 [兼容指南](https://code.claude.com/docs/en/llm-gateway-protocol)。以下 R01–R12、T01–T22 为本项目验收条目。

### 本次复核补充的判定规则

1. **改签名与改显示文字分开判断。** 官方明确：回传 `display: "omitted"` 思考块时，服务端从签名恢复思考，回传的 `thinking` 文字会被忽略。因此，原签名不变、只向空 `thinking` 填字后得到 200，不能判为签名校验失效。客户端仍须原样回传。不能把这个特例推广为任意模型、任意显示模式下“改思考文字必须 200”。[官方 omitted 规则](https://platform.claude.com/docs/en/build-with-claude/thinking)
2. **损坏签名仍须拒绝。** 签名篡改与合法签名的前缀不匹配不同；`drop_block` 不得用来把损坏签名当作通过。启用 binding beta 后，检查 `input_transformations`，区分保留、因模型/前缀/组织不匹配而丢弃等情况。[官方思考排错](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting)、[AWS preserved thinking](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-thinking-block-binding.html)
3. **签名长度、消息 ID 外观和相同错误文案不作为来源认证。** 上游的 [10 月 17 日八项报告](八项对照报告_现签_vs_官KEY_20261017.md) 是供应方提交的测试证据，需要本轮独立复测；即使负例均为 400，也不能单靠外观或文案证明报错一定由 Anthropic 官方服务器产生。正式验收仍需官方参考环境和可关联的实际路线记录。
4. **模型特有的拒绝行为也要一致。** Fable 5.1 对 `thinking: enabled/disabled`、强制工具选择 `any/tool`、不支持的采样参数有明确限制，不能通过静默删除、改写参数得到 200。[Fable 5.1 官方变化说明](https://platform.claude.com/docs/en/models/fable-5-1/whats-new-fable-5-1)

上述为本轮查阅后补齐的验收口径；官方页面未提供逐条更新时间，因此不据抓取日期声称它们均在 10 月 17–18 日新增。最新实测见 [两端 Claude 兼容复测报告](2026-10-18-两端Claude兼容复测报告.md)。

## 2. 接口与请求要求

### R01 入口与客户端配置

| 项目 | 本次要求 |
| --- | --- |
| 生成接口 | 支持 `POST /v1/messages` 的 JSON 与 SSE；同一路径加 `?beta=true` 不能误路由 |
| Token 计数 | 完整交付必须支持 `POST /v1/messages/count_tokens`；不能返回 HTML、伪造固定数或用字符串长度冒充模型计数 |
| 错误路径 | `/v1/message` 保持明确的路径错误，不作为标准接口或偷偷返回模拟成功 |
| 模型列表 | 若启用 Claude Code 的网关模型发现，`GET /v1/models` 必须满足 Claude 格式、权限和分页；未启用时使用明确的模型配置 |
| 启动探测 | 识别 `HEAD /api/hello` 是预热探测；其被拒绝不等同于推理不可用，不混入模型成功率统计 |

**官方当前将 token 计数列为可选，缺失时 Claude Code 使用字符估算；把它列为完整交付要求是本项目的目标，不是官方对所有网关的最低要求。** 计数结果本身也可能与实际生成请求的输入用量略有差异，不作逐 token 相等断言。[网关兼容指南](https://code.claude.com/docs/en/llm-gateway-protocol)、[官方 Token 计数说明](https://platform.claude.com/docs/en/build-with-claude/token-counting)

模型列表不能只按 HTTP 200 验收。核对 `data` 中的 `type: "model"`、模型标识、时间/显示名及当前合同的能力信息，并核对 `first_id`、`last_id`、`has_more` 和游标行为；OpenAI 风格的 `object/success/data` 不能替代 Claude 合同。未知能力不得伪造为支持。[官方 Models API](https://platform.claude.com/docs/en/api/models/list)

CLI 测试配置示意，凭据由受控环境注入，不写入仓库：

```sh
export ANTHROPIC_BASE_URL='https://lightingtheword.com'
export ANTHROPIC_API_KEY="${LIGHTING_API_KEY}"
export ANTHROPIC_MODEL='claude-fable-5-1'
```

此示例对应已测试的 `x-api-key` 方式。另验收 Bearer 模式时使用 `ANTHROPIC_AUTH_TOKEN`，只启用本轮选定的凭据来源，记录实际认证方式。客户端网关凭据与号池服务凭据分别管理。[官方连接与凭据映射](https://code.claude.com/docs/en/llm-gateway-connect)

### R02 头部与能力协商

1. 保留请求中的 `anthropic-version` 与完整 `anthropic-beta` 值；不得把 beta 集合改成写死的旧列表。验证 HTTP 头大小写、多个能力值及 `?beta=true`。
2. 同一 beta 与其请求字段必须成对到达实际模型端。能力不支持时返回真实协议错误，不能剥掉字段后假装支持。
3. 新增 `anthropic-*` 头和请求扩展不得被过时 DTO 自动丢弃；跨供应商协议需要明确的等价适配和回归。
4. `x-claude-code-session-id`、agent 及版本相关 hint 头可用于路由和关联；由客户端提供的值不是已认证的用户身份，也不替代权限检查。
5. 网关消耗客户凭据后，用对应上游配置认证；不得把 Lighting Key 当作号池 Key，或把它继续发送给无关服务。

R02 按官方 [网关请求头与扩展处理规则](https://code.claude.com/docs/en/llm-gateway-protocol) 验收；凭据方式按 R01 配置。本项目额外要求使用脱敏日志确认头部到达情况。

### R03 请求体无损与模型能力

| 请求部分 | 必须满足的行为 |
| --- | --- |
| `model` | 外部模型与上游路线映射可追溯；不得暗换能力较低的模型 |
| `system` 与消息 | 保留字符串/块数组的合法形式、顺序和内容，不拼接或重排系统块；多轮消息按对应合同解释 |
| `tools`、`tool_choice` | 保留 schema、工具类型及参数；支持情况按目标模型及 beta 验证 |
| `thinking`、`output_config` | 保留 adaptive、显示模式、effort 和已启用的输出约束；非法组合按真实模型合同报错 |
| `cache_control` | 保留所有合法位置、断点与 TTL；不能转换内容结构后丢失缓存语义 |
| `context_management` | 连同能力头转发，保留清理/压缩结果，不静默删除 |
| `max_tokens`、`stream` 及其他可选标量 | 区分缺省、显式 `0`、`false` 与非法值；不得因重新序列化自动吞掉显式值 |
| 图片、文档及扩展块 | 对实际开放的能力保持类型、来源与顺序；不支持时明确失败 |

官方当前 `max_tokens` 允许 `0` 用于预热缓存，应按冻结模型的合同单独测试，不能一律把零改成默认值。安全上限仍执行项目既有校验，不为透传绕过限制。[Messages API 请求合同](https://platform.claude.com/docs/en/api/messages/create)

Fable 5.1 当前官方只接受 adaptive，不能套用旧模型的 `thinking.type: enabled` 或 `disabled`。`tool_choice.type: any/tool` 应返回 400；`auto/none` 另测。非默认 `temperature`、`top_p` 和 `top_k` 按该模型最新合同拒绝，不能因字段未透传而假成功。测试应覆盖 omitted 与 summarized；简单请求可能没有思考块，不能仅凭缺少块判错。若实际返回 omitted 思考块，空 `thinking` 和有效签名必须保留。[官方思考配置排错](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting)、[显示模式说明](https://platform.claude.com/docs/en/build-with-claude/thinking)

Claude Code 的完整请求还可能包含会话中间的 system 消息、工具搜索字段和压缩设置，不能只依据最小聊天示例冻结请求 schema。[会话中间系统消息](https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages)、[上下文编辑](https://platform.claude.com/docs/en/build-with-claude/context-editing)

## 3. 响应与状态机要求

### R04 非流式结构

稳定字段须使用 Claude 命名：`id`、`type: "message"`、`role: "assistant"`、`model`、内容块数组、`stop_reason`、`stop_sequence` 和 `usage`。**不得输出 `stopReason` 替代 `stop_reason`。** 完成响应的结束原因与流式开始阶段的空值分别验证。

对 `container`、`stop_details`、`diagnostics`、`context_management`、`iterations`、用量细分等，依据冻结合同决定是否应出现、可为空或可省略。上游真实返回时不因网关类型定义缺失而删掉；缺省时不参考其他渠道样本捏造值。[官方响应类型](https://platform.claude.com/docs/en/api/messages/create)

字段验收须分别记录：核心消息字段、内容块专属字段、能力启用后才要求的字段，以及可选扩展。`text` 块核对 `text` 字符串；`thinking` 块核对 `thinking/signature`；`tool_use` 块核对 `id/name/input`，且 `input` 为对象。未使用容器/诊断等能力时，不单凭某个示例含有对应字段判定当前响应缺字段；SDK 允许省略也不等于线上的原始报文已经完全一致。类型可解析、字段语义正确和同一请求的转发无损，分别给出结论。[官方 Message 类型](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/message.py)、[官方 Usage 类型](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/usage.py)

### R05 停止原因必须符合实际

| 实际事件 | 必须返回或保持的含义 |
| --- | --- |
| 自然完成 | `end_turn`，没有命中的 `stop_sequence` |
| 达到输出上限 | `max_tokens`，不能假报自然完成 |
| 命中自定义标记 | `stop_sequence`，并返回实际命中的标记；正文不包含标记和后续内容 |
| 请求客户端工具 | `tool_use`，包含完整工具调用块，允许继续回传工具结果 |
| 目标模型的暂停、拒绝或窗口终止 | 保留对应的 `pause_turn`、`refusal`、`model_context_window_exceeded` 等合法原因，不统一降为 `end_turn` |

相同含义必须在 JSON 最终消息及 SSE 最后消息增量中一致。请求带了 `stop_sequences` 但没有命中时，仍可自然完成；不得靠“请求有该字段”强改结束原因。标记跨文本分片时也要正确处理。[官方自定义停止定义](https://platform.claude.com/docs/en/api/messages/create#body-stop_sequences)

历史回归请求为输出 `ALPHA|END|OMEGA`、设置 `stop_sequences: ["|END|"]`。确认命中后，必须同时得到 `ALPHA`、`stop_reason: "stop_sequence"`、`stop_sequence: "|END|"`。无标记、未命中、多标记和截断对照分别测试；模型未按控制提示生成目标串时记录未形成有效对照，不冒充协议通过。

### R06 SSE 顺序与持续交付

完整正常流应经历 `message_start`，按 index 关联的内容块 start/delta/stop，最后的 `message_delta` 和 `message_stop`。消息开始阶段核对 `stop_reason: null`、`stop_sequence: null`。`event` 与数据中的 `type` 对齐；不以 OpenAI `[DONE]` 替代 Claude 终态。

文本、工具参数和思考分别使用对应 delta。工具 JSON 分片允许暂时不完整，但聚合后 `input` 必须是对象。思考流保留签名增量及原有内容块顺序。`message_delta.usage` 为累计值，不得将多个累计帧直接累加。[官方流式协议](https://platform.claude.com/docs/en/build-with-claude/streaming)

字段对照以原始事件为准，不能只检查 SDK/自写脚本的最终对象。`message_delta` 事件中，`usage`、`context_management` 等可能与 `delta` 同级；不能仅合并 `delta` 后把其余字段漏收，再误判为网关删字段。绑定 beta 的 `input_transformations` 按官方规定检查 `message_start.message`，并保留服务端 fallback 后可能在最终增量更新的结果。[官方绑定返回位置](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking)

本项目还要求在真实反向代理链上验证增量可见、长思考心跳、慢读、非 ASCII 分片和中途断流。不能缓存到生成结束再一次性发给客户端；缺终态不得补一个虚假的成功结束。新事件不能让代理崩溃或截断流。

### R07 工具调用完整往返

非流式 `tool_use.input` 必须为 JSON 对象，不能是包着 JSON 的字符串。`id`、`name` 和参数类型完整；多工具并行不能串错 index 或 ID。

测试必须回传原 assistant 内容及相应 `tool_result`，核对 `tool_use_id`、错误工具结果和下一轮回答；不能只看到一个工具调用就算完成。缺失/不匹配结果须按协议报错，不自动补造成功结果。`strict`、`defer_loading` 和工具搜索按目标能力分别验收。[工具定义](https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools)、[工具结果处理](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)

### R08 思考签名与号池会话连续性

原样保存可回传的 `thinking`、`signature` 和 `redacted_thinking.data`；不能通过丢掉这些块来让多轮调用表面成功。签名是不可解释的协议数据，不要求不同请求生成相同字节。

签名分别验收四项：返回字段及 SSE 重组、真实签名正例与损坏负例、显式会话绑定、同一次请求的逐跳保存。前两项通过不能替代后两项。逐跳保存须关联 Lighting 收到的上游响应与客户响应，再关联客户回传与 Lighting 发往上游的请求；比较同一块解码后的签名字符串及块顺序。受控日志可记录长度与 SHA-256，普通报告不落完整签名。两端独立生成的长度/内容不同不是转发损坏证据；仅比对原始 SSE 与本地聚合也不能证明网关逐跳无损。

必须测试：从本轮真实 JSON 和 SSE 响应分别取得签名后，原样回传成功；单字符篡改、截断、空值、删除字段和随机字符串分别形成拒绝负例，JSON/SSE 均覆盖。各负例只改目标签名字段；流式响应携带的签名先按事件完整聚合。没有取得带签名的真实块时记为“未形成对照”，不能人工补一个块冒充有效签名正例。已经损坏的签名不能靠 `prefix_mismatch_behavior: "drop_block"` 变成“校验成功”。

当前官方还说明 Fable 5.1 等模型有前缀绑定，执行范围与账户创建时间、显式 binding 配置有关。**签名损坏与合法签名的前缀变化必须分组测试**；对支持的账户/beta 再验证前缀不变、变更以及显式 `drop_block`，不得把历史四次错误签名被接受解释为已经支持绑定恢复。[官方签名及前缀绑定排错](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting)

号池提供方须交付轮换测试：同一会话固定路线、连续请求切换不同账号、故障后切换账号及模型别名。若实际供应商存在账户/区域绑定，应采用保证原块有效的调度策略；不能静默清空思考、上下文或工具历史。具体采用何种粘性策略由提供方证明，不预设“所有签名必然跨账号可用”。

## 4. 缓存、错误与号池运行要求

### R09 真实缓存及用量

5 分钟和 1 小时缓存分别验证写入与相同前缀的读取。请求长度须达到目标模型门槛；按本次官方资料 Fable 5.1 为 512 tokens，正式运行仍须复核冻结版本。前缀、模型、作用域和 TTL 满足条件后，才以真实 `cache_creation_input_tokens`、`cache_read_input_tokens` 及 TTL 明细判断缓存。

字段为零只能证明字段出现，不能证明命中。号池轮换、账户/工作区与区域变化后的命中边界必须记录；不承诺未经证明的跨账户共享，不借缓存混用客户数据。[官方缓存说明](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)

对外返回权威的输入、输出、缓存创建和缓存读取用量；存在 `thinking_tokens` 等细分时保留，但不重复叠加到其所属总量。JSON 与 SSE 聚合保持相同语义。请求实际有费用但结果不完整时，按项目既有结算、失败及对账入口处理，禁止凭字段缺失伪造零用量；实现时必须遵循 [项目计费规则](../../../.agents/rules/billing.md)，这里不另建计费算法。

### R10 错误、状态码与重试

保留 Claude `type: "error"`、`error.type`、`error.message` 和可用请求关联信息。参数/签名错误须返回相应 `invalid_request_error`；`<nil>`、空类型或网关私有 `new_api_error` 不能作为“与官方完整一致”的结果，即使 HTTP 状态码正确。覆盖参数错误、无效凭据、无权访问、模型/资源不存在、请求过大、限流、内部错误及过载，对照冻结官方合同处理状态码；不能全部转成 HTTP 200 或同一种 500。[官方错误合同](https://platform.claude.com/docs/en/api/errors)

已经开始的 SSE 若失败，应保留真实错误事件或中断事实，不能继续宣告完成。头部至少核对正确的 `Content-Type`、`retry-after`、`x-should-retry` 和实际返回的限流信息；不能冒充某个账户计划的使用上限。错误中的能力与签名说明应足以使 Claude Code 执行其恢复流程，避免改写成不相关的通用文案。流开始前的失败须保持可解析的 JSON 错误及匹配的 Content-Type；不能只因请求 `stream: true` 就把 JSON 错误标成 SSE。按当前官方合同，504 使用 `timeout_error`，响应的标准 `request-id` 与错误体的 `request_id` 用于原生追踪，私有请求头可额外保留，但不能冒充这些标准字段。

号池内部重试、Lighting 重试及 Claude Code 重试必须可关联。已向客户输出部分流后，不把另一个账号的新回答拼接到原流中。一次最终请求可关联多个尝试，但不能出现重复输出、重复本地结算，或把有费用但未知的尝试当成未发生。

### R11 模型身份与能力一致

`claude-fable-5-1` 与 `ccmax/claude-fable-5-1` 的关系须由运营方确认，形成可追溯配置。外部响应模型名按公布的映射规则保持 JSON/SSE 一致；不凭模型名前缀推断真实模型和支持能力。

每条生产可路由的号池路线分别验收，包括主路由、备用路由与故障切换。未通过签名、停止、工具或能力协商检查的路线不得作为“完全兼容”的备用号。外部模型别名还须在 Claude Code 中检验实际能力选择，不只在 curl 中验证能够请求。

### R12 上下文与实际 Claude Code 行为

用冻结版本的真实 Claude Code，在隔离测试目录完成读取文件、提出变更、调用测试工具、接收工具结果后继续回答的完整流程；目录仅包含测试数据。检查后台请求、会话恢复、工具搜索与上下文收敛，记录请求类型而非假定每个请求都等同于主聊天。

`context_management`、清理结果和压缩后的继续对话分别验收；客户端重建前缀、服务端编辑和服务端压缩不能混为同一种签名场景。模型窗口与输出能力按实际配置证明，不把别名推断的默认值当作供应商承诺。[上下文编辑](https://platform.claude.com/docs/en/build-with-claude/context-editing)、[官方压缩说明](https://platform.claude.com/docs/en/build-with-claude/compaction)

## 5. 必须执行的验收矩阵

后续各中转站的详细操作、原子变体、执行优先级和结果格式统一使用 [中转站 Claude 接口验收测试用例](中转站Claude接口验收测试用例.md)。下表 T01–T22 及其早期 P 分级保留为要求追溯及历史覆盖摘要，不单独维护另一套操作步骤或执行清单。

下列检查分别在上游直连与 Lighting 客户入口运行；具备官方参考环境的项目，再执行相同模型/版本的官方对照。差异只允许出现在预先记录的凭据、域名和模型映射，不能改变关键请求内容来掩盖行为差异。

P0 为阻止兼容交付的缺陷或核心流程；P1 为完整交付要求。某条路线宣称的能力不适用时，必须写出合同依据与影响；不能以“未测”代替通过。

| 编号 | 级别与要求 | 测试输入或场景 | 必须检查的结果 | 已有证据 |
| --- | --- | --- | --- | --- |
| T01 | P0，R01/R04 | 最小 JSON、`?beta=true`、错误单数路径 | 正确路由、下划线字段、类型/null；错误路径失败 | 10 月 18 日两端基础和 beta 查询通过；单数路径均 404，上游首次超时后独立重测 |
| T02 | P0，R04/R06 | 基础 SSE，含中文和分片 | 类型/index/终态完整，开始 null、增量可见 | 10 月 18 日两端已测成功流顺序/开始 null 通过；长流、慢读和指定字节分片仍待验收 |
| T03 | P0，R05 | 输出上限很小，JSON/SSE | 实际触顶为 `max_tokens` | 已测样例通过，须保留 |
| T04 | P0，R05 | `ALPHA\|END\|OMEGA`，命中 `\|END\|` | 正文、`stop_sequence` 原因及标记三项同时正确 | 10 月 14 日失败；10 月 18 日新 Key 同批测试两端命中/多标记均通过 |
| T05 | P0，R05 | 无标记、未命中、多标记、标记跨分片 | 自然结束不误报；命中值准确 | 10 月 18 日两端未命中/多标记通过；跨上游分片边界未专门构造 |
| T06 | P0，R07 | 对象参数、嵌套/布尔/数字/转义字符，JSON/SSE | `input` 对象，聚合无损 | 10 月 18 日两端对象/嵌套/0/false/中文/转义样例通过 |
| T07 | P0，R07/R12 | 多轮工具、并行多工具及错误结果 | ID 关联正确，继续调用和回答正确 | 10 月 18 日两端嵌套工具往返通过；并行及工具错误分支待验收 |
| T08 | P0，R03/R08 | adaptive + omitted/summarized | 合法块及签名保留；不把空思考当无效块 | 已修复样例通过 |
| T09 | P0，R08 | 新鲜有效签名原样回传 | 同模型同前缀继续成功 | 10 月 18 日新 Key 同批两端新鲜 JSON/SSE 签名均通过 |
| T10 | P0，R08 | 单字符篡改、随机、截断、空签名，JSON/SSE | 拒绝损坏签名，不静默清空历史 | 10 月 18 日两端五类负例各 10 次均拒绝，但错误类型和流式 Content-Type 都不符合合同 |
| T11 | P1，R08 | 合法签名改变前缀、显式 binding/drop 配置 | 按账户/模型/beta 执行，不混淆签名损坏 | 两端显式 error 得 200、drop 缺变换记录；上游已复现，待逐跳定位 |
| T12 | P1，R03/R09 | 足长相同前缀，5m/1h 写入及读取 | 真实命中及 TTL 统计，非全零外观 | 两端 5m/1h 均有读取；上游独立 1h 冷写补测被拒绝，未形成正常对照；TTL/轮换未测 |
| T13 | P1，R01/R03 | Token 计数含 tools/system；合法 `max_tokens: 0` 预热 | 模型计数、无生成副作用；零不改为默认 | 10 月 18 日两端 count 均 404、零上限均生成正文，未通过 |
| T14 | P0，R02/R03 | 实际客户端 beta、未知扩展合法透传、非法配置 | 能力头与字段成对；错误不被掩盖 | 10 月 18 日两端 enabled/零值/错误体均有差异；其余见复测报告 |
| T15 | P0，R08/R11 | 同会话轮换账号、备用路线、上游失败切换 | 模型/工具/签名连续，不悄悄丢块 | 当前无前缀主路线可用；新 Key 的 ccmax 探测为 503；真实账号切换待验收 |
| T16 | P0，R06/R10 | 长思考、心跳、主动断流、上游 error | 不缓冲、不假成功、不拼接重放；费用可追溯 | 待验收 |
| T17 | P0，R10 | 参数、鉴权、权限、限流、请求过大及过载 | 状态码、错误体和重试头语义正确 | 10 月 18 日参数/签名错误已测，错误类型/Content-Type/追踪字段失败；其余分支待验收 |
| T18 | P1，R04/R09/R11 | 扩展字段/null/零值、JSON/SSE usage | 不删真实扩展，不造字段，不重复累计 | 10 月 18 日两端已测成功结构/模型名改善；错误/模型元数据仍有差异 |
| T19 | P0，R12 | 真实 Claude Code 完整编码工具循环 | 无协议错误，工具结果驱动后续动作 | 10 月 18 日 Claude Code 2.1.221 bare 模式读改测循环通过；完整功能仍待验收 |
| T20 | P1，R03/R07/R12 | 工具搜索、strict、上下文编辑及压缩继续 | 已开放能力正确，无静默降级 | 待验收 |
| T21 | P1，R01/R11 | 模型发现/手动模型配置、别名与后台模型 | 权限、列表格式、所选模型和能力一致 | 上游列表为 OpenAI 格式；Lighting 有基本 Claude 外层，完整能力/分页仍未验收 |
| T22 | P1，R04/R06/R10 | 固定 SDK JSON 严格校验及 SSE 聚合、C# 原始响应对照 | 解析通过；原始 HTTP 与客户端对象差异可定位 | Python 1.13.0 同批 67 份 HTTP 200 消息解析通过，含 2 份 refusal；C# 待验收 |

`redacted_thinking`、拒绝、暂停、窗口终止等不一定能由模型提示稳定触发：应使用供应商可重复的真实样本，另辅以原始报文 fixture 保护转发与解析。图片/文档使用固定的受控输入验证。fixture 通过不能冒充模型端已经验证。P0/P1 只表示本次交付优先级，不改变官方字段的可选性。

## 6. 归因、证据与交付门槛

### 逐跳记录

对失败用例记录以下三处的脱敏原始 HTTP，而不是仅保存 SDK 对象重新序列化的 JSON：

```text
Claude Code 发出的请求与收到的响应
  ↔ Lighting 发给上游及从上游收到的报文
  ↔ 号池实际路线的请求、响应和尝试记录
```

保存时间/时区、测试编号、版本组合、脱敏 route/account ID、网关/上游 request ID、请求参数与能力头、状态码/必要响应头、原始 JSON/SSE、首字节及终态时间、SDK 聚合结果和语义断言。认证头值、真实客户代码与私人对话不进入仓库；完整签名等可回传材料在受控证据库保存。

归因规则：上游返回的原始响应已错误，由号池提供方修复；上游正确、Lighting 输出发生错误变化，由我方修复；Lighting 原始 HTTP 正确、客户端展示变化，检查客户端映射与序列化。独立对照只证明同类缺陷存在，不能冒充同次请求逐跳归因。修正字段名不能替代停止、工具与签名语义修复。

### 交付清单

1. 号池方交付版本、路线能力表、修复说明和对应 T 编号证据；Lighting 交付部署及路由配置快照，补齐环境表中的未知项。
2. 所有 P0 通过；完整交付的 P1 全部通过或有明确的不适用合同依据。仍需客户端关闭能力才能运行的项目不能标为该能力完全一致。
3. 在修复版上重新执行两端矩阵和真实 Claude Code 流程；旧版本 HTTP 200、旧版 SDK 20/20 或口头说明不作修复后的证据。
4. 在发布前核验生产所有可命中路线及切换路径；保留回退策略，失败路线不得继续以“完全兼容”对外提供。

### 蒸馏采集附加验收

以下是用于训练数据采集时的要求，不改变官方协议，不将所有兼容差异等同于训练样本损坏。严重程度及本轮证据统一见 [复测报告的蒸馏影响分级](2026-10-18-两端Claude兼容复测报告.md#面向蒸馏采集的问题严重程度)，其中 S0–S3 表示影响，P0/P1 仍表示协议交付优先级。

1. **冻结训练目标。** 最终答案、工具轨迹、可见思考摘要分别定义数据类型；omitted 空文字、summarized 摘要和不透明签名均不能标成完整原始思维链。logprobs/logits 属于另需确认的训练信号，usage 不提供其替代品。不得要求号池绕过官方思考绑定来满足采集。[官方思考显示边界](https://platform.claude.com/docs/en/build-with-claude/thinking)、[Preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking)
2. **记录实际生成来源及条件。** 保存采集批次、会话/轮次、输入快照或可核验指纹、请求模型/已确认实际模型版本、路线/脱敏账户、beta、thinking/display/effort、输出约束及请求关联 ID。未知的实际来源标未知，不能用外部模型字符串自动认证；客户端请求参数和服务端已证实执行条件分别记录。
3. **样本类型与协议状态分开标注。** 普通答案、工具调用、工具结果、可见思考摘要、拒绝、截断、协议错误和传输失败分别保存。`refusal` 不自动视为代理错误；除非目标就是拒绝行为训练，否则不混作普通答案正例。`max_tokens` 截断可作为专门数据类型，但不能标成完整回答；SSE 缺终态时隔离。零上限预热不产出答案样本。
4. **轨迹对应可核查。** 保留完整内容块顺序、工具 schema、工具 ID/参数和对应结果；区分原始历史与清理/压缩后实际上下文。binding error、drop 及 `input_transformations` 要能解释输出所用上下文；未解决的显式绑定差异样本不进入忠实轨迹集。签名用于受控重放和传输校验，不作为可读训练目标。
5. **尝试与入库去重。** 分别记录同一逻辑请求的每次尝试及最终选中响应；不能把内部重试的多份输出拼成一个答案或当成独立采样而不标注。训练/评估切分按会话和题目来源分组，避免同一轨迹或重试副本跨集。保留失败及过滤数量，使只保留成功样本产生的选择偏差可见。
6. **以训练侧评测补齐协议测试。** 使用学生模型 tokenizer 计算训练长度，不拿教师 usage 代替；用受控题目检查答案正确性、工具任务结果、摘要与答案的关系及长轨迹质量。先做小批验收，再决定扩大采集。当前 107 份接口记录及 67 份 SDK 可解析消息不是已经合格的训练集，也不是“107 条训练正例”。

`thinking: enabled` 本轮负例还同时使用 `budget_tokens: 1024` 和 `max_tokens: 128`，存在第二项不合法条件。其返回 200 足以说明“不合法配置未被正确拒绝”，但不能单独证明 enabled、budget 中哪一字段被删改或由哪层处理。补验应保持其他参数合法，只改目标字段；不得通过接受不合法请求来认证采集配置已经生效。

**10 月 18 日已用新 Key 完成报告列出的上游与 Lighting 同批对照；停止标记、签名拒绝、工具往返已有通过证据，但完整一致验收仍未完成。** 两端在零上限、enabled、错误、绑定和计数等项目上均有差异，上游接口侧已直接复现。用户确认暂无官方账户环境，因此这是官方文档对照实测。历史失败不能自动当作当前失败，供应方报告也不替代本方两端复测。本轮没有实施或发布网关修复。

## 7. 官方资料

以下官方参考在 2026-10-18 重新核对，正式验收仍按冻结版本执行：

| 资料 | 用途 |
| --- | --- |
| [Claude Code 网关概述](https://code.claude.com/docs/en/llm-gateway) | 客户端与网关、供应商的关系 |
| [Claude Code 网关兼容指南](https://code.claude.com/docs/en/llm-gateway-protocol) | 端点、能力协商、头部、模型发现及客户端行为 |
| [连接网关](https://code.claude.com/docs/en/llm-gateway-connect) | 根地址和凭据变量 |
| [Messages API](https://platform.claude.com/docs/en/api/messages/create) | 请求、响应及停止标记定义 |
| [流式协议](https://platform.claude.com/docs/en/build-with-claude/streaming) | SSE 事件与累计用量 |
| [工具定义](https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools) 与 [工具结果处理](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls) | 工具 schema 和多轮往返 |
| [思考模式](https://platform.claude.com/docs/en/build-with-claude/thinking) 与 [思考排错](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting) | 显示、签名及前缀绑定 |
| [Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) | TTL、长度门槛与真实命中 |
| [Token counting](https://platform.claude.com/docs/en/build-with-claude/token-counting) | 计数语义与局限 |
| [上下文编辑](https://platform.claude.com/docs/en/build-with-claude/context-editing) 与 [压缩](https://platform.claude.com/docs/en/build-with-claude/compaction) | 长会话与清理恢复 |
| [错误合同](https://platform.claude.com/docs/en/api/errors) | 状态码、错误体及流式失败 |

本要求不依赖未公开客户端实现、社区逆向猜测或来源不明的“官方同款”承诺。号池必须在供应商允许的接入和账户范围内实现上述行为，不以绕过登录、限额或验证替代兼容。

补充参考：[Fable 5.1 变化说明](https://platform.claude.com/docs/en/models/fable-5-1/whats-new-fable-5-1)、[Preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking)、[AWS Bedrock preserved thinking](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-thinking-block-binding.html)。
