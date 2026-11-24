package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

func useLocale(t *testing.T, locale string) {
	t.Helper()
	prev := CurrentLocale()
	SetLocale(locale)
	t.Cleanup(func() {
		SetLocale(prev)
	})
}

func sampleConfig() map[string]any {
	return map[string]any{
		"port":                     8080,
		"host":                     "127.0.0.1",
		"debug":                    true,
		"proxy-url":                "https://proxy.example.com",
		"request-retry":            3,
		"max-retry-interval":       30,
		"force-model-prefix":       "demo",
		"logging-to-file":          true,
		"logs-max-total-size-mb":   128,
		"error-logs-max-files":     5,
		"usage-statistics-enabled": true,
		"request-log":              true,
		"quota-exceeded": map[string]any{
			"switch-project":       true,
			"switch-preview-model": false,
		},
		"routing": map[string]any{
			"strategy": "round-robin",
		},
		"ws-auth": true,
		"ampcode": map[string]any{
			"upstream-url":                     "https://amp.example.com",
			"upstream-api-key":                 "amp-secret-key",
			"restrict-management-to-localhost": true,
		},
	}
}

func TestConfigTabRenderContentUsesLocalizedFieldLabels(t *testing.T) {
	useLocale(t, "zh")

	m := newConfigTabModel(nil)
	m.width = 100
	m.fields = m.parseConfig(sampleConfig())

	content := m.renderContent()
	if !strings.Contains(content, "代理 URL") {
		t.Fatalf("expected zh config label in content, got:\n%s", content)
	}
	if !strings.Contains(content, "最大重试间隔（秒）") {
		t.Fatalf("expected translated retry interval label in content, got:\n%s", content)
	}
	if strings.Contains(content, "Max Retry Interval (s)") {
		t.Fatalf("unexpected english config label in zh content:\n%s", content)
	}
}

func TestAuthTabRenderDetailUsesLocalizedLabels(t *testing.T) {
	useLocale(t, "zh")

	m := newAuthTabModel(nil)
	content := m.renderDetail(map[string]any{
		"name":       "demo",
		"channel":    "gemini",
		"email":      "user@example.com",
		"file_name":  "auth.json",
		"proxy_url":  "https://proxy.example.com",
		"priority":   1,
		"created_at": "2026-03-12T14:00:00Z",
	})

	if !strings.Contains(content, "文件名") {
		t.Fatalf("expected translated auth detail label in content, got:\n%s", content)
	}
	if !strings.Contains(content, "代理 URL") {
		t.Fatalf("expected translated proxy label in auth detail, got:\n%s", content)
	}
	if strings.Contains(content, "File Name") {
		t.Fatalf("unexpected english auth detail label in zh content:\n%s", content)
	}
}

func TestKeysTabRenderContentUsesLocalizedSectionTitles(t *testing.T) {
	useLocale(t, "zh")

	m := newKeysTabModel(nil)
	m.width = 100
	m.keys = []string{"sk-demo-secret"}
	m.gemini = []map[string]any{{"api-key": "gm-demo-secret"}}
	m.openai = []map[string]any{{
		"name":     "Compat Demo",
		"base-url": "https://api.example.com",
	}}

	content := m.renderContent()
	if !strings.Contains(content, "访问 API 密钥") {
		t.Fatalf("expected translated access key section title, got:\n%s", content)
	}
	if !strings.Contains(content, "Gemini API 密钥") {
		t.Fatalf("expected translated provider section title, got:\n%s", content)
	}
	if !strings.Contains(content, "OpenAI 兼容配置") {
		t.Fatalf("expected translated openai compatibility title, got:\n%s", content)
	}
	if strings.Contains(content, "Access API Keys") {
		t.Fatalf("unexpected english section title in zh content:\n%s", content)
	}
}

func TestLogsAndUsageRenderLocalizedLabels(t *testing.T) {
	useLocale(t, "zh")

	logs := newLogsTabModel(nil, nil)
	logs.lastErr = errors.New("boom")
	logContent := logs.renderLogs()
	if !strings.Contains(logContent, "⚠ 错误: boom") {
		t.Fatalf("expected translated log error prefix, got:\n%s", logContent)
	}
	if !strings.Contains(logContent, "全部") {
		t.Fatalf("expected translated log filter label, got:\n%s", logContent)
	}

	usage := newUsageTabModel(nil)
	usage.width = 120
	usage.usage = map[string]any{
		"usage": map[string]any{
			"total_requests": 1,
			"success_count":  1,
			"failure_count":  0,
			"total_tokens":   10,
			"apis": map[string]any{
				"secret-demo-api-key": map[string]any{
					"total_requests": 1,
					"total_tokens":   10,
					"models": map[string]any{
						"gpt-4.1": map[string]any{
							"total_requests": 1,
							"total_tokens":   10,
						},
					},
				},
			},
		},
	}

	usageContent := usage.renderContent()
	if !strings.Contains(usageContent, "接口") {
		t.Fatalf("expected translated usage api header, got:\n%s", usageContent)
	}
	if !strings.Contains(usageContent, "API 详细统计") {
		t.Fatalf("expected translated usage section title, got:\n%s", usageContent)
	}
}

func TestToggleLocaleChangesRenderedConfigLabels(t *testing.T) {
	useLocale(t, "zh")

	m := newConfigTabModel(nil)
	m.width = 100
	m.fields = m.parseConfig(sampleConfig())

	zhContent := m.renderContent()
	if !strings.Contains(zhContent, "代理 URL") {
		t.Fatalf("expected zh label before toggle, got:\n%s", zhContent)
	}

	ToggleLocale()
	enContent := m.renderContent()
	if !strings.Contains(enContent, "Proxy URL") {
		t.Fatalf("expected english label after toggle, got:\n%s", enContent)
	}
	if strings.Contains(enContent, "代理 URL") {
		t.Fatalf("unexpected zh label after toggle:\n%s", enContent)
	}
}

func TestAuthEditPromptUpdatesOnLocaleChange(t *testing.T) {
	useLocale(t, "en")

	m := newAuthTabModel(nil)
	m.viewport = viewport.New(80, 20)
	m.files = []map[string]any{{
		"name":      "demo",
		"proxy_url": "https://proxy.example.com",
	}}

	m.startEdit(1)
	if !strings.Contains(m.editInput.Prompt, "Proxy URL") {
		t.Fatalf("expected english prompt before locale change, got %q", m.editInput.Prompt)
	}

	SetLocale("zh")
	updated, _ := m.Update(localeChangedMsg{})
	m = updated
	if !strings.Contains(m.editInput.Prompt, "代理 URL") {
		t.Fatalf("expected zh prompt after locale change, got %q", m.editInput.Prompt)
	}
}

func TestKeysPromptUpdatesOnLocaleChange(t *testing.T) {
	useLocale(t, "en")

	m := newKeysTabModel(nil)
	m.viewport = viewport.New(80, 20)
	m.adding = true
	m.refreshInputPrompt()
	if !strings.Contains(m.editInput.Prompt, "New Key") {
		t.Fatalf("expected english add prompt before locale change, got %q", m.editInput.Prompt)
	}

	SetLocale("zh")
	updated, _ := m.Update(localeChangedMsg{})
	m = updated
	if !strings.Contains(m.editInput.Prompt, "新 API Key") {
		t.Fatalf("expected zh add prompt after locale change, got %q", m.editInput.Prompt)
	}
}

func TestOAuthInputTextsUpdateOnLocaleChange(t *testing.T) {
	useLocale(t, "en")

	m := newOAuthTabModel(nil)
	m.viewport = viewport.New(80, 20)
	if !strings.Contains(m.callbackInput.Prompt, "Callback URL") {
		t.Fatalf("expected english oauth prompt before locale change, got %q", m.callbackInput.Prompt)
	}
	if !strings.Contains(m.callbackInput.Placeholder, "http://localhost") {
		t.Fatalf("expected english oauth placeholder before locale change, got %q", m.callbackInput.Placeholder)
	}

	SetLocale("zh")
	updated, _ := m.Update(localeChangedMsg{})
	m = updated
	if !strings.Contains(m.callbackInput.Prompt, "回调 URL") {
		t.Fatalf("expected zh oauth prompt after locale change, got %q", m.callbackInput.Prompt)
	}
	if !strings.Contains(m.callbackInput.Placeholder, "请粘贴回调 URL") {
		t.Fatalf("expected zh oauth placeholder after locale change, got %q", m.callbackInput.Placeholder)
	}
}
