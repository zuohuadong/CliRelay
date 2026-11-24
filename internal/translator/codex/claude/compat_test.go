package claude

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/tidwall/gjson"
)

func codexClaudeStopReason(response gjson.Result, hasToolCall bool) string {
	return mapCodexStopReasonToClaude(codexStopReason(ojson.Parse(response.Raw)), hasToolCall)
}
