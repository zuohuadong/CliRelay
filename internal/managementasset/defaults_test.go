package managementasset

import (
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// repositoryPath reduces either a repository URL ("https://github.com/o/r") or a
// releases API URL ("https://api.github.com/repos/o/r/releases/latest") to "o/r".
func repositoryPath(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	path := strings.Trim(parsed.Path, "/")
	path = strings.TrimPrefix(path, "repos/")
	path = strings.TrimSuffix(path, "/releases/latest")
	if path == "" {
		t.Fatalf("%q carries no repository path", raw)
	}
	return path
}

// The panel is installed from a release asset of DefaultPanelGitHubRepository.
// The two constants live apart — the release URL is the updater's fallback when
// a config names no repository — but they have to name the same project, or a
// deployment installs one panel and the next update replaces it with another
// project's build.
func TestDefaultPanelReleaseURLMatchesTheConfiguredRepository(t *testing.T) {
	want := repositoryPath(t, config.DefaultPanelGitHubRepository)
	if got := repositoryPath(t, defaultManagementReleaseURL); got != want {
		t.Fatalf("fallback release URL points at %q, want %q", got, want)
	}
}

// The updater builds the release URL from whatever a config supplies, so the
// default repository form has to resolve to a usable releases endpoint.
func TestResolveReleaseURLFromRepository(t *testing.T) {
	got := repositoryPath(t, resolveReleaseURL(config.DefaultPanelGitHubRepository))
	want := repositoryPath(t, config.DefaultPanelGitHubRepository)
	if got != want {
		t.Fatalf("resolveReleaseURL() = %q, want %q", got, want)
	}
}
