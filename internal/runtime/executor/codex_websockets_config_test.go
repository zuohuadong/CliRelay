package executor

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestCodexWebsocketTimeoutConfig(t *testing.T) {
	t.Parallel()

	if got := codexResponsesWebsocketHandshakeTimeout(nil); got != 30*time.Second {
		t.Fatalf("default handshake timeout = %v, want 30s", got)
	}
	if got := codexResponsesWebsocketFirstMsgTimeout(nil); got != 30*time.Second {
		t.Fatalf("default first message timeout = %v, want 30s", got)
	}
	if got := codexResponsesWebsocketIdleTimeout(nil); got != 20*time.Minute {
		t.Fatalf("default idle timeout = %v, want 20m", got)
	}

	cfg := &config.Config{}
	cfg.CodexWebsocket.HandshakeTimeoutSeconds = 11
	cfg.CodexWebsocket.FirstMessageTimeoutSeconds = 12
	cfg.CodexWebsocket.IdleTimeoutSeconds = 13

	if got := codexResponsesWebsocketHandshakeTimeout(cfg); got != 11*time.Second {
		t.Fatalf("configured handshake timeout = %v, want 11s", got)
	}
	if got := codexResponsesWebsocketFirstMsgTimeout(cfg); got != 12*time.Second {
		t.Fatalf("configured first message timeout = %v, want 12s", got)
	}
	if got := codexResponsesWebsocketIdleTimeout(cfg); got != 13*time.Second {
		t.Fatalf("configured idle timeout = %v, want 13s", got)
	}
}
