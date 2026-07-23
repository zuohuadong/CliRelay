package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestRequestCompressionCacheReusesAndExtendsHistoryPrefix(t *testing.T) {
	manager, compressor, target := newCompressionTestManager(t, "bigmodel-coding", "cache-model", internalconfig.RequestPolicy{
		Name: "cache-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"cache-model"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"cache-model"},
		},
		Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider:            "gemini",
				Model:               "cache-flash",
				TargetRequestBytes:  1200,
				PreserveRecentItems: 1,
			},
		},
	}, &registry.ModelInfo{ID: "cache-model", Name: "cache-model", ContextLength: 100000})

	first := compressionTestRequest("cache-model", strings.Repeat("old prefix evidence ", 160), "latest first")
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "cache-model", first, nil)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "cache-model", first, nil)

	second := []byte(`{"model":"cache-model","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("old prefix evidence ", 160) + `"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest first"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest second"}]}` +
		`]}`)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "cache-model", second, nil)

	compressorPayloads := compressor.Payloads()
	if len(compressorPayloads) != 2 {
		t.Fatalf("compressor calls = %d, want 2 for identical+growing three requests", len(compressorPayloads))
	}
	secondPrompt := string(compressorPayloads[1])
	if !strings.Contains(secondPrompt, "previous_capsule") || !strings.Contains(secondPrompt, "latest first") {
		t.Fatalf("incremental compressor prompt did not reuse the previous capsule: %s", secondPrompt)
	}
	if strings.Contains(secondPrompt, strings.Repeat("old prefix evidence ", 8)) {
		t.Fatalf("incremental compressor resent the cached raw prefix: %s", secondPrompt)
	}
	if got := len(target.Payloads()); got != 3 {
		t.Fatalf("target calls = %d, want 3", got)
	}
}

func TestRequestCompressionAutoContextUsesModelTokenBudget(t *testing.T) {
	policy := internalconfig.RequestPolicy{
		Name: "auto-token-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"auto-token-model"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"auto-token-model"},
		},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider:            "gemini",
				Model:               "cache-flash",
				PreserveRecentItems: 1,
			},
		},
	}
	manager, compressor, target := newCompressionTestManager(t, "bigmodel-coding", "auto-token-model", policy, &registry.ModelInfo{
		ID:                  "auto-token-model",
		Name:                "auto-token-model",
		ContextLength:       1000,
		MaxCompletionTokens: 100,
	})
	raw := compressionTestRequest("auto-token-model", strings.Repeat("old context ", 35), "latest")

	executeCompressionTestRequest(t, manager, "bigmodel-coding", "auto-token-model", raw, map[string]any{
		cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(500),
	})
	if got := len(compressor.Payloads()); got != 0 {
		t.Fatalf("compressor calls below auto trigger = %d, want 0", got)
	}

	executeCompressionTestRequest(t, manager, "bigmodel-coding", "auto-token-model", raw, map[string]any{
		cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(700),
	})
	if got := len(compressor.Payloads()); got != 1 {
		t.Fatalf("compressor calls above auto trigger = %d, want 1", got)
	}
	if got := len(target.Payloads()); got != 2 {
		t.Fatalf("target calls = %d, want 2", got)
	}
	if !strings.Contains(string(target.Payloads()[1]), "[compacted_context]") {
		t.Fatalf("target did not receive compacted context: %s", target.Payloads()[1])
	}
}

func TestRequestCompressionDoesNotTrustUnderreportedTokenMetadata(t *testing.T) {
	policy := internalconfig.RequestPolicy{
		Name: "underreported-token-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"underreported-model"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"underreported-model"},
		},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider: "gemini", Model: "cache-flash", PreserveRecentItems: 1,
			},
		},
	}
	manager, compressor, _ := newCompressionTestManager(t, "bigmodel-coding", "underreported-model", policy, &registry.ModelInfo{
		ID: "underreported-model", Name: "underreported-model", ContextLength: 1000, MaxCompletionTokens: 100,
	})
	raw := compressionTestRequest("underreported-model", strings.Repeat("historical context ", 500), "latest")
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "underreported-model", raw, map[string]any{
		cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(1),
	})
	if len(compressor.Payloads()) == 0 {
		t.Fatal("underreported handler metadata bypassed local full-request estimation")
	}
}

func TestCompressorContextCapacityPrefersTokenEstimateOverRequestBytes(t *testing.T) {
	cfg := &internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name: "token-aware",
		Models: []internalconfig.OpenAICompatibilityModel{{
			Name: "small-model", ContextLength: 100,
		}},
	}}}
	auth := &Auth{Provider: "openai-compatibility", Attributes: map[string]string{
		"provider_key": "token-aware",
		"compat_name":  "token-aware",
	}}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RequestBytesMetadataKey:         int64(1000),
		cliproxyexecutor.EstimatedInputTokensMetadataKey: int64(50),
		requestCompressionDisabledMetadataKey:            true,
	}}
	if !requestFitsConfiguredModelContext(cfg, auth, opts, "small-model") {
		t.Fatal("request bytes incorrectly overrode a valid token estimate")
	}
	opts.Metadata[cliproxyexecutor.EstimatedInputTokensMetadataKey] = int64(101)
	if requestFitsConfiguredModelContext(cfg, auth, opts, "small-model") {
		t.Fatal("estimated input tokens above context were accepted")
	}
}

func TestExplicitTokenLimitTightensAutomaticTarget(t *testing.T) {
	budget := explicitTokenBudget(requestCompressionBudget{triggerTokens: 656, targetTokens: 480}, internalconfig.RequestPolicy{
		Limits: internalconfig.RequestPolicyLimits{MaxInputTokens: 100},
		OverLimit: internalconfig.RequestPolicyOverLimit{Compression: internalconfig.RequestPolicyCompression{
			TriggerRatio: 0.82,
			TargetRatio:  0.60,
		}},
	})
	if budget.triggerTokens != 100 || budget.targetTokens != 73 {
		t.Fatalf("explicit token budget = trigger %d target %d, want 100/73", budget.triggerTokens, budget.targetTokens)
	}
}

func TestAutoContextBudgetRejectsImpossibleOutputReserve(t *testing.T) {
	budget := autoContextBudget(requestCompressionBudget{contextLength: 1000}, internalconfig.RequestPolicyCompression{
		ReserveOutputTokens: 950,
		SafetyMarginPercent: 10,
		TriggerRatio:        0.82,
		TargetRatio:         0.60,
	}, 0)
	if !budget.unusableContext {
		t.Fatalf("budget = %#v, want unusable context", budget)
	}
}

func TestDynamicByteTargetDoesNotInvalidateGrowingPrefixCache(t *testing.T) {
	policy := internalconfig.RequestPolicy{
		Name: "dynamic-target",
		OverLimit: internalconfig.RequestPolicyOverLimit{Compression: internalconfig.RequestPolicyCompression{
			Provider: "gemini", Model: "cache-flash", PreserveRecentItems: 1,
		}},
	}
	first := requestCompressionProfile(compressionProfileRequest{policy: policy, provider: "target", upstreamModel: "small", sourceFormat: "openai-response", historyField: "input", budget: requestCompressionBudget{targetBytes: 1000, dynamicTargetBytes: true}})
	second := requestCompressionProfile(compressionProfileRequest{policy: policy, provider: "target", upstreamModel: "small", sourceFormat: "openai-response", historyField: "input", budget: requestCompressionBudget{targetBytes: 2000, dynamicTargetBytes: true}})
	if first != second {
		t.Fatalf("dynamic target changed cache profile: %s != %s", first, second)
	}
}

func TestRequestCompressionChunksInitialHistoryForSmallCompressorWindow(t *testing.T) {
	policy := internalconfig.RequestPolicy{
		Name: "chunked-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"chunk-target"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"chunk-target"},
		},
		Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider: "gemini", Model: "cache-flash", TargetRequestBytes: 2000, PreserveRecentItems: 1,
			},
		},
	}
	manager, compressor, _ := newCompressionTestManager(t, "bigmodel-coding", "chunk-target", policy, &registry.ModelInfo{ID: "chunk-target", Name: "chunk-target", ContextLength: 100000})
	registry.GetGlobalRegistry().UnregisterClient("compression-test-compressor-auth")
	registry.GetGlobalRegistry().RegisterClient("compression-test-compressor-auth", "gemini", []*registry.ModelInfo{{
		ID: "cache-flash", Name: "cache-flash", ContextLength: 1400, MaxCompletionTokens: 100,
	}})
	raw := compressionTestRequest("chunk-target", strings.Repeat("large historical evidence ", 1200), "latest")
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "chunk-target", raw, nil)
	payloads := compressor.Payloads()
	if len(payloads) < 2 {
		t.Fatalf("compressor calls = %d, want multiple bounded batches", len(payloads))
	}
	limits, err := compressorTokenLimits("gemini", "cache-flash")
	if err != nil {
		t.Fatalf("compressorTokenLimits() error = %v", err)
	}
	for payloadIndex, payload := range payloads {
		if tokens := estimateCompressionTokens("cache-flash", payload); tokens > limits.inputTokens {
			t.Fatalf("compressor payload %d tokens = %d, limit %d", payloadIndex, tokens, limits.inputTokens)
		}
	}
	if !strings.Contains(string(payloads[1]), "previous_capsule") {
		t.Fatalf("second compressor batch did not reduce from the previous capsule: %s", payloads[1])
	}
}

func TestRequestCompressionMultimodalCompressorSummarizesOnlyOldImage(t *testing.T) {
	policy := internalconfig.RequestPolicy{
		Name: "multimodal-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"vision-target-model"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"vision-target-model"},
		},
		Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider:            "gemini",
				Model:               "cache-flash",
				TargetRequestBytes:  1600,
				PreserveRecentItems: 1,
				MediaMode:           "auto",
			},
		},
	}
	manager, compressor, target := newCompressionTestManager(t, "bigmodel-coding", "vision-target-model", policy, &registry.ModelInfo{
		ID:                       "vision-target-model",
		Name:                     "vision-target-model",
		ContextLength:            100000,
		SupportedInputModalities: []string{"text", "image"},
	})
	registry.GetGlobalRegistry().UnregisterClient("compression-test-compressor-auth")
	registry.GetGlobalRegistry().RegisterClient("compression-test-compressor-auth", "gemini", []*registry.ModelInfo{{
		ID:                       "cache-flash",
		Name:                     "cache-flash",
		SupportedInputModalities: []string{"text", "image"},
	}})

	raw := []byte(`{"model":"vision-target-model","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/old.png"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/current.png"},{"type":"input_text","text":"inspect current image"}]}` +
		`]}`)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "vision-target-model", raw, nil)

	compressorBody := string(compressor.Payloads()[0])
	if !strings.Contains(compressorBody, "old.png") || strings.Contains(compressorBody, "current.png") {
		t.Fatalf("compressor should see only old image: %s", compressorBody)
	}
	targetBody := string(target.Payloads()[0])
	if strings.Contains(targetBody, "old.png") || !strings.Contains(targetBody, "current.png") || !strings.Contains(targetBody, "media_observations") {
		t.Fatalf("target old/current media handling is wrong: %s", targetBody)
	}
}

func TestRequestCompressionTextTargetExtractsMediaBeforeCompaction(t *testing.T) {
	extractorCalls := 0
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls++
		_, _ = w.Write([]byte(`{"text":"The screenshot shows error E_CONN_RESET."}`))
	}))
	defer extractor.Close()

	policy := internalconfig.RequestPolicy{
		Name: "text-target-media-compress",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"text-target-model"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"text-target-model"},
		},
		Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider:            "gemini",
				Model:               "cache-flash",
				TargetRequestBytes:  1600,
				PreserveRecentItems: 1,
			},
		},
	}
	manager, compressor, target := newCompressionTestManager(t, "bigmodel-coding", "text-target-model", policy, &registry.ModelInfo{
		ID:                       "text-target-model",
		Name:                     "text-target-model",
		ContextLength:            100000,
		SupportedInputModalities: []string{"text"},
	})
	compressor.responseByModel["cache-flash"] = []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"version\":\"1\",\"summary\":\"The screenshot showed error E_CONN_RESET.\"}"}]}]}`)
	cfg := manager.runtimeConfig.Load().(*internalconfig.Config)
	enabled := true
	cfg.MultimodalAdapters = internalconfig.MultimodalAdaptersConfig{
		Enabled:       &enabled,
		DefaultAction: "extract",
		InjectAs:      "visual_context",
		Rules: []internalconfig.MultimodalAdapterRule{{
			Name:      "text-target-vision",
			Extractor: "vision",
			Match: internalconfig.MultimodalAdapterMatch{
				RequestedModels:   []string{"text-target-model"},
				UpstreamProviders: []string{"bigmodel-coding"},
				UpstreamModels:    []string{"text-target-model"},
				Protocols:         []string{"openai-response"},
			},
		}},
		Extractors: []internalconfig.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: extractor.URL}},
	}
	cfg.SanitizeMultimodalAdapters()
	manager.SetConfig(cfg)

	raw := []byte(`{"model":"text-target-model","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("old diagnostic notes ", 80) + `"},{"type":"input_image","image_url":"https://example.com/error.png"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue diagnosis"}]}` +
		`]}`)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "text-target-model", raw, nil)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "text-target-model", raw, nil)
	if extractorCalls != 1 {
		t.Fatalf("extractor calls = %d, want 1 across repeated relay requests", extractorCalls)
	}
	if len(compressor.Payloads()) != 1 {
		t.Fatalf("compressor calls = %d, want 1 across repeated relay requests", len(compressor.Payloads()))
	}
	if strings.Contains(string(compressor.Payloads()[0]), "error.png") {
		t.Fatalf("text compressor received raw media: %s", compressor.Payloads()[0])
	}
	targetBody := string(target.Payloads()[0])
	if strings.Contains(targetBody, "error.png") || !strings.Contains(targetBody, "E_CONN_RESET") || !strings.Contains(targetBody, "[compacted_context]") {
		t.Fatalf("text target did not receive extracted media plus compacted context: %s", targetBody)
	}
}

func TestRequestCompressionTextTargetReusesOldVisualSummaryWhenImageIsAdded(t *testing.T) {
	extractorCalls := 0
	extractor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extractorCalls++
		var request struct {
			Media []struct {
				URL string `json:"url"`
			} `json:"media"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode extractor request: %v", err)
		}
		response := `{"text":"image A visual summary"}`
		if len(request.Media) == 1 && strings.Contains(request.Media[0].URL, "b.png") {
			response = `{"text":"image B visual summary"}`
		}
		_, _ = w.Write([]byte(response))
	}))
	defer extractor.Close()

	policy := internalconfig.RequestPolicy{
		Name: "incremental-text-target-media",
		Match: internalconfig.RequestPolicyMatch{
			RequestedModels:   []string{"incremental-text-target"},
			UpstreamProviders: []string{"bigmodel-coding"},
			UpstreamModels:    []string{"incremental-text-target"},
		},
		Limits: internalconfig.RequestPolicyLimits{MaxRequestBytes: 10},
		OverLimit: internalconfig.RequestPolicyOverLimit{
			Action: "compress",
			Compression: internalconfig.RequestPolicyCompression{
				Provider: "gemini", Model: "cache-flash", TargetRequestBytes: 1800, PreserveRecentItems: 1,
			},
		},
	}
	manager, compressor, _ := newCompressionTestManager(t, "bigmodel-coding", "incremental-text-target", policy, &registry.ModelInfo{
		ID: "incremental-text-target", Name: "incremental-text-target", ContextLength: 100000, SupportedInputModalities: []string{"text"},
	})
	configureCompressionTestVisualExtractor(t, manager, extractor.URL, "incremental-text-target")

	first := []byte(`{"model":"incremental-text-target","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest first"}]}` +
		`]}`)
	second := []byte(`{"model":"incremental-text-target","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest first"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/b.png"},{"type":"input_text","text":"latest second"}]}` +
		`]}`)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "incremental-text-target", first, nil)
	executeCompressionTestRequest(t, manager, "bigmodel-coding", "incremental-text-target", second, nil)

	if extractorCalls != 2 {
		t.Fatalf("extractor calls = %d, want 2 for images A then B", extractorCalls)
	}
	payloads := compressor.Payloads()
	if len(payloads) != 2 {
		t.Fatalf("compressor calls = %d, want 2", len(payloads))
	}
	secondPrompt := string(payloads[1])
	if !strings.Contains(secondPrompt, "previous_capsule") || !strings.Contains(secondPrompt, "latest first") {
		t.Fatalf("second compressor prompt did not extend the cached capsule: %s", secondPrompt)
	}
	if strings.Contains(secondPrompt, "image A visual summary") || strings.Contains(secondPrompt, "a.png") {
		t.Fatalf("second compressor prompt resent image A history: %s", secondPrompt)
	}
}

func configureCompressionTestVisualExtractor(t *testing.T, manager *Manager, endpoint, model string) {
	t.Helper()
	cfg := manager.runtimeConfig.Load().(*internalconfig.Config)
	enabled := true
	cfg.MultimodalAdapters = internalconfig.MultimodalAdaptersConfig{
		Enabled: &enabled, DefaultAction: "extract", InjectAs: "visual_context",
		Rules: []internalconfig.MultimodalAdapterRule{{
			Name: "incremental-vision", Extractor: "vision",
			Match: internalconfig.MultimodalAdapterMatch{
				RequestedModels: []string{model}, UpstreamProviders: []string{"bigmodel-coding"}, UpstreamModels: []string{model}, Protocols: []string{"openai-response"},
			},
		}},
		Extractors: []internalconfig.MultimodalExtractorConfig{{Name: "vision", Type: "http", Endpoint: endpoint}},
	}
	cfg.SanitizeMultimodalAdapters()
	manager.SetConfig(cfg)
}

func newCompressionTestManager(t *testing.T, targetProvider, targetModel string, policy internalconfig.RequestPolicy, targetInfo *registry.ModelInfo) (*Manager, *requestPolicyTestExecutor, *requestPolicyTestExecutor) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	compressor := &requestPolicyTestExecutor{
		id: "gemini",
		responseByModel: map[string][]byte{
			"cache-flash": []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"version\":\"1\",\"summary\":\"cached history summary\",\"media_observations\":[\"old image evidence\"]}"}]}]}`),
		},
	}
	target := &requestPolicyTestExecutor{id: targetProvider}
	manager.RegisterExecutor(compressor)
	manager.RegisterExecutor(target)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name:    targetProvider,
				BaseURL: "https://target.example/v1",
				APIKeyEntries: []internalconfig.OpenAICompatibilityAPIKey{
					{APIKey: "target-key"},
				},
				Models: []internalconfig.OpenAICompatibilityModel{{
					Name:                targetModel,
					Alias:               targetModel,
					ContextLength:       targetInfo.ContextLength,
					MaxCompletionTokens: targetInfo.MaxCompletionTokens,
					InputModalities:     append([]string(nil), targetInfo.SupportedInputModalities...),
				}},
			},
		},
		RequestPolicies: []internalconfig.RequestPolicy{policy},
	})

	compressorAuth := &Auth{
		ID:       "compression-test-compressor-auth",
		Provider: "gemini",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key": "compressor-key",
		},
	}
	targetAuth := &Auth{
		ID:       "compression-test-" + targetProvider + "-auth",
		Provider: targetProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "target-key",
			"base_url":     "https://target.example/v1",
			"provider_key": targetProvider,
			"compat_name":  targetProvider,
		},
	}
	for _, auth := range []*Auth{compressorAuth, targetAuth} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}
	registry.GetGlobalRegistry().RegisterClient(compressorAuth.ID, compressorAuth.Provider, []*registry.ModelInfo{{ID: "cache-flash", Name: "cache-flash"}})
	registry.GetGlobalRegistry().RegisterClient(targetAuth.ID, targetAuth.Provider, []*registry.ModelInfo{targetInfo})
	manager.RefreshSchedulerEntry(compressorAuth.ID)
	manager.RefreshSchedulerEntry(targetAuth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(compressorAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(targetAuth.ID)
	})
	return manager, compressor, target
}

func compressionTestRequest(model, old, latest string) []byte {
	return []byte(`{"model":"` + model + `","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + old + `"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + latest + `"}]}` +
		`]}`)
}

func executeCompressionTestRequest(t *testing.T, manager *Manager, provider, model string, raw []byte, extraMeta map[string]any) {
	t.Helper()
	meta := map[string]any{cliproxyexecutor.RequestBytesMetadataKey: len(raw)}
	for key, value := range extraMeta {
		meta[key] = value
	}
	_, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model, Payload: raw}, cliproxyexecutor.Options{
		OriginalRequest: raw,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		Metadata:        meta,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}
