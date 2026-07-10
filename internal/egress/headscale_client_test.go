package egress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestHeadscaleClientListsNodesWithBearerToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/node" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[{"id":"17","name":"sg.internal","givenName":"sg-01","ipAddresses":["100.64.0.17"],"online":true,"lastSeen":"2026-07-10T08:00:00Z","tags":["tag:clirelay-egress"]}]}`))
	}))
	defer server.Close()

	client := NewHeadscaleClient(config.HeadscaleConfig{URL: server.URL, APIKeyEnv: "HEADSCALE_API_KEY"})
	client.lookupEnv = func(name string) string {
		if name == "HEADSCALE_API_KEY" {
			return "test-key"
		}
		return ""
	}
	nodes, err := client.ListNodes(context.Background())
	if err != nil {
		t.Fatalf("ListNodes() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "17" || nodes[0].Name != "sg-01" || len(nodes[0].Addresses) != 1 {
		t.Fatalf("ListNodes() = %#v", nodes)
	}
}

func TestHeadscaleClientCreatesOneTimeTaggedEnrollment(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/preauthkey" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["reusable"] != false || body["ephemeral"] != false {
			t.Fatalf("request body = %#v", body)
		}
		tags, _ := body["aclTags"].([]any)
		if len(tags) != 1 || tags[0] != "tag:clirelay-egress" {
			t.Fatalf("aclTags = %#v", body["aclTags"])
		}
		if body["expiration"] != expiresAt.Format(time.RFC3339) {
			t.Fatalf("expiration = %#v", body["expiration"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"preAuthKey":{"key":"tskey-auth-once","expiration":"2026-07-10T09:00:00Z"}}`))
	}))
	defer server.Close()

	client := NewHeadscaleClient(config.HeadscaleConfig{URL: server.URL, APIKeyEnv: "KEY", ServiceTag: "tag:clirelay-egress"})
	client.lookupEnv = func(string) string { return "test-key" }
	enrollment, err := client.CreateEnrollment(context.Background(), "SG-01", expiresAt)
	if err != nil {
		t.Fatalf("CreateEnrollment() error = %v", err)
	}
	if enrollment.Key != "tskey-auth-once" || !enrollment.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("CreateEnrollment() = %#v", enrollment)
	}
	if !strings.Contains(enrollment.Command, "--auth-key=tskey-auth-once") || !strings.Contains(enrollment.Command, "--advertise-tags=tag:clirelay-egress") {
		t.Fatalf("command = %q", enrollment.Command)
	}
	if !strings.Contains(enrollment.Command, "--hostname=sg-01") {
		t.Fatalf("command = %q", enrollment.Command)
	}
}

func TestHeadscaleClientRejectsUnsafeEnrollmentHostname(t *testing.T) {
	t.Parallel()

	client := NewHeadscaleClient(config.HeadscaleConfig{})
	_, err := client.CreateEnrollment(context.Background(), `bad name'; reboot`, time.Now().Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "invalid enrollment hostname") {
		t.Fatalf("CreateEnrollment() error = %v", err)
	}
}
