package gemini

// The historical reference translator used these process-level identifiers to
// model Claude Code's metadata defaults. Keep them in the test package so the
// byte-for-byte reference remains self-contained while the production
// translator derives IDs per request.
var (
	account string
	session string
	user    string
)
