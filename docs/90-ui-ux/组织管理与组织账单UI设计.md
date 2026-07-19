---
status: current
owner: Dev Team
last-reviewed: 2026-07-19
---

# 组织管理与组织账单 UI 设计

## 文档边界

本文只定义界面信息架构、页面状态、交互和呈现，不重复定义组织数据、权限或账单算法。组织领域设计以 [组织与组织账单架构设计](../20-architecture/组织与组织账单架构设计.md) 为准。

界面采用简洁后台工作台风格：表格、紧凑筛选、摘要指标和明确操作优先，不把组织页做成营销页或复杂财务中台。

## 当前状态

基础组织管理、Billing、成员和日志页面已经上线。本次候选版本新增 Invoice 页面和结算系数配置；代码与自动化验证已完成，实际数据库环境与生产规模验证仍是发布前检查项。

## 设计基础

当前默认前端使用：

- React 19、TanStack Router、React Query。
- Base UI/shadcn `base-nova` 组合，CSS Variables 与 Tailwind CSS。
- Hugeicons 图标。
- `@/components/ui` 中现有 Button、Input、NativeSelect、Table、Badge、Dialog、Empty 等组件。
- i18next；所有用户可见文本使用 `useTranslation()` 和 `t()`。

组织 feature 由 `api.ts`、`types.ts`、`index.tsx` 以及独立的 `invoice.tsx`、`invoice-api.ts`、`invoice-types.ts`、`beijing-time.ts` 组成，路由文件保持薄层。

## 信息架构

### 用户侧

侧边栏只有在 `/api/organization/self` 返回当前组织时才显示组织分组：

```text
Organization billing  -> /organization/usage
Organization invoice  -> /organization/invoice
Organization members  -> /organization/members
Organization logs     -> /organization/logs
```

目标可见性：

| 角色 | Billing | Invoice | Members | Logs |
|---|---:|---:|---:|---:|
| Admin | 是 | 是 | 是 | 是 |
| Member | 只看本人 | 否 | 否 | 只看本人 |
| 未加入组织 | 不显示入口 | 不显示入口 | 不显示入口 | 不显示入口 |

### 管理员侧

```text
/admin/organizations       组织列表
/admin/organizations/:id   组织详情
```

组织详情保持四个一级 Tab：

```text
Members | Billing | Invoice | Logs
```

不增加独立 Overview 或 Settings 页面。名称和状态在设置弹窗中编辑，减少导航层级。

## 管理员组织列表

页面结构：

```text
Organizations                                  [Create organization]
Search organizations | Status                 [Refresh]

Name / ID | Status | Updated at | Manage
```

交互约定：

- 搜索覆盖组织名称和可解析的组织 ID。
- 状态筛选提供 All、Active、Suspended。
- 创建弹窗只输入组织名称；成功后创建空组织，再到详情添加 Admin 或 Member。
- 列表分页每页 20 条，搜索或状态变化时回到第一页。

## 管理员组织详情

页头展示组织名称、组织 ID、状态 Badge 和 Settings。

### Members Tab

```text
Members                         Active / Include removed  [Add member]

User | Role | Joined at | Status | Actions
```

- Add member 先搜索用户，再选择角色。
- 角色选项固定为 Admin、Member。
- Include removed 展示历史成员和离开状态。
- 移除使用危险操作确认对话框；成功后刷新组织详情、成员和列表缓存。

### Billing Tab

```text
Start date | End date | User | Model | Channel | Refresh | Export

Requests | Consumption amount | Prompt tokens | Completion tokens | Active members

Usage trend | Member usage
Model usage | Channel usage
```

四个区块共用同一筛选参数。Refresh 同时刷新 summary、trend、members、models、channels；Export 复用相同参数。

维度表展示：

```text
Dimension | Consumption amount | Share | Requests
          | Prompt tokens | Completion tokens | Tokens | Current pricing(模型表)
```

- Consumption amount 是用户首要关注字段，用户、模型、渠道都必须展示；它由历史 `total_quota` 换算，不重新计费。
- USD、CNY 和自定义货币沿用站点显示配置；TOKENS 模式仍显示 USD 等值。
- Share 为当前筛选窗口内维度 quota / summary quota；总额不大于 0 时显示 0%。
- Current pricing 只显示当前 Tiered、Fixed price 或 Ratio 摘要；固定价格使用货币格式，不暗示历史重算。

### Logs Tab

```text
Start date | End date | User | Model | Channel | Refresh | Export

Time | User | Model | Consumption amount | Prompt tokens | Completion tokens
```

实际页面列为 Time、User、Model、Consumption amount、Prompt tokens、Completion tokens，不显示内部 quota 和渠道。日志使用服务端分页；日期和时间均按北京时间展示。Logs 页 Export 使用 `billing/logs/display-export` 单表 CSV，与页面列口径一致；既有 `billing/logs/export` 保留原始排障列和时间戳。

Billing 页 Export 使用 `billing/export`。完整 CSV 的汇总、用户、模型、渠道、趋势和明细区块包含消费金额、币种和原始 quota；消费金额为不带货币符号的 6 位小数。Logs 和 Billing 明细均由后端有界流式输出。

### Invoice Tab

```text
Start date | End date | Current month | Previous month | Apply
Refresh | Export CSV | Configure factors

Gross amount | Settled amount | Billing period (Beijing time)
Model category settlement summary
AI model usage summary
```

- 默认打开当前北京时间自然月；支持本月、上月和自定义开始/结束日期。
- 两张交叉表横向滚动，模型/类别列 sticky；零金额单元格显示 `-`。
- 内置类别名称走 i18n，fallback 直接显示模型名。
- 多月存在多档规则时，系数列展示月份与系数列表。
- Export 使用 Invoice 专用 CSV，不包含日志内容、请求 ID 或上游请求 ID。
- Configure factors 打开 Sheet，只展示本组织使用过的类别。输入范围 `0.0000` 至 `10.0000`；零系数、恢复默认和历史月份都有明确确认/提示。
- 查询失败显示可重试错误状态；HTTP 409 后刷新规则版本，避免继续用旧版本提交。

## 用户侧页面

### Organization billing

Admin 可查看组织完整范围，Member 只查看本人范围：

- 与管理员相同的日期、成员、模型、渠道筛选。
- 摘要指标。
- 北京时间日趋势。
- 模型和渠道维度表。
- 导出按钮。

### Organization invoice

只对组织 Admin 展示，页面结构与管理员 Invoice Tab 一致；组织由当前活动成员关系推导。Member 无入口，直接访问也由服务端拒绝。

当前用户页没有成员排行表；成员维度只在管理员详情 Billing Tab 展示。

### Organization members

Admin 可进入：

- Settings：当前用户侧只编辑名称，状态选择只在系统管理员详情中显示。
- Add member：只可选择 Admin、Member。
- Active / Include removed。
- Admin 可以维护其他成员的角色。

### Organization logs

Admin 和 Member 都可进入，筛选、分页和导出与管理员 Logs Tab 一致，但组织 ID 由当前登录用户推导；Member 的服务端范围强制为本人。

### 空状态与拒绝状态

- 当前组织查询加载中：边框内 Loading 区块。
- 未加入组织：No organization 空状态，并提示联系管理员添加。
- 角色不允许：No permission 空状态，不渲染敏感数据查询结果。
- 无表格数据：使用统一 No data 行，不保留空白区域。

## 筛选与时间

- 日期输入按北京时间日边界转换：开始日 00:00:00，结束日 23:59:59。
- Billing/Logs 默认日期为空，表示不额外限制账期；Invoice 独立默认当前北京时间自然月。
- 当前筛选保存在页面本地状态，没有同步到 URL search params。
- 修改任一筛选会把日志页码重置为 1。
- Billing 的 User、Model、Channel 使用下拉选择；Logs 收敛为 User、Model。界面显示用户名、模型名、渠道名，提交查询时保留相应参数合同。
- 下拉选项从当前用户可见的组织账单维度加载，系统管理员使用目标组织范围，普通 Member 不显示成员筛选。

## 响应式与可访问性

- 筛选区从单列开始，在 `sm` 变为两列，在大屏变为多列加操作区。
- 摘要指标从两列扩展到大屏五列。
- Billing 维度区在 `xl` 使用两列；窄屏纵向排列。
- 表格容器允许横向滚动，不在移动端截断审计字段。
- 图标按钮必须带 `sr-only` 文本或 `aria-label`。
- Dialog 需要标题、说明、可见提交状态和合理焦点顺序。
- 角色和状态不能只用颜色表达，必须同时显示文字。
- 键盘 focus、loading、disabled、empty 和 error 是功能状态，不得因视觉改版删除。

## 已上线版本边界

### 状态语义

UI 把 disabled 显示为 Suspended，但后端当前没有阻断组织访问或个人 API 使用。界面文案不得暗示它已经冻结消费；如要实现强制停用，必须先完成后端语义设计。

### 导出敏感字段

兼容日志导出与 Billing 完整导出仍可能包含内容和内部请求标识，只适合管理员排障。对外结算账单使用独立 Invoice CSV。

### 请求状态

Invoice 对组织上下文、账单查询和规则查询提供区块级 retry；其他组织页面仍主要依赖全局错误处理。所有页面保留 React Query 的缓存和失效语义。

## 已上线版本明确不展示

- 组织余额、充值、订阅、付款客户和法定税务发票；Invoice 仅是结算报表。
- 组织 API Key、Token 绑定和请求组织选择器。
- 部门树、项目、成本中心、邀请链接和批量导入。
- 历史价格重算结果。
- 大屏图表或装饰性数据可视化。

这些内容属于产品和领域边界，不得通过 UI 先行引入。
