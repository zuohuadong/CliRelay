# Upstream Sync Conflict Report

The automated merge from `router-for-me/CLIProxyAPI:main` could not be completed.
This branch was recreated from clean base `33805898cf3ec8efd8c20d0e3d16647eec8e3135`; it contains no partial upstream merge.

## Conflicted Files
- `cmd/server/main.go`
- `config.example.yaml`
- `go.mod`
- `go.sum`
- `internal/api/handlers/management/api_tools.go`
- `internal/api/handlers/management/api_tools_test.go`
- `internal/api/handlers/management/auth_files.go`
- `internal/api/handlers/management/auth_files_fields.go`
- `internal/api/handlers/management/auth_files_upload_test.go`
- `internal/api/handlers/management/handler.go`
- `internal/api/handlers/management/oauth_sessions.go`
- `internal/api/handlers/management/plugin_quota.go`
- `internal/api/handlers/management/plugin_quota_test.go`
- `internal/api/server_management.go`
- `internal/api/server_test.go`
- `internal/cmd/auth_manager.go`
- `internal/config/config_load.go`
- `internal/config/config_types.go`
- `internal/config/parse.go`
- `internal/registry/model_definitions.go`
- `internal/registry/model_definitions_test.go`
- `internal/registry/model_updater.go`
- `internal/registry/models/models.json`
- `internal/runtime/executor/codex_executor_execute.go`
- `internal/runtime/executor/codex_executor_stream.go`
- `internal/runtime/executor/codex_executor_terminal.go`
- `internal/runtime/executor/codex_openai_images.go`
- `internal/runtime/executor/codex_stream_bootstrap_buffering_test.go`
- `internal/runtime/executor/codex_websockets_connection.go`
- `internal/runtime/executor/codex_websockets_request.go`
- `internal/runtime/executor/codex_websockets_session.go`
- `internal/runtime/executor/codex_websockets_stream.go`
- `internal/runtime/executor/devin_executor.go`
- `internal/runtime/executor/helps/proxy_helpers.go`
- `internal/runtime/executor/helps/proxy_helpers_test.go`
- `internal/runtime/executor/kimi_executor.go`
- `internal/runtime/executor/openai_responses_signature.go`
- `internal/runtime/executor/xai_websockets_executor_test.go`
- `internal/translator/openai/claude/openai_claude_response_test.go`
- `internal/translator/openai/openai/responses/openai_openai-responses_response.go`
- `internal/watcher/diff/config_diff.go`
- `internal/watcher/synthesizer/config_test.go`
- `internal/watcher/synthesizer/file.go`
- `internal/watcher/watcher_test.go`
- `sdk/api/handlers/openai/openai_handlers.go`
- `sdk/api/handlers/openai/openai_responses_handlers.go`
- `sdk/api/handlers/openai/openai_responses_handlers_stream_test.go`
- `sdk/api/handlers/openai/openai_responses_websocket_forward.go`
- `sdk/api/handlers/openai_responses_stream_error.go`
- `sdk/api/handlers/openai_responses_stream_error_test.go`
- `sdk/auth/filestore.go`
- `sdk/auth/refresh_registry.go`
- `sdk/auth/refresh_registry_test.go`
- `sdk/cliproxy/antigravity_models.go`
- `sdk/cliproxy/auth/auto_refresh_loop.go`
- `sdk/cliproxy/auth/conductor_cooldown.go`
- `sdk/cliproxy/auth/conductor_execution.go`
- `sdk/cliproxy/auth/conductor_lifecycle.go`
- `sdk/cliproxy/auth/conductor_selection.go`
- `sdk/cliproxy/auth/metadata_merge_test.go`
- `sdk/cliproxy/auth/oauth_model_alias_test.go`
- `sdk/cliproxy/builder.go`
- `sdk/cliproxy/service.go`
- `sdk/cliproxy/service_auth.go`
- `sdk/cliproxy/service_executors.go`
- `sdk/cliproxy/service_models.go`
- `sdk/cliproxy/service_oauth_model_alias_test.go`
- `sdk/pluginapi/types.go`
- `sdk/pluginapi/types_test.go`

Resolve these files manually in a follow-up sync branch, then rerun the safety guard.
