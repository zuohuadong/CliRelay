package executor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCollectCodexOutputItemsInjectsCompletionOutput(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		``,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
		``,
	}, "\n")

	got := collectCodexOutputItems([]byte(input))
	completed := findSSEPayloadByType(got, "response.completed")
	if len(completed) == 0 {
		t.Fatalf("expected response.completed event after collect")
	}
	if gjson.GetBytes(completed, "response.output.0.id").String() != "msg_1" {
		t.Fatalf("response.output[0].id mismatch, got %q", gjson.GetBytes(completed, "response.output.0.id").String())
	}
	if gjson.GetBytes(completed, "response.output.0.content.0.text").String() != "hello" {
		t.Fatalf("response.output[0].content[0].text mismatch, got %q", gjson.GetBytes(completed, "response.output.0.content.0.text").String())
	}
}

func TestCollectCodexOutputItemsNormalizesResponseDone(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		``,
		`data: {"type":"response.done","response":{"id":"resp_2","output":[]}}`,
		``,
	}, "\n")

	got := collectCodexOutputItems([]byte(input))
	if bytes.Contains(got, []byte(`"type":"response.done"`)) {
		t.Fatalf("expected response.done to be normalized, got %s", string(got))
	}
	completed := findSSEPayloadByType(got, "response.completed")
	if len(completed) == 0 {
		t.Fatalf("expected normalized response.completed event")
	}
	if gjson.GetBytes(completed, "response.output.0.id").String() != "msg_1" {
		t.Fatalf("expected injected output item, got %q", gjson.GetBytes(completed, "response.output.0.id").String())
	}
}

func TestCollectCodexOutputItemsKeepsExistingOutput(t *testing.T) {
	input := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_3","output":[{"id":"existing_1","type":"message"}]}}`,
		``,
	}, "\n")

	got := collectCodexOutputItems([]byte(input))
	completed := findSSEPayloadByType(got, "response.completed")
	if len(completed) == 0 {
		t.Fatalf("expected response.completed event")
	}
	if gotID := gjson.GetBytes(completed, "response.output.0.id").String(); gotID != "existing_1" {
		t.Fatalf("existing output should be preserved, got %q", gotID)
	}
	if gjson.GetBytes(completed, "response.output.1").Exists() {
		t.Fatalf("unexpected extra output item: %s", gjson.GetBytes(completed, "response.output").Raw)
	}
}

func findSSEPayloadByType(data []byte, eventType string) []byte {
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		payload := bytes.TrimSpace(line[len(dataTag):])
		if gjson.GetBytes(payload, "type").String() == eventType {
			return payload
		}
	}
	return nil
}

func TestNormalizeCodexWebsocketCompletionPrefixesResponseID(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		wantID string
	}{
		{
			name:   "numeric_id_gets_resp_prefix",
			input:  `{"type":"response.created","response":{"id":"202605131251361c390ef60c044cec"}}`,
			wantID: "resp_202605131251361c390ef60c044cec",
		},
		{
			name:   "resp_prefix_preserved",
			input:  `{"type":"response.created","response":{"id":"resp_abc123"}}`,
			wantID: "resp_abc123",
		},
		{
			name:   "response_completed_numeric_id",
			input:  `{"type":"response.completed","response":{"id":"202605131251361c390ef60c044cec","output":[]}}`,
			wantID: "resp_202605131251361c390ef60c044cec",
		},
		{
			name:   "response_done_normalized_and_id_prefixed",
			input:  `{"type":"response.done","response":{"id":"abc123","output":[]}}`,
			wantID: "resp_abc123",
		},
		{
			name:   "empty_id_unchanged",
			input:  `{"type":"response.created","response":{"id":""}}`,
			wantID: "",
		},
		{
			name:   "in_progress_id_prefixed",
			input:  `{"type":"response.in_progress","response":{"id":"xyz789"}}`,
			wantID: "resp_xyz789",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeCodexWebsocketCompletion([]byte(tc.input))
			gotID := gjson.GetBytes(got, "response.id").String()
			if gotID != tc.wantID {
				t.Fatalf("response.id = %q, want %q", gotID, tc.wantID)
			}
		})
	}
}

func TestNormalizeCodexWebsocketCompletionDoneToCompleted(t *testing.T) {
	input := `{"type":"response.done","response":{"id":"resp_1","output":[]}}`
	got := normalizeCodexWebsocketCompletion([]byte(input))
	if gjson.GetBytes(got, "type").String() != "response.completed" {
		t.Fatalf("type = %q, want response.completed", gjson.GetBytes(got, "type").String())
	}
	if gjson.GetBytes(got, "response.id").String() != "resp_1" {
		t.Fatalf("response.id = %q, want resp_1", gjson.GetBytes(got, "response.id").String())
	}
}
