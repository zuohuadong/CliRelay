// Command clirelay-replay replays a recorded request so a production failure can
// be reproduced on demand instead of guessed at.
//
// A failure that only shows up in production is hard to argue about: the client
// reports a symptom, the proxy reports a completion, and the upstream body that
// explains it was the one thing not kept. The request-log tables keep both the
// client's body and the upstream's own log for the requests that failed, so this
// tool replays one of them, prints the event stream it gets back, and says
// whether it ended on a terminal event.
//
//	go run ./cmd/clirelay-replay -dsn "postgres://..." -log-id 3419814 -api-key sk-...
//	go run ./cmd/clirelay-replay -in body.json -base https://cliapi.example -api-key sk-...
//
// It never writes to the database and never touches credentials.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
}

type options struct {
	dsn      string
	logID    int64
	in       string
	base     string
	apiKey   string
	model    string
	path     string
	repeat   int
	timeout  time.Duration
	outDir   string
	compare  bool
	showBody bool
}

func run(args []string) error {
	var opts options
	flag.StringVar(&opts.dsn, "dsn", "", "PostgreSQL DSN to load -log-id from")
	flag.Int64Var(&opts.logID, "log-id", 0, "request_logs.id to replay (needs -dsn)")
	flag.StringVar(&opts.in, "in", "", "file holding the client request body (default: -log-id or stdin)")
	flag.StringVar(&opts.base, "base", "http://127.0.0.1:8317", "relay base URL")
	flag.StringVar(&opts.apiKey, "api-key", "", "API key to send as bearer (or CLIRELAY_REPLAY_KEY)")
	flag.StringVar(&opts.model, "model", "", "override the request model")
	flag.StringVar(&opts.path, "path", "/v1/responses", "request path")
	flag.IntVar(&opts.repeat, "repeat", 1, "attempts to make")
	flag.DurationVar(&opts.timeout, "timeout", 3*time.Minute, "per-attempt timeout")
	flag.StringVar(&opts.outDir, "out", "", "directory to write each attempt's raw SSE into")
	flag.BoolVar(&opts.compare, "compare", true, "with -log-id, show the stored upstream log's last events too")
	flag.BoolVar(&opts.showBody, "show-body", false, "print the replayed request body")
	flag.Parse()

	if opts.apiKey == "" {
		opts.apiKey = strings.TrimSpace(os.Getenv("CLIRELAY_REPLAY_KEY"))
	}
	body, err := loadBody(opts)
	if err != nil {
		return err
	}
	if opts.model != "" {
		body, err = withModel(body, opts.model)
		if err != nil {
			return err
		}
	}
	if opts.showBody {
		fmt.Printf("--- request (%d bytes) ---\n%s\n", len(body), body)
	}
	if opts.apiKey == "" {
		return errors.New("-api-key (or CLIRELAY_REPLAY_KEY) is required")
	}

	failures := 0
	for attempt := 1; attempt <= opts.repeat; attempt++ {
		res, err := replayOnce(opts, body, attempt)
		if err != nil {
			return err
		}
		if !res.terminal {
			failures++
		}
	}

	if opts.compare && opts.logID > 0 {
		if stored := loadStoredUpstreamTail(opts.dsn, opts.logID); stored != "" {
			fmt.Printf("--- stored upstream response (log %d) ---\n%s\n", opts.logID, stored)
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d attempt(s) ended without a terminal event", failures, opts.repeat)
	}
	return nil
}

func loadBody(opts options) ([]byte, error) {
	switch {
	case opts.in != "":
		return os.ReadFile(opts.in)
	case opts.logID > 0:
		if opts.dsn == "" {
			return nil, errors.New("-log-id needs -dsn")
		}
		return loadClientBody(opts.dsn, opts.logID)
	default:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, errors.New("no request body: pass -in, -log-id, or stdin")
		}
		return data, nil
	}
}

func withModel(body []byte, model string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("request body is not a JSON object: %w", err)
	}
	payload["model"] = model
	payload["stream"] = true
	return json.Marshal(payload)
}

type attemptResult struct {
	status   int
	events   []string
	terminal bool
	errorMsg string
	took     time.Duration
}

func replayOnce(opts options, body []byte, attempt int) (attemptResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	url := strings.TrimSuffix(opts.base, "/") + opts.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return attemptResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.apiKey)

	started := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return attemptResult{}, err
	}
	defer resp.Body.Close()

	result := attemptResult{status: resp.StatusCode}
	var raw bytes.Buffer
	summary := parseSSE(resp.Body, &raw, func(event string) {
		result.events = append(result.events, event)
		if isTerminalEvent(event) {
			result.terminal = true
		}
	})
	result.errorMsg = summary
	result.took = time.Since(started)

	fmt.Printf("attempt %d: HTTP %d in %s\n", attempt, result.status, result.took.Round(time.Millisecond))
	for _, event := range result.events {
		fmt.Println("  ", event)
	}
	if result.errorMsg != "" {
		fmt.Println("   error:", result.errorMsg)
	}
	if result.status >= 400 && !result.terminal {
		fmt.Printf("   body: %s\n", truncate(raw.String(), 400))
	}
	if result.terminal {
		fmt.Println("   terminal: yes")
	} else {
		fmt.Println("   terminal: NO (the client would report a truncated stream)")
	}
	if opts.outDir != "" {
		name := fmt.Sprintf("%s/attempt-%d.sse", strings.TrimSuffix(opts.outDir, "/"), attempt)
		if errWrite := os.WriteFile(name, raw.Bytes(), 0o644); errWrite != nil {
			return result, errWrite
		}
		fmt.Println("   saved:", name)
	}
	return result, nil
}

// parseSSE reads an SSE stream, reporting each event name (or the payload's type
// when the stream does not name it), and returns the first error message it sees.
// The raw bytes are collected into raw for the caller.
func parseSSE(r io.Reader, raw *bytes.Buffer, onEvent func(event string)) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, 8<<20)
	var eventName, data string
	errorMsg := ""
	flush := func() {
		if eventName == "" && data == "" {
			return
		}
		name := eventName
		if name == "" {
			name = jsonType(data)
		}
		if name == "" {
			name = "<untyped>"
		}
		onEvent(name)
		if errorMsg == "" && (name == "error" || name == "response.failed") {
			errorMsg = jsonError(data)
		}
		eventName, data = "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			chunk := strings.TrimPrefix(line, "data:")
			chunk = strings.TrimSpace(chunk)
			if data != "" && chunk != "" {
				data += "\n"
			}
			data += chunk
		}
	}
	flush()
	return errorMsg
}

func jsonType(data string) string {
	var payload struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(data), &payload) == nil {
		return strings.TrimSpace(payload.Type)
	}
	return ""
}

func jsonError(data string) string {
	var payload struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
		Response struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &payload) != nil {
		return truncate(data, 300)
	}
	for _, candidate := range []string{
		payload.Response.Error.Message,
		payload.Error.Message,
		payload.Message,
	} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return truncate(data, 300)
}

func isTerminalEvent(event string) bool {
	switch event {
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		return true
	}
	return false
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
