package executor

import (
	"context"
	"net/http"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg           *config.Config
	egress        egress.Resolver
	strictEgress  bool
	homeRefresh   func(context.Context, *config.Config, *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error)
	refreshTokens func(context.Context, *http.Client, string) (*codexauth.CodexTokenData, error)
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }
