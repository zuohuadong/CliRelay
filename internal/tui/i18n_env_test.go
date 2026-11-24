package tui

import "testing"

func TestNormalizeLocale(t *testing.T) {
	testCases := map[string]string{
		"":            "",
		"zh":          "zh",
		"zh_CN.UTF-8": "zh",
		"EN":          "en",
		"en_US.UTF-8": "en",
		"fr_FR":       "",
	}

	for input, want := range testCases {
		if got := normalizeLocale(input); got != want {
			t.Fatalf("normalizeLocale(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInitLocaleFromEnvPrefersCliRelayLocale(t *testing.T) {
	useLocale(t, "zh")
	t.Setenv("CLIRELAY_LOCALE", "en_US.UTF-8")
	t.Setenv("LANG", "zh_CN.UTF-8")

	InitLocaleFromEnv()

	if got := CurrentLocale(); got != "en" {
		t.Fatalf("CurrentLocale() = %q, want %q", got, "en")
	}
}

func TestLocaleFromEnvFallsBackToSystemLocale(t *testing.T) {
	t.Setenv("CLIRELAY_LOCALE", "")
	t.Setenv("TUI_LOCALE", "")
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_CN.UTF-8")

	if got := LocaleFromEnv(); got != "zh" {
		t.Fatalf("LocaleFromEnv() = %q, want %q", got, "zh")
	}
}
