---
type: Decision
title: Before You Write Code
x-ai-rulez:
  kind: rule
  id: before-you-write-code
  metadata:
    priority: critical
---

# Before you write code

Apply these four rules before you write, not after.

1. Type external data precisely. Decode a `gh`, `bd` or agent-CLI payload once, at the edge, into a named struct. Do not pass `map[string]any` or `any` inward.
2. Do not copy the pattern of the nearby code without checking it against these rules. The nearby code can predate a rule.
3. Write one-line doc comments. Say what the thing is. Do not write "previously", "now also", "kept for" or "moved here because".
4. Run `make lint` before you commit. The lint config is the smell gate. A `//nolint` needs the linter name and a written reason (`nolintlint` enforces it).

## Project

obee takes a backlog of issues to reviewed, gated pull requests with any coding agent. Implementer lanes build and prove, reviewer lanes verify at pinned SHAs, and the loop merges pinned. The engine is DBOS Transact Go on SQLite. The TUI is Bubble Tea. Go 1.27, module `github.com/tobocop2/obee`.

## Commands

```bash
make setup             # install the tools and generate the agent files
make check             # the full gate: format, lint, vet, govulncheck, race tests, coverage gate, smoke, agent rules
make format            # gofumpt and goimports through golangci-lint
make lint              # golangci-lint
make test              # go test -race, writes coverage.out
make coverage          # the 100% coverage gate on coverage.out
make vuln              # govulncheck
make build             # build bin/obee
```

Use `make`, never the underlying tools, for the checks. `make check` mirrors CI.

## Task tracking

Every code change has a beads issue. Create it before the work and close it when the gate is green (`bd create`, `bd update <id> -s in_progress`, `bd close <id>`).
