# Agent Progress Log

> 多 Workspace Agent 协调日志。每个 Agent 在开始和完成任务时更新此文件。
> 
> **格式：** `[时间] [workspace名] [状态] 描述`

---

<!-- Agent 工作记录按时间倒序排列 -->
[2026-05-21 15:20:55 +0800] [CliRelay] [done] Re-aligned Responses HTTP immediate stream errors and websocket error payloads with latest CLIProxyAPI upstream shapes for OpenAI-compatible clients
[2026-05-21 15:08:14 +0800] [CliRelay] [done] Restored CLIProxyAPI-style websocket top-level error events for non-context failures to avoid synthetic resp_error completed IDs causing previous_response_not_found loops
[2026-05-21 14:57:07 +0800] [CliRelay] [done] Removed downstream raw-close subscription on Codex upstream websocket disconnect so response.failed/response.completed can be delivered instead of resetting the client connection
[2026-05-21 14:46:57 +0800] [CliRelay] [done] Matched Codex CLI source handling by emitting websocket response.failed/context_length_exceeded for context-too-large so the client can mark context full and pre-turn compact
[2026-05-21 14:26:37 +0800] [CliRelay] [done] Restored official CLIProxyAPI-style websocket error event for context_too_large while preserving terminal response.completed so Codex can trigger compaction
[2026-05-21 14:08:56 +0800] [CliRelay] [done] Fixed bigmodel-coding GLM-5.1 MCP routing to use Z.AI MCP server URLs with upstream Authorization after logs showed 1210/400 parameter errors and server_url=null MCP connection failures
[2026-05-21 12:50:06 +0800] [CliRelay] [done] Upgraded Docker publish workflow actions to current releases and enabled Node 24 action runtime after deployment tag mismatch recovery
[2026-05-21 12:40:08 +0800] [CliRelay] [done] Fixed bigmodel-coding GLM-5.1 official MCP injection to use the built-in mcp code server shape and corrected Responses WebSocket downstream logging to only report written terminal events
[2026-05-21 10:29:22 +0800] [CliRelay] [done] Aligned bigmodel-coding GLM-5.1 payload shaping with Coding Plan conventions by normalizing thinking into enable_thinking/thinking_budget and enabling parallel_tool_calls for tool requests
[2026-05-21 10:06:48 +0800] [CliRelay] [done] Fixed Responses WebSocket error termination to send a terminal response.completed with nested error metadata so Codex clients do not report stream closed before response.completed
[2026-05-21 09:27:13 +0800] [CliRelay] [done] Added a targeted bigmodel-coding management-panel entry that maps to openai-compatibility with Codex identity fingerprint and glm-5.1 -> gpt-5.3-codex alias defaults
[2026-05-21 08:59:25 +0800] [CliRelay] [done] Fixed Codex executor terminal event handling so glm-5.1/Codex-style streams stop treating response.failed and response.incomplete as disconnected-before-completion
[2026-05-21 08:51:24 +0800] [CliRelay] [done] Fixed Codex-compatible Responses streaming error chunks so context_too_large carries nested error/status metadata for auto-compaction
[2026-05-21 07:55:20 +0800] [CliRelay] [done] Fixed Responses WebSocket error termination for gpt-5.3-codex -> bigmodel-coding/glm-5.1 and enabled codex identity fingerprint in bigmodel-coding config
[2026-05-21 00:23:13 +0800] [CliRelay] [done] Repaired Redis protocol integration tests after PR #119; full go test and required server build now pass
[2026-05-20 19:43:05 +0800] [CliRelay] [done] Merged bigmodel-coding glm-5.1 MCP upstream changes to main via PR #115, waited for GHCR build success, updated server to ghcr.io/zuohuadong/clirelay:main-491c98e, and migrated production config to restore gpt-5.3-codex alias plus multimodal adapter settings
[2026-05-20 19:07:11 +0800] [CliRelay] [done] Added first-class bigmodel-coding upstream handling for glm-5.1 official MCP web/search injection, scoped multimodal adaptation, and redacted upstream request logging
[2026-05-19 22:52:20 +0800] [CliRelay] [done] Root-caused restart storm to broken container-cli-proxy-api systemd unit; disabled watchdog timer, removed service from runtime path, and verified cli-proxy-api stays up past the prior timeout window
[2026-05-19 12:43:54 +0800] [CliRelay] [running] Investigating gpt-5.3-codex -> bigmodel-coding 503 and request-policy behavior
[2026-05-19 12:45:30 +0800] [CliRelay] [done] Hotfixed glm-5.1 request policy from skip-channel to reject; verified 413 for oversized gpt-5.3-codex requests
[2026-05-19 16:20:44 +0800] [CliRelay] [running] Validating codex/codex-responses-context-too-large for main push and deploy
[2026-05-20 09:16:53 +0800] [CliRelay] [running] Investigating and fixing xhigh -> glm-5.1 thinking validation failure on latest main image
[2026-05-20 09:36:43 +0800] [CliRelay] [done] Deployed main-b9aa7d6 and verified gpt-5.3-codex xhigh requests clamp to glm-5.1 high without validation errors
