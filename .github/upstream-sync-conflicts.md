# Upstream Sync Conflict Report

The automated merge from `router-for-me/CLIProxyAPI:main` could not be completed.
This branch was recreated from clean base `530a5728492cd2800d51a735148fa1c9c01d97f9`; it contains no partial upstream merge.

## Conflicted Files
- `internal/api/handlers/management/auth_files_upload_test.go`
- `internal/api/handlers/management/handler.go`
- `internal/api/server_management.go`
- `internal/api/server_test.go`
- `internal/registry/model_definitions.go`
- `internal/registry/model_definitions_test.go`
- `internal/runtime/executor/codex_executor_execute.go`
- `internal/runtime/executor/codex_executor_stream.go`
- `internal/runtime/executor/codex_executor_terminal.go`
- `internal/runtime/executor/codex_openai_images.go`
- `internal/runtime/executor/codex_websockets_connection.go`
- `internal/runtime/executor/codex_websockets_request.go`
- `internal/runtime/executor/codex_websockets_session.go`
- `internal/runtime/executor/codex_websockets_stream.go`
- `internal/runtime/executor/kimi_executor.go`
- `internal/translator/openai/openai/responses/openai_openai-responses_response.go`
- `internal/watcher/synthesizer/file.go`
- `sdk/api/handlers/openai/openai_handlers.go`
- `sdk/api/handlers/openai/openai_responses_handlers.go`
- `sdk/api/handlers/openai/openai_responses_handlers_stream_test.go`
- `sdk/api/handlers/openai/openai_responses_websocket_forward.go`
- `sdk/api/handlers/openai_responses_stream_error.go`
- `sdk/auth/filestore.go`
- `sdk/cliproxy/antigravity_models.go`
- `sdk/cliproxy/auth/auto_refresh_loop.go`
- `sdk/cliproxy/auth/conductor_execution.go`
- `sdk/cliproxy/auth/conductor_selection.go`
- `sdk/cliproxy/auth/metadata_merge_test.go`
- `sdk/cliproxy/service.go`
- `sdk/cliproxy/service_models.go`

Resolve these files manually in a follow-up sync branch, then rerun the safety guard.
