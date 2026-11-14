# beehave architecture

beehave runs a backlog of work items through a workflow that you write in Starlark. Each workflow runs on DBOS Transact Go with SQLite, so after a crash the finished steps return their recorded results and the run continues at the step that was running. Coding work is the first use: issues go to reviewed, gated pull requests with any coding agent.

This page is the map. The detail lives in the topic files under [`design/`](design/).

## Where to start

For a first read, take these three in order:

1. [One item, end to end](design/durable-loop.md#one-item-end-to-end): the path of one item, from the claim to the merge, as a diagram.
2. [Packages and layers](design/packages.md#packages-and-layers): the Go packages and the layer rule.
3. [Workflow files](design/workflow-files.md): the Starlark file and its builtins.

## The layer rule

A package imports its own layer and lower layers. `depguard` in `.golangci.yml` fails an upward import, and it fails a package that is not on a layer list. [Packages and layers](design/packages.md#packages-and-layers) shows each package and its layer.

## Topics

### Design choices

| File | What it covers |
|---|---|
| [Why DBOS and Starlark](design/why-dbos-and-starlark.md) | The engine and the workflow language, and the options that beehave rejected. |
| [Packages](design/packages.md) | The packages and layers, the composition root and the adapters. |

### The engine

| File | What it covers |
|---|---|
| [The durable loop](design/durable-loop.md) | Item and phase workflows, queues, cost budgets, fix rounds, claim beats, process control, status, resume and cancel. |
| [Workflow files](design/workflow-files.md) | The Starlark file, its builtins, replay, and the examples. |
| [Configuration](design/configuration.md) | The config folder, the project files, the loader and the environment. |
| [DBOS and secrets](design/dbos-and-secrets.md) | The rules that keep the DBOS state private and keep secrets out of the records. |

### Steps and checks

| File | What it covers |
|---|---|
| [Agent runs](design/agent-runs.md) | Agent runs, the command rules and their hook, the shell rules and the lane papers. |
| [Sandbox](design/sandbox.md) | The sandbox of an agent step and of the gate. |
| [Gate](design/gate.md) | The gate command, its steps and their three states. |
| [Runtime checks](design/runtime-checks.md) | The ledger, the runner pool, `beehave doctor`, preflight, the review poll and the scope check. |
| [Merge](design/merge.md) | The merge guards, the batch merge and the remediation tools. |
| [The trust boundary](design/trust.md) | The threat model, the two GitHub Apps, the ruleset, the sandbox default, the ledger and the claim fence. |

### Adapters

| File | What it covers |
|---|---|
| [Tracker and forge](design/tracker-and-forge.md) | The beads and GitHub issue backends, the claim leases, the forge and the command runner. |
| [Build hosts](design/hosts.md) | The host contract and the SkyPilot, SSH and Slurm providers. |

## Decisions

- **Interfaces at the consumer.** A package states what it needs, so a fake needs few methods and an adapter can change without edits in policy code.
- **CLI as contract for `gh` and `bd`.** The `gh` and `bd` commands are what the owner runs by hand. `bd` has a public Go package, but it needs cgo or a Dolt backend and pulls its whole tree. Revisit when `bd` ships a slim client module.
- **No framework for config.** A config file is a Starlark file. `starlark.UnpackArgs` rejects unknown arguments, and each problem carries `file:line:col`. The overlay is explicit code in one package.
- **Make, not Task.** A single Makefile holds the targets: `check`, `lint`, `test`, `format`, `vet` and `coverage`.
- **Coverage exclusion.** Only `cmd/beehave/main.go` is excluded from the 100% gate. The smoke step runs the built binary.
- **Agent rules from `.ai-rulez/`.** Generated files (`AGENTS.md`, `CLAUDE.md` and the preset folders) are ignored by git.
