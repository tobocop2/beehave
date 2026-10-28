---
type: Decision
title: Review And Smells
x-ai-rulez:
  kind: rule
  id: review-and-smells
  metadata:
    priority: high
---

# Code review and smell triggers

## Review standards

- Low complexity: about 3 branches per function. DRY: no copy-paste. Minimal changes: the smallest edit that is right.
- No unexported identifiers leak through a package surface. A package exports only its real API.
- Named types over inline maps. Repeated shape means a struct.
- Compile before you test. Review until a pass finds nothing new.
- Keep architecture changes and hygiene sweeps (comments, renames) in separate commits.

## Smell triggers

A reviewer and the author run these before review. A hit is fixed or justified in the PR body. Most are linters in `.golangci.yml`.

```bash
grep -rnE "^var [a-z]" --include=*.go internal/                  # mutable package state (gochecknoglobals)
grep -rn "func init()" --include=*.go .                           # init side effects (gochecknoinits)
grep -rnE "map\[string\]any|interface\{\}|\bany\b" --include=*.go internal/   # untyped data inward
grep -rn "os.Getenv" --include=*.go internal/ | grep -v internal/config        # env read outside config
grep -rnE "fmt\.(Print|Fprint)" --include=*.go internal/ | grep -v internal/cli
grep -rnE "reflect\." --include=*.go internal/                    # reflection on owned types
grep -rnE "(kind|role|status|mode|task|verdict) string" --include=*.go internal/   # closed set as string
grep -rn "errors.New(\"" --include=*.go internal/ | grep -v "Err"  # inline sentinel
grep -rnE "Deprecated:|legacy|backward|historical|so existing" --include=*.go --include=*.md internal/ docs/
grep -rn "continue-on-error" .github/workflows/                   # swallowed gate
grep -rn "//nolint" --include=*.go . | grep -v "//nolint:[a-z]* //"   # nolint without a reason
```

Layering: `depguard` in `.golangci.yml` is the check. Run `go list -deps ./internal/<pkg>` to see what a package pulls in.

## Self-review checklist

1. Trace consumers: for each changed package, `grep` who imports it, two levels deep for shared types.
2. Constants over raw strings: search for the raw value of any new constant.
3. Fakes and tests still match the real interface after a signature change.
4. No dead code: removed functions have no references and no orphaned imports.
5. Every new path has a test with an assertion.
6. A field added or removed: every constructor, decoder and encoder is updated.
7. A new exported name is needed, and a removed one has no dangling importer.
8. No back-compat scaffolding (see the smell list).
9. No new `//nolint` without a reason.
10. Every smell grep returns no more hits than on `main`.
11. `make check` is green on the final tree.

## Cross-cutting changes

Adding an enum constant, a config field or an entry point means a blast-radius pass before the code.

- Grep every `switch` and map keyed by the type (`grep -rn "model\.Verdict" internal/`). Update every site in the same commit. `exhaustive` catches switches; maps need the grep.
- A validation at one entry point (CLI flag, env var, HCL key, workflow input) is mirrored at every sibling entry point.
- A trait-like interface documents the call order of its methods (which runs first, which can run at the same time).
- Error text from an entry point names the problem and the fix.
