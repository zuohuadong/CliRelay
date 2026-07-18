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

func TestConvertCodexResponseToOpenAIResponsesNonStreamIncomplete(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	out := ConvertCodexResponseToOpenAIResponsesNonStream(context.Background(), "gpt-5.5", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
		t.Fatalf("status = %q, want incomplete; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("incomplete reason = %q, want max_output_tokens; payload=%s", got, out)
	}
}
