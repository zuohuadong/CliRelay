package executor

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveBedrockModelID_FriendlyMapping(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{"region": "us-east-1"}}
	got, ok := resolveBedrockModelID(auth, "claude-sonnet-4-5")
	if !ok {
		t.Fatalf("expected mapping for claude-sonnet-4-5")
	}
	if got != "us.anthropic.claude-sonnet-4-5-20250929-v1:0" {
		t.Fatalf("model id = %q", got)
	}
}

func TestResolveBedrockModelID_RegionalPrefixAdjusted(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{"region": "eu-west-1"}}
	got, ok := resolveBedrockModelID(auth, "claude-sonnet-4-5")
	if !ok {
		t.Fatalf("expected mapping")
	}
	if !strings.HasPrefix(got, "eu.") {
		t.Fatalf("expected eu. prefix, got %q", got)
	}
}

func TestResolveBedrockModelID_UnknownRejected(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{"region": "us-east-1"}}
	if _, ok := resolveBedrockModelID(auth, "random-model"); ok {
		t.Fatal("expected unknown model to be rejected")
	}
}

func TestResolveBedrockModelID_CustomMapping(t *testing.T) {
	auth := &coreauth.Auth{Attributes: map[string]string{
		"region":        "us-east-1",
		"model_mapping": `{"my-alias":"anthropic.claude-3-5-sonnet-20240620-v1:0"}`,
	}}
	got, ok := resolveBedrockModelID(auth, "my-alias")
	if !ok {
		t.Fatalf("expected custom mapping")
	}
	if got != "anthropic.claude-3-5-sonnet-20240620-v1:0" {
		t.Fatalf("got %q", got)
	}
}

func TestPrepareBedrockRequestBody_InjectsVersion(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	out, err := prepareBedrockRequestBody(body, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", nil)
	if err != nil {
		t.Fatalf("prepareBedrockRequestBody: %v", err)
	}
	if !strings.Contains(string(out), `"anthropic_version":"bedrock-2023-05-31"`) {
		t.Fatalf("missing anthropic_version: %s", out)
	}
	if strings.Contains(string(out), `"model"`) || strings.Contains(string(out), `"stream"`) {
		t.Fatalf("model/stream should be stripped: %s", out)
	}
}

func TestBedrockSigV4Signer_SignsRequest(t *testing.T) {
	signer, err := newBedrockSigV4Signer("AKIDEXAMPLE", "secret", "session", "us-east-1")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke", strings.NewReader(`{"messages":[]}`))
	if err := signer.signRequest(req, []byte(`{"messages":[]}`)); err != nil {
		t.Fatalf("signRequest: %v", err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("missing Authorization header: %q", auth)
	}
	if req.Header.Get("X-Amz-Date") == "" {
		t.Fatal("missing X-Amz-Date")
	}
	if req.Header.Get("X-Amz-Security-Token") != "session" {
		t.Fatalf("missing session token header")
	}
	if req.Header.Get("X-Amz-Content-Sha256") == "" {
		t.Fatal("missing content sha256")
	}
}

func TestBedrockSigV4Signer_CanonicalQueryUsesAWSEscaping(t *testing.T) {
	signer, err := newBedrockSigV4Signer("AKIDEXAMPLE", "secret", "", "us-east-1")
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke?z=last&a=x+y&a=x%2By", nil)
	got := signer.canonicalQuery(req.URL)
	want := "a=x%20y&a=x%2By&z=last"
	if got != want {
		t.Fatalf("canonical query = %q, want %q", got, want)
	}
}

func TestBedrockSigV4Signer_RequiresCredentials(t *testing.T) {
	if _, err := newBedrockSigV4Signer("", "secret", "", "us-east-1"); err == nil {
		t.Fatal("expected error for missing access key")
	}
	if _, err := newBedrockSigV4Signer("AKID", "", "", "us-east-1"); err == nil {
		t.Fatal("expected error for missing secret key")
	}
}

func TestParseBedrockClaudeUsage(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`)
	detail := parseBedrockClaudeUsage(data)
	if detail.InputTokens != 10 || detail.OutputTokens != 5 {
		t.Fatalf("tokens = %+v", detail)
	}
	if detail.CacheReadTokens != 3 {
		t.Fatalf("cache read = %d", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 2 {
		t.Fatalf("cache creation = %d", detail.CacheCreationTokens)
	}
}

func TestTransformBedrockInvocationMetrics(t *testing.T) {
	data := []byte(`{"type":"message","amazon-bedrock-invocationMetrics":{"inputTokenCount":7,"outputTokenCount":4}}`)
	out := transformBedrockInvocationMetrics(data)
	if !strings.Contains(string(out), `"input_tokens":7`) {
		t.Fatalf("metrics not transformed: %s", out)
	}
	if strings.Contains(string(out), "amazon-bedrock-invocationMetrics") {
		t.Fatalf("invocationMetrics should be removed: %s", out)
	}
}

func TestNewBedrockExecutor(t *testing.T) {
	e := NewBedrockExecutor(&config.Config{})
	if e.Identifier() != "bedrock" {
		t.Fatalf("identifier = %q", e.Identifier())
	}
}
