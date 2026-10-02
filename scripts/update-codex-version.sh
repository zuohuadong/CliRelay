#!/usr/bin/env bash
# update-codex-version.sh — Update codex-tui version across the codebase.
#
# Usage:
#   ./scripts/update-codex-version.sh [NEW_VERSION]
#   ./scripts/update-codex-version.sh --current  # print the local version without changes
#
# When NEW_VERSION is omitted, the latest version is fetched from the npm registry.
# The script updates all Go source files that embed the codex-tui version string
# (constants, defaults, and test expectations), then runs gofmt and a compile check.
#
# Exit codes:
#   0 — version updated (or already up to date)
#   1 — error

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ── Helpers ──────────────────────────────────────────────────────────────────

valid_version() {
    [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[[:alnum:]][[:alnum:].-]*)?(\+[[:alnum:]][[:alnum:].-]*)?$ ]]
}

current_version() {
    local version
    version="$(sed -nE 's/^[[:space:]]*DefaultCodexFingerprintVersion[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' \
        "$REPO_ROOT/internal/config/local_types.go")" || return 1
    # Reject missing or duplicate definitions before any replacements run.
    if ! valid_version "$version"; then
        echo "ERROR: expected one valid DefaultCodexFingerprintVersion in internal/config/local_types.go" >&2
        return 1
    fi
    printf '%s\n' "$version"
}

latest_npm_version() {
    curl -fsS 'https://registry.npmjs.org/@openai/codex/latest' \
        | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
        | head -1
}

replace_literal() {
    local file="$1"
    local old="$2"
    local new="$3"

    SEARCH="$old" REPLACEMENT="$new" perl -0pi -e 's/\Q$ENV{SEARCH}\E/$ENV{REPLACEMENT}/g' "$file"
}

# ── Determine target version ─────────────────────────────────────────────────

if [ $# -gt 1 ]; then
    echo "Usage: $0 [NEW_VERSION | --current]" >&2
    exit 1
fi

CUR_VERSION="$(current_version)"
if [ "${1:-}" = "--current" ]; then
    printf '%s\n' "$CUR_VERSION"
    exit 0
fi

if [ $# -eq 1 ]; then
    NEW_VERSION="$1"
else
    echo "Fetching latest @openai/codex version from npm..."
    NEW_VERSION="$(latest_npm_version)"
fi
if ! valid_version "$NEW_VERSION"; then
    echo "ERROR: target version must be a version such as 0.160.0 or 0.160.0-beta.1" >&2
    exit 1
fi

if [ "$NEW_VERSION" = "$CUR_VERSION" ]; then
    echo "Version already up to date: $CUR_VERSION"
    exit 0
fi

echo "Updating codex-tui version: $CUR_VERSION -> $NEW_VERSION"

# ── Build replacement strings ────────────────────────────────────────────────
# User-Agent format: codex-tui/{version} (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; {version})
# Only the version segments change; OS/terminal info stays as representative defaults.

OLD_UA="codex-tui/${CUR_VERSION} (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; ${CUR_VERSION})"
NEW_UA="codex-tui/${NEW_VERSION} (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; ${NEW_VERSION})"

# ── Update source files ─────────────────────────────────────────────────────

# 1. internal/config/local_types.go — default fingerprint constants
replace_literal \
    "$REPO_ROOT/internal/config/local_types.go" \
    "$OLD_UA" \
    "$NEW_UA"
# Match the declaration, not gofmt's current alignment width.
REPLACEMENT="$NEW_VERSION" perl -pi -e \
    's/^(\s*DefaultCodexFingerprintVersion\s*=\s*")[^"]+(")/${1}$ENV{REPLACEMENT}${2}/' \
    "$REPO_ROOT/internal/config/local_types.go"
if [ "$(current_version)" != "$NEW_VERSION" ]; then
    echo "ERROR: failed to update DefaultCodexFingerprintVersion" >&2
    exit 1
fi

# 2. internal/runtime/executor/codex_executor_request.go — fallback User-Agent constant
replace_literal \
    "$REPO_ROOT/internal/runtime/executor/codex_executor_request.go" \
    "$OLD_UA" \
    "$NEW_UA"

# 3. Test files — update all version and User-Agent references
for f in "$REPO_ROOT/internal/config/identity_fingerprint_test.go"; do
    [ -f "$f" ] || continue
    replace_literal "$f" "$OLD_UA" "$NEW_UA"
    # Bare version references in assertions and failure messages.
    replace_literal "$f" "$CUR_VERSION" "$NEW_VERSION"
done

# ── Format & verify ─────────────────────────────────────────────────────────

gofmt -w \
    "$REPO_ROOT/internal/config/local_types.go" \
    "$REPO_ROOT/internal/runtime/executor/codex_executor_request.go" \
    "$REPO_ROOT/internal/config/identity_fingerprint_test.go"

echo "Running compile check..."
cd "$REPO_ROOT"
if go build -o /dev/null ./cmd/server 2>&1; then
    echo "Compile check passed."
else
    echo "ERROR: compile check failed" >&2
    exit 1
fi

echo ""
echo "Updated codex-tui version from $CUR_VERSION to $NEW_VERSION"
echo "Files modified:"
git -C "$REPO_ROOT" diff --name-only
