package config

// DefaultCodexCapacitySameAccountRetries is how many times the same account is
// asked again after a transient "model is at capacity" rejection before the
// turn is failed. Capacity is model-scoped and short-lived, so asking the same
// account again after a pause usually works; a deployment with several
// credentials still prefers another account first (the handlers do that).
const DefaultCodexCapacitySameAccountRetries = 2

// CodexExecutorOptions tunes how the Codex executor handles a stream.
//
// It lives on SDKConfig so both the executors (which read the full config) and
// the stream forwarder (which only has the SDK view) can see the same values.
type CodexExecutorOptions struct {
	// StreamBootstrapBuffering holds the handshake events of a Codex stream back
	// until the upstream either produces real output or ends the turn. A
	// capacity rejection is then still recoverable: nothing has reached the
	// client, so the request can be retried. Nil means off, which forwards every
	// event immediately; set true to make capacity rejections retryable.
	StreamBootstrapBuffering *bool `yaml:"stream-bootstrap-buffering,omitempty" json:"stream-bootstrap-buffering,omitempty"`

	// CapacitySameAccountRetries is how many more times the same account may be
	// asked after a transient capacity rejection. Nil means
	// DefaultCodexCapacitySameAccountRetries; 0 disables the retries.
	CapacitySameAccountRetries *int `yaml:"capacity-same-account-retries,omitempty" json:"capacity-same-account-retries,omitempty"`
}

// CodexStreamBootstrapBuffering reports whether handshake events are buffered
// until the stream shows real output.
func (c *SDKConfig) CodexStreamBootstrapBuffering() bool {
	if c == nil || c.Codex.StreamBootstrapBuffering == nil {
		return false
	}
	return *c.Codex.StreamBootstrapBuffering
}

// CodexCapacitySameAccountRetries returns how many more attempts one account
// gets after a transient capacity rejection.
func (c *SDKConfig) CodexCapacitySameAccountRetries() int {
	if c == nil || c.Codex.CapacitySameAccountRetries == nil {
		return DefaultCodexCapacitySameAccountRetries
	}
	if retries := *c.Codex.CapacitySameAccountRetries; retries > 0 {
		return retries
	}
	return 0
}
