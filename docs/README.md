---
status: current
owner: Dev Team
last-reviewed: 2026-11-21
---

# 文档入口

## 项目
本项目是 Go 实现的 AI API 网关/代理，统一聚合 OpenAI、Claude、Gemini、Azure、AWS Bedrock 等上游模型服务，并提供用户、计费、限流与管理后台能力。

## 当前阶段

基础组织管理与 Billing、Logs 已上线。Invoice、Root 只读 Token 账单、实时折扣、代理分销、睿观接合、系统邮件与使用日志的发布条件见[路线图](50-planning/路线图.md)。

| 专题 | 产品和实现入口 | 当前发布门槛 |
| --- | --- | --- |
| 组织账单 | [产品设计](10-product/组织与账单产品设计.md)、[架构设计](20-architecture/组织与组织账单架构设计.md) | 目标环境对账、历史归属回填与生产规模验证 |
| 睿观模型服务 | [产品设计](10-product/睿观模型服务产品设计.md)、[接合设计](20-architecture/睿观模型服务接合设计.md) | 真实模型、业务存量和双系统目标部署验收 |
| 代理分销 | [产品设计](10-product/代理分销与用户专属倍率产品设计.md)、[架构设计](20-architecture/代理分销与用户专属倍率架构设计.md)、[ADR-0003](20-architecture/decisions/0003-代理额度异步记账与用户专属倍率.md) | 实现及隔离验证已交付，容量、目标拓扑和灰度仍待验收 |
| 用户与组织实时模型折扣 | [产品设计](10-product/用户与组织实时模型折扣产品设计.md)、[架构设计](20-architecture/用户与组织实时模型折扣架构设计.md)、[UI 设计](90-ui-ux/用户与组织实时模型折扣UI设计.md) | 生产盘点、迁移与 Root 启用 |
| 微软应用身份邮件与运维通知 | [邮件产品设计](10-product/微软应用身份邮件产品设计.md)、[邮件架构](20-architecture/微软应用身份邮件架构设计.md)、[运维手册](40-operations/运维手册.md#microsoft-graph-发信验证) | Graph 已受理网关测试，目标环境送达、轮换、配置同步和通知容量待验收 |
| 使用日志 | [业务流程](10-product/核心业务流程.md#个人使用日志)、[架构概览](20-architecture/架构概览.md#使用日志的读写隔离)、[运维手册](40-operations/运维手册.md#使用日志灰度) | 个人游标默认关闭，写入稳定性与后续统计待验证 |

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
