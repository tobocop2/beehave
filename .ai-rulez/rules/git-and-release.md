---
type: Decision
title: Git And Release
x-ai-rulez:
  kind: rule
  id: git-and-release
  metadata:
    priority: high
---

# Git, workflow and release artifacts

## Git and workflow

- Every change has a beads issue. Run `make check` before you close it. CI runs the same gate.
- Ask before any externally visible action: an issue, a PR, a comment, a push. Never push without approval.
- Do not stop to ask whether to keep going during multi-step work. Finish the work.
- Before a push, run `make check`, the affected tests and the review sweep. Never push on hope.
- No `Co-Authored-By` line in commits. No "Phase N" in commit messages.
- Stage explicit paths only. Never `git add -A` or `git add .`.
- A PR description is short and human. No internal names, no test-plan section. Follow the repo's header convention.
- Do not rename a branch that has an open PR. Use a worktree for parallel work.
- Fix the root cause, not a downstream choke point.
- QA runs in a dedicated worktree. File bugs as beads issues; do not fix them inline.
- Do not commit specs or plans to the repo.

## Build and release artifacts

- GoReleaser builds darwin and linux for amd64 and arm64 with `CGO_ENABLED=0`. `make release-snapshot` runs it without publishing.
- A new gate must be proven to fail. Run it against input it should reject before you trust it.
- `obee --version` passing is not verification of a binary. The smoke step runs a real command on the built artifact.
- Never put `continue-on-error` on a smoke or correctness step. Skip a step that cannot run in an environment with an explicit `if:` and a stated reason.
- Release shell lives in `scripts/*.sh`, not inline in a workflow, so it can be linted and tested off a runner.
- `gh api --paginate` runs the `-q` filter once per page. Do not use it with a filter that returns a count.
- `echo x=y >> $GITHUB_OUTPUT` does not set a shell variable. Assign it first, then emit it.
- A new publish workflow is wired in three places: the dispatching job, the verify step and the watch list.
- Renaming a workflow file breaks `workflow_dispatch` until the rename is on the default branch.

## Agent files

`.ai-rulez/` is the source. `make governance-generate` writes `AGENTS.md`, `CLAUDE.md` and the preset folders; they are ignored by git. Edit the rules, not the outputs. `make governance-verify` validates the config and a dry-run render.
