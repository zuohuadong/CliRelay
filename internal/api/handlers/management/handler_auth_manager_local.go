package management

import coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"

func (h *Handler) authManagerSnapshot() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authManager
}
