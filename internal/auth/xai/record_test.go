package xai

import "testing"

func TestOAuthRecordFromTokenStoragePersistsEndpointMode(t *testing.T) {
	storage := &TokenStorage{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Email:        "member@example.test",
		BaseURL:      DefaultAPIBaseURL,
		AuthKind:     "oauth",
	}

	record := OAuthRecordFromTokenStorage(storage, true)
	if record == nil {
		t.Fatal("OAuthRecordFromTokenStorage() = nil")
	}
	if got := record.Attributes["using_api"]; got != "true" {
		t.Fatalf("attributes[using_api] = %q, want true", got)
	}
	if got, ok := record.Metadata["using_api"].(bool); !ok || !got {
		t.Fatalf("metadata[using_api] = %#v, want true", record.Metadata["using_api"])
	}
	if got := record.Attributes["auth_kind"]; got != "oauth" {
		t.Fatalf("attributes[auth_kind] = %q, want oauth", got)
	}
}
