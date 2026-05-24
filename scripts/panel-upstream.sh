#!/usr/bin/env bash
# Maintains PR compatibility with upstream codeProxy repositories.
#
# Usage:
#   ./scripts/panel-upstream.sh setup    - Add upstream remotes
#   ./scripts/panel-upstream.sh fetch    - Fetch latest from upstream
#   ./scripts/panel-upstream.sh pr       - Create PR against upstream (kittors/codeProxy)
#   ./scripts/panel-upstream.sh sync     - Merge upstream changes into panel/
#   ./scripts/panel-upstream.sh status   - Show upstream remote status
#
# The panel/ directory tracks codeProxy source code.
# Two upstream remotes are configured:
#   upstream-codeproxy      -> https://github.com/kittors/codeProxy (canonical upstream)
#   origin-codeproxy        -> git@github.com:zuohuadong/codeProxy (fork)
#
# To submit PRs to the canonical upstream:
#   1. Make changes in panel/
#   2. Push to origin-codeproxy
#   3. Create PR from zuohuadong/codeProxy -> kittors/codeProxy

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PANEL_DIR="$(cd "$SCRIPT_DIR/../panel" 2>/dev/null && pwd || echo "")"

cmd_setup() {
  if [ -z "$PANEL_DIR" ]; then
    echo "Error: panel/ directory not found"
    exit 1
  fi

  cd "$PANEL_DIR"

  if [ ! -d ".git" ]; then
    echo "Initializing separate git repo for panel/ (monorepo subtree)..."
    git init
    git add -A
    git commit -m "Initial panel source from codeProxy"
  fi

  # Add upstream remotes
  if ! git remote get-url upstream-codeproxy &>/dev/null; then
    git remote add upstream-codeproxy https://github.com/kittors/codeProxy.git
    echo "Added upstream-codeproxy -> https://github.com/kittors/codeProxy"
  else
    echo "upstream-codeproxy already exists: $(git remote get-url upstream-codeproxy)"
  fi

  if ! git remote get-url origin-codeproxy &>/dev/null; then
    git remote add origin-codeproxy git@github.com:zuohuadong/codeProxy.git
    echo "Added origin-codeproxy -> git@github.com:zuohuadong/codeProxy"
  else
    echo "origin-codeproxy already exists: $(git remote get-url origin-codeproxy)"
  fi
}

cmd_fetch() {
  cd "$PANEL_DIR"
  echo "Fetching from upstream-codeproxy..."
  git fetch upstream-codeproxy 2>&1 || echo "Warning: fetch upstream-codeproxy failed"
  echo "Fetching from origin-codeproxy..."
  git fetch origin-codeproxy 2>&1 || echo "Warning: fetch origin-codeproxy failed"
  echo "Done."
}

cmd_pr() {
  cd "$PANEL_DIR"

  local branch
  branch="$(git branch --show-current)"

  if [ "$branch" = "main" ] || [ "$branch" = "master" ]; then
    echo "Error: cannot create PR from main/master branch. Create a feature branch first."
    exit 1
  fi

  echo "Pushing current branch '$branch' to origin-codeproxy..."
  git push -u origin-codeproxy "$branch" 2>&1

  echo ""
  echo "Create PR at:"
  echo "  https://github.com/zuohuadong/codeProxy/compare/${branch}?expand=1"
  echo ""
  echo "Target: kittors/codeProxy base: dev"
  echo ""
  echo "Or use gh CLI:"
  echo "  gh pr create --repo kittors/codeProxy --head zuohuadong:${branch} --base dev"
}

cmd_sync() {
  cd "$PANEL_DIR"

  echo "Fetching upstream..."
  git fetch upstream-codeproxy 2>&1

  local current_branch
  current_branch="$(git branch --show-current)"

  echo "Merging upstream-codeproxy/main into ${current_branch}..."
  git merge upstream-codeproxy/main --no-edit 2>&1 || {
    echo ""
    echo "Merge conflicts detected. Resolve them manually, then:"
    echo "  cd panel/"
    echo "  git add -A"
    echo "  git commit"
    exit 1
  }

  echo "Sync complete."
}

cmd_status() {
  cd "$PANEL_DIR"

  echo "=== Panel Git Status ==="
  echo "Branch: $(git branch --show-current 2>/dev/null || echo 'not a git repo')"
  echo ""

  for remote in upstream-codeproxy origin-codeproxy; do
    if git remote get-url "$remote" &>/dev/null; then
      echo "${remote}: $(git remote get-url "$remote")"
    else
      echo "${remote}: not configured"
    fi
  done

  echo ""
  echo "Recent commits:"
  git log --oneline -5 2>/dev/null || echo "No commits yet"
}

case "${1:-}" in
  setup)  cmd_setup  ;;
  fetch)  cmd_fetch  ;;
  pr)     cmd_pr     ;;
  sync)   cmd_sync   ;;
  status) cmd_status ;;
  *)
    echo "Usage: $0 {setup|fetch|pr|sync|status}"
    echo ""
    echo "Commands:"
    echo "  setup   - Initialize panel/ git repo and add upstream remotes"
    echo "  fetch   - Fetch latest from upstream remotes"
    echo "  pr      - Push branch and create PR against upstream"
    echo "  sync    - Merge upstream changes into panel/"
    echo "  status  - Show upstream remote status"
    exit 1
    ;;
esac
