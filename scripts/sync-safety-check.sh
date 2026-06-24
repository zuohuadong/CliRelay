#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "::error::$1"
  exit 1
}

unmerged_files="$(git diff --name-only --diff-filter=U || true)"
if [ -n "${unmerged_files}" ]; then
  echo "Unmerged files remain:"
  echo "${unmerged_files}"
  fail "Resolve all merge conflicts before opening or merging a sync PR."
fi

conflict_markers="$(git grep -n -E '^(<{7}|={7}|>{7})([[:space:]].*)?$' -- \
  ':!.mailbox/**' \
  ':!cli-proxy-api-linux' || true)"
if [ -n "${conflict_markers}" ]; then
  echo "Conflict markers remain in tracked files:"
  echo "${conflict_markers}"
  fail "Remove conflict markers before opening or merging a sync PR."
fi

for removed_model in sora-2; do
  stale_hits="$(git grep -n -i -F "${removed_model}" -- \
    ':!progress.md' \
    ':!.mailbox/**' \
    ':!**/*_test.go' \
    ':!**/*.test.ts' \
    ':!**/*.test.tsx' \
    ':!**/__tests__/**' || true)"
  if [ -n "${stale_hits}" ]; then
    echo "Removed model '${removed_model}' is still referenced outside tests:"
    echo "${stale_hits}"
    fail "Remove stale model references or move rejection coverage into tests."
  fi
done

echo "Sync safety check passed."
