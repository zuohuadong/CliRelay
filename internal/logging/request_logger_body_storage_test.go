package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileRequestLoggerBodyStorageDisabledKeepsMetadataOnly(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	logger.SetBodyEnabled(false)

	err := logger.LogRequest(
		"/v1/responses", "POST", map[string][]string{"X-Trace": {"trace-1"}}, []byte(`{"secret":"request-secret"}`),
		200, map[string][]string{"X-Upstream": {"ok"}}, []byte(`{"secret":"response-secret"}`), nil,
		[]byte(`{"secret":"api-request-secret"}`), []byte(`{"secret":"api-response-secret"}`), nil, nil,
		"request-1", time.Now(), time.Now(),
	)
	if err != nil {
		t.Fatalf("LogRequest() error = %v", err)
	}
	entries, err := os.ReadDir(logsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("log entries = %#v, err=%v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(logsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(body)
	for _, secret := range []string{"request-secret", "response-secret", "api-request-secret", "api-response-secret"} {
		if strings.Contains(content, secret) {
			t.Fatalf("log retained %q: %s", secret, content)
		}
	}
	if !strings.Contains(content, "URL: /v1/responses") || !strings.Contains(content, "Status: 200") {
		t.Fatalf("metadata missing from log: %s", content)
	}
}

func TestFileRequestLoggerPurgeStoredBodiesPreservesMetadata(t *testing.T) {
	logsDir := t.TempDir()
	path := filepath.Join(logsDir, "historical.log")
	before := "=== REQUEST INFO ===\nURL: /v1/responses\n\n=== REQUEST BODY ===\nrequest-secret\n\n=== API REQUEST ===\napi-request-secret\n\n=== API ERROR RESPONSE ===\nHTTP Status: 502\napi-error-secret\n\n=== RESPONSE ===\nStatus: 502\nX-Upstream: failed\n\nresponse-secret\n"
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatalf("write historical log: %v", err)
	}

	logger := NewFileRequestLogger(true, logsDir, "", 0)
	if err := logger.PurgeStoredBodies(); err != nil {
		t.Fatalf("PurgeStoredBodies() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read purged log: %v", err)
	}
	content := string(after)
	for _, secret := range []string{"request-secret", "api-request-secret", "api-error-secret", "response-secret"} {
		if strings.Contains(content, secret) {
			t.Fatalf("purged log retained %q: %s", secret, content)
		}
	}
	for _, metadata := range []string{"URL: /v1/responses", "HTTP Status: 502", "Status: 502", "X-Upstream: failed", "<not stored>"} {
		if !strings.Contains(content, metadata) {
			t.Fatalf("purged log missing %q: %s", metadata, content)
		}
	}
}
