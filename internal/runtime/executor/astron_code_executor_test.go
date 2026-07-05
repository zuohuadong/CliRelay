package executor

import "testing"

func TestAstronCodeEndpointURLSwapsV2ToV1ForResponses(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v2",
		"/responses",
		true,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v1/responses"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAstronCodeEndpointURLKeepsV2ForChat(t *testing.T) {
	got := astronCodeEndpointURL(
		"https://maas-coding-api.cn-huabei-1.xf-yun.com/v2",
		"/chat/completions",
		false,
	)
	want := "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2/chat/completions"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
