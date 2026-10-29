package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityRecoveryMaxAttempts = 2

// Keep the active session on the opener: a malformed-call retry must not fall
// back onto a session that already exceeded the upstream token ceiling.
type antigravityRequestOpener struct {
	executor     *AntigravityExecutor
	ctx          context.Context
	auth         *cliproxyauth.Auth
	token        string
	model        string
	payload      []byte
	alt          string
	baseURL      string
	trace        string
	sessionID    string
	sessionBumps int
	client       *http.Client
}

func (o *antigravityRequestOpener) open() (*http.Response, error) {
	if o.sessionID == "" {
		o.sessionID = strings.TrimSpace(gjson.GetBytes(o.payload, "request.sessionId").String())
		if o.sessionID == "" {
			o.sessionID = generateStableSessionID(o.payload)
		}
	}
	for {
		if err := o.ctx.Err(); err != nil {
			return nil, err
		}
		req, err := o.executor.buildRequest(o.ctx, o.auth, o.token, o.model, o.payload, true, o.alt, o.baseURL, o.sessionID)
		if err != nil {
			return nil, err
		}
		resp, err := helps.WithAntigravityHTTPClientTrace(o.client, o.auth, o.trace).Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusBadRequest || o.sessionBumps >= antigravityRecoveryMaxAttempts {
			return resp, nil
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if !antigravityShouldBumpSession(resp.StatusCode, body) || strings.Contains(o.model, "image") ||
			gjson.GetBytes(o.payload, "requestType").String() == "web_search" {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		}
		helps.RecordAPIResponseMetadata(o.ctx, o.executor.cfg, resp.StatusCode, resp.Header.Clone())
		helps.AppendAPIResponseChunk(o.ctx, o.executor.cfg, body)
		if o.sessionBumps == 0 {
			if explicit := strings.TrimSpace(gjson.GetBytes(o.payload, "request.sessionId").String()); explicit != "" {
				o.sessionID = explicit
			}
			if o.sessionID == "" {
				o.sessionID = generateStableSessionID(o.payload)
			}
		}
		o.sessionBumps++
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s:bump:%d", o.sessionID, o.sessionBumps)))
		rotated := "-" + strconv.FormatUint(binary.BigEndian.Uint64(hash[:8])&0x7fffffffffffffff, 10)
		o.payload, _ = sjson.SetBytes(bytes.Clone(o.payload), "request.sessionId", rotated)
	}
}

func antigravityShouldBumpSession(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(string(body))
	return strings.Contains(message, "exceeds the maximum number of tokens") ||
		(strings.Contains(message, "input token count") && strings.Contains(message, "1048576"))
}

// Replay only before visible output, with a separate small budget from normal
// credential retries. Exhaustion and malformed calls after output are errors,
// never a successful empty completion (the same contract used by sub2api).
func (o *antigravityRequestOpener) readStream(resp *http.Response, emit func([]byte) error, restart func()) error {
	for attempt := 0; ; attempt++ {
		retry, err := o.readStreamAttempt(resp, emit)
		_ = resp.Body.Close()
		if !retry || attempt >= antigravityRecoveryMaxAttempts {
			return err
		}
		restart()
		resp, err = o.open()
		if err != nil {
			return err
		}
		helps.RecordAPIResponseMetadata(o.ctx, o.executor.cfg, resp.StatusCode, resp.Header.Clone())
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			body, errRead := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if errRead != nil {
				return errRead
			}
			helps.AppendAPIResponseChunk(o.ctx, o.executor.cfg, body)
			return newAntigravityStatusErr(resp.StatusCode, body)
		}
	}
}

func (o *antigravityRequestOpener) readStreamAttempt(resp *http.Response, emit func([]byte) error) (bool, error) {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, streamScannerBuffer)
	var decoder antigravityStreamDecoder
	visible := false
	for scanner.Scan() {
		if err := o.ctx.Err(); err != nil {
			return false, err
		}
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(o.ctx, o.executor.cfg, line)
		payload, err := decoder.Decode(line)
		if err != nil {
			return false, err
		}
		if payload == nil {
			continue
		}
		response := gjson.ParseBytes(payload)
		if wrapped := response.Get("response"); wrapped.Exists() {
			response = wrapped
		}
		malformed := false
		response.Get("candidates").ForEach(func(_, candidate gjson.Result) bool {
			candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
				if (!part.Get("thought").Bool() && part.Get("text").String() != "") ||
					part.Get("functionCall").Exists() || part.Get("function_call").Exists() ||
					part.Get("inlineData").Exists() || part.Get("inline_data").Exists() ||
					part.Get("fileData").Exists() || part.Get("file_data").Exists() ||
					part.Get("executableCode").Exists() || part.Get("codeExecutionResult").Exists() {
					visible = true
				}
				return true
			})
			if strings.EqualFold(candidate.Get("finishReason").String(), "MALFORMED_FUNCTION_CALL") {
				malformed = true
			}
			return true
		})
		if malformed {
			return !visible, statusErr{code: http.StatusBadGateway, msg: "antigravity: upstream returned MALFORMED_FUNCTION_CALL"}
		}
		if err := emit(payload); err != nil {
			return false, err
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	if err := o.ctx.Err(); err != nil {
		return false, err
	}
	return false, decoder.Finish()
}
