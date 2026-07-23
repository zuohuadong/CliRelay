package contextorchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPlanAssemblePreservesPinnedStructure(t *testing.T) {
	raw := []byte(`{
      "model":"gpt-5.3-codex",
      "instructions":"never change this",
      "tools":[{"type":"function","name":"lookup"}],
      "input":[
        {"type":"message","role":"developer","content":[{"type":"input_text","text":"developer rules"}]},
        {"type":"message","role":"user","content":[{"type":"input_text","text":"old text to compact"}]},
        {"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},
        {"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/old.png"}]},
        {"type":"message","role":"user","content":[{"type":"input_text","text":"latest request"}]}
      ]
    }`)

	plan, err := BuildTextPlan(raw, "openai-response", 1)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got := plan.CandidateCount(); got != 1 {
		t.Fatalf("candidate count = %d, want 1", got)
	}
	out, err := plan.Assemble(Capsule{Summary: "old text summary", Facts: []string{"timeout was observed"}})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	body := string(out)
	for _, exact := range []string{
		`"instructions":"never change this"`,
		`"tools":[{"type":"function","name":"lookup"}]`,
		`"call_id":"call_1"`,
		`https://example.com/old.png`,
		`latest request`,
		`[compacted_context]`,
	} {
		if !strings.Contains(body, exact) {
			t.Fatalf("assembled request missing %q: %s", exact, body)
		}
	}
	if strings.Contains(body, "old text to compact") {
		t.Fatalf("old raw text was not removed: %s", body)
	}
}

func TestPlanCompactsOnlyClosedOldToolPairs(t *testing.T) {
	raw := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"closed_1","name":"read_file","arguments":"{\"path\":\"old.go\"}"},` +
		`{"type":"function_call_output","call_id":"closed_1","output":"old result"},` +
		`{"type":"function_call","call_id":"open_1","name":"shell","arguments":"{}"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}` +
		`]}`)
	plan, err := BuildTextPlan(raw, "openai-response", 1)
	if err != nil {
		t.Fatalf("BuildTextPlan() error = %v", err)
	}
	if got := plan.CandidateCount(); got != 2 {
		t.Fatalf("candidate count = %d, want closed call+output", got)
	}
	source := plan.SourceItems(0)
	if len(source) != 2 || !strings.Contains(source[0].Text, "closed_1") || !strings.Contains(source[1].Text, "old result") {
		t.Fatalf("closed tool source = %#v", source)
	}
	out, err := plan.Assemble(Capsule{Summary: "read old.go", ToolResults: []string{"closed_1 returned old result"}})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	body := string(out)
	if strings.Contains(body, `"call_id":"closed_1"`) || !strings.Contains(body, `"call_id":"open_1"`) {
		t.Fatalf("closed/open tool preservation is wrong: %s", body)
	}
}

func TestPlanKeepsToolPairWhenOutputIsStillRecent(t *testing.T) {
	raw := []byte(`{"input":[` +
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"result"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}` +
		`]}`)
	if _, err := BuildTextPlan(raw, "openai-response", 2); err == nil || !strings.Contains(err.Error(), "no safely compressible") {
		t.Fatalf("BuildTextPlan() error = %v, want pair to stay pinned", err)
	}
}

func TestPlanMultimodalCompressorMayCompactOnlyOldImages(t *testing.T) {
	raw := []byte(`{"model":"vision","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/old.png"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.com/current.png"},{"type":"input_text","text":"what is this?"}]}` +
		`]}`)
	plan, err := BuildMultimodalPlan(raw, "openai-response", 1)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got := plan.CandidateCount(); got != 1 {
		t.Fatalf("candidate count = %d, want 1", got)
	}
	media := plan.MediaRefs(0)
	if len(media) != 1 || media[0].Ordinal != 0 || media[0].URL != "https://example.com/old.png" {
		t.Fatalf("media = %#v", media)
	}
	out, err := plan.Assemble(Capsule{Summary: "the old image showed an error dialog"})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	body := string(out)
	if strings.Contains(body, "old.png") || !strings.Contains(body, "current.png") {
		t.Fatalf("old/current image preservation is wrong: %s", body)
	}
}

func TestPlanClaudeImageIsPinnedForTextAndForwardedForMultimodalCompression(t *testing.T) {
	raw := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"ZmFrZQ=="}},{"type":"text","text":"old screenshot"}]},` +
		`{"role":"user","content":"latest"}` +
		`]}`)
	if _, err := BuildTextPlan(raw, "claude", 1); err == nil || !strings.Contains(err.Error(), "no safely compressible") {
		t.Fatalf("BuildTextPlan() error = %v, want old image pinned", err)
	}
	plan, err := BuildMultimodalPlan(raw, "claude", 1)
	if err != nil {
		t.Fatalf("BuildMultimodalPlan() error = %v", err)
	}
	refs := plan.MediaRefs(0)
	if len(refs) != 1 || refs[0].URL != "data:image/png;base64,ZmFrZQ==" {
		t.Fatalf("Claude media refs = %#v", refs)
	}
	out, err := plan.Assemble(Capsule{Summary: "old screenshot summary"})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if strings.Contains(string(out), "ZmFrZQ==") {
		t.Fatalf("old Claude image was not compacted: %s", out)
	}
}

func TestPlanGeminiToolMessagesRemainPinned(t *testing.T) {
	raw := []byte(`{"contents":[` +
		`{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"old"}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"done"}}}]},` +
		`{"role":"user","parts":[{"text":"latest"}]}` +
		`]}`)
	if _, err := BuildTextPlan(raw, "gemini", 1); err == nil || !strings.Contains(err.Error(), "no safely compressible") {
		t.Fatalf("BuildTextPlan() error = %v, want Gemini tool pair pinned", err)
	}
}

func TestPlanPrefixDigestSupportsGrowingRelayRequests(t *testing.T) {
	first := []byte(`{"messages":[{"role":"user","content":"old one"},{"role":"user","content":"latest one"}]}`)
	second := []byte(`{"messages":[{"role":"user","content":"old one"},{"role":"user","content":"latest one"},{"role":"user","content":"latest two"}]}`)
	firstPlan, err := BuildTextPlan(first, "openai", 1)
	if err != nil {
		t.Fatalf("first BuildPlan() error = %v", err)
	}
	secondPlan, err := BuildTextPlan(second, "openai", 1)
	if err != nil {
		t.Fatalf("second BuildPlan() error = %v", err)
	}
	firstDigests := firstPlan.PrefixDigests()
	secondDigests := secondPlan.PrefixDigests()
	if len(firstDigests) != 1 || len(secondDigests) != 2 {
		t.Fatalf("digest counts = %d/%d, want 1/2", len(firstDigests), len(secondDigests))
	}
	if firstDigests[0] != secondDigests[0] {
		t.Fatalf("shared history prefix digest changed: %s != %s", firstDigests[0], secondDigests[0])
	}
	if got := secondPlan.SourceItems(1); len(got) != 1 || got[0].Text != "latest one" {
		t.Fatalf("incremental source items = %#v", got)
	}
}

func TestParseCapsuleRejectsWholeRequestJSON(t *testing.T) {
	if _, err := ParseCapsule(`{"model":"gpt-5","input":"rewritten request"}`); err == nil {
		t.Fatal("expected whole request JSON to be rejected")
	}
	capsule, err := ParseCapsule("```json\n{\"memory_capsule\":{\"summary\":\"kept\",\"goals\":[\"ship\",\"ship\"]}}\n```")
	if err != nil {
		t.Fatalf("ParseCapsule() error = %v", err)
	}
	if capsule.Version != CapsuleVersion || len(capsule.Goals) != 1 {
		t.Fatalf("capsule = %#v", capsule)
	}
	if _, err := ParseCapsule(`{"version":"2","summary":"future"}`); err == nil {
		t.Fatal("expected unsupported capsule version to be rejected")
	}
	if _, err := ParseCapsule(`{"summary":"kept","model":"rewritten"}`); err == nil {
		t.Fatal("expected unknown capsule fields to be rejected")
	}
}

func TestAssembleUsesProtocolCompatibleSummaryItems(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		format string
		path   string
		role   string
	}{
		{name: "openai chat", raw: `{"messages":[{"role":"user","content":"old"},{"role":"user","content":"latest"}]}`, format: "openai", path: "messages.0.role", role: "user"},
		{name: "openai responses", raw: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"latest"}]}]}`, format: "openai-response", path: "input.0.role", role: "user"},
		{name: "claude", raw: `{"messages":[{"role":"user","content":"old"},{"role":"user","content":"latest"}]}`, format: "claude", path: "messages.0.role", role: "user"},
		{name: "gemini", raw: `{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"user","parts":[{"text":"latest"}]}]}`, format: "gemini", path: "contents.0.role", role: "user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := BuildTextPlan([]byte(tt.raw), tt.format, 1)
			if err != nil {
				t.Fatalf("BuildPlan() error = %v", err)
			}
			out, err := plan.Assemble(Capsule{Summary: "summary"})
			if err != nil {
				t.Fatalf("Assemble() error = %v", err)
			}
			if !json.Valid(out) {
				t.Fatalf("invalid JSON: %s", out)
			}
			if got := gjson.GetBytes(out, tt.path).String(); got != tt.role {
				t.Fatalf("summary role = %q, want %q; body=%s", got, tt.role, out)
			}
		})
	}
}
