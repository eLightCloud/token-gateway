---
status: current
owner: Dev Team
last-reviewed: 2026-08-09
---

# 文档入口

## 项目
本项目是 Go 实现的 AI API 网关/代理，统一聚合 OpenAI、Claude、Gemini、Azure、AWS Bedrock 等上游模型服务，并提供用户、计费、限流与管理后台能力。

## 当前阶段
- 当前阶段：基础组织管理与用量分析已上线；组织 Invoice 与结算系数候选版本、Root 只读 Token 账单工作台已实现，进入发布前数据库、权限、导出与视觉验证阶段。
- 当前重点：保持个人扣费、组织汇总和账单只读的边界，完成北京时间账期、跨数据库查询、CSV/页面口径、Root 权限和生产规模性能验证。
- 当前进度：[路线图](./50-planning/路线图.md)；组织专项见 [组织与账单上线进度](./50-planning/组织与账单上线进度.md)。

## Read First（AI 与新人按此顺序，不得跳读）
1. AGENTS.md
2. docs/00-context/硬约束.md
3. docs/00-context/项目简介.md
4. docs/20-architecture/架构概览.md
5. docs/30-engineering/命令清单.md
6. docs/30-engineering/AI编码指南.md

## Directory Map
见各目录的 README.md（目标说明）。除 README、Taskfile、模板等通用文件外，业务文档文件名使用中文。

编码现场的一次性草稿不进入 `docs/`。任务结束前将仍然有效的结论直接更新到产品、架构、工程、运维、规划、调研或 UI 目录；已被替代但必须保留的历史材料进入 `99-archive/`。
