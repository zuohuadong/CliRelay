# Upstream Sync Conflict Report

The automated merge from `router-for-me/CLIProxyAPI:main` could not be completed.
This branch was recreated from clean base `78eb9f5cf8152ea78e03928daeeb171073e75f6a`; it contains no partial upstream merge.

## Conflicted Files
- `AGENTS.md`
- `cmd/fetch_codex_models/main.go`
- `cmd/server/main.go`
- `cmd/server/main_test.go`
- `internal/api/handlers/management/config_auth_index.go`
- `internal/api/server_routes.go`
- `internal/client/codex/models/models_test.go`
- `internal/config/sdk_config.go`
- `internal/pluginhost/adapters_usage_translation.go`
- `internal/pluginhost/host_callbacks.go`
- `internal/registry/devin_models_updater.go`
- `internal/registry/models/models.json`
- `internal/runtime/executor/antigravity_executor_execute.go`
- `internal/runtime/executor/antigravity_executor_request.go`
- `internal/runtime/executor/antigravity_executor_stream.go`
- `internal/runtime/executor/codex_executor_execute.go`
- `internal/runtime/executor/codex_executor_request.go`
- `internal/runtime/executor/codex_executor_stream.go`
- `internal/runtime/executor/codex_openai_images.go`
- `internal/runtime/executor/codex_websockets_connection.go`
- `internal/runtime/executor/codex_websockets_duplex.go`
- `internal/runtime/executor/codex_websockets_execute.go`
- `internal/runtime/executor/codex_websockets_session.go`
- `internal/runtime/executor/codex_websockets_stream.go`
- `internal/runtime/executor/devin_executor.go`
- `internal/runtime/executor/devin_executor_test.go`
- `internal/runtime/executor/helps/devin_wire.go`
- `internal/runtime/executor/helps/kimi_responses_test.go`
- `internal/runtime/executor/helps/usage_helpers_test.go`
- `internal/runtime/executor/helps/utls_client.go`
- `internal/runtime/executor/kimi_executor_test.go`
- `internal/runtime/executor/openai_compat_executor.go`
- `internal/runtime/executor/xai_executor_stream.go`
- `internal/runtime/executor/xai_websockets_executor.go`
- `internal/runtime/executor/xai_websockets_executor_test.go`
- `internal/translator/antigravity/openai/responses/antigravity_openai-responses_request_test.go`
- `internal/translator/claude/openai/responses/claude_openai-responses_request.go`
- `internal/translator/claude/openai/responses/claude_openai-responses_request_test.go`
- `internal/translator/codex/openai/responses/codex_openai-responses_response.go`
- `internal/translator/gemini/claude/gemini_claude_request_test.go`
- `internal/translator/gemini/openai/responses/gemini_openai-responses_request_test.go`
- `internal/translator/openai/claude/openai_claude_request_test.go`
- `internal/translator/openai/interactions/responses/interactions_openai_responses_response_test.go`
- `internal/translator/openai/openai/responses/openai_openai-responses_response.go`
- `internal/util/nocopy_invariant_test.go`
- `sdk/api/handlers/model_execution.go`
- `sdk/api/handlers/model_execution_test.go`
- `sdk/api/handlers/openai/codex_client_models.go`
- `sdk/api/handlers/openai/codex_client_models_test.go`
- `sdk/api/handlers/openai/openai_responses_handlers.go`
- `sdk/api/handlers/openai/openai_responses_websocket.go`
- `sdk/cliproxy/antigravity_models.go`
- `sdk/cliproxy/auth/auto_refresh_loop.go`
- `sdk/cliproxy/auth/conductor.go`
- `sdk/cliproxy/auth/conductor_execution.go`
- `sdk/cliproxy/auth/conductor_home_execution.go`
- `sdk/cliproxy/auth/conductor_lifecycle.go`
- `sdk/cliproxy/auth/conductor_stream.go`
- `sdk/cliproxy/service.go`
- `sdk/cliproxy/service_excluded_models_test.go`
- `sdk/cliproxy/service_lifecycle.go`
- `sdk/cliproxy/service_models.go`
- `sdk/cliproxy/usage/manager.go`
- `sdk/pluginapi/types.go`

Resolve these files manually in a follow-up sync branch, then rerun the safety guard.
