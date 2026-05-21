package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponsesNonStreamAcceptsIncomplete(t *testing.T) {
	out := ConvertCodexResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gpt-5.4",
		nil,
		nil,
		[]byte(`{"type":"response.incomplete","response":{"id":"resp_123","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`),
		nil,
	)

	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete_details.reason = %q, want max_output_tokens; payload=%s", got, string(out))
	}
}
