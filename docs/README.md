---
status: current
owner: Dev Team
last-reviewed: 2026-07-15
---

# 文档入口

## 项目
本项目是 Go 实现的 AI API 网关/代理，统一聚合 OpenAI、Claude、Gemini、Azure、AWS Bedrock 等上游模型服务，并提供用户、计费、限流与管理后台能力。

## 当前阶段
- 当前阶段：组织管理与组织账单产品已完成并上线，进入生产运行与持续维护阶段。
- 当前重点：保持个人扣费、组织汇总的产品边界，维护生产稳定性、账单口径和上游兼容性。
- 当前进度：[组织与账单上线进度](./50-planning/组织与账单上线进度.md)。

## Read First（AI 与新人按此顺序，不得跳读）
1. AGENTS.md
2. docs/00-context/硬约束.md
3. docs/00-context/项目简介.md
4. docs/20-architecture/架构概览.md
5. docs/30-engineering/命令清单.md
6. docs/30-engineering/AI编码指南.md

## Directory Map
见各目录的 README.md（目标说明）。除 README、Taskfile、模板等通用文件外，业务文档文件名使用中文。
