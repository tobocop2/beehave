# Why DBOS and Starlark

Part of the [beehave architecture](../architecture.md).

This part records the choice of engine and workflow language, the options that beehave rejected, and the reason for each verdict.

## Why DBOS and Starlark

beehave reads Starlark for workflows and for config files. It used HCL before, and the code no longer reads HCL. An old `.hcl` config file is refused with the name of the file.

### The decision

- DBOS is the engine.
- Starlark is the workflow language and the config language, through a thin binding (issue #72).
- beehave owns the method and its adapters (see [What beehave owns](#what-beehave-owns)).
- Every licence is permissive (MIT, BSD, Apache). No dependency is GPL.

### Requirements

Each justification below points to one of these requirements.

| Id | Requirement |
|---|---|
| Q1 | A user runs a workflow with no compile step. |
| Q2 | A workflow is replay-safe and resumes after a crash. |
| Q3 | beehave runs on one laptop as one program, with no listener and no telemetry. |
| Q4 | Every licence is permissive. |
| Q5 | The method is written once and reused. |
| Q6 | beehave maintains no part that a mature tool already owns. |
| Q7 | Every new dependency has a commit in the last 30 days. A project with no tags is accepted when beehave pins it by commit. |
| Q8 | A workflow reads as steps in order, with branches, loops and early exit. |

### Why DBOS

| Reason | Serves |
|---|---|
| DBOS records each step result and replays the workflow around the results. A crash must not run an agent step twice. After a crash, the steps that finished return their recorded results, and the run continues at the step that was running. | Q2 |
| Durable waits survive a crash. A wait for a review or a human answer can last days. DBOS stores the wait in its database, so beehave has no checkpoint code. | Q2, Q6 |
| DBOS is a library inside the binary, with SQLite or Postgres. There is no separate engine server to start. | Q3 |
| DBOS is MIT and already a dependency. | Q4, Q6 |

### Engines compared

"No gain over DBOS" marks an engine that fails no requirement but adds nothing DBOS lacks.

| Engine | What it is | Why not |
|---|---|---|
| Dagu | A single binary that keeps state in local files | Fails Q4: GPLv3, so beehave cannot link it. It restarts a failed step and does not replay a Go function, which gives less than DBOS for Q2. Its bounded loop and reference check are unproven. The method (reviewer, merge rule, transcript checks) would still be beehave code, run as shell steps |
| Windmill | A workflow platform | Fails Q4: its core is AGPLv3. It also needs Postgres, so it is not one program (Q3) |
| go-workflows | A Go engine (v1.4.2, MIT) with SQLite, in process | No gain over DBOS: one maintainer, no CLI, no partitioned queues |
| Argo Workflows | A workflow engine for Kubernetes | Fails Q3: it runs only on Kubernetes |
| Temporal | A durable engine | Fails Q3: it needs a server that listens on port 7233 |
| Restate | A durable engine | Fails Q3 and Q4: its server is BUSL-1.1 and sends telemetry unless the user disables it |
| Inngest | A durable engine | Fails Q4: its self-hosted server is not under a permissive licence |
| Hatchet, Cadence | Durable engines | Fail Q3: each needs a server |
| Kestra, Conductor | Durable engines | No gain over DBOS, which is already a dependency. The method stays beehave code on any engine |

### Why Starlark

| Reason | Serves |
|---|---|
| A workflow file runs with no compile step. A user edits a file and runs it. | Q1 |
| Starlark has no clock, no randomness and no I/O. A workflow is replay-safe for DBOS by construction, with no rule for the author to remember. | Q2 |
| A file acts only through beehave's steps. Each effect of a workflow goes through a recorded step, so a replay repeats no effect. | Q2 |
| Starlark has functions, `for` loops, `if` statements and early return in the language. A workflow reads as steps in order. | Q8 |
| Functions and `load()` let a method be written once. The review loop lives in one shared file, and each workflow loads it (see issue #70). | Q5 |
| starlark-go parses, resolves and runs the file. beehave will delete its own loader, checker and step-graph walker (about 2,400 lines, issue #72). | Q6 |

#### Pinning and limits

starlark-go publishes no tags, so beehave pins it by version and hash (a pseudo-version in `go.mod` and `go.sum`). It has recent commits and merged outside pull requests (Q7).

starlark-go has a deterministic step limit (`SetMaxExecutionSteps`), no `while` and no recursion. A replay hits a limit at the same point as the first run (Q2).

#### Prior art

Uber's `cadence-workflow/starlark-worker` (Apache-2.0) runs Starlark on Cadence and Temporal. The script re-runs inside the workflow function, and each side effect is an activity. The research found none for DBOS, so beehave writes a thin binding of the same shape.

#### The cost

A wrong step reference or a bad argument is a run-time error with the file, line and column. `beehave check workflow` reports syntax and name errors before a run.

### Languages compared

| Language | What it is | Why not |
|---|---|---|
| YAML | A data format | It has no loops or conditions of its own, so beehave writes the interpreter and schema for them (fails Q6): a parser layer of about 2,900 to 3,300 lines against about 2,200 to 2,300 with HCL |
| HCL | A configuration language (`hashicorp/hcl/v2`); beehave used it before Starlark | It evaluates expressions but has no loop over steps, so beehave wrote the step interpreter and schema (fails Q6): about 2,200 to 2,300 lines in all, of which the interpreter is about 600. Starlark removes about 2,400 lines of beehave code |
| CUE | A constraint language | Fails Q8: it has no statements and no sequenced host calls with early return, so a step sequence cannot be written in it |
| CEL | An expression language | Fails Q7: no commit for 47 days at the time of the research. beehave must also declare a type for every step output |
| Go as the workflow language | Plain Go on DBOS (v1.8.0, MIT) | Fails Q1: a change to a workflow needs a rebuild, and the per-file hash pin of a workflow changes meaning. The compiler does check step references |
| Pkl | A configuration language | Fails Q3: it runs through the separate `pkl` binary, so a workflow needs a second program at run time (checked 2026-10-28) |
| goja | JavaScript in Go | Fails Q2. goja can control time and randomness (`SetTimeSource`, `SetRandSource`), but it has no deterministic execution bound: only a host-set `Interrupt`, so a limit hit can differ between the first run and a replay. It also allows `while` and recursion. Starlark has a deterministic step limit, no `while`, no recursion and no clock (checked 2026-10-28) |
| Jsonnet (go-jsonnet) | A data templating language | Fails Q7: no commit in the 30 days before the check (checked 2026-10-28) |
| Risor | A scripting language for Go | Fails Q7: no commit in the 30 days before the check (checked 2026-10-28) |
| gopher-lua | Lua in Go | Fails Q7: no commit in the 30 days before the check. Its standard library includes the `os` and `io` modules unless the host leaves them out, which works against Q2 (checked 2026-10-28) |

### Agent orchestrators compared

None of these tools covers three things together:

- the pinned reviewer with a bounded fix loop,
- the merge of only the reviewed head after reading every thread,
- transcript evidence.

Nothing on the market owns the method, so beehave would still write it on top of any of them (see [What beehave owns](#what-beehave-owns)).

| Tool | What it is | Why not the base |
|---|---|---|
| Archon | MIT, v0.13.0. A workflow runner with durable waits | Closest by requirements. It pins a merge to a head, and it has a round-limited review loop. It has no pinned fresh reviewer, no stated escalation to a fresh stronger reviewer, and no transcript or skill checks. It sends telemetry until the user turns it off, and `archon serve` listens on all interfaces, which fails Q3 by default. It owns the run process |
| Sortie | Apache-2.0, v1.30.0. A single Go binary with SQLite | Right footprint, but it has no reviewer, no merge rule and no transcript checks. The loop would live in a model prompt |
| no_human | MIT. A package with a local board server | Merging is a human click. Its tamper guard checks tests, not transcripts |
| Orbi | AGPL-3.0 or Sustainable Use. Keeps its state in GitHub Issues | Fails Q4: AGPL. It merges only the reviewed head, which confirms the design. The research did not read whether it checks transcripts |
| Bernstein | Apache-2.0, v3.23.0. A local agent runner | It has a quorum approval bound to the diff, but no reviewer agent and no transcript rules. It runs a local server |
| Gas Town | MIT, v1.2.1. Standing agent sessions | Needs Dolt, tmux and several standing agent sessions that run on a model. It is far from one program (Q3) |
| Symphony | Apache-2.0. A specification, with an Elixir reference | A spec only. It has no review rule, merge rule or transcript check |
| gh-aw | v1.0.1. GitHub agentic workflows | It has a `/review` alias and a threat-detection gate on outputs. It has no pinned reviewer, merge rule or transcript check |

### What beehave owns

beehave owns the method and its adapters (about 17,700 lines). Nothing found on the market owns these:

- The pinned reviewer.
- The bounded fix loop with escalation.
- The gate on the exact head.
- The transcript rules.
- The forge read before a merge.
- The account pick by headroom.
- The adapters for `bd`, `gh` and `sky`.
