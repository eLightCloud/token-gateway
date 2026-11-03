---
status: current
owner: Dev Team
last-reviewed: 2026-11-03
---

# 架构决策记录

## 目标

存放已经采纳、需要长期追溯理由和后果的架构决策记录（ADR）。

## 规则

- 使用 `docs/templates/adr.md` 和 `task docs:new-adr -- title=<标题>` 创建。
- 文件名使用四位序号和 kebab-case，例如 `0001-example-decision.md`。
- 临时实施方案、排障过程和普通功能说明不进入本目录。

## 已确认、待实施决策

- [ADR-0003：代理额度异步记账与用户专属倍率](0003-代理额度异步记账与用户专属倍率.md)：B 实时扣费、A 持久事件批量记账，允许延迟且不因积压停用；架构已接受，代理业务尚未实现，基线取证与后续状态见阶段计划。
