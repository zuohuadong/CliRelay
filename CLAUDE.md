# Claude Code Agent Rules

You are acting within the `agent-team-config` framework. Please adhere to the following principles defined for this repository:

1. **Check Progress**: Always read `progress.md` before starting to understand the current task list and context.
2. **Mailbox Coordination**: Check `.mailbox/` for any pending messages directed to you. Wait for user input if conflicting instructions are found.
3. **Roles and Workflows**: Depending on your exact assignment, rely on `.agents/prompts/` and `.agents/workflows/` for specialized instructions (e.g. executing `/dev`, `/deploy-verify`, etc.).
4. **No Secrets**: Never hardcode API keys or secrets in logs or code.
5. **No Scope Reduction**: Do not silently reduce the scope of the task if you find it complex. Stop and ask the user.
6. **Verify First**: Verify changes via tests or building before declaring a task done. Use checklist conventions in `progress.md` where applicable.
7. **Delegation Gate（默认启动子代理）**: 有任务时默认启动子代理执行（`agent-team subagent dispatch <role> "<prompt>"`），主进程负责审查。标准流程：explorer → executor → verifier → orchestrator 裁决。低风险简单任务可跳过但必须记录 `safe_skip_reason`。Design deliverables should go through `/design-review` or an equivalent Goal Forge review.

## Task Automation

- Task Ledger (`tasks.md`) is the execution source; re-read it and `.mailbox/` after completing or blocking any task.
- Claim only one task at a time. Update `progress.md` for every claim, block, or completion.
- Before execution, form a Task Contract with goal, non-goals, acceptance criteria, required skills, and verification plan.
- Stack/Deployment/Database Profile required when task involves technology stack, hosting, or persistence choices. Use recommended fallbacks only for greenfield projects.

## Skills and Conventions

- Load relevant skills from `~/.agent-team-config/references/skills/` or project-level `.agents/` before implementation.
- Use `stack-profile-selector` for stack decisions, `deployment-target-selector` for hosting, `database-profile-selector` for persistence.
- Follow existing project patterns, framework conventions, and testing practices.

## Safety

- Never use `git push -f` or `rm -rf /`.
- Confirm before destructive git operations, production deploys, or remote write commands.
- State management files are in `.agents/state/`.
