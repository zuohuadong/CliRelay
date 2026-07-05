package videotool

import "testing"

func TestCommonFlagSetAppliesParsedValues(t *testing.T) {
	t.Setenv("CLIRELAY_BASE_URL", "")
	t.Setenv("CLIRELAY_API_KEY", "")
	t.Setenv("CLIRELAY_VIDEO_MODEL", "")

	fs, opts := commonFlagSet("test")
	if err := fs.Parse([]string{"-base-url", "https://example.com", "-api-key", "test-key", "-model", "video-model"}); err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if opts.BaseURL != "https://example.com" || opts.APIKey != "test-key" || opts.Model != "video-model" {
		t.Fatalf("opts = %+v", opts)
	}
}
