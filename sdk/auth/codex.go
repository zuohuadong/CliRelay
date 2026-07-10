package auth

import (
	"context"
	"errors"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var ErrCodexLoginRequiresManagementEgress = errors.New("egress_required: Codex OAuth login must be started from the management panel with an egress endpoint")

// CodexAuthenticator retains refresh scheduling metadata, while interactive
// login is intentionally restricted to the management flow where an endpoint
// can be selected before any OAuth network request is made.
type CodexAuthenticator struct {
	CallbackPort int
}

func NewCodexAuthenticator() *CodexAuthenticator {
	return &CodexAuthenticator{CallbackPort: 1455}
}

func (a *CodexAuthenticator) Provider() string {
	return "codex"
}

func (a *CodexAuthenticator) RefreshLead() *time.Duration {
	return new(5 * 24 * time.Hour)
}

func (a *CodexAuthenticator) Login(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return nil, ErrCodexLoginRequiresManagementEgress
}
