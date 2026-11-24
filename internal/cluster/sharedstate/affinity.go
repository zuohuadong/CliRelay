package sharedstate

// Session affinity bindings map a session key to the upstream account serving
// that conversation. Sharing them is what keeps a conversation on one account,
// and so on that account's prompt cache, when its turns land on different
// nodes.

// Binding is a shared session affinity binding.
type Binding struct {
	// Account is the reference the binding was created with.
	Account string
	// Served counts the requests the binding served before this one.
	Served int
	// Found is false when no binding existed.
	Found bool
	// Created is true when Bind created the binding rather than finding one.
	Created bool
}
