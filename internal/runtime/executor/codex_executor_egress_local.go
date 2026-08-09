package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

const (
	codexEgressModeSharedProxy        = "shared_proxy"
	codexDefaultResponseHeaderTimeout = time.Duration(config.DefaultCodexResponseHeaderTimeoutSeconds) * time.Second
)

func NewCodexExecutorWithEgress(cfg *config.Config, resolver egress.Resolver) *CodexExecutor {
	return &CodexExecutor{cfg: cfg, egress: resolver, strictEgress: true}
}

func (e *CodexExecutor) refreshViaHome(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, bool, error) {
	if e != nil && e.homeRefresh != nil {
		return e.homeRefresh(ctx, e.cfg, auth)
	}
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	return helps.RefreshAuthViaHome(ctx, cfg, auth)
}

func (e *CodexExecutor) refreshCodexTokens(ctx context.Context, client *http.Client, refreshToken string) (*codexauth.CodexTokenData, error) {
	if e != nil && e.refreshTokens != nil {
		return e.refreshTokens(ctx, client, refreshToken)
	}
	return codexauth.NewCodexAuthWithHTTPClient(client).RefreshTokensWithRetry(ctx, refreshToken, 3)
}

func codexUsesSharedProxyEgress(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	egressMode, _ := auth.Metadata["egress_mode"].(string)
	return egressMode == codexEgressModeSharedProxy
}

func (e *CodexExecutor) usesStrictEgress(auth *cliproxyauth.Auth) bool {
	return e != nil && e.strictEgress && !codexUsesSharedProxyEgress(auth)
}

func (e *CodexExecutor) validateSharedProxyEgress(auth *cliproxyauth.Auth) error {
	if !codexUsesSharedProxyEgress(auth) {
		return nil
	}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && e != nil && e.cfg != nil {
		proxyURL = strings.TrimSpace(e.cfg.ProxyURL)
	}
	if proxyURL == "" {
		return egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy is required", egress.ErrEgressRequired))
	}
	setting, err := proxyutil.Parse(proxyURL)
	if err != nil || setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		return egress.RuntimeError(fmt.Errorf("%w: shared Codex proxy must be a valid non-direct proxy", egress.ErrEndpointInvalid))
	}
	return nil
}

func (e *CodexExecutor) outboundHTTPClient(ctx context.Context, auth *cliproxyauth.Auth, timeout, responseHeaderTimeout time.Duration, useUTLS bool) (*http.Client, error) {
	if err := e.validateSharedProxyEgress(auth); err != nil {
		return nil, err
	}
	if e.usesStrictEgress(auth) {
		proxyURL := ""
		if auth != nil {
			proxyURL = strings.TrimSpace(auth.ProxyURL)
		}
		var (
			client *http.Client
			err    error
		)
		if useUTLS {
			client, err = helps.NewStrictUtlsHTTPClient(proxyURL, timeout)
		} else {
			client, err = helps.NewStrictProxyHTTPClient(proxyURL, timeout, responseHeaderTimeout)
		}
		if err != nil {
			return nil, egress.RuntimeError(fmt.Errorf("%w: %v", egress.ErrEndpointInvalid, err))
		}
		if client == nil || client.Transport == nil {
			return nil, egress.RuntimeError(fmt.Errorf("%w: strict proxy transport is unavailable", egress.ErrEndpointInvalid))
		}
		client.Transport = strictEgressRoundTripper{base: client.Transport}
		if useUTLS {
			client = helps.WithResponseHeaderTimeout(client, responseHeaderTimeout)
		}
		return client, nil
	}
	if useUTLS {
		return helps.WithResponseHeaderTimeout(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, timeout), responseHeaderTimeout), nil
	}
	return helps.NewProxyAwareHTTPClientWithResponseHeaderTimeout(ctx, e.cfg, auth, timeout, responseHeaderTimeout), nil
}

func codexResponseHeaderTimeout(cfg *config.Config) time.Duration {
	if cfg != nil {
		seconds := cfg.Codex.ResponseHeaderTimeoutSeconds
		if seconds < 0 {
			return 0
		}
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return codexDefaultResponseHeaderTimeout
}

func codexResponseHeaderTimeoutError(err error) error {
	if !helps.IsResponseHeaderTimeout(err) {
		return err
	}
	return statusErr{code: http.StatusGatewayTimeout, msg: `{"error":{"message":"upstream response header timeout","type":"server_error","code":"upstream_response_header_timeout"}}`}
}

type strictEgressRoundTripper struct {
	base http.RoundTripper
}

func (t strictEgressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, wrapStrictEgressTransportError(err, "proxy request")
	}
	if resp != nil && resp.Body != nil {
		resp.Body = strictEgressReadCloser{ReadCloser: resp.Body}
	}
	return resp, nil
}

type strictEgressReadCloser struct {
	io.ReadCloser
}

func (r strictEgressReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	return n, wrapStrictEgressTransportError(err, "proxy response read")
}

func wrapStrictEgressTransportError(err error, operation string) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var runtimeErr *egress.Error
	if errors.As(err, &runtimeErr) {
		return err
	}
	var statusError interface{ StatusCode() int }
	if errors.As(err, &statusError) && statusError.StatusCode() > 0 {
		return err
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "transport"
	}
	return egress.RuntimeError(fmt.Errorf("%w: strict egress %s failed: %v", egress.ErrEndpointDisabled, operation, err))
}

func (e *CodexExecutor) wrapStrictEgressTransportError(err error, operation string) error {
	if e == nil || !e.strictEgress {
		return err
	}
	return wrapStrictEgressTransportError(err, operation)
}

func (e *CodexExecutor) wrapStrictEgressTransportErrorForAuth(auth *cliproxyauth.Auth, err error, operation string) error {
	if !e.usesStrictEgress(auth) {
		return err
	}
	return wrapStrictEgressTransportError(err, operation)
}

func (e *CodexExecutor) resolveEgressAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if codexUsesSharedProxyEgress(auth) {
		if err := e.validateSharedProxyEgress(auth); err != nil {
			return nil, err
		}
		return auth, nil
	}
	if !e.usesStrictEgress(auth) {
		return auth, nil
	}
	if e.egress == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: Codex egress resolver is unavailable", egress.ErrEgressRequired))
	}
	if auth == nil {
		return nil, egress.RuntimeError(fmt.Errorf("%w: Codex auth is missing", egress.ErrEgressRequired))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := codexAccountIDFromAuth(auth)
	if accountID == "" {
		return nil, egress.RuntimeError(fmt.Errorf("%w: codex account_id is required", egress.ErrIdentityRequired))
	}
	resolved, err := e.egress.Resolve(ctx, accountID)
	if err != nil {
		return nil, egress.RuntimeError(err)
	}
	if strings.TrimSpace(resolved.ProxyURL) == "" {
		return nil, egress.RuntimeError(fmt.Errorf("%w: resolved endpoint has no proxy URL", egress.ErrEndpointInvalid))
	}
	cloned := auth.Clone()
	cloned.ProxyURL = strings.TrimSpace(resolved.ProxyURL)
	if cloned.Attributes == nil {
		cloned.Attributes = make(map[string]string)
	}
	cloned.Attributes["egress_id"] = resolved.Endpoint.ID
	return cloned, nil
}

// codexAccountIDFromAuth extracts the Codex account_id from auth metadata.
// It falls back to parsing the id_token JWT when account_id is missing and
// backfills the metadata map in place so downstream readers see it.
func codexAccountIDFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	return codexauth.AccountIDFromMetadata(auth.Metadata)
}
