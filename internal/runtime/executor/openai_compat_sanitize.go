package executor

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripCodexClientMetadata removes the Codex-only `client_metadata` envelope
// before an OpenAI-compatible upstream sees it.
//
// Codex Desktop stamps each request with client_metadata, and the Responses ->
// Chat Completions translation preserves unknown top-level members. OpenAI
// itself ignores the field, but Zhipu's BigModel endpoint rejects the whole
// request with code 1210 "API 调用参数有误，请检查文档。" whenever it is present.
// The field carries no information the upstream can use, so dropping it is
// always safe for a chat-completions target.
func stripCodexClientMetadata(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !gjson.GetBytes(payload, "client_metadata").Exists() {
		return payload
	}
	updated, err := sjson.DeleteBytes(payload, "client_metadata")
	if err != nil {
		return payload
	}
	return updated
}
