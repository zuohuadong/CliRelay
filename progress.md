# Agent Progress Log

> 多 Workspace Agent 协调日志。每个 Agent 在开始和完成任务时更新此文件。
> 
> **格式：** `[时间] [workspace名] [状态] 描述`

---

<!-- Agent 工作记录按时间倒序排列 -->
[2026-05-25 14:31:33 +0800] [CliRelay] [done] Verified gpt-5.3-codex image requests were incorrectly routed to astron-code, added production astron-code image skip policy, restarted cli-proxy-api so multimodal adapters load into the BigModel executor, and confirmed a left-green/right-blue image routes to glm-5.1 and returns green/blue
[2026-05-23 18:35:33 +0800] [CliRelay] [done] Manually tested astron-code-latest upstream tools support; basic tool_choice=auto works, but Codex-like tool_choice=required returns finish_reason=abort with no tool call
[2026-05-23 18:20:10 +0800] [CliRelay] [done] Investigated frequent zero-output EOF errors and restored production astron-code tools skip policy after logs showed all failures routing gpt-5.3-codex to astron-code-latest
[2026-05-22 21:34:30 +0800] [CliRelay] [done] Enabled 4G production swapfile with persistent fstab entry and vm.swappiness=10 after OOMKilled restart investigation
[2026-05-22 21:42:00 +0800] [CliRelay] [done] Traced the OOM risk to unbounded Responses WebSocket timeline accumulation in openai_responses_websocket.go and added per-event and total-size caps before request-log cloning/flush
[2026-05-22 21:31:00 +0800] [CliRelay] [done] Investigated production restart as OOMKilled/no-panic container death and installed systemd health watchdog timer to restart cli-proxy-api when /healthz fails
[2026-05-22 15:06:30 +0800] [CliRelay] [done] Added production astron-code tools skip policy so Codex/glm-5.1 tool turns route to bigmodel-coding instead of surfacing zero-output EOF errors
[2026-05-22 14:48:30 +0800] [CliRelay] [done] Fixed Responses WebSocket EOF-with-zero-output path to emit a visible error instead of a silent empty completed, and added regression coverage
[2026-05-22 14:35:30 +0800] [CliRelay] [done] Surface context_length_exceeded as response.failed so Codex can compact on real context errors; deployed main-e96dde8 and verified server health
[2026-05-22 12:20:12 +0800] [CliRelay] [done] Verified production astron-code smart routing and raised astron priority above bigmodel-coding so small gpt-5.3-codex requests route to astron-code-latest while MCP and oversized requests fall back to bigmodel-coding
[2026-05-22 11:53:00 +0800] [CliRelay] [done] Added astron-code small-window routing guidance and MCP feature detection so gpt-5.3-codex can prefer astron-code-latest under 200k and fall back to bigmodel-coding for MCP or oversized requests
[2026-05-22 09:16:57 +0800] [CliRelay] [done] Added bigmodel-coding GLM-5.1 request_user_input guidance plus request/response passthrough tests so Codex choice UI stays structural instead of plain text
[2026-05-22 08:43:23 +0800] [CliRelay] [done] Fixed bigmodel-coding glm-5.1 streaming tool calls that end with raw JSON 500 by synthesizing terminal DONE after prior SSE data instead of surfacing Upstream request failed
[2026-05-22 08:33:00 +0800] [CliRelay] [done] Replayed missing Responses tool fields when falling back from previous_response_id so mixed codex/bigmodel websocket turns keep tool context on glm-5.1
[2026-05-22 08:20:49 +0800] [CliRelay] [done] Fixed mixed Codex websocket and bigmodel-coding routing so gpt-5.3-codex replays the full transcript to glm-5.1 instead of forwarding previous_response_id-only tool outputs
[2026-05-22 08:01:40 +0800] [CliRelay] [done] Fixed Responses WebSocket second-turn transcript merge when the initial Codex input is a string and the next request returns function_call_output
[2026-05-22 07:27:11 +0800] [CliRelay] [done] Verified gpt-5.3-codex manually routes to bigmodel-coding/glm-5.1 over HTTP and WebSocket, then added bootstrap retry coverage for pre-stream upstream 500 failures
[2026-05-21 23:16:26 +0800] [CliRelay] [done] Reworked Codex Responses terminal error handling to emit assistant output_item.done plus empty-id response.completed, and fixed websocket transcript replay continuation for previous_response_not_found
[2026-05-21 22:54:07 +0800] [CliRelay] [done] Fixed Responses HTTP/SSE clean EOF handling for gpt-5.3-codex -> bigmodel-coding/glm-5.1 by synthesizing response.completed when upstream closes without terminal event
[2026-05-21 22:35:47 +0800] [CliRelay] [done] Aligned Responses WebSocket EOF handling with Codex CLI by synthesizing terminal response.completed on clean upstream close while preserving real response.failed error frames
[2026-05-21 22:10:07 +0800] [CliRelay] [done] Changed Responses WebSocket upstream errors to terminal response.failed events after logs showed Codex clients converting top-level error frames into stream closed before response.completed
[2026-05-21 20:38:11 +0800] [CliRelay] [done] Completed pending Responses stream items on terminal DONE for gpt-5.3-codex -> bigmodel-coding/glm-5.1 tool-call streams without finish_reason
[2026-05-21 16:03:56 +0800] [CliRelay] [done] Fixed Codex OAuth websocket executor to treat upstream response.failed and response.incomplete as terminal so real upstream errors are not masked as stream closed before response.completed
[2026-05-21 15:39:06 +0800] [CliRelay] [done] Rechecked current Codex CLI source and restored response.failed/context_length_exceeded for Responses streaming and websocket context-window errors so Codex can mark context full and compact on the next turn
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
