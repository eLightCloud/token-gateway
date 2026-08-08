---
status: open
owner: Dev Team
last-reviewed: 2026-08-08
---

# 流式断开续读：dify/coze usageComplete 与估算回退分歧（遗留）

## 背景

关联实施方案：[80-dev/2026-08-08-客户侧超时Token对账最小方案](../80-dev/2026-08-08-客户侧超时Token对账最小方案.md)。

该方案的核心不变量：客户侧断开后，只有取得**完整权威上游 usage** 才能经结算门 `ValidateTextStreamCompletion` 结算；本地估算（`ResponseText2Usage`）不得冒充"已与上游对齐"通过该门。

OpenAI 包内已按统一原则修复并补回归（见上述 80-dev 方案）：

> `MarkUsageComplete()` / `TerminalSuccess(true)` 当且仅当"即将结算的 usage 来自权威上游、而非 `ResponseText2Usage` 本地估算"。

## 遗留问题

`dify` 与 `coze` 仍用宽口径 `dto.HasOpenAIUsageTokens` 标记 `usageComplete`，但其估算回退条件是 `TotalTokens == 0`。两谓词不等价，属于与 OpenAI 路径同一类的缺陷：估算值可能被标记为已对齐、从而通过 client-gone 结算门（overcharge 方向）。

### dify（可触发）

- 标记：`relay/channel/dify/relay-dify.go:246` `sr.TerminalSuccess(dto.HasOpenAIUsageTokens(usage))`
- 回退：`relay/channel/dify/relay-dify.go:265` `if usage.TotalTokens == 0 { usage = service.ResponseText2Usage(...) }`
- usage 来源：dify `message_end` 事件的 `metadata.usage`，通常带 `prompt_tokens/completion_tokens`。
- 触发条件：上游回传了 `prompt_tokens/completion_tokens` 但 `total_tokens` 缺省或为 0（上游不一致的边界）→ `HasOpenAIUsageTokens=true`（标记 complete）而 `TotalTokens==0`（走估算）→ client-gone → 门通过 → **以本地估算结算**。

### coze（当前不可触发，脆弱）

- 标记：`relay/channel/coze/relay-coze.go:135` `sr.TerminalSuccess(dto.HasOpenAIUsageTokens(usage))`
- 回退：`relay/channel/coze/relay-coze.go:143` `if usage.TotalTokens == 0 { usage = service.ResponseText2Usage(...) }`
- coze 的 usage 仅从 `TokenCount` 写入 `TotalTokens`，故 `HasOpenAIUsageTokens` 当前退化为 `TotalTokens!=0`，与回退条件恰好互补，**今天不可触发**；但一旦 coze 写入 `prompt_tokens/completion_tokens` 或 detail 字段，分歧立刻出现。

## 暂不处理理由

dify/coze 非当前重点渠道，业务上可接受其按现行为运行；优先级低于核心 OpenAI/Claude/Gemini 路径。登记于此，待相关渠道治理或例行清理时一并修复。

## 修复方案（与 OpenAI 包同手法）

1. 流内将 `sr.TerminalSuccess(dto.HasOpenAIUsageTokens(usage))` 改为 `sr.TerminalSuccess(false)`（只发终态、不标 complete）。
2. 在 `TotalTokens == 0` 估算回退决策**之后**，仅当回退未发生（`usage` 来自上游、非估算）时 `info.StreamStatus.MarkUsageComplete()`。
3. 各补一条回归测试：client-gone + 部分 usage（dify：`prompt/completion` 无 `total`；coze：detail-only）→ 断言 `IsUsageComplete()==false` 且 `ValidateTextStreamCompletion` 返回 error（落 `usage_missing` → 退款）。

## 验收

- dify/coze 在部分 usage + client-gone 下落 `usage_missing` 并退款，不以本地估算结算。
- 其余渠道不受影响：`xai`、`tencent` 标记与回退共用同一谓词（一致）；`gemini`、`claude`、`aws`、`xunfei`、`palm` 各自使用权威判定。

## 关联

- 实施方案与验收标准：[80-dev/2026-08-08-客户侧超时Token对账最小方案](../80-dev/2026-08-08-客户侧超时Token对账最小方案.md)
- 已修复的同类实例：`relay/channel/openai/` 下 `relay-openai.go`、`responses_via_chat.go`、`chat_via_responses.go`、`relay_responses.go`。
