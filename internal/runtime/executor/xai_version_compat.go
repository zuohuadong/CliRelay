package executor

import "context"

// StartXAIVersionUpdater is retained for service compatibility with upstream,
// which refreshes the Grok CLI version from the npm registry at runtime.
//
// This fork pins xaiClientVersionValue to a known-good Grok CLI build because
// its xAI request shape (headers, workspace routing) is validated against that
// exact version. Silently following npm releases would change the upstream
// contract without a corresponding fork-side review, so the updater is a no-op
// here; update xaiClientVersionValue deliberately instead.
func StartXAIVersionUpdater(ctx context.Context, proxyURL string) {
	_ = ctx
	_ = proxyURL
}
