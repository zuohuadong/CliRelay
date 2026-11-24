// Package modelcapacity remembers the models an upstream recently refused for
// being at capacity.
//
// A capacity rejection is model-scoped and short-lived: the model is full for
// everybody, whichever account asks, and the same model usually answers again a
// minute later. A virtual model would otherwise pick its first member on every
// request, so through that minute each turn pays a fresh failing attempt (tens
// of seconds with Codex) before it falls back to another member. Remembering the
// refusal lets the next request start with the member that is not full.
//
// The memory is advisory: a cooling model is still tried when nothing else is
// left, so a mis-recorded rejection costs an attempt, never a turn.
package modelcapacity

import (
	"strings"
	"sync"
	"time"
)

// Window is how long one refusal is remembered. It matches the scheduler's
// transient rate-limit cooldown: after it, the account's own model state no
// longer holds the model back either, so the memory must not outlive it.
const Window = time.Minute

var (
	mu      sync.Mutex
	refused = map[string]time.Time{}
)

// Mark records that model was refused for capacity at now.
func Mark(model string, now time.Time) {
	key := normalize(model)
	if key == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	pruneLocked(now)
	refused[key] = now.Add(Window)
}

// Cooling reports whether model was refused for capacity less than Window
// before now.
func Cooling(model string, now time.Time) bool {
	key := normalize(model)
	if key == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	until, ok := refused[key]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(refused, key)
		return false
	}
	return true
}

// Forget drops what is remembered about model, for a model that served a turn:
// the refusal is over whatever the window says.
func Forget(model string) {
	key := normalize(model)
	if key == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	delete(refused, key)
}

// Reset forgets everything. Tests use it; nothing else needs to.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	refused = map[string]time.Time{}
}

// pruneLocked drops the entries that have expired so a long-lived process does
// not accumulate one entry per model name it has ever seen.
func pruneLocked(now time.Time) {
	for key, until := range refused {
		if !now.Before(until) {
			delete(refused, key)
		}
	}
}

// normalize makes one model's name one key: names are matched the way model ids
// are elsewhere in the gateway, exactly, but the thinking suffix is not part of
// the model the upstream refused.
func normalize(model string) string {
	name := strings.TrimSpace(model)
	if open := strings.IndexByte(name, '('); open > 0 {
		name = name[:open]
	}
	return strings.ToLower(name)
}
