package auth

import "testing"

type metadataStorageStub struct {
	meta map[string]any
}

func (s *metadataStorageStub) SaveTokenToFile(string) error { return nil }

func (s *metadataStorageStub) SetMetadata(meta map[string]any) {
	s.meta = meta
}

func TestApplyMetadata(t *testing.T) {
	storage := &metadataStorageStub{}
	input := map[string]any{"label": "Team Alpha"}

	ApplyMetadata(storage, input)

	if storage.meta["label"] != "Team Alpha" {
		t.Fatalf("metadata label = %v, want %q", storage.meta["label"], "Team Alpha")
	}
}
