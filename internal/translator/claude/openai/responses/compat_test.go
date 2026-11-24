package responses

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

var (
	account string
	session string
	user    string
)

const testClaudeSig = "test-signature-12345678901234567890123456789012345678901234567890"

func reasoningItemJSON(signature string) string {
	return `{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"` + signature + `"}`
}

func contentTypes(message gjson.Result) []string {
	var types []string
	message.Get("content").ForEach(func(_, part gjson.Result) bool {
		types = append(types, part.Get("type").String())
		return true
	})
	return types
}

type claudeStreamBlock struct {
	kind   string
	chunks []string
}

type claudeConvertedEvent struct {
	name string
	data gjson.Result
}

func claudeStreamLines(t *testing.T, blocks []claudeStreamBlock, stop string) []string {
	t.Helper()
	lines := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":25,"output_tokens":0}}}`,
	}
	for i, block := range blocks {
		lines = append(lines,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":`+itoa(i)+`,"content_block":{"type":"`+block.kind+`","text":""}}`,
		)
		for _, chunk := range block.chunks {
			lines = append(lines,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":`+itoa(i)+`,"delta":{"type":"text_delta","text":`+strconv.Quote(chunk)+`}}`,
			)
		}
		lines = append(lines,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":`+itoa(i)+`}`,
		)
	}
	lines = append(lines,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"`+stop+`"},"usage":{"output_tokens":42}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	)
	return lines
}

func convertClaudeStream(t *testing.T, lines []string) []claudeConvertedEvent {
	t.Helper()
	var state any
	raw := ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, []byte(strings.Join(lines, "\n")), &state)
	events := make([]claudeConvertedEvent, 0, len(raw))
	for _, chunk := range raw {
		var name string
		var data string
		for _, line := range strings.Split(string(chunk), "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name != "" && data != "" {
			events = append(events, claudeConvertedEvent{name: name, data: gjson.Parse(data)})
		}
	}
	return events
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
