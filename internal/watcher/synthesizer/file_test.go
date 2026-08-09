package synthesizer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestNewFileSynthesizer(t *testing.T) {
	synth := NewFileSynthesizer()
	if synth == nil {
		t.Fatal("expected non-nil synthesizer")
	}
}

func TestResolveFileAuthKindSeparatesManagedAndStandaloneAgentIdentity(t *testing.T) {
	agentFields := map[string]any{
		"agent_runtime_id":  "runtime-1",
		"agent_private_key": "private-key",
		"task_id":           "task-1",
	}
	standalone := map[string]any{"type": coreauth.AuthKindAgentIdentity}
	managed := map[string]any{"type": "codex", "refresh_token": "refresh-token"}
	for key, value := range agentFields {
		standalone[key] = value
		managed[key] = value
	}
	if got := resolveFileAuthKind(standalone); got != coreauth.AuthKindAgentIdentity {
		t.Fatalf("standalone auth kind = %q", got)
	}
	if got := resolveFileAuthKind(managed); got != coreauth.AuthKindOAuth {
		t.Fatalf("managed auth kind = %q", got)
	}
}

func TestSynthesizeStandaloneAgentIdentitySelectsCodexProvider(t *testing.T) {
	fullPath := filepath.Join(t.TempDir(), "agent-auth.json")
	raw := []byte(`{
		"type":"agent_identity",
		"refresh_token":"stale-refresh",
		"agent_runtime_id":"runtime-1",
		"agent_private_key":"private-key",
		"task_id":"task-1",
		"account_id":"account-1"
	}`)
	auths, err := SynthesizeAuthFile(&SynthesisContext{Now: time.Now()}, fullPath, raw)
	if err != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "codex" {
		t.Fatalf("Provider = %q, want codex", auth.Provider)
	}
	if auth.AuthKind() != coreauth.AuthKindAgentIdentity {
		t.Fatalf("AuthKind() = %q, want agent_identity", auth.AuthKind())
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexExecutor(nil))
	if _, ok := manager.Executor(auth.Provider); !ok {
		t.Fatalf("no executor registered for synthesized provider %q", auth.Provider)
	}
}

func TestFileSynthesizer_Synthesize_NilContext(t *testing.T) {
	synth := NewFileSynthesizer()
	auths, err := synth.Synthesize(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_EmptyAuthDir(t *testing.T) {
	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     "",
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_NonExistentDir(t *testing.T) {
	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     "/non/existent/path",
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_ValidAuthFile(t *testing.T) {
	tempDir := t.TempDir()

	// Create a valid auth file
	authData := map[string]any{
		"type":      "claude",
		"email":     "test@example.com",
		"proxy_url": "http://proxy.local",
		"prefix":    "test-prefix",
		"headers": map[string]string{
			" X-Test ": " value ",
			"X-Empty":  "  ",
		},
		"disable_cooling": true,
		"request_retry":   2,
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "claude-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	if auths[0].Provider != "claude" {
		t.Errorf("expected provider claude, got %s", auths[0].Provider)
	}
	if auths[0].Label != "test@example.com" {
		t.Errorf("expected label test@example.com, got %s", auths[0].Label)
	}
	if auths[0].Prefix != "test-prefix" {
		t.Errorf("expected prefix test-prefix, got %s", auths[0].Prefix)
	}
	if auths[0].ProxyURL != "http://proxy.local" {
		t.Errorf("expected proxy_url http://proxy.local, got %s", auths[0].ProxyURL)
	}
	if got := auths[0].Attributes["header:X-Test"]; got != "value" {
		t.Errorf("expected header:X-Test value, got %q", got)
	}
	if _, ok := auths[0].Attributes["header:X-Empty"]; ok {
		t.Errorf("expected header:X-Empty to be absent, got %q", auths[0].Attributes["header:X-Empty"])
	}
	if v, ok := auths[0].Metadata["disable_cooling"].(bool); !ok || !v {
		t.Errorf("expected disable_cooling true, got %v", auths[0].Metadata["disable_cooling"])
	}
	if v, ok := auths[0].Metadata["request_retry"].(float64); !ok || int(v) != 2 {
		t.Errorf("expected request_retry 2, got %v", auths[0].Metadata["request_retry"])
	}
	if auths[0].Status != coreauth.StatusActive {
		t.Errorf("expected status active, got %s", auths[0].Status)
	}
}

func TestFileSynthesizer_Synthesize_OpenAICompatibilityProviderMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"provider":    "openai-compatibility",
		"compat_name": "agnes",
		"base_url":    "https://example.com/v1",
		"api_key":     "sk-test",
		"models": []map[string]any{
			{"name": "agnes-2.0-flash", "alias": "agnes-2.0-flash"},
			{"name": "agnes-image-2.1-flash", "alias": "agnes-image-2.1-flash", "image": true},
		},
	}
	data, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(tempDir, "agnes.json"), data, 0644); err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "openai-compatibility" {
		t.Fatalf("provider = %q, want openai-compatibility", auth.Provider)
	}
	if auth.Label != "agnes" {
		t.Fatalf("label = %q, want agnes", auth.Label)
	}
	if auth.Attributes["compat_name"] != "agnes" || auth.Attributes["provider_key"] != "agnes" {
		t.Fatalf("compat attrs = %#v", auth.Attributes)
	}
	if auth.Attributes["base_url"] != "https://example.com/v1" || auth.Attributes["api_key"] != "sk-test" {
		t.Fatalf("upstream attrs = %#v", auth.Attributes)
	}
	if auth.Attributes["auth_kind"] != "api_key" {
		t.Fatalf("auth_kind = %q, want api_key", auth.Attributes["auth_kind"])
	}
	if _, ok := auth.Metadata["models"]; !ok {
		t.Fatalf("models metadata missing: %#v", auth.Metadata)
	}
}

func TestFileSynthesizer_Synthesize_IgnoresGeminiProviderFile(t *testing.T) {
	tempDir := t.TempDir()

	authData := map[string]any{
		"type":  "gemini",
		"email": "gemini@example.com",
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "gemini-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected Gemini auth file to be ignored, got %d auths", len(auths))
	}
}

func TestSynthesizeAuthFileExpandsPluginMultiAuths(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "geminicli.json")
	raw := []byte(`{"type":"gemini-cli","excluded_models":["model-a"],"headers":{"X-Test":"value"}}`)

	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			if req.Provider != "gemini-cli" || req.Path != fullPath || req.FileName != "geminicli.json" {
				t.Fatalf("ParseAuths request = %#v, want file context", req)
			}
			return []*coreauth.Auth{
				{
					ID:       "geminicli.json",
					Provider: "gemini-cli",
					Metadata: map[string]any{
						"type": "gemini-cli",
						"headers": map[string]any{
							"X-Test": "value",
						},
					},
				},
				nil,
				{
					ID:       "geminicli-project-a.json",
					Provider: "gemini-cli",
					Metadata: map[string]any{
						"type":       "gemini-cli",
						"project_id": "project-a",
						"headers": map[string]any{
							"X-Test": "value",
						},
					},
				},
			}, true, nil
		}),
	}

	auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 2 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want two plugin auths", len(auths))
	}
	if firstIndex, secondIndex := auths[0].EnsureIndex(), auths[1].EnsureIndex(); firstIndex == "" || firstIndex == secondIndex {
		t.Fatalf("auth indexes = %q/%q, want distinct non-empty indexes", firstIndex, secondIndex)
	}
	for _, auth := range auths {
		if !coreauth.IsPluginVirtualAuth(auth) {
			t.Fatalf("auth attributes = %#v, want plugin virtual marker", auth.Attributes)
		}
		if auth.Attributes[coreauth.AttributeVirtualSource] != fullPath {
			t.Fatalf("virtual_source = %q, want %q", auth.Attributes[coreauth.AttributeVirtualSource], fullPath)
		}
		if auth.Attributes["path"] != fullPath || auth.Attributes["source"] != fullPath {
			t.Fatalf("auth attributes = %#v, want source path", auth.Attributes)
		}
		if gotHeader := auth.Attributes["header:X-Test"]; gotHeader != "value" {
			t.Fatalf("header:X-Test = %q, want value", gotHeader)
		}
		if gotKind := auth.Attributes["auth_kind"]; gotKind != "oauth" {
			t.Fatalf("auth_kind = %q, want oauth", gotKind)
		}
	}
	if gotProject := auths[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestSynthesizeAuthFileSkipsInvalidPluginAuthWeight(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "plugin.json")
	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			return []*coreauth.Auth{
				{ID: "invalid", Provider: "plugin", Attributes: map[string]string{coreauth.AttributeWeight: "1.5"}},
				{ID: "valid", Provider: "plugin", Attributes: map[string]string{coreauth.AttributeWeight: "0"}},
			}, true, nil
		}),
	}

	auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, []byte(`{"type":"plugin"}`))
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 || auths[0].ID != "valid" {
		t.Fatalf("SynthesizeAuthFile() auths = %#v, want only valid zero-weight auth", auths)
	}
}

func TestSynthesizeAuthFileAppliesSourceDisabledToPluginMultiAuths(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "geminicli.json")
	raw := []byte(`{"type":"gemini-cli","disabled":true}`)

	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			return []*coreauth.Auth{
				{ID: "geminicli.json", Provider: "gemini-cli", Metadata: map[string]any{"type": "gemini-cli"}},
				{ID: "geminicli-project-a.json", Provider: "gemini-cli", Metadata: map[string]any{"type": "gemini-cli", "project_id": "project-a"}},
			}, true, nil
		}),
	}

	auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 2 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want two plugin auths", len(auths))
	}
	for _, auth := range auths {
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("auth %s disabled/status = %v/%s, want disabled", auth.ID, auth.Disabled, auth.Status)
		}
		if got, _ := auth.Metadata["disabled"].(bool); !got {
			t.Fatalf("auth %s metadata disabled = %#v, want true", auth.ID, auth.Metadata["disabled"])
		}
	}
}

func TestSynthesizeAuthFilePluginHandledEmptySuppressesBuiltin(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "codex.json")
	raw := []byte(`{"type":"codex","access_token":"token"}`)

	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			return nil, true, nil
		}),
	}

	auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 0 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want plugin-handled empty result", len(auths))
	}
}

type multiAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)

func (f multiAuthParserFunc) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return nil, false, nil
}

func (f multiAuthParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return f(ctx, req)
}

func TestFileSynthesizer_Synthesize_SkipsInvalidFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create various invalid files
	_ = os.WriteFile(filepath.Join(tempDir, "not-json.txt"), []byte("text content"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "invalid.json"), []byte("not valid json"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "empty.json"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "no-type.json"), []byte(`{"email": "test@example.com"}`), 0644)

	// Create one valid file
	validData, _ := json.Marshal(map[string]any{"type": "claude", "email": "valid@example.com"})
	_ = os.WriteFile(filepath.Join(tempDir, "valid.json"), validData, 0644)

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("only valid auth file should be processed, got %d", len(auths))
	}
	if auths[0].Label != "valid@example.com" {
		t.Errorf("expected label valid@example.com, got %s", auths[0].Label)
	}
}

func TestFileSynthesizer_Synthesize_SkipsDirectories(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory with a json file inside
	subDir := filepath.Join(tempDir, "subdir.json")
	err := os.Mkdir(subDir, 0755)
	if err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Create a valid file in root
	validData, _ := json.Marshal(map[string]any{"type": "claude"})
	_ = os.WriteFile(filepath.Join(tempDir, "valid.json"), validData, 0644)

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_RelativeID(t *testing.T) {
	tempDir := t.TempDir()

	authData := map[string]any{"type": "claude"}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "my-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	// ID should be relative path
	if auths[0].ID != "my-auth.json" {
		t.Errorf("expected ID my-auth.json, got %s", auths[0].ID)
	}
}

func TestFileSynthesizer_Synthesize_PrefixValidation(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		wantPrefix string
	}{
		{"valid prefix", "myprefix", "myprefix"},
		{"prefix with slashes trimmed", "/myprefix/", "myprefix"},
		{"prefix with spaces trimmed", "  myprefix  ", "myprefix"},
		{"prefix with internal slash rejected", "my/prefix", ""},
		{"empty prefix", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			authData := map[string]any{
				"type":   "claude",
				"prefix": tt.prefix,
			}
			data, _ := json.Marshal(authData)
			_ = os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)

			synth := NewFileSynthesizer()
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, err := synth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}
			if auths[0].Prefix != tt.wantPrefix {
				t.Errorf("expected prefix %q, got %q", tt.wantPrefix, auths[0].Prefix)
			}
		})
	}
}

func TestFileSynthesizer_Synthesize_PriorityParsing(t *testing.T) {
	tests := []struct {
		name     string
		priority any
		want     string
		hasValue bool
	}{
		{
			name:     "string with spaces",
			priority: " 10 ",
			want:     "10",
			hasValue: true,
		},
		{
			name:     "number",
			priority: 8,
			want:     "8",
			hasValue: true,
		},
		{
			name:     "invalid string",
			priority: "1x",
			hasValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			authData := map[string]any{
				"type":     "claude",
				"priority": tt.priority,
			}
			data, _ := json.Marshal(authData)
			errWriteFile := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)
			if errWriteFile != nil {
				t.Fatalf("failed to write auth file: %v", errWriteFile)
			}

			synth := NewFileSynthesizer()
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, errSynthesize := synth.Synthesize(ctx)
			if errSynthesize != nil {
				t.Fatalf("unexpected error: %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}

			value, ok := auths[0].Attributes["priority"]
			if tt.hasValue {
				if !ok {
					t.Fatal("expected priority attribute to be set")
				}
				if value != tt.want {
					t.Fatalf("expected priority %q, got %q", tt.want, value)
				}
				return
			}
			if ok {
				t.Fatalf("expected priority attribute to be absent, got %q", value)
			}
		})
	}
}

func TestFileSynthesizer_Synthesize_WeightParsing(t *testing.T) {
	tests := []struct {
		name   string
		weight any
		want   string
		valid  bool
	}{
		{name: "number", weight: 5, want: "5", valid: true},
		{name: "numeric string", weight: " 3 ", want: "3", valid: true},
		{name: "zero excludes", weight: 0, want: "0", valid: true},
		{name: "negative excludes", weight: -5, want: "0", valid: true},
		{name: "maximum", weight: 1000000, want: "1000000", valid: true},
		{name: "fraction rejected", weight: 1.5},
		{name: "above maximum rejected", weight: 1000001},
		{name: "overflow rejected", weight: "9223372036854775808"},
		{name: "invalid string", weight: "heavy"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			tempDir := t.TempDir()
			data, errMarshal := json.Marshal(map[string]any{"type": "claude", "weight": testCase.weight})
			if errMarshal != nil {
				t.Fatalf("json.Marshal() error = %v", errMarshal)
			}
			if errWrite := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644); errWrite != nil {
				t.Fatalf("WriteFile() error = %v", errWrite)
			}
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}
			auths, errSynthesize := NewFileSynthesizer().Synthesize(ctx)
			if errSynthesize != nil {
				t.Fatalf("Synthesize() error = %v", errSynthesize)
			}
			if !testCase.valid {
				if len(auths) != 0 {
					t.Fatalf("auth count = %d, want invalid credential skipped", len(auths))
				}
				if _, errDirect := SynthesizeAuthFile(ctx, filepath.Join(tempDir, "auth.json"), data); errDirect == nil {
					t.Fatal("SynthesizeAuthFile() error = nil, want weight validation error")
				}
				return
			}
			if len(auths) != 1 {
				t.Fatalf("auth count = %d, want 1", len(auths))
			}
			if gotWeight := auths[0].Attributes[coreauth.AttributeWeight]; gotWeight != testCase.want {
				t.Fatalf("weight = %q, want %q", gotWeight, testCase.want)
			}
		})
	}
}

func TestFileSynthesizer_Synthesize_OAuthExcludedModelsMerged(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"type":            "claude",
		"excluded_models": []string{"custom-model", "MODEL-B"},
	}
	data, _ := json.Marshal(authData)
	errWriteFile := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)
	if errWriteFile != nil {
		t.Fatalf("failed to write auth file: %v", errWriteFile)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"claude": {"shared", "model-b"},
			},
		},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("unexpected error: %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	got := auths[0].Attributes["excluded_models"]
	want := "custom-model,model-b,shared"
	if got != want {
		t.Fatalf("expected excluded_models %q, got %q", want, got)
	}
}

func TestFileSynthesizer_Synthesize_OAuthModelAliases(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"type":  "codex",
		"email": "codex@example.com",
		"model-aliases": []map[string]any{
			{"name": " gpt-5.3-codex-spark ", "alias": " gpt-5.5 "},
			{"name": "gpt-5.3-codex-spark", "alias": "gpt-5.4", "fork": true},
			{"name": "gpt-5.3-codex-spark", "alias": "gpt-5.5"},
			{"name": "", "alias": "ignored"},
		},
	}
	data, _ := json.Marshal(authData)
	errWriteFile := os.WriteFile(filepath.Join(tempDir, "codex-auth.json"), data, 0644)
	if errWriteFile != nil {
		t.Fatalf("failed to write auth file: %v", errWriteFile)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("unexpected error: %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	got := auths[0].Attributes["model_aliases"]
	want := `[{"name":"gpt-5.3-codex-spark","alias":"gpt-5.5"},{"name":"gpt-5.3-codex-spark","alias":"gpt-5.4","fork":true}]`
	if got != want {
		t.Fatalf("expected model_aliases %q, got %q", want, got)
	}
}

func TestFileSynthesizer_Synthesize_IgnoresGeminiOAuthFile(t *testing.T) {
	tempDir := t.TempDir()

	authData := map[string]any{
		"type":       "gemini",
		"email":      "multi@example.com",
		"project_id": "project-a, project-b, project-c",
		"priority":   " 10 ",
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "gemini-multi.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected Gemini auth file to be ignored, got %d auths", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_NoteParsing(t *testing.T) {
	tests := []struct {
		name     string
		note     any
		want     string
		hasValue bool
	}{
		{
			name:     "valid string note",
			note:     "hello world",
			want:     "hello world",
			hasValue: true,
		},
		{
			name:     "string note with whitespace",
			note:     "  trimmed note  ",
			want:     "trimmed note",
			hasValue: true,
		},
		{
			name:     "empty string note",
			note:     "",
			hasValue: false,
		},
		{
			name:     "whitespace only note",
			note:     "   ",
			hasValue: false,
		},
		{
			name:     "non-string note ignored",
			note:     12345,
			hasValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			authData := map[string]any{
				"type": "claude",
				"note": tt.note,
			}
			data, _ := json.Marshal(authData)
			errWriteFile := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)
			if errWriteFile != nil {
				t.Fatalf("failed to write auth file: %v", errWriteFile)
			}

			synth := NewFileSynthesizer()
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, errSynthesize := synth.Synthesize(ctx)
			if errSynthesize != nil {
				t.Fatalf("unexpected error: %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}

			value, ok := auths[0].Attributes["note"]
			if tt.hasValue {
				if !ok {
					t.Fatal("expected note attribute to be set")
				}
				if value != tt.want {
					t.Fatalf("expected note %q, got %q", tt.want, value)
				}
				return
			}
			if ok {
				t.Fatalf("expected note attribute to be absent, got %q", value)
			}
		})
	}
}

func TestFileSynthesizerCodexIgnoresPersistedEgressIDAndExposesStableIdentity(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	data := []byte(`{"type":"codex","account_id":"acct-123","egress_id":"endpoint-1","email":"user@example.test"}`)
	if err := os.WriteFile(filepath.Join(tempDir, "codex.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{Config: &config.Config{}, AuthDir: tempDir, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %#v", auths)
	}
	if got, ok := auths[0].Attributes["egress_id"]; ok {
		t.Fatalf("legacy egress_id leaked into runtime attributes: %q", got)
	}
	if got, want := auths[0].Attributes["stable_identity"], "codex:3abf465e869e7b65598ec70e64b86462802516681a49069caa7947457c9d17aa"; got != want {
		t.Fatalf("stable_identity = %q, want %q", got, want)
	}
}

func TestFileSynthesizerCodexBackfillsAccountIDFromJWT(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	// Auth file with no account_id but has an id_token containing chatgpt_account_id
	data := []byte(`{"type":"codex","email":"user@example.test","id_token":"eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWp3dC1iYWNrZmlsbCIsICJjaGF0Z3B0X3BsYW5fdHlwZSI6ICJwbHVzIn19.ZmFrZXNpZw"}`)
	if err := os.WriteFile(filepath.Join(tempDir, "codex-no-acct.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{Config: &config.Config{}, AuthDir: tempDir, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %#v", auths)
	}
	if got, want := auths[0].Attributes["stable_identity"], "codex:501cfdc3adf6652457ca5e79cc10aecdfe75100580565578e20a6da0e714e1aa"; got != want {
		t.Fatalf("stable_identity = %q, want %q", got, want)
	}
	if got, want := auths[0].Attributes["plan_type"], "plus"; got != want {
		t.Fatalf("plan_type = %q, want %q", got, want)
	}
	// Verify account_id was backfilled in metadata
	if got, _ := auths[0].Metadata["account_id"].(string); got != "acct-jwt-backfill" {
		t.Fatalf("metadata account_id = %q, want acct-jwt-backfill", got)
	}
}

func TestFileSynthesizerFlattensVersionlessAgentIdentityBundleExport(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	// Agent Identity exports can omit the top-level version while keeping OAuth
	// tokens under accounts[0].credentials. The nested id_token must still be
	// promoted so identity resolution and JWT plan_type extraction work.
	idToken := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJiODkyY2VjNC00ZGI0LTQ5ZmQtOTJhMC03ZjhlYjg3MTY0N2IiLCAiY2hhdGdwdF9wbGFuX3R5cGUiOiAiazEyIn19.ZmFrZXNpZw"
	data := []byte(`{
		"type": "codex",
		"disabled": false,
		"exported_at": "2026-07-20T22:45:57Z",
		"proxies": [],
		"accounts": [
			{
				"name": "bundle@example.test",
				"type": "oauth",
				"platform": "openai",
				"priority": 1,
				"concurrency": 10,
				"rate_multiplier": 1,
				"auto_pause_on_expired": true,
				"credentials": {
					"email": "bundle@example.test",
					"id_token": "` + idToken + `",
					"plan_type": "k12",
					"auth_mode": "agentIdentity",
					"chatgpt_account_id": "b892cec4-4db4-49fd-92a0-7f8eb871647b"
				},
				"extra": {"source": "chatgpt_web_session"}
			}
		]
	}`)
	if err := os.WriteFile(filepath.Join(tempDir, "codex-bundle.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{Config: &config.Config{}, AuthDir: tempDir, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %#v", auths)
	}
	auth := auths[0]
	// The promoted id_token must derive and backfill account_id for egress identity.
	if got, _ := auth.Metadata["account_id"].(string); got != "b892cec4-4db4-49fd-92a0-7f8eb871647b" {
		t.Fatalf("metadata account_id = %v, want b892cec4-4db4-49fd-92a0-7f8eb871647b", auth.Metadata["account_id"])
	}
	// plan_type from the promoted id_token JWT.
	if got, want := auth.Attributes["plan_type"], "k12"; got != want {
		t.Fatalf("plan_type = %q, want %q", got, want)
	}
	// stable_identity derived from the promoted account_id.
	identity, err := egress.StableIdentity("b892cec4-4db4-49fd-92a0-7f8eb871647b")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := auth.Attributes["stable_identity"], identity; got != want {
		t.Fatalf("stable_identity = %q, want %q", got, want)
	}
	// email promoted from credentials for the label.
	if got, _ := auth.Metadata["email"].(string); got != "bundle@example.test" {
		t.Fatalf("metadata email = %v, want bundle@example.test", auth.Metadata["email"])
	}
}

func TestFileSynthesizerLeavesFlatCodexAuthUnchanged(t *testing.T) {
	t.Parallel()

	// Flat auth files (no "version"/"accounts") must not be treated as bundles.
	metadata := map[string]any{
		"type":       "codex",
		"account_id": "acct-flat",
		"id_token":   "ignored",
	}
	if got := expandCodexBundle(metadata); got != nil {
		t.Fatalf("expandCodexBundle() = %#v, want nil for flat auth", got)
	}
	if got, _ := metadata["account_id"].(string); got != "acct-flat" {
		t.Fatalf("flat account_id mutated to %v", metadata["account_id"])
	}
}

func TestExpandCodexBundlePreservesSourceDisabledForAllMembers(t *testing.T) {
	metadata := map[string]any{
		"type":     "codex",
		"disabled": true,
		"accounts": []any{
			map[string]any{
				"disabled": false,
				"credentials": map[string]any{
					"email": "first@example.test",
				},
			},
			map[string]any{
				"disabled": false,
				"credentials": map[string]any{
					"email": "second@example.test",
				},
			},
		},
	}

	expanded := expandCodexBundle(metadata)
	if len(expanded) != 2 {
		t.Fatalf("expandCodexBundle() len = %d, want 2", len(expanded))
	}
	for index, account := range expanded {
		if disabled, _ := account["disabled"].(bool); !disabled {
			t.Fatalf("expanded account %d disabled = %v, want true", index, account["disabled"])
		}
	}
}

func TestFileSynthesizerExpandsMultiAccountAgentIdentityBundle(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	// Agent Identity exports can carry several accounts under accounts[]. Each
	// must expand into a distinct Auth with its own id_token/account_id/identity
	// and a distinct ID so the watcher registers both instead of one clobbering
	// the other.
	idTokenA := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWFycC0wMDEiLCAiY2hhdGdwdF9wbGFuX3R5cGUiOiAiazEyIn19.ZmFrZXNpZw"
	idTokenB := "eyJhbGciOiAiUlMyNTYiLCAidHlwIjogIkpXVCJ9.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOiB7ImNoYXRncHRfYWNjb3VudF9pZCI6ICJhY2N0LWFycC0wMDIiLCAiY2hhdGdwdF9wbGFuX3R5cGUiOiAiazEyIn19.ZmFrZXNpZw"
	data := []byte(`{
		"type": "codex",
		"disabled": false,
		"exported_at": "2026-07-26T22:30:34Z",
		"proxies": [],
		"accounts": [
			{
				"name": "alice@example.test",
				"type": "oauth",
				"platform": "openai",
				"priority": 1,
				"concurrency": 10,
				"credentials": {
					"email": "alice@example.test",
					"id_token": "` + idTokenA + `",
					"plan_type": "k12",
					"auth_mode": "agentIdentity",
					"chatgpt_account_id": "acct-arp-001"
				}
			},
			{
				"name": "bob@example.test",
				"type": "oauth",
				"platform": "openai",
				"priority": 2,
				"concurrency": 5,
				"credentials": {
					"email": "bob@example.test",
					"id_token": "` + idTokenB + `",
					"plan_type": "k12",
					"auth_mode": "agentIdentity",
					"chatgpt_account_id": "acct-arp-002"
				}
			}
		]
	}`)
	if err := os.WriteFile(filepath.Join(tempDir, "codex-bundle.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{Config: &config.Config{}, AuthDir: tempDir, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("auths = %#v, want 2 expanded accounts", auths)
	}
	alice := auths[0]
	bob := auths[1]
	// Distinct IDs: baseID#0 and baseID#1.
	if got, want := alice.ID, "codex-bundle.json#0"; got != want {
		t.Fatalf("alice ID = %q, want %q", got, want)
	}
	if got, want := bob.ID, "codex-bundle.json#1"; got != want {
		t.Fatalf("bob ID = %q, want %q", got, want)
	}
	// Per-account credentials promoted independently.
	if got, _ := alice.Metadata["email"].(string); got != "alice@example.test" {
		t.Fatalf("alice email = %v, want alice@example.test", got)
	}
	if got, _ := bob.Metadata["email"].(string); got != "bob@example.test" {
		t.Fatalf("bob email = %v, want bob@example.test", got)
	}
	if got, _ := alice.Metadata["account_id"].(string); got != "acct-arp-001" {
		t.Fatalf("alice account_id = %v, want acct-arp-001", got)
	}
	if got, _ := bob.Metadata["account_id"].(string); got != "acct-arp-002" {
		t.Fatalf("bob account_id = %v, want acct-arp-002", got)
	}
	// Per-account priority promoted from account-level fields.
	if got := alice.Attributes["priority"]; got != "1" {
		t.Fatalf("alice priority = %q, want 1", got)
	}
	if got := bob.Attributes["priority"]; got != "2" {
		t.Fatalf("bob priority = %q, want 2", got)
	}
	// Distinct stable identities per account.
	identA, err := egress.StableIdentity("acct-arp-001")
	if err != nil {
		t.Fatal(err)
	}
	identB, err := egress.StableIdentity("acct-arp-002")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := alice.Attributes["stable_identity"], identA; got != want {
		t.Fatalf("alice stable_identity = %q, want %q", got, want)
	}
	if got, want := bob.Attributes["stable_identity"], identB; got != want {
		t.Fatalf("bob stable_identity = %q, want %q", got, want)
	}
	if identA == identB {
		t.Fatalf("stable identities collide: %q", identA)
	}
	// Bundle members are independently indexed without being treated as
	// plugin-owned virtual auths, so management can disable/delete their source.
	if coreauth.IsPluginVirtualAuth(alice) || coreauth.IsPluginVirtualAuth(bob) {
		t.Fatalf("multi-account auths must not be plugin virtual: alice=%v bob=%v", coreauth.IsPluginVirtualAuth(alice), coreauth.IsPluginVirtualAuth(bob))
	}
	if !coreauth.IsFileBundleAuth(alice) || !coreauth.IsFileBundleAuth(bob) {
		t.Fatalf("multi-account auths must be marked as bundle members: alice=%v bob=%v", coreauth.IsFileBundleAuth(alice), coreauth.IsFileBundleAuth(bob))
	}
	if alice.Attributes[coreauth.AttributeAuthIndexSeed] == bob.Attributes[coreauth.AttributeAuthIndexSeed] {
		t.Fatalf("index seeds collide: %q", alice.Attributes[coreauth.AttributeAuthIndexSeed])
	}
}

func TestFileSynthesizerPromotesOAuthWeight(t *testing.T) {
	tempDir := t.TempDir()
	data := []byte(`{"type":"claude","email":"weighted@example.test","access_token":"token","weight":7}`)
	if err := os.WriteFile(filepath.Join(tempDir, "claude-weighted.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes[coreauth.AttributeWeight]; got != "7" {
		t.Fatalf("weight attribute = %q, want 7", got)
	}
}
