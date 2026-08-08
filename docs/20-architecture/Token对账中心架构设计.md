---
status: current
owner: Dev Team
last-reviewed: 2026-08-09
---

# Token 账单与人工对账中心架构设计

## 1. 架构目标

本功能是一个面向 `root` 的只读账单查询与导出模块。它从现有数据库账单事实生成汇总、分组和明细，供管理员在系统外与上游人工对账。

架构优先级：

1. 与当前客户实际账单完全一致；
2. 与中转、Relay、计费和退款主链路完全解耦；
3. 查询、页面和导出共用同一口径；
4. 不创建第二套计量事实；
5. 不依赖上游 API、账单文件或网络连接；
6. SQLite、MySQL、PostgreSQL 与项目已支持的日志数据库行为一致。

## 2. 系统边界

```text
现有业务主链路
请求 -> 渠道选择 -> 上游调用 -> BillingSession -> Log / 退款
                                                        |
                                                        v
                                                  现有数据库
                                                        ^
                                                        |
只读账单工作台
页面 -> Root API -> Bill Query Service -> 现有账单查询
```

账单模块只依赖现有数据库模型和只读查询。业务主链路不得导入账单模块，Relay 和计费服务不得调用账单采集函数。

## 3. 依赖方向约束

允许：

```text
controller/bill -> service/bill -> model/log and existing billing models
```

禁止：

```text
relay/*              -> service/bill
service/quota.go     -> service/bill
service/text_quota.go -> service/bill
service/task_*.go    -> service/bill
controller/relay.go  -> service/bill
```

账单功能不能以“旁路”为名在主链路新增同步或异步写入。无论是 fail-open 还是异步写入，都会形成第二套事实口径，因此不允许。

## 4. 数据真源

### 4.1 唯一真源

账单数据从现有 `Log` 及现有计费领域已落库事实查询。汇总使用数据库中已结算的 `quota`、`prompt_tokens`、`completion_tokens` 和现有维度字段。

不使用：

- 当前模型价格重算历史账单；
- Relay 内存状态重建历史账单；
- 上游用量替换客户账单；
- `QuotaData` 替代详细账单；
- 新增 attempt、usage ledger 或快照表替代现有账单事实。

`summary` 同时返回当前 `LogConsumeEnabled` 状态。该字段只用于提示当前消费日志可能停止写入，不参与额度计算，也不伪造历史完整性结论。

### 4.2 事实错误的修复边界

当账单查询发现原始事实错误时：

1. 先证明原业务落库与实际结算不一致；
2. 在 BillingSession、消费日志、退款或任务结算的权威路径修复；
3. 为该业务口径增加回归测试；
4. 账单查询不增加补偿性分支或私有修正表。

## 5. 查询模型

### 5.1 统一筛选器

服务端定义一个共用账单筛选 DTO：

- `start_timestamp` / `end_timestamp`；
- `perspective`：`customer` / `upstream` / `api_address`；
- `type`：消费、退款；
- `organization_id`；
- `user_id`；
- `model_name`；
- `channel_id`；
- `api_address`：渠道 API 地址视角下按当前有效地址筛选，`__unknown__` 表示无法解析当前地址；
- `request_id`：同时精确匹配本地请求 ID 或已落库的上游请求 ID。

时间范围使用半开区间 `[start, end)`。交互查询最大 92 天。所有汇总、明细和导出均复用该筛选器。

`customer` 视角支持 `all/consume/refund`；`upstream` 与 `api_address` 视角在模型层强制只查 `LogTypeConsume`，不信任前端传入的账单类型。

### 5.2 聚合口径

同一组过滤条件下：

```text
consume_quota = sum(type = LogTypeConsume 的 log.quota)
refund_quota = -sum(type = LogTypeRefund 的 log.quota)
net_quota = consume_quota + refund_quota
request_count = count(进入当前账单口径的记录)
prompt_tokens = sum(log.prompt_tokens)
completion_tokens = sum(log.completion_tokens)
```

权威写入路径将消费和退款 `quota` 均以正数保存；账单 DTO 根据日志类型将退款投影为负数。数据库原记录不被修改。

### 5.3 账单类型边界

首版只识别消费与退款两类账单事实。`LogTypeSystem` 的当前权威写入路径只保存操作文本，不保存可计费 `quota`；将其当作“系统调整”会创造数据库中不存在的账单语义，因此明确排除。页面已直接展示消费、退款与净额指标，不再保留未渲染的“账单构成”数据结构。

组织筛选复用已有组织成员生效时间：`BillingStartAt` 优先，否则使用 `JoinedAt`，并以 `LeftAt` 作为排他结束时间。组织名称和渠道名称在明细查询后从主数据库批量回填；账单数值始终来自日志数据库。

### 5.4 明细

分组枚举表达完整业务粒度：`user`、`user_model`、`user_channel`、`channel`、`channel_model`、`upstream_channel`、`upstream_channel_model`。其中：

- `user_model` 必须按 `user_id + model_name` 聚合；
- `user_channel` 必须按 `user_id + channel_id` 聚合；
- `channel_model` 必须按 `channel_id + model_name` 聚合；
- `upstream_channel` 先在日志数据库按 `channel_id` 聚合，再关联主数据库当前有效 API 地址；最终行键包含 `api_address + channel_id`，不得把同地址下不同渠道合并。
- `upstream_channel_model` 先按 `channel_id + model_name` 聚合，再关联当前有效 API 地址；最终行键包含 `api_address + channel_id + model_name`，不得退化为纯地址与模型分组。

汇总使用服务端分页，数值均由数据库聚合，不把分页明细交给前端求和。按上游只将按 `channel_id` 聚合后的低基数结果加载到 Go，与当前渠道配置组合，绝不加载原始全量日志。

日志事实只有 `channel_id`，没有请求发生时的 API 地址快照。地址分组因此使用渠道当前 `GetBaseURL()` 有效值，统一去除末尾 `/`；已删除渠道和当前地址为空的渠道归为 `__unknown__`。该口径必须在 UI 中明确标注为“当前配置”，不能宣称还原历史发送地址。

分组行显式返回组成复合键的 `user_id/username/channel_id/channel_name/model_name/api_address` 字段。明细使用这些字段的交集过滤，而不是只根据当前按钮名称推导一个条件。页面仅在点击分组后加载稳定分页明细，不一次加载整个账期，不在浏览器中对明细重新汇总。

## 6. API 架构

后端使用独立只读路由组，统一经过 `middleware.RootAuth()`。建议保留 `/api/reconciliation` 作为页面兼容路径，但仅提供账单查询语义：

| 方法与路径 | 行为 |
| --- | --- |
| `GET /summary` | 账单总览 |
| `GET /groups` | 按完整对账主体复合粒度分页汇总 |
| `GET /entries` | 账单明细 |
| `GET /export.csv` | 按同一筛选口径流式导出 |

不得存在 POST、PUT、DELETE 对账领域路由，也不得存在 settings、sources、sync、runs、resolutions、snapshots、capture health 等端点。

## 7. 前端架构

前端保留 `web/src/features/reconciliation/` 作为独立 feature 边界，但收敛为已评审的方案 2 单页工作台：

- 一个路由入口；
- 一组 URL 可复现的筛选条件；
- “客户账单 / 渠道用量 / 按上游”三个视角；按上游内部支持“API 地址 × 渠道 / 上游 × 模型”切换；
- 总体指标、分组汇总、按需明细三个阅读层级；
- 一个导出操作；
- 无设置表单、无数据源管理、无差异处置、无关账操作。

`summary` 与 `groups` 使用 React Query 并行加载，共享同一标准化筛选对象，不形成页面级串行瀑布。视角、维度、筛选和汇总页码进入 URL；`entries` 只在明细抽屉打开后请求。页面沿用项目已有 `SectionPageLayout`、shadcn/ui、Tailwind token 和 Hugeicons，不引入新的设计系统或页面运行时。

## 8. 导出架构

CSV 导出复用明细查询的过滤器和行投影，分批读取并通过 HTTP 响应输出，不落库导出任务、不保存 artifact、不创建快照。

导出前再次执行 Root 权限检查。CSV 字段使用明确白名单，不输出 `Log.Other` 原始 JSON、IP、密钥或业务正文。

## 9. 数据库兼容与性能

- 复用现有 GORM 查询和日志数据库分支；
- 不为账单功能新增业务表；
- 如需索引，只能根据经验证的查询计划增加最小索引，且必须验证 SQLite、MySQL 和 PostgreSQL；
- 不将全量日志加载到 Go 内存再聚合；
- 按上游只将按 `channel_id` 或 `channel_id + model_name` 聚合后的低基数结果加载到 Go，与主数据库当前渠道配置组合成“API 地址 × 渠道”或“上游 × 模型”行；
- 不在前端对分页数据伪造全局总计；
- 查询超时或失败只影响账单页面，不影响业务请求。

## 10. 禁止的架构对象

修正后的实现不应包含：

- `RecAttempt`、`RecUsageRecord`、`RecCaptureNode`、`RecCaptureGap`；
- `RecSource`、`RecSyncRun` 和 Provider Connector；
- `RecRun`、`RecResultRow`、`RecResolution`、`RecSnapshot`；
- `MarkReconciliationAttemptDispatched`、`AttachReconciliationUsage`；
- reconciliation mode、shadow capture、workspace enablement；
- 上游凭证 HMAC、连接器 secret 与对账专用密钥；
- reconciliation SystemTask、同步任务、导出 artifact 任务。

## 11. 测试策略

后端测试保护：

- 同一 fixture 下直接数据库求和与 summary 一致；
- 消费原始额度减去退款原始额度与账单净额一致；
- 渠道视角只使用消费事实，与同条件按渠道/模型直接聚合一致；
- 客户 × 模型、客户 × 渠道、渠道 × 模型分别保留客户或渠道主体，详情过滤使用全部复合键；
- 按上游只使用消费事实，同一当前 API 地址下的不同渠道保持为不同行；上游 × 模型继续保留地址、渠道和模型三重主体，详情使用完整交集过滤；
- 分组汇总分页不重复、不遗漏，分组合计与总览口径一致；
- 消费额度与退款额度之和与净额一致；
- 分页不重复、不遗漏；
- CSV 与同条件明细一致；
- 非 root 无法访问；
- 时间范围与无效筛选在服务端被拒绝。

前端测试保护：

- 筛选器与 URL 同步；
- 加载、空数据和错误状态；
- 总体指标、分组汇总和明细分页；
- 导出携带当前筛选；
- 页面不出现上游数据源、成本、采集、差异处置和关账操作。
