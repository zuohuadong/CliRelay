package executor

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func codexWebsocketRouteKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	endpointID := ""
	if auth.Attributes != nil {
		endpointID = strings.TrimSpace(auth.Attributes["egress_id"])
	}
	proxyURL := strings.TrimSpace(auth.ProxyURL)
	if parsed, err := url.Parse(proxyURL); err == nil && parsed != nil {
		parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
		parsed.Host = strings.ToLower(strings.TrimSpace(parsed.Host))
		proxyURL = parsed.String()
	}
	proxySum := sha256.Sum256([]byte(proxyURL))
	identity := agentIdentityMetadataString(auth, "agent_runtime_id") + ":" + agentIdentityMetadataString(auth, "task_id")
	identitySum := sha256.Sum256([]byte(identity))
	return endpointID + ":" + fmt.Sprintf("%x:%x", proxySum[:], identitySum[:])
}
