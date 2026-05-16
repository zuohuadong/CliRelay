package util

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultHTTPClientTimeout = 30 * time.Second

	defaultProviderHTTPResponseLimit = 1 << 20 // 1 MiB
)

// NewHTTPClient creates a plain HTTP client with an optional timeout.
func NewHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

// ProviderHTTPResponseLimit returns the safe response-body read limit for provider control-plane calls.
func ProviderHTTPResponseLimit(provider string) int64 {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex-device", "codex-oauth", "claude-oauth", "gemini-oauth", "qwen-oauth", "iflow-oauth", "kimi-oauth", "antigravity-oauth":
		return defaultProviderHTTPResponseLimit
	default:
		return defaultProviderHTTPResponseLimit
	}
}

// ReadHTTPResponseBody reads a provider HTTP response with a hard upper bound.
func ReadHTTPResponseBody(provider string, r io.Reader) ([]byte, error) {
	limit := ProviderHTTPResponseLimit(provider)
	if r == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return data, err
	}
	if int64(len(data)) > limit {
		return data[:limit], fmt.Errorf("%s response body exceeds %d byte read limit", providerLabel(provider), limit)
	}
	return data, nil
}

func providerLabel(provider string) string {
	if trimmed := strings.TrimSpace(provider); trimmed != "" {
		return trimmed
	}
	return "provider"
}
