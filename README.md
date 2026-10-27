<div align="center">

# obee

**Your agents write the code. A program runs the process.**

[![CI](https://github.com/tobocop2/obee/actions/workflows/ci.yaml/badge.svg)](https://github.com/tobocop2/obee/actions/workflows/ci.yaml)
[![Go 1.27](https://img.shields.io/badge/go-1.27-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

</div>

obee turns a backlog of GitHub issues into reviewed pull requests with coding agents. One agent session writes the change, a fresh session reviews it, and the reviewed commit is the only one that goes forward.

obee is one executable. It has no server, no web app and no telemetry. You watch it in a terminal dashboard, and so can an agent.

> **Do not let your agent be the orchestrator.** Agents are good at writing and reviewing code. They are not reliable at keeping the rules of the process around it. obee is the orchestrator, and it is a program.

## Who this is for

You run more than one coding agent at a time, and you have tried to make one agent the orchestrator of the others. It worked for a while, and then it skipped a step. obee is for you if you want agents to do the work and a program to keep the rules.

## An agent as the orchestrator, or obee

| | An agent as the orchestrator | obee |
|---|---|---|
| The rules | Text in a prompt, followed most of the time | Code, checked every time |
| A long session | Forgets the rules it got at the start | Has no session to forget |
| Waiting on a build | Re-reads its whole context after each wait | Waits for free |
| Spending limits | Lost when the context is compacted | Set in the config |
| The review | Whatever the agent says it did | A step obee runs and records |
| The merge | Whatever commit is there | Only the reviewed commit, after your approval or on its own if you turn that on |
| After a crash | Starts over | Resumes where it stopped |

## What went wrong when an agent was the orchestrator

All of these happened to me:

| What happened | What it cost | What obee does instead |
|---|---|---|
| A cleanup woke 15 finished agents | 5.3 million tokens, no work done | An agent's process ends with its step, so nothing is left to wake |
| An agent re-read its whole memory after each build | 47 million tokens | obee waits on the build, not the agent |
| A timer kept the orchestrator awake for ten hours | 34 million tokens | obee is a program, so waiting costs nothing |
| A memory cleanup erased the spending limits | Nine agents at once, usage limit hit twice | Limits live in the config and are checked in code |
| Three agents said they ran a review | No review happened | obee runs the review step itself and records the verdict |
| A pull request merged 15 minutes after a request for changes | A merge against the maintainer | obee reads every review before it marks a pull request ready |
| A cloud server started after I cancelled it, and the agent said it had not | A server I did not know was running | obee asks the provider what exists, not the agent |
| An unattended run stopped on a command the safety check could not read | Hours lost waiting for approval | obee checks commands and job scripts; a bad one stops that step only |

If you do not want this to happen to you, obee solves these problems.

## How it works

| Step | What happens |
|---|---|
| 1. Claim | obee claims the issue in beads, so no other run takes it |
| 2. Write | An agent writes the change and its tests in its own copy of the repository |
| 3. Check | obee runs your project's tests and checks the commands the agent ran |
| 4. Review | A fresh agent session reviews the exact commit that passed the tests |
| 5. Fix | The findings go back to the writer, then to review again, until the review is clean |
| 6. Ask | obee marks the pull request ready only when GitHub shows the reviewed commit, no requested changes and no open threads |
| 7. Merge | obee merges that commit when you approve, or on its own with `--auto-merge`; a changed commit is never merged |

Who reviews is your choice. The agent, the model, the skills and the time limits of each step are configuration. Whether the review happens is not.

## Why obee instead of Archon

[Archon](https://github.com/coleam00/Archon) is the closest tool to obee, and I used it before I wrote obee. It has a real review step and a merge pinned to the reviewed commit. I replaced it because it is much more than I need, and it does things I do not want.

| | Archon | obee |
|---|---|---|
| Scope | Any workflow you write in YAML, with a web app | Today one workflow: an issue becomes a reviewed pull request. Workflows you write, with rules you choose to enforce, are planned |
| Workflow engine | Its own: a 72,000-line engine with its own runs, resume and queue | [DBOS Transact](https://github.com/dbos-inc/dbos-transact-golang), an existing library for durable workflows. obee's own code is the rules, not the engine |
| Size | 271,000 lines, 63 direct dependencies | 14,000 lines, 6 direct dependencies |
| What you install | A Bun app with SQLite or PostgreSQL; Docker files for a server setup | One executable |
| Server | `archon serve` starts a web server that listens on every network interface by default | None. A test fails if obee opens a network socket |
| Telemetry | PostHog, on by default; you turn it off with an environment variable | None |
| Merge | An agent decides which pull requests are clear enough to merge | Code decides: the reviewed commit, with no requested changes, open threads or failed checks. You approve each merge, or turn on `--auto-merge` (planned) |
| The review | A step in a YAML workflow; you can edit it or take it out | Set in the project's TOML file: the agent, model, skills and time limits. No setting takes the review out |

Every fact about Archon comes from its source code at commit `72817c5` (October 2026).

## Other tools

These are active agent orchestration projects with many stars in 2025 and 2026. Each had a commit in the 30 days before 27 October 2026. Every fact comes from the project's source code, not its README. Size is lines of source code without tests, docs and vendored code.

### What each tool is, and when to use it

| Project | Stars | Size (lines / direct deps) | Use it when you want |
|---|---|---|---|
| [ruflo](https://github.com/ruvnet/ruflo) | 80k | 810k / 140 | Many agent roles and swarms around Claude Code |
| [oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent) | 70k | 660k / 77 | A plugin that makes one OpenCode or Codex session stronger |
| [Gas Town](https://github.com/gastownhall/gastown) | 19k | 249k / 30 | Many agent sessions in tmux, with a merge queue |
| [GitHub Agentic Workflows](https://github.com/github/gh-aw) | 5k | 605k / 37 | Agent steps inside GitHub Actions |
| [Bernstein](https://github.com/sipyourdrink-ltd/bernstein) | 1k | 860k / 35 | Agent runs under written policies, with an audit record |
| **obee** (milestone 8) | | **14k / 6** | **A backlog that becomes reviewed pull requests by rules nobody can skip** |

### What each tool needs, and what it runs

| Project | Needs | Server | Telemetry | Agents |
|---|---|---|---|---|
| ruflo | Node 20 | MCP over stdio; HTTP on localhost with a command | Its own endpoint, off until you agree | Claude Code, Codex, model APIs |
| oh-my-openagent | OpenCode or Codex, Bun | None on the network | PostHog, on by default | OpenCode, Codex |
| Gas Town | tmux, Dolt, beads, SQLite | A Dolt server on port 3307; a dashboard with a command | Only if you set an OpenTelemetry address | 14, Claude Code by default |
| GitHub Agentic Workflows | `gh`, GitHub Actions, Docker on the runner | None by default | Only if you set an OpenTelemetry address | Copilot by default, Claude Code, Codex, Gemini, Pi |
| Bernstein | Python 3.12 | A task server on localhost, started by each run | Off by default | More than 100 |
| **obee** | **`gh`, `git`, beads** | **None** | **None** | **Claude Code; OpenCode, Pi and Hermes are planned** |

### How each tool reviews and merges

| Project | What drives the loop | The review | The merge |
|---|---|---|---|
| ruflo | A model session calls its tools | None required | Merges on request, not pinned to a commit |
| oh-my-openagent | A model session, kept going by hooks | A verifier agent, only in its "ultrawork" mode | None |
| Gas Town | A daemon and coordinator agents | A pull request approval, only if you turn it on | Pushes to the branch by default |
| GitHub Agentic Workflows | GitHub Actions; the model decides inside the job | A security scan of the output, not a code review | Your branch rules |
| Bernstein | Code | A review gate; the check by a second model is off by default | Auto-merge, not pinned to a commit |
| **obee** | **Code** | **Always runs, at the commit that passed the tests** | **The reviewed commit only, after your approval or automatically (planned)** |

## Status

obee is under construction. The packages land one milestone at a time; `docs/architecture.md` lists them.

| Written, in review | Planned |
|---|---|
| The durable loop, claims, the review rounds, the ready check, the command and job checks | Wiring the loop end to end, the pinned merge, the terminal dashboard, agents other than Claude Code |

## Build

obee needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/obee
```

## Design

`docs/architecture.md` describes the packages, their layers and the durable loop. The loop runs on DBOS Transact Go with SQLite. The terminal dashboard uses Bubble Tea.

## License

MIT. See `LICENSE`.
