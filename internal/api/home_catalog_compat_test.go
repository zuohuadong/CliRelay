package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

func newHomeCatalogClient(t *testing.T, payload string) *home.Client {
	t.Helper()
	client, _, _ := newReadyHomeCatalogClient(t, payload)
	return client
}
