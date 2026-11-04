---
type: Decision
title: Layering And State
x-ai-rulez:
  kind: rule
  id: layering-and-state
  metadata:
    priority: critical
---

# Layering, configuration and state

## Layers

The engine is under `internal/` (and the one `cmd/beehave` root). A package imports its own layer and lower layers, never a higher one. `depguard` in `.golangci.yml` fails an upward import.

The method tools are under `examples/remediation/tools/`, in the same Go module. A file under `internal/` or `cmd/beehave/` never imports `examples/`. The rule has no exception, test files included; the scripts that run `remedy` are a test under `examples/remediation/tools/cmd/remedy`. A package under `examples/` may import any layer of `internal/`. Only `examples/remediation/tools/cmd/remedy` imports a backend (`forge/github`). The other packages of the tools take a collaborator through an interface, as the engine does. `remedy` is a second binary: `make build` makes `bin/remedy`, and the release archive holds only `beehave`.

| Layer | Packages | Holds |
|---|---|---|
| 0 | `model`, `config`, `lockfile` | Domain types and enums, the typed config, the file lock opener. No upward imports. |
| 1 | `store`, `forge`, `proc`, `agent`, `host` | Adapters for `bd`, `gh`, agent CLIs and build hosts. Each wraps an external shape once. |
| 2 | `check/shell`, `claim`, `gate`, `merge`, `review`, `pool`, `workflow` | Round policy and the workflow file schema. Declares the small interfaces it needs from layer 1. |
| 3 | `flow` | DBOS workflows and steps that call layer 2. |
| 4 | `cli`, `tui` | Surfaces. Then `cmd/beehave`. |

- One contract, one parent package, one package per implementation: `store/beads`, `store/github`, `forge/github`, `agent/claude`. The parent holds the interface, its errors and its conformance suite (`storetest`, `forgetest`, `agenttest`) and imports no implementation. An implementation never imports another one. Only `cmd/beehave` imports implementations and holds the name-to-constructor table.
- Put a domain type in the lowest layer that uses it. Move it down, never up.
- An effect that crosses layers goes through a function value or an interface the higher layer passes in.
- No re-export shims or alias types when you move a symbol. Update every importer in the same commit.
- Do not use build tags or test-helper imports in production code to work around a cycle. A cycle means the layer is wrong.

## Services container

The service graph is a plain struct built once in `cmd/beehave/main.go`. Constructors take their collaborators and return concrete types. There is no global, no lazy getter and no reset function. Tests build the same struct with fakes.

## Configuration and state

- No mutable package-level state. No `init()` functions. `gochecknoglobals` and `gochecknoinits` enforce this. The build-time variable `version` in `cmd/beehave`, set by `-ldflags`, is the only exception; the linter allows that name.
- One Starlark config, read through one constructor for each block. Unknown arguments are errors. `BEEHAVE_*` environment variables override the file; the flags override both.
- Read the environment only in the `config` package. `forbidigo` bans `os.Getenv` elsewhere.
- Canonicalize a value once, at the write boundary (`Config.Validate`, `ParseX`). Do not add alias fallbacks to a resolver.
- A default must pass the validator. Test `DefaultConfig().Validate()`.
- Never duplicate state across packages. Pass values as parameters.
- Do not hide state in a closure over a package variable or a `sync.Once` at package scope. Put it on a struct.
- Code that branches on "running under test" is a defect. Make the test build the production type with fakes.
