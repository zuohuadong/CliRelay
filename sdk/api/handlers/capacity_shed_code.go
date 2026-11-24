package handlers

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A capacity refusal must not reach a Codex client with its own error code.
//
// Codex classifies a Responses error by a closed set of codes, and
// server_is_overloaded / slow_down sit in the fatal set: the CLI prints
// "Selected model is at capacity. Please try a different model." and ends the
// session instead of using its built-in backoff. Codes outside that set, such as
// server_error, are retried by the client with a delay.
//
// Whenever a capacity refusal has to be forwarded — the stream already started,
// or every attempt is spent — the message is kept but the code is rewritten, so
// the turn is retried instead of abandoned. Anything else, rate_limit_exceeded
// included, is left alone: the client parses that code for its own delay.
const (
	capacityShedRetryableCode = "server_error"

	capacityShedCodeOverloaded = "server_is_overloaded"
	capacityShedCodeSlowDown   = "slow_down"
)

// capacityShedMessages are the wordings providers use for their own capacity.
var capacityShedMessages = []string{
	"server is overloaded",
	"servers are overloaded",
	"servers are currently overloaded",
	"currently overloaded",
	"model is at capacity",
	"model is currently at capacity",
	"at capacity due to high demand",
}

// isCapacityShedCode reports the two codes Codex treats as fatal.
func isCapacityShedCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case capacityShedCodeOverloaded, capacityShedCodeSlowDown:
		return true
	}
	return false
}

// isCapacityShedMessage reports capacity wording in a provider's own words.
func isCapacityShedMessage(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, wording := range capacityShedMessages {
		if strings.Contains(lower, wording) {
			return true
		}
	}
	return false
}

// isCapacityShedPayload reports whether an error event or error body describes a
// capacity refusal, by code or by wording.
func isCapacityShedPayload(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"response.error.code", "error.code", "code"} {
		if isCapacityShedCode(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	for _, path := range []string{"response.error.message", "error.message", "message", "response.error.type", "error.type"} {
		if isCapacityShedMessage(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	return false
}

// RewriteCapacityShedCode replaces a capacity refusal's error code with the
// retryable one, keeping the message. Nothing else in the payload changes, and a
// payload without a capacity signal is returned untouched.
func RewriteCapacityShedCode(payload []byte) ([]byte, bool) {
	if !isCapacityShedPayload(payload) {
		return payload, false
	}
	updated := payload
	changed := false
	// An error object that names capacity but carries no code gets one, so the
	// client has something retryable to classify.
	for _, parent := range []string{"response.error", "error"} {
		if !gjson.GetBytes(updated, parent).IsObject() {
			continue
		}
		code := strings.TrimSpace(gjson.GetBytes(updated, parent+".code").String())
		if code != "" && !isCapacityShedCode(code) {
			// A code the client already understands stays: it parses
			// rate_limit_exceeded for its own delay.
			continue
		}
		next, err := sjson.SetBytes(updated, parent+".code", capacityShedRetryableCode)
		if err != nil {
			return payload, false
		}
		updated = next
		changed = true
	}
	if code := strings.TrimSpace(gjson.GetBytes(updated, "code").String()); isCapacityShedCode(code) {
		next, err := sjson.SetBytes(updated, "code", capacityShedRetryableCode)
		if err != nil {
			return payload, false
		}
		updated = next
		changed = true
	}
	return updated, changed
}

// SanitizeCapacityShedStreamChunk rewrites a chunk on its way to the client.
//
// A chunk is one or more complete SSE lines. Only a data line holding valid JSON
// with a capacity signal is rewritten; any other chunk, including a partial one,
// is returned as it is, so framing is never disturbed.
func SanitizeCapacityShedStreamChunk(chunk []byte) []byte {
	if len(chunk) == 0 || !bytes.Contains(chunk, []byte("data:")) {
		return chunk
	}
	value, ok := sseChunkDataValue(chunk)
	if !ok {
		return chunk
	}
	rewritten, changed := RewriteCapacityShedCode(value)
	if !changed {
		return chunk
	}
	return bytes.Replace(chunk, value, rewritten, 1)
}

// sseChunkDataValue returns the JSON a single-line data chunk carries.
func sseChunkDataValue(chunk []byte) ([]byte, bool) {
	line := bytes.TrimSpace(chunk)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	// A chunk with more than one line is not a single event; leave it alone
	// rather than guess which line the payload belongs to.
	if bytes.ContainsAny(line, "\r\n") {
		return nil, false
	}
	value := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(value) == 0 || !gjson.ValidBytes(value) {
		return nil, false
	}
	return value, true
}

// SanitizeCapacityShedErrorText rewrites a non-streaming error body (or an error
// text that happens to be one) the same way.
func SanitizeCapacityShedErrorText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return text
	}
	rewritten, changed := RewriteCapacityShedCode([]byte(trimmed))
	if !changed {
		return text
	}
	return string(rewritten)
}
