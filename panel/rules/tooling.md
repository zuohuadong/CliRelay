---
name: tooling
summary: 常用命令、构建校验与升级约定
---

<!-- AI-KIT:START -->

# 工具链规范（Tooling Rules）

## Purpose

- 统一命令使用方式与验证口径，保证“可编译/可运行”的交付底线。

## When to Read

- 运行/构建失败排查、升级依赖、调整脚本与工具链时。

## Quick Start（按需补全）

- 安装依赖：`bun install`
- 开发启动：`bun run dev`
- 构建：`bun run build`
- 测试：暂无（当前仓库未配置测试脚本/用例）
- Lint：`bun run lint`
- 格式化：`bun run format`
- 格式化检查（CI 友好，如有）：`bunx oxfmt . --check`
- 一键验证：`bun run check`

## 依赖升级约定

- 升级前先确认目标版本与破坏性变更；不确定 API/版本时优先用 Context7 核对官方文档，不要猜。
- 升级后必须跑通最小验证闭环（见 `rules/workflow.md`）。
<!-- AI-KIT:END -->

<!-- PROJECT-OVERRIDES:START -->

（可选）在此处追加本项目“必须跑哪些命令才算验证闭环”、CI 约束、版本策略（脚手架不会覆盖）。

<!-- PROJECT-OVERRIDES:END -->
