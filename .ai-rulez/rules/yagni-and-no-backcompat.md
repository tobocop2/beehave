---
type: Decision
title: Yagni And No Backcompat
x-ai-rulez:
  kind: rule
  id: yagni-and-no-backcompat
  metadata:
    priority: high
---

# YAGNI and no back-compat scaffolding

## YAGNI

- Do not add a feature, abstraction or config field that nothing needs yet.
- Three similar lines are better than a premature abstraction.
- A config field needs a real user scenario named in its help text. Otherwise it is a constant.
- Validate at system boundaries only (user input, TOML, `gh` and `bd` output, agent output). Trust internal code.
- Do not wrap a stdlib or mature-library call in a helper that adds nothing.

## Build before you build

Before you design or write scaffolding, check whether a mature tool owns it. Scaffolding means CLI parsing, config loading, retry, caching, logging, release, packaging, workflow state and the forge client. The custom part is the round policy. Put a table (component, existing tool, keep or adopt, why) in the plan.

## No back-compat scaffolding

obee has no released version and no pinned consumers. A refactor updates the consumers.

- No shim package or file at an old path. No alias type (`type Old = New`). No `Deprecated:` comment on code with no outside callers.
- No re-export of an unexported helper to keep a test patch working. Test where the symbol lives.
- No comment that justifies why code exists ("kept for", "legacy", "preserves the old API"). If a comment must justify the code, delete the code.
- No method labeled "for backward compat". Delete it and update the callers.
- When you move a symbol, grep every consumer (code, tests, docs, workflows, CLI help) and update all in the same commit.
