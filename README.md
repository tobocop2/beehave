<div align="center">

# obee

**Your agents write the code. A program keeps the process.**

[![Go 1.27](https://img.shields.io/badge/go-1.27-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

</div>

obee runs coding agents through workflows that you write in Starlark. A workflow says which agent does each step, which command checks the work, who reviews it, and when the loop stops. obee runs the steps in order, records each result, and resumes after a crash at the step where it stopped.

obee is one static binary with no runtime to install. Copy one file and run it. It needs no language runtime, no virtual environment, no Node, no JVM and no server. It has no web app and no telemetry. It needs git and the command line tool of each agent that you use. It needs no GitHub account and no issue tracker. Those are optional adapters.

> **Do not let an agent be the orchestrator.** Agents are good at writing and reviewing code. They are not reliable at keeping the rules of the process around the code. In obee, the process is a file, and a program runs it.

## Who this is for

You run coding agents on real work, and you want a repeatable process around them: plan, implement, test, review, fix, merge. You tried to put that process in a prompt, or you made one agent the orchestrator of the others. It worked for a while, and then it skipped a step.

obee is for you if you want the agents to do the work and a program to enforce the steps.

<details>
<summary><b>What went wrong when an agent was the orchestrator</b></summary>

<br>

All of these happened to me while one agent orchestrated others:

| What happened | What it cost | What obee does instead |
|---|---|---|
| A cleanup woke 15 finished agents | 5.3 million tokens, no work done | An agent process ends with its step, so nothing is left to wake |
| An agent read its whole context again after each build | 47 million tokens | obee waits on the build. No model waits with it |
| A timer kept the orchestrator awake for ten hours | 34 million tokens | Waiting is a durable step, and it costs nothing |
| A context compaction erased the agent limits | Nine agents at once, usage limit hit twice | The limits are in the config, and code checks them |
| Three agents said that they ran a review | No review happened | The review is a step. obee runs it and records the verdict |
| A pull request merged 15 minutes after a request for changes | A merge against the maintainer | The merge step refuses a standing change request, an open thread and a missing check |
| A review approved one commit, and a different commit merged | Unreviewed code on main | The merge is pinned to the head that the gate and the review saw |
| The orchestrator session hit its limit in the middle of a run | The work in progress was lost | obee resumes the run at the step where it stopped |

</details>

## What this enables

- **A process that cannot skip a step.** The workflow file is the process. A review that the file requires always runs, at the commit that passed the gate.
- **Loops with an exit.** Gate, review and fix until the head is approved, for a fixed number of passes at most. The bound is in the file.
- **Runs that survive.** A crash, a reboot or a closed laptop does not lose the run. Each finished step is recorded, and the run continues at the first step that did not finish.
- **Any repository, with or without GitHub.** Items can live in a local store, in beads or in GitHub issues. Results can be a branch in a local repository or a merged pull request.
- **Questions for a person.** A `wait` step stops the run until you answer with `obee answer`.

## A workflow

This is the local example, shortened. It needs no tracker and no forge.

```
def main(item):
    plan = agent(name = "plan", prompt = "prompts/plan.md", model = "sonnet", inputs = {"item": item})
    agent(name = "implement", prompt = "prompts/implement.md", model = "sonnet", inputs = {"item": item, "plan": plan.text})

    approved = False

    for _ in range(3):
        check = gate(name = "check")
        findings = check.report

        if check.passed:
            review = agent(
                name = "review",
                prompt = "prompts/review.md",
                model = "sonnet",
                pin = check.head,
                outputs = ["verdict"],
                inputs = {"item": item},
            )
            approved = review.verdict == "approve"
            findings = review.text

        if approved:
            break

        agent(name = "fix", prompt = "prompts/fix.md", model = "sonnet", inputs = {"item": item, "findings": findings})

    if approved:
        command(name = "push", run = ["git", "push", "origin", "HEAD:refs/heads/" + item.id])
```

`obee check workflow` reads the file before any agent starts. It prints one line for each problem: a file that does not parse, a name that is not defined, or a missing `main`.

## Quick start

To build obee, you need Go 1.27, Make and git. To run it, you need git and a coding agent on the path.

```bash
make build                                   # write bin/obee
mkdir -p ~/.config/obee/projects
cp examples/local/project.star ~/.config/obee/projects/demo.star
# Set source in demo.star to a git repository on this machine.

obee doctor                                  # check the machine
obee item add demo fix-login --title "Fix the login" --body-file task.md
obee run fix-login demo --workflow examples/local/workflow.star
obee status
```

The result is a branch named `fix-login` in the source repository.

## Examples

| Example | What it does | Needs |
|---|---|---|
| `examples/local` | An item becomes a reviewed branch in a local repository | git |
| `examples/research` | A question becomes a report file with notes and sources, not code | `curl` |
| `examples/swarm` | A job splits into tasks, one agent works on each, and one gate run lands the pull requests | git, `gh` |
| `examples/remediation` | A backlog of issues becomes reviewed, merged pull requests | git, `gh`, beads |

Each example folder has a `workflow.star` with comments that name the variables it reads.

## Claude Code plugin

`plugins/claude-code` adds five slash commands to Claude Code: `/obee:queue`, `/obee:run`, `/obee:status`, `/obee:answer` and `/obee:cancel`. Each one runs an obee command. To try it, run `claude --plugin-dir plugins/claude-code`. `docs/claude-code-plugin.md` has the install steps.

## Inspired by Archon

[Archon](https://github.com/coleam00/Archon) showed that a coding agent works better inside a workflow than inside one long prompt. I used it before I wrote obee. obee keeps that idea and makes two different choices.

**The workflow language is Starlark, not YAML.** Starlark has functions, loops and conditions in the language itself. A workflow can bound a review loop with `for _ in range(3)` and read `review.verdict` as a value. obee checks the file before the run, and a workflow has no template strings to fill in.

**The engine is a library, not custom code.** A durable run needs a record of each step, a resume after a crash and a queue. Archon builds its own (about 72,000 lines at commit `72817c5`, October 2026). obee uses [DBOS Transact](https://github.com/dbos-inc/dbos-transact-golang), an existing engine for durable workflows on SQLite. obee's own code is the step kinds and the rules, not the engine.

| | Archon | obee |
|---|---|---|
| Workflow files | YAML | Starlark, checked before the run |
| Durable runs | Its own engine | DBOS Transact, a durable workflow library |
| What you install | A Bun app with SQLite or PostgreSQL | One static binary |
| Server and telemetry | A web server and PostHog telemetry, both on by default | None |
| A web interface | Yes | No. A terminal interface is planned |
| Agents | Claude Code and Codex | Claude Code today. Codex and Copilot are planned |

## Status

obee is under construction, and the workflow file format can still change.

| Works today | Planned |
|---|---|
| The Starlark workflow files and `obee check workflow` | A step that opens a pull request, and stacked pull requests |
| The `agent`, `command`, `gate`, `wait`, `merge`, `batch_merge`, `spawn` and `gather` calls, with `stdin`, `env` and `stop_on_rc` on `command` | A terminal interface |
| Durable runs that resume after a crash | Codex, Copilot and other agents |
| Local items, beads and GitHub issues | |
| A pinned merge with review, thread and check guards | |

`docs/architecture.md` describes the packages, their layers and the durable loop.

## Build

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate
make build    # write bin/obee
```

## License

MIT. See `LICENSE`.
