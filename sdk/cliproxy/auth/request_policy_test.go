package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type requestPolicyTestExecutor struct {
	id string

	mu              sync.Mutex
	calls           []string
	models          []string
	payloads        [][]byte
	responseByModel map[string][]byte
	err             error
}

func (e *requestPolicyTestExecutor) Identifier() string { return e.id }

func (e *requestPolicyTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = req
	_ = opts
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.calls = append(e.calls, authID)
	e.models = append(e.models, req.Model)
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	e.mu.Unlock()
	if e.err != nil {
		return cliproxyexecutor.Response{}, e.err
	}
	if payload := e.responseByModel[req.Model]; len(payload) > 0 {
		return cliproxyexecutor.Response{Payload: append([]byte(nil), payload...)}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *requestPolicyTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = req
	_ = opts
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.calls = append(e.calls, authID)
	e.models = append(e.models, req.Model)
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"ok":true}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *requestPolicyTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *requestPolicyTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *requestPolicyTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *requestPolicyTestExecutor) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *requestPolicyTestExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func (e *requestPolicyTestExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	for i := range e.payloads {
		out[i] = append([]byte(nil), e.payloads[i]...)
	}
	return out
}

type requestPolicyStatusError struct {
	status int
	msg    string
}

func (e requestPolicyStatusError) Error() string   { return e.msg }
func (e requestPolicyStatusError) StatusCode() int { return e.status }

func TestRequestPolicyLimitError_RequestTooLargeUsesContextLengthExceededAnd413(t *testing.T) {
	err := &requestPolicyLimitError{
		policy:           "glm-limit",
		requestedModel:   "gpt-5.3-codex",
		upstreamProvider: "bigmodel-coding",
		upstreamModel:    "glm-5.1",
		requestBytes:     715241,
		maxRequestBytes:  600000,
		reason:           "request_bytes 715241 exceeds max-request-bytes 600000",
		action:           requestPolicyActionReject,
	}

	if got := err.StatusCode(); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", got, http.StatusRequestEntityTooLarge)
	}

	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if unmarshalErr := json.Unmarshal([]byte(err.Error()), &payload); unmarshalErr != nil {
		t.Fatalf("unmarshal error payload: %v", unmarshalErr)
	}
	if payload.Error.Code != "context_length_exceeded" {
		t.Fatalf("error code = %q, want %q", payload.Error.Code, "context_length_exceeded")
	}
	if payload.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q, want %q", payload.Error.Type, "invalid_request_error")
	}
}

func TestManagerExecute_RequestPolicySkipChannelFallsBack(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	codex := &requestPolicyTestExecutor{id: "codex"}
	manager.RegisterExecutor(bigmodel)
	manager.RegisterExecutor(codex)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "bigmodel-coding",
				BaseURL: "https://bigmodel.example/v1",
				APIKeyEntries: []internalconfig.OpenAICompatibilityAPIKey{
					{APIKey: "bigmodel-key"},
				},
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-limit",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"base_url":     "https://bigmodel.example/v1",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
		{
			ID:       "codex-auth",
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "codex@example.com"},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	resp, err := manager.Execute(context.Background(), []string{"bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
	}, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
		Metadata:        map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 42},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("response = %s", resp.Payload)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls = %v, want none", calls)
	}
	if calls := codex.Calls(); len(calls) != 1 || calls[0] != "codex-auth" {
		t.Fatalf("codex calls = %v, want [codex-auth]", calls)
	}
}

func TestRequestPolicyDecision_AstronCode360KGuard(t *testing.T) {
	cfg := &internalconfig.Config{
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "astron-code-360k-guard",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 360000},
			},
		},
	}
	auth := &Auth{Provider: "astron-code"}

	blocked, errLimit := requestPolicyDecision(cfg, auth, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 350000},
	}, "gpt-5.3-codex", "astron-code", "astron-code-latest")
	if blocked || errLimit != nil {
		t.Fatalf("350KB request blocked=%v err=%v, want allowed", blocked, errLimit)
	}

	blocked, errLimit = requestPolicyDecision(cfg, auth, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 370000},
	}, "gpt-5.3-codex", "astron-code", "astron-code-latest")
	if !blocked || errLimit == nil {
		t.Fatalf("370KB request blocked=%v err=%v, want blocked", blocked, errLimit)
	}
	if errLimit.policy != "astron-code-360k-guard" {
		t.Fatalf("policy = %q, want astron-code-360k-guard", errLimit.policy)
	}
}

func TestRequestPolicyDecision_RequiredToolsDoesNotSkipAstron(t *testing.T) {
	cfg := &internalconfig.Config{
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "astron-code-unsupported-tools-skip",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
					RequestFeatures:   []string{"xunfei-unsupported-tools"},
				},
			},
		},
	}
	auth := &Auth{Provider: "astron-code"}

	blocked, errLimit := requestPolicyDecision(cfg, auth, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestBytesMetadataKey:    120000,
			cliproxyexecutor.RequestFeaturesMetadataKey: []string{"tools", "required-tools"},
		},
	}, "gpt-5.3-codex", "astron-code", "astron-code-latest")
	if blocked || errLimit != nil {
		t.Fatalf("required tools blocked=%v err=%v, want allowed", blocked, errLimit)
	}
}

func TestManagerExecute_RequestPolicyCompressThenUsesOriginalProvider(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	compressed := []byte(`{"model":"gpt-5.3-codex","input":"short"}`)
	compressor := &requestPolicyTestExecutor{
		id: "gemini",
		responseByModel: map[string][]byte{
			"gemini-3-flash-preview": []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"model\":\"gpt-5.3-codex\",\"input\":\"short\"}"}]}]}`),
		},
	}
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(compressor)
	manager.RegisterExecutor(bigmodel)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "bigmodel-coding",
				BaseURL: "https://bigmodel.example/v1",
				APIKeyEntries: []internalconfig.OpenAICompatibilityAPIKey{
					{APIKey: "bigmodel-key"},
				},
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-compress",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
				OverLimit: internalconfig.RequestPolicyOverLimit{
					Action: "compress",
					Compression: internalconfig.RequestPolicyCompression{
						Provider:           "gemini",
						Model:              "gemini-3-flash-preview",
						TargetRequestBytes: 80,
					},
				},
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "gemini-auth",
			Provider: "gemini",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key": "gemini-key",
			},
		},
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"base_url":     "https://bigmodel.example/v1",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		model := "gpt-5.3-codex"
		if auth.Provider == "gemini" {
			model = "gemini-3-flash-preview"
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model, Name: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	original := []byte(`{"model":"gpt-5.3-codex","input":"this payload is intentionally long enough to require compression before GLM"}`)
	resp, err := manager.Execute(context.Background(), []string{"bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: original,
	}, cliproxyexecutor.Options{
		OriginalRequest: original,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		Metadata:        map[string]any{cliproxyexecutor.RequestBytesMetadataKey: len(original)},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("response = %s", resp.Payload)
	}
	if calls := compressor.Calls(); len(calls) != 1 || calls[0] != "gemini-auth" {
		t.Fatalf("compressor calls = %v, want [gemini-auth]", calls)
	}
	if models := bigmodel.Models(); len(models) != 1 || models[0] != "glm-5.1" {
		t.Fatalf("bigmodel models = %v, want [glm-5.1]", models)
	}
	payloads := bigmodel.Payloads()
	if len(payloads) != 1 || string(payloads[0]) != string(compressed) {
		t.Fatalf("bigmodel payloads = %q, want %q", payloads, compressed)
	}
}

func TestManagerExecute_RequestPolicyCompressionUnavailableSkipsCompression(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(bigmodel)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "bigmodel-coding",
				BaseURL: "https://bigmodel.example/v1",
				APIKeyEntries: []internalconfig.OpenAICompatibilityAPIKey{
					{APIKey: "bigmodel-key"},
				},
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-compress",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
				OverLimit: internalconfig.RequestPolicyOverLimit{
					Action: "compress",
					Compression: internalconfig.RequestPolicyCompression{
						Provider:           "gemini",
						Model:              "missing-compressor-model",
						TargetRequestBytes: 80,
					},
				},
			},
		},
	})

	auth := &Auth{
		ID:       "bigmodel-auth",
		Provider: "bigmodel-coding",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "bigmodel-key",
			"base_url":     "https://bigmodel.example/v1",
			"provider_key": "bigmodel-coding",
			"compat_name":  "bigmodel-coding",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register bigmodel: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	original := []byte(`{"model":"gpt-5.3-codex","input":"this payload stays original because compressor auth is unavailable"}`)
	_, err := manager.Execute(context.Background(), []string{"bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: original,
	}, cliproxyexecutor.Options{
		OriginalRequest: original,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		Metadata:        map[string]any{cliproxyexecutor.RequestBytesMetadataKey: len(original)},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	payloads := bigmodel.Payloads()
	if len(payloads) != 1 || string(payloads[0]) != string(original) {
		t.Fatalf("bigmodel payloads = %q, want original %q", payloads, original)
	}
}

func TestManagerExecute_AstronCodePrefersSmallRequestsAndSkipsMCP(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	astron := &requestPolicyTestExecutor{id: "astron-code"}
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(astron)
	manager.RegisterExecutor(bigmodel)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "astron-code",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "astron-code-latest", Alias: "gpt-5.3-codex"},
				},
			},
		},
		BigModelCodingAPIKey: []internalconfig.OpenAICompatibility{
			{
				Name: "bigmodel-coding",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		ProviderPreferences: []internalconfig.ProviderPreference{
			{
				Name: "prefer-astron-code",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
				},
				Priority: 100,
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "astron-code-360k-guard",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 360000},
			},
			{
				Name: "astron-code-mcp-skip",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
					RequestFeatures:   []string{"mcp"},
				},
			},
			{
				Name: "astron-code-unsupported-tools-skip",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
					RequestFeatures:   []string{"xunfei-unsupported-tools"},
				},
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "astron-auth",
			Provider: "astron-code",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "astron-key",
				"provider_key": "astron-code",
				"compat_name":  "astron-code",
			},
		},
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"small"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 120000},
	})
	if err != nil {
		t.Fatalf("Execute() small error = %v", err)
	}
	if calls := astron.Calls(); len(calls) != 1 || calls[0] != "astron-auth" {
		t.Fatalf("astron calls = %v, want [astron-auth]", calls)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls after small request = %v, want none", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"mcp"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestBytesMetadataKey:    120000,
			cliproxyexecutor.RequestFeaturesMetadataKey: []string{"tools", "mcp"},
		},
	})
	if err != nil {
		t.Fatalf("Execute() mcp error = %v", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 1 || calls[0] != "bigmodel-auth" {
		t.Fatalf("bigmodel calls after mcp request = %v, want [bigmodel-auth]", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"auto function tool"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestBytesMetadataKey:    120000,
			cliproxyexecutor.RequestFeaturesMetadataKey: []string{"tools"},
		},
	})
	if err != nil {
		t.Fatalf("Execute() auto tools error = %v", err)
	}
	if calls := astron.Calls(); len(calls) != 2 || calls[1] != "astron-auth" {
		t.Fatalf("astron calls after auto tools request = %v, want second astron-auth", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"unsupported tool"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestBytesMetadataKey:    120000,
			cliproxyexecutor.RequestFeaturesMetadataKey: []string{"tools", "xunfei-unsupported-tools"},
		},
	})
	if err != nil {
		t.Fatalf("Execute() unsupported tools error = %v", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 2 || calls[1] != "bigmodel-auth" {
		t.Fatalf("bigmodel calls after unsupported tools request = %v, want second bigmodel-auth", calls)
	}
}

func TestManagerExecuteStream_AstronCodeTransientErrorDoesNotFallBackToBigmodel(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	astronErr := requestPolicyStatusError{status: http.StatusBadGateway, msg: "astron upstream closed before first payload"}
	astron := &requestPolicyTestExecutor{id: "astron-code", err: astronErr}
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(astron)
	manager.RegisterExecutor(bigmodel)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "astron-code",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "astron-code-latest", Alias: "gpt-5.3-codex"},
				},
			},
		},
		BigModelCodingAPIKey: []internalconfig.OpenAICompatibility{
			{
				Name: "bigmodel-coding",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		ProviderPreferences: []internalconfig.ProviderPreference{
			{
				Name: "prefer-astron-code",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
				},
				Priority: 100,
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "astron-auth",
			Provider: "astron-code",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "astron-key",
				"provider_key": "astron-code",
				"compat_name":  "astron-code",
			},
		},
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.ExecuteStream(context.Background(), []string{"astron-code", "bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"small"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 120000},
	})
	if err == nil || err.Error() != astronErr.Error() {
		t.Fatalf("ExecuteStream error = %v, want %v", err, astronErr)
	}
	if calls := astron.Calls(); len(calls) != 1 || calls[0] != "astron-auth" {
		t.Fatalf("astron calls = %v, want [astron-auth]", calls)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls after astron transient error = %v, want none", calls)
	}
}

func TestManagerExecute_ProviderPreferencePrefersBigmodelAndFallsBack(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	codex := &requestPolicyTestExecutor{id: "codex"}
	manager.RegisterExecutor(bigmodel)
	manager.RegisterExecutor(codex)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    "bigmodel-coding",
				BaseURL: "https://bigmodel.example/v1",
				APIKeyEntries: []internalconfig.OpenAICompatibilityAPIKey{
					{APIKey: "bigmodel-key"},
				},
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		ProviderPreferences: []internalconfig.ProviderPreference{
			{
				Name: "prefer-bigmodel-coding",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Priority: 100,
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-limit",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"base_url":     "https://bigmodel.example/v1",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
		{
			ID:       "codex-auth",
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "codex@example.com"},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.Execute(context.Background(), []string{"bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"small"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 5},
	})
	if err != nil {
		t.Fatalf("Execute() small error = %v", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 1 || calls[0] != "bigmodel-auth" {
		t.Fatalf("bigmodel calls = %v, want [bigmodel-auth]", calls)
	}
	if calls := codex.Calls(); len(calls) != 0 {
		t.Fatalf("codex calls after small request = %v, want none", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 42},
	})
	if err != nil {
		t.Fatalf("Execute() large error = %v", err)
	}
	if calls := codex.Calls(); len(calls) != 1 || calls[0] != "codex-auth" {
		t.Fatalf("codex calls after fallback = %v, want [codex-auth]", calls)
	}
}

func TestManagerExecute_CodexSparkAliasPreferenceAndGuard(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	codex := &requestPolicyTestExecutor{id: "codex"}
	astron := &requestPolicyTestExecutor{id: "astron-code"}
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(codex)
	manager.RegisterExecutor(astron)
	manager.RegisterExecutor(bigmodel)

	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "astron-code",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "astron-code-latest", Alias: "gpt-5.3-codex", ContextLength: 220000},
				},
			},
		},
		BigModelCodingAPIKey: []internalconfig.OpenAICompatibility{
			{
				Name: "bigmodel-coding",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex", ContextLength: 220000},
					{Name: "glm-5.2", Alias: "gpt-5.3-codex", ContextLength: 1048576},
				},
			},
		},
		OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5.3-codex-spark", Alias: "gpt-5.3-codex", Fork: true},
			},
		},
		ProviderPreferences: []internalconfig.ProviderPreference{
			{
				Name: "prefer-codex-spark",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"codex"},
					UpstreamModels:    []string{"gpt-5.3-codex-spark"},
				},
				Priority: 500,
			},
			{
				Name: "prefer-astron",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"astron-code"},
					UpstreamModels:    []string{"astron-code-latest"},
				},
				Priority: 300,
			},
			{
				Name: "prefer-bigmodel",
				Match: internalconfig.ProviderPreferenceMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
				},
				Priority: 100,
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "codex-spark-128k-guard",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"codex"},
					UpstreamModels:    []string{"gpt-5.3-codex-spark"},
				},
				Limits:    internalconfig.RequestPolicyLimits{MaxRequestBytes: 128000},
				OverLimit: internalconfig.RequestPolicyOverLimit{Action: "skip-channel"},
			},
		},
	}
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)

	for _, auth := range []*Auth{
		{
			ID:       "codex-auth",
			Provider: "codex",
			Status:   StatusActive,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"plan_type": "plus",
			},
		},
		{
			ID:       "astron-auth",
			Provider: "astron-code",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "astron-key",
				"provider_key": "astron-code",
				"compat_name":  "astron-code",
			},
		},
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		authID := auth.ID
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		})
	}
	registry.GetGlobalRegistry().RegisterClient("codex-auth", "codex", []*registry.ModelInfo{{ID: "gpt-5.3-codex-spark", Name: "gpt-5.3-codex-spark"}})
	registry.GetGlobalRegistry().RegisterClient("astron-auth", "astron-code", []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
	registry.GetGlobalRegistry().RegisterClient("bigmodel-auth", "bigmodel-coding", []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})

	_, err := manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"small"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 120000},
	})
	if err != nil {
		t.Fatalf("Execute() small error = %v", err)
	}
	if calls := codex.Calls(); len(calls) != 1 || calls[0] != "codex-auth" {
		t.Fatalf("codex calls = %v, want [codex-auth]", calls)
	}
	if models := codex.Models(); len(models) != 1 || models[0] != "gpt-5.3-codex-spark" {
		t.Fatalf("codex models = %v, want [gpt-5.3-codex-spark]", models)
	}
	if calls := astron.Calls(); len(calls) != 0 {
		t.Fatalf("astron calls after small request = %v, want none", calls)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls after small request = %v, want none", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 150000},
	})
	if err != nil {
		t.Fatalf("Execute() large error = %v", err)
	}
	if calls := astron.Calls(); len(calls) != 1 || calls[0] != "astron-auth" {
		t.Fatalf("astron calls after large request = %v, want [astron-auth]", calls)
	}
	if models := astron.Models(); len(models) != 1 || models[0] != "astron-code-latest" {
		t.Fatalf("astron models = %v, want [astron-code-latest]", models)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls after astron-sized request = %v, want none", calls)
	}

	_, err = manager.Execute(context.Background(), []string{"astron-code", "bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"oversized"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 220001},
	})
	if err != nil {
		t.Fatalf("Execute() oversized error = %v", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 1 || calls[0] != "bigmodel-auth" {
		t.Fatalf("bigmodel calls after oversized request = %v, want [bigmodel-auth]", calls)
	}
	if models := bigmodel.Models(); len(models) != 1 || models[0] != "glm-5.2" {
		t.Fatalf("bigmodel models = %v, want [glm-5.2]", models)
	}
	if calls := astron.Calls(); len(calls) != 1 {
		t.Fatalf("astron calls after oversized request = %v, want one previous call only", calls)
	}
}

func TestManagerExecute_RequestPolicyRejectsWhenNoFallback(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	manager.RegisterExecutor(bigmodel)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "bigmodel-coding",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-limit",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
				},
				Limits:    internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
				OverLimit: internalconfig.RequestPolicyOverLimit{Action: "reject"},
			},
		},
	})

	auth := &Auth{
		ID:       "bigmodel-auth",
		Provider: "bigmodel-coding",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "bigmodel-key",
			"provider_key": "bigmodel-coding",
			"compat_name":  "bigmodel-coding",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	_, err := manager.Execute(context.Background(), []string{"bigmodel-coding"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
	}, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"model":"gpt-5.3-codex","input":"large"}`),
		Metadata:        map[string]any{cliproxyexecutor.RequestBytesMetadataKey: 42},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if statusErr, ok := err.(interface{ StatusCode() int }); !ok || statusErr.StatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("error status = %#v, want 413", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls = %v, want none", calls)
	}
}

func TestManagerExecute_InvalidRequestErrorDoesNotFallback(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{
		id: "bigmodel-coding",
		err: requestPolicyStatusError{
			status: http.StatusBadRequest,
			msg:    `{"error":{"message":"multimodal adapter matched this route but no extractor is configured","type":"invalid_request_error","code":"multimodal_extractor_unavailable"}}`,
		},
	}
	codex := &requestPolicyTestExecutor{id: "codex"}
	manager.RegisterExecutor(bigmodel)
	manager.RegisterExecutor(codex)
	manager.SetConfig(&internalconfig.Config{})

	for _, auth := range []*Auth{
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"priority": "10",
			},
		},
		{
			ID:       "codex-auth",
			Provider: "codex",
			Status:   StatusActive,
			Attributes: map[string]string{
				"priority": "1",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.Execute(context.Background(), []string{"bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"small"}`),
	}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls := bigmodel.Calls(); len(calls) != 1 || calls[0] != "bigmodel-auth" {
		t.Fatalf("bigmodel calls = %v, want [bigmodel-auth]", calls)
	}
	if calls := codex.Calls(); len(calls) != 0 {
		t.Fatalf("codex calls = %v, want none", calls)
	}
}

func TestManagerExecute_RequestPolicyFeatureSkipChannel(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	bigmodel := &requestPolicyTestExecutor{id: "bigmodel-coding"}
	codex := &requestPolicyTestExecutor{id: "codex"}
	manager.RegisterExecutor(bigmodel)
	manager.RegisterExecutor(codex)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "bigmodel-coding",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "glm-5.1", Alias: "gpt-5.3-codex"},
				},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{
			{
				Name: "glm-multimodal-skip",
				Match: internalconfig.RequestPolicyMatch{
					RequestedModels:   []string{"gpt-5.3-codex"},
					UpstreamProviders: []string{"bigmodel-coding"},
					UpstreamModels:    []string{"glm-5.1"},
					RequestFeatures:   []string{"multimodal"},
				},
			},
		},
	})

	for _, auth := range []*Auth{
		{
			ID:       "bigmodel-auth",
			Provider: "bigmodel-coding",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      "bigmodel-key",
				"provider_key": "bigmodel-coding",
				"compat_name":  "bigmodel-coding",
			},
		},
		{
			ID:       "codex-auth",
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "codex@example.com"},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-5.3-codex", Name: "gpt-5.3-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	_, err := manager.Execute(context.Background(), []string{"bigmodel-coding", "codex"}, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestFeaturesMetadataKey: []string{"multimodal"},
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if calls := bigmodel.Calls(); len(calls) != 0 {
		t.Fatalf("bigmodel calls = %v, want none", calls)
	}
	if calls := codex.Calls(); len(calls) != 1 || calls[0] != "codex-auth" {
		t.Fatalf("codex calls = %v, want [codex-auth]", calls)
	}
}
