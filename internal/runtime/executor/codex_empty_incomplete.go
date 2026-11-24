package executor

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// codexEmptyIncompleteStreamMessage is what a client is told when the upstream
// ends a turn without producing anything.
const codexEmptyIncompleteStreamMessage = "stream error: upstream terminated with incomplete empty response (0 tokens)"

// isCodexTerminalEmptyIncomplete reports whether an event is a silent upstream
// abort: a response.incomplete that produced nothing at all. Codex ends such a
// turn with zero output tokens, no output items and no delta, so forwarding it
// as a completed turn leaves the client with an empty answer and no reason for
// it. A turn the client itself capped (max_output_tokens) still reports a
// non-zero token count and is left alone.
func isCodexTerminalEmptyIncomplete(eventData []byte, outputItems int, sawOutputDelta bool) bool {
	if gjson.GetBytes(eventData, "type").String() != "response.incomplete" {
		return false
	}
	if sawOutputDelta || outputItems > 0 {
		return false
	}
	if output := gjson.GetBytes(eventData, "response.output"); output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	// Require an explicit numeric zero: a missing count, null, or a fraction
	// says nothing about this being an empty turn.
	outputTokens := gjson.GetBytes(eventData, "response.usage.output_tokens")
	if !outputTokens.Exists() || outputTokens.Type != gjson.Number {
		return false
	}
	if outputTokens.Num != 0 || strings.TrimSpace(outputTokens.Raw) != "0" {
		return false
	}
	return true
}

// newCodexEmptyIncompleteStreamError is the failure reported for such a turn.
// It is a 502: the upstream broke the turn, and the body said nothing else.
func newCodexEmptyIncompleteStreamError() statusErr {
	return statusErr{code: http.StatusBadGateway, msg: codexEmptyIncompleteStreamMessage}
}
