---
status: active
owner: Dev Team
last-reviewed: 2026-08-09
---

# Token 账单页面视觉验收

- source visual truth path: `docs/90-ui-ux/uis/token-bill-reconciliation-subjects.png`
- source pixels: 1536 × 1024
- implementation screenshot path: unavailable; the browser rendered a blank document body
- state: `/reconciliation`, frontend development build, backend/auth bootstrap unavailable
- console errors checked: no JavaScript error was reported; i18n initialization completed

## Full-view comparison evidence

设计基准图按对账主体更新：总览先展示“客户账单 / 渠道用量 / 按上游”，按上游支持“API 地址 × 渠道 / 上游 × 模型”切换；前者同地址不同渠道分行，后者继续保留地址、渠道与模型三重身份。详情保留完整复合上下文，再显示底层账单记录。开发 URL 此前已加载 HTML、脚本和样式，但 `#root` 为空，仍没有可用于并排比较的已认证实现截图。

## Findings

- [P0] 缺少已认证的完整应用运行环境，视觉比较被阻塞。
  - Location: local `/reconciliation` preview.
  - Impact: 无法对字体、间距、表格密度、抽屉比例和响应式行为签署视觉验收结论。
  - Fix: 使用安全测试数据启动完整后端和已认证前端，分别捕获三个视角及明细抽屉，再与设计基准进行同尺寸比较。

## Implementation checklist

1. 提供不连接生产数据的已认证本地运行环境。
2. 捕获客户账单、渠道用量、按上游三个总览视角和明细抽屉。
3. 在相同 viewport 与 density 下和设计基准比较。
4. 修复 P0/P1/P2 偏差后重复验证。

final result: blocked
