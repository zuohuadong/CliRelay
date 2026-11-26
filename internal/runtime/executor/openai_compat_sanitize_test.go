package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripCodexClientMetadata(t *testing.T) {
	in := []byte(`{"model":"glm-5.4","client_metadata":{"session_id":"s1"},"messages":[{"role":"user","content":"hi"}]}`)
	got := stripCodexClientMetadata(in)
	if gjson.GetBytes(got, "client_metadata").Exists() {
		t.Fatalf("client_metadata survived: %s", got)
	}
	if gjson.GetBytes(got, "messages.0.content").String() != "hi" {
		t.Fatalf("unrelated fields changed: %s", got)
	}
}

func TestStripCodexClientMetadataIsNoOpWithoutField(t *testing.T) {
	in := []byte(`{"model":"glm-5.4","messages":[]}`)
	if got := stripCodexClientMetadata(in); string(got) != string(in) {
		t.Fatalf("payload changed without client_metadata: %s", got)
	}
	if got := stripCodexClientMetadata(nil); len(got) != 0 {
		t.Fatalf("nil payload changed: %s", got)
	}
	if got := stripCodexClientMetadata([]byte(`{"client_metadata":`)); string(got) != `{"client_metadata":` {
		t.Fatalf("invalid payload changed: %s", got)
	}
}
