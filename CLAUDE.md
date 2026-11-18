# Claude Code Agent Rules

## Principles

1. **Language**: Reply in Simplified Chinese. Keep comments and responses clear and concise.
2. **Scope Discipline**: Implement requested features/fixes directly without unrelated refactors, speculative complexity, or framework sprawl.
3. **Verification**: Run targeted local tests and typechecks before marking tasks complete.
4. **Safety**: Never leak or hardcode secrets/tokens. Do not perform unrequested destructive git operations or auto-deployments.
5. **Preserve Untracked State**: Do not touch unrelated dirty files or untracked directories.
