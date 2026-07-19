---
status: current
owner: Dev Team
last-reviewed: 2026-07-19
---

# AI 编码指南

## Before Any Task
1. 读 AGENTS.md 与 docs/README.md。
2. 读 docs/00-context/硬约束.md（硬约束，不可违反）。
3. 改架构前读 docs/20-architecture/架构概览.md。
4. 新建文档前先读目标目录的 README.md，确认放对位置。

## Verification
- 后端改动：跑与改动范围匹配的 Go 测试。
- 前端改动：在 `web/default/` 下优先使用 Bun 运行对应检查。
- 文档改动：跑 `task docs:check`。

## Boundaries
- 只改与任务相关的代码，不顺手重构。
- 不修改受保护的项目身份、组织身份、授权和版权归属信息。
- 不改变 AI 辅助入口文件，除非用户明确要求。
- 重大架构决策必须新增 ADR。
- 编码现场的一次性问题分析和方案不进入 `docs/`；任务结束前把仍有效的结论直接更新到对应主题目录。
- 已确认的架构事实进入 `20-architecture/`，可复用工程实践进入 `30-engineering/`，生产操作进入 `40-operations/`，阶段结果与变更进入 `50-planning/`；已被替代但必须保留的材料进入 `99-archive/`。

## Review Checklist（完成任务前自检）
- [ ] 改了架构，是否补了 ADR？
- [ ] 改了硬约束，是否更新 `docs/00-context/硬约束.md`？
- [ ] 新增文档是否放对了目录（对照该目录 README）？
- [ ] 是否误把一次性开发草稿提交进 `docs/`？
- [ ] 任务中的稳定结论是否已经同步到产品、架构、工程、运维、规划或 UI 真源？
- [ ] 跑通必要检查？
