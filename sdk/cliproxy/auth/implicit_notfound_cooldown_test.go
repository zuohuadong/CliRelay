package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestIsImplicitUpstreamNotFoundResultError(t *testing.T) {
	cloudflareHTML := "<!DOCTYPE html><html><head><title>404 Not Found</title></head><body>cloudflare</body></html>"
	cases := []struct {
		name string
		err  *Error
		want bool
	}{
		{
			name: "cloudflare html 404 is implicit",
			err:  &Error{Message: cloudflareHTML, HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "unmarked html 404 is implicit",
			err:  &Error{Message: "<html><head><title>404 Not Found</title></head><body>nginx</body></html>", HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "empty 404 body is implicit",
			err:  &Error{Message: "", HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "plain text 404 body is implicit",
			err:  &Error{Message: "Not Found", HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "gateway json 404 without not-found identifier is implicit",
			err:  &Error{Message: `{"error":"Not found"}`, HTTPStatus: http.StatusNotFound},
			want: true,
		},
		{
			name: "openai structured model_not_found 404 is explicit",
			err:  &Error{Message: `{"error":{"message":"The model 'gpt-5.6-terra' does not exist or you do not have access to it.","type":"invalid_request_error","param":"model","code":"model_not_found"}}`, HTTPStatus: http.StatusNotFound},
			want: false,
		},
		{
			name: "gemini structured NOT_FOUND 404 is explicit",
			err:  &Error{Message: `{"error":{"code":404,"message":"Model models/gemini-pro is not found for API version v1beta","status":"NOT_FOUND"}}`, HTTPStatus: http.StatusNotFound},
			want: false,
		},
		{
			name: "error code model_not_found is explicit even with plain message",
			err:  &Error{Code: "model_not_found", Message: "model missing", HTTPStatus: http.StatusNotFound},
			want: false,
		},
		{
			name: "error code not_found is explicit even with plain message",
			err:  &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
			want: false,
		},
		{
			name: "non-404 status is never implicit-not-found",
			err:  &Error{Message: cloudflareHTML, HTTPStatus: http.StatusForbidden},
			want: false,
		},
		{
			name: "nil error is never implicit-not-found",
			err:  nil,
			want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isImplicitUpstreamNotFoundResultError(testCase.err); got != testCase.want {
				t.Fatalf("isImplicitUpstreamNotFoundResultError() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestApplyAuthFailureStateImplicit404UsesTransientCooldown(t *testing.T) {
	now := time.Now()

	// Unmarked HTML 404: no cloudflare challenge markers, so the error reaches
	// the plain 404 cooldown branch instead of the challenge backoff path.
	implicitErr := &Error{Message: "<html><head><title>404 Not Found</title></head></html>", HTTPStatus: http.StatusNotFound}
	auth := &Auth{ID: "auth-implicit-404"}
	applyAuthFailureState(auth, implicitErr, nil, now, false)
	if auth.NextRetryAfter.IsZero() {
		t.Fatal("implicit 404 should keep a finite retry deadline")
	}
	if auth.NextRetryAfter.After(now.Add(time.Hour)) {
		t.Fatalf("implicit 404 cooldown %v is too long; want transient-level cooldown", auth.NextRetryAfter.Sub(now))
	}
	if auth.StatusMessage != "implicit upstream 404" {
		t.Fatalf("status message = %q, want %q", auth.StatusMessage, "implicit upstream 404")
	}

	explicitErr := &Error{Message: `{"error":{"message":"The model 'gpt-x' does not exist","type":"invalid_request_error","code":"model_not_found"}}`, HTTPStatus: http.StatusNotFound}
	explicitAuth := &Auth{ID: "auth-explicit-404"}
	applyAuthFailureState(explicitAuth, explicitErr, nil, now, false)
	// Structured model-not-found bodies are request faults: the credential is
	// left untouched so the pool stays available for other models.
	if !explicitAuth.NextRetryAfter.IsZero() {
		t.Fatalf("explicit model-not-found 404 must not cool the credential, got %v", explicitAuth.NextRetryAfter.Sub(now))
	}
}

func TestMarkResultImplicit404DoesNotDrainCredentialPool(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-implicit-404-model",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	// Edge-node 404 without challenge markers or a structured model-not-found
	// payload: previously this hit the 12h "model does not exist" cooldown.
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-terra",
		Success:  false,
		Error: &Error{
			Message:    "<html><head><title>404 Not Found</title></head><body></body></html>",
			HTTPStatus: http.StatusNotFound,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth after failure")
	}
	state := updated.ModelStates["gpt-5.6-terra"]
	if state == nil {
		t.Fatal("expected model state after failure")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("implicit 404 should keep a finite model retry deadline")
	}
	if wait := time.Until(state.NextRetryAfter); wait > time.Hour {
		t.Fatalf("implicit 404 model cooldown %v is too long; want transient-level cooldown", wait)
	}
	// The whole credential must not be locked for 12 hours either.
	if wait := time.Until(updated.NextRetryAfter); wait > time.Hour {
		t.Fatalf("implicit 404 credential cooldown %v is too long; want transient-level cooldown", wait)
	}
	// Recovery: after the transient deadline the model becomes available again,
	// so a persistent Cloudflare block no longer drains the entire pool.
	if blocked, _, _ := isAuthBlockedForModel(updated, "gpt-5.6-terra", state.NextRetryAfter.Add(time.Second)); blocked {
		t.Fatal("auth did not recover after the transient implicit-404 deadline")
	}
}

func TestMarkResultExplicitModelNotFoundSkipsCredentialCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-explicit-404-model",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	// A structured OpenAI model-not-found body is a request fault: it must not
	// cool the credential at all (request-scoped), keeping the pool available
	// for other models.
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.6-terra",
		Success:  false,
		Error: &Error{
			Message:    `{"error":{"message":"The model 'gpt-5.6-terra' does not exist or you do not have access to it.","type":"invalid_request_error","param":"model","code":"model_not_found"}}`,
			HTTPStatus: http.StatusNotFound,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth after failure")
	}
	if blocked, _, _ := isAuthBlockedForModel(updated, "gpt-5", time.Now()); blocked {
		t.Fatalf("explicit model-not-found 404 must not cool the credential for other models; NextRetryAfter=%v", updated.NextRetryAfter)
	}
	if !updated.NextRetryAfter.IsZero() && time.Until(updated.NextRetryAfter) > time.Hour {
		t.Fatalf("explicit model-not-found 404 must not trigger a long credential cooldown, got %v", time.Until(updated.NextRetryAfter))
	}
}
