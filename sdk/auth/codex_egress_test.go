package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCodexCLIAndDeviceLoginRequireManagementEgressWithoutNetwork(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer proxy.Close()
	cfg := &config.Config{}
	cfg.ProxyURL = proxy.URL
	authenticator := NewCodexAuthenticator()

	for _, opts := range []*LoginOptions{
		{},
		{Metadata: map[string]string{codexLoginModeMetadataKey: codexLoginModeDevice}},
	} {
		if _, err := authenticator.Login(context.Background(), cfg, opts); !errors.Is(err, ErrCodexLoginRequiresManagementEgress) {
			t.Fatalf("Login() error = %v", err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("network hits = %d, want 0", hits.Load())
	}
}
