package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
	"golang.org/x/net/proxy"
)

const (
	codexResponsesWebsocketBetaHeaderValue = "responses_websockets=2026-02-06"
	codexResponsesWebsocketIdleTimeout     = 5 * time.Minute
	codexResponsesWebsocketHandshakeTO     = 30 * time.Second
)

func (e *CodexWebsocketsExecutor) dialCodexWebsocket(ctx context.Context, auth *cliproxyauth.Auth, wsURL string, headers http.Header) (*websocket.Conn, *websocketConnectionCloser, *http.Response, error) {
	dialHeaders, err := applyAgentIdentityWebsocketHeaders(headers, auth, wsURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("codex websocket: apply Agent Identity auth: %w", err)
	}
	var dialer *websocket.Dialer
	if e != nil && e.CodexExecutor != nil && e.CodexExecutor.usesStrictEgress(auth) {
		proxyURL := ""
		if auth != nil {
			proxyURL = auth.ProxyURL
		}
		dialer, err = newStrictProxyWebsocketDialer(proxyURL)
		if err != nil {
			return nil, nil, nil, egress.RuntimeError(err)
		}
	} else {
		dialer = newProxyAwareWebsocketDialer(e.cfg, auth)
	}
	dialer.HandshakeTimeout = codexResponsesWebsocketHandshakeTO
	dialer.EnableCompression = true
	if ctx == nil {
		ctx = context.Background()
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, dialHeaders)
	if resp != nil && resp.Request == nil {
		resp.Request = &http.Request{Header: dialHeaders.Clone()}
	}
	closer := newWebsocketConnectionCloser(conn)
	if e != nil && e.CodexExecutor != nil && e.CodexExecutor.usesStrictEgress(auth) && err != nil && resp == nil {
		err = e.CodexExecutor.wrapStrictEgressTransportErrorForAuth(auth, err, "websocket proxy dial")
	}
	if conn != nil {
		// Avoid gorilla/websocket flate tail validation issues on some upstreams/Go versions.
		// Negotiating permessage-deflate is fine; we just don't compress outbound messages.
		conn.EnableWriteCompression(false)
	}
	return conn, closer, resp, err
}

func newStrictProxyWebsocketDialer(proxyURL string) (*websocket.Dialer, error) {
	dialer := &websocket.Dialer{
		Proxy:             nil,
		HandshakeTimeout:  codexResponsesWebsocketHandshakeTO,
		EnableCompression: true,
		NetDialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	setting, err := proxyutil.Parse(strings.TrimSpace(proxyURL))
	if err != nil || setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		if err == nil {
			err = egress.ErrEndpointInvalid
		}
		return nil, fmt.Errorf("%w: strict websocket proxy is invalid: %v", egress.ErrEndpointInvalid, err)
	}
	switch setting.URL.Scheme {
	case "socks5", "socks5h":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			return nil, fmt.Errorf("%w: create SOCKS5 dialer: %v", egress.ErrEndpointInvalid, errSOCKS5)
		}
		dialer.NetDialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
	case "http":
		dialer.Proxy = http.ProxyURL(setting.URL)
	default:
		return nil, fmt.Errorf("%w: unsupported websocket proxy scheme %s", egress.ErrEndpointInvalid, setting.URL.Scheme)
	}
	return dialer, nil
}

func writeCodexWebsocketMessage(sess *codexWebsocketSession, conn *websocket.Conn, payload []byte) error {
	if sess != nil {
		return sess.writeMessage(conn, websocket.TextMessage, payload)
	}
	if conn == nil {
		return fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (e *CodexWebsocketsExecutor) writeCodexWebsocketMessage(sess *codexWebsocketSession, conn *websocket.Conn, payload []byte) error {
	err := writeCodexWebsocketMessage(sess, conn, payload)
	if e == nil || e.CodexExecutor == nil {
		return err
	}
	return e.CodexExecutor.wrapStrictEgressTransportError(err, "websocket send")
}

func (e *CodexWebsocketsExecutor) writeCodexWebsocketMessageForAuth(auth *cliproxyauth.Auth, sess *codexWebsocketSession, conn *websocket.Conn, payload []byte) error {
	err := writeCodexWebsocketMessage(sess, conn, payload)
	if e == nil || e.CodexExecutor == nil {
		return err
	}
	return e.CodexExecutor.wrapStrictEgressTransportErrorForAuth(auth, err, "websocket send")
}

func mapCodexWebsocketWriteError(sess *codexWebsocketSession, conn *websocket.Conn, err error) error {
	if err == nil || sess == nil || conn == nil {
		return err
	}
	upstreamErr := sess.upstreamDisconnectError(conn)
	var closeErr *websocket.CloseError
	if !errors.As(upstreamErr, &closeErr) || closeErr.Code != websocket.CloseMessageTooBig {
		return err
	}
	return mapCodexWebsocketReadError(upstreamErr)
}

func shouldRetryCodexWebsocketSend(err error) bool {
	if err == nil {
		return false
	}
	var requestErr cliproxyexecutor.RequestScopedError
	return !errors.As(err, &requestErr) || !requestErr.IsRequestScoped()
}

type codexWebsocketMessageTooBigError struct {
	statusErr
}

func (codexWebsocketMessageTooBigError) IsRequestScoped() bool {
	return true
}

func mapCodexWebsocketReadError(err error) error {
	if err == nil {
		return nil
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		return codexWebsocketMessageTooBigError{statusErr: statusErr{
			code: http.StatusRequestEntityTooLarge,
			msg:  `{"error":{"message":"upstream websocket message too big","type":"invalid_request_error","code":"message_too_big"}}`,
		}}
	}
	if isCodexWebsocketDirtyDisconnect(err) {
		return statusErr{code: http.StatusRequestTimeout, msg: `{"error":{"message":"stream closed before response.completed","type":"server_error","code":"upstream_disconnected"}}`}
	}
	return err
}

func (e *CodexWebsocketsExecutor) mapWebsocketReadError(err error) error {
	mapped := mapCodexWebsocketReadError(err)
	if e == nil || e.CodexExecutor == nil || !e.CodexExecutor.strictEgress || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return mapped
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		return mapped
	}
	transportErr := err
	if errors.Is(err, io.EOF) {
		transportErr = io.ErrUnexpectedEOF
	}
	return e.CodexExecutor.wrapStrictEgressTransportError(transportErr, "websocket read")
}

func (e *CodexWebsocketsExecutor) mapWebsocketReadErrorForAuth(auth *cliproxyauth.Auth, err error) error {
	mapped := mapCodexWebsocketReadError(err)
	if e == nil || e.CodexExecutor == nil || !e.CodexExecutor.usesStrictEgress(auth) || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return mapped
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseMessageTooBig {
		return mapped
	}
	transportErr := err
	if errors.Is(err, io.EOF) {
		transportErr = io.ErrUnexpectedEOF
	}
	return e.CodexExecutor.wrapStrictEgressTransportErrorForAuth(auth, transportErr, "websocket read")
}

func isCodexWebsocketDirtyDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseMessageTooBig:
			return false
		}
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection reset") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection aborted") ||
		strings.Contains(text, "websocket: close") ||
		strings.Contains(text, "forcibly closed")
}

func normalizeCodexWebsocketParallelToolCalls(body []byte, headers http.Header) []byte {
	if !isCodexResponsesLiteRequest(body, headers) {
		return body
	}
	body = helps.SetBoolIfDifferent(body, "parallel_tool_calls", false)
	return body
}

func buildCodexWebsocketRequestBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}

	// Match codex-rs websocket v2 semantics: every request is `response.create`.
	// Incremental follow-up turns continue on the same websocket using
	// `previous_response_id` + incremental `input`, not `response.append`.
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body = helps.SanitizeCodexInputItemIDs(body)
	wsReqBody, errSet := sjson.SetBytes(body, "type", "response.create")
	if errSet == nil && len(wsReqBody) > 0 {
		return wsReqBody
	}
	return body
}

func readCodexWebsocketMessage(ctx context.Context, sess *codexWebsocketSession, conn *websocket.Conn, readCh chan codexWebsocketRead) (int, []byte, error) {
	if sess == nil {
		if conn == nil {
			return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
		}
		_ = conn.SetReadDeadline(time.Now().Add(codexResponsesWebsocketIdleTimeout))
		msgType, payload, errRead := conn.ReadMessage()
		return msgType, payload, errRead
	}
	if conn == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: websocket conn is nil")
	}
	if readCh == nil {
		return 0, nil, fmt.Errorf("codex websockets executor: session read channel is nil")
	}
	for {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case ev, ok := <-readCh:
			if !ok {
				return 0, nil, fmt.Errorf("codex websockets executor: session read channel closed")
			}
			if ev.conn != conn {
				continue
			}
			if ev.err != nil {
				return 0, nil, ev.err
			}
			return ev.msgType, ev.payload, nil
		}
	}
}

func newProxyAwareWebsocketDialer(cfg *config.Config, auth *cliproxyauth.Auth) *websocket.Dialer {
	dialer := &websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  codexResponsesWebsocketHandshakeTO,
		EnableCompression: true,
		NetDialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL == "" {
		return dialer
	}

	setting, errParse := proxyutil.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("codex websockets executor: %v", errParse)
		return dialer
	}

	switch setting.Mode {
	case proxyutil.ModeDirect:
		dialer.Proxy = nil
		return dialer
	case proxyutil.ModeProxy:
	default:
		return dialer
	}

	switch setting.URL.Scheme {
	case "socks5", "socks5h":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("codex websockets executor: create SOCKS5 dialer failed: %v", errSOCKS5)
			return dialer
		}
		dialer.Proxy = nil
		dialer.NetDialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
	case "http", "https":
		dialer.Proxy = http.ProxyURL(setting.URL)
	default:
		log.Errorf("codex websockets executor: unsupported proxy scheme: %s", setting.URL.Scheme)
	}

	return dialer
}

func buildCodexResponsesWebsocketURL(httpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpURL))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("codex websockets executor: unsupported responses websocket URL scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("codex websockets executor: responses websocket URL host is empty")
	}
	return parsed.String(), nil
}
