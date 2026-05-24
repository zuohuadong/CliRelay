---
name: naming
summary: 文件/目录/组件命名规范
---

<!-- AI-KIT:START -->

# 命名规范（Naming Rules）

## Purpose

- 通过一致的命名降低认知成本，提升可维护性与可搜索性。

## When to Read

- 新增文件/目录/组件/Hook/常量时。

## Rules

### 1) 通用

- 见名知意：避免 `utils.ts` / `helpers.ts` 无限制膨胀；更推荐“按领域命名”。
- 统一风格：优先跟随仓库现有风格（历史包袱优先兼容，不强推重命名）。

### 2) 常见前端/TS 约定（如适用）

- 组件：`PascalCase`（如 `AppShell`）。
- Hooks：`useXxx`（如 `useSidebarResize`）。
- 常量：`SCREAMING_SNAKE_CASE`。
- 事件处理函数：`handleXxx` / `onXxx` 语义区分（`onXxx` 更偏向回调 props，`handleXxx` 偏向内部处理）。
<!-- AI-KIT:END -->

<!-- PROJECT-OVERRIDES:START -->

（可选）在此处追加本项目的命名细则与示例（脚手架不会覆盖）。

<!-- PROJECT-OVERRIDES:END -->
