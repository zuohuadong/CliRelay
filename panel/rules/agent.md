---
name: agent
summary: Agent/技能开发规范（无此类需求则忽略）
---

<!-- AI-KIT:START -->

# Agent/技能开发规范（Agent Rules）

## Purpose

- 统一 `.agents/skills/` 的组织方式、技能边界、可追溯性与“可复用工作流”，避免技能变成不可维护的提示词垃圾场。

## When to Read

- 新增/修改项目内 Skill（`.agents/skills/*`）、优化 Agent 工作流、或希望把重复性任务沉淀为“可复用技能”时。

## Rules

### 1) Skill 的定位（强制）

- Skill 是“可复用工作流/约束/工具说明”，不是长篇教学文。
- `SKILL.md` 必须短、可执行、可分层加载：正文只写流程与决策树；细节放 `references/`；模板/脚手架放 `assets/`；确定性操作放 `scripts/`。

### 2) 触发与命名（强制）

- Skill 名称使用小写 + 连字符（hyphen-case），并在 `description` 中写清楚触发场景（这是自动触发的关键）。
- 避免“万能技能”：一个 Skill 只覆盖一个清晰问题域；跨域就拆分。

### 3) 与仓库规范的关系（强制）

- 项目内 Skill 与 `rules/*` 的关系：
  - `rules/*`：仓库级长期约束（稳定、少变）。
  - `.agents/skills/*`：针对某类任务的“短路径最佳实践/工作流”（可演进、可替换）。
- 若 Skill 引入了新规则/新边界，必须同步更新 `AGENTS.md` 与 `docs/evolution.md`（见 `rules/rules-authoring.md`）。

### 4) 可追溯性与安全（强制）

- Skill 不得要求上传/打印敏感信息（密钥、Token、用户数据）。
- 若 Skill 会执行危险操作（删除/覆盖/批量修改/重写历史），必须要求二次确认并提供回滚策略（见 `rules/workflow.md`）。
<!-- AI-KIT:END -->

<!-- PROJECT-OVERRIDES:START -->

（可选）在此处记录本项目的技能清单约定、命名空间、或团队协作规范（脚手架不会覆盖）。

<!-- PROJECT-OVERRIDES:END -->
