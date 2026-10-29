<div align="center">

# obee

[![Go 1.27](https://img.shields.io/badge/go-1.27-007ec6?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-007ec6)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/tobocop2/obee?style=social)](https://github.com/tobocop2/obee/stargazers)

**Your agents write the code. A program keeps the process.**

One static binary · Starlark workflows · Durable runs · Merges pinned to the reviewed commit · No server, no telemetry

[What you get](#what-you-get) · [Quick start](#quick-start) · [Examples](#examples) · [Status](#status)

</div>

> **Do not let an agent be the orchestrator.** Agents are good at writing and reviewing code. They are not reliable at keeping the rules of the process around it. In obee, the process is a file, and a program runs it.

## What you get

| | |
|---|---|
| **A process that cannot skip a step** | The workflow file is the process. A review that the file requires always runs, at the commit that passed the gate. |
| **Merges pinned to the review** | The merge uses the head that the gate and the review saw, and refuses a standing change request or an open thread. |
| **Runs that survive** | Every step is recorded. After a crash or a closed laptop, `obee resume` continues at the step that did not finish. |
| **Waiting costs nothing** | obee waits on a build, a timer or your answer with no model running. |
| **Workflows you can check** | `obee check workflow` finds a broken file, a wrong keyword or a bad value before any agent starts. |
| **One file to install** | No language runtime, no server, no web app. You need git and the command line tool of your agent. GitHub and an issue tracker are optional. |

## Quick start

```bash
make build                                   # write bin/obee (needs Go 1.27)
mkdir -p ~/.config/obee/projects
cp examples/local/project.star ~/.config/obee/projects/demo.star   # set source to a git repository

obee doctor
obee item add demo fix-login --title "Fix the login" --body-file task.md
obee run fix-login demo --workflow examples/local/workflow.star
```

The result is a reviewed branch named `fix-login` in the source repository.

## A workflow

```starlark
def main(item):
    agent(name = "implement", prompt = "prompts/implement.md", model = "sonnet", inputs = {"item": item})

    approved = False
    for _ in range(3):
        check = gate(name = "check")
        if check.passed:
            review = agent(name = "review", prompt = "prompts/review.md", model = "sonnet", pin = check.head, outputs = ["verdict"], inputs = {"item": item})
            approved = review.verdict == "approve"
        if approved:
            break
        agent(name = "fix", prompt = "prompts/fix.md", model = "sonnet", inputs = {"item": item})

    if approved:
        command(name = "push", run = ["git", "push", "origin", "HEAD:refs/heads/" + item.id])
```

The review runs at the head that passed the gate, and the loop stops after three passes.

## Examples

| Example | What it does |
|---|---|
| [`local`](examples/local) | An item becomes a reviewed branch. No tracker, no forge. |
| [`research`](examples/research) | A question becomes a report with notes and sources. |
| [`swarm`](examples/swarm) | One agent per task, then one gate run lands all the pull requests. |
| [`remediation`](examples/remediation) | A backlog of issues becomes reviewed, merged pull requests. |

`plugins/claude-code` adds `/obee:queue`, `/obee:run`, `/obee:status`, `/obee:answer` and `/obee:cancel` to Claude Code.

<details>
<summary><b>Why I built it: what went wrong when an agent was the orchestrator</b></summary>

<br>

| What happened | What it cost | What obee does instead |
|---|---|---|
| A cleanup woke 15 finished agents | 5.3 million tokens, no work done | An agent process ends with its step, so nothing is left to wake |
| An agent read its whole context again after each build | 47 million tokens | obee waits on the build. No model waits with it |
| A timer kept the orchestrator awake for ten hours | 34 million tokens | Waiting is a durable step, and it costs nothing |
| A context compaction erased the agent limits | Nine agents at once, usage limit hit twice | The limits are in the config, and code checks them |
| Three agents said that they ran a review | No review happened | The review is a step. obee runs it and records the verdict |
| A pull request merged 15 minutes after a request for changes | A merge against the maintainer | The merge step refuses a standing change request and an open thread |
| A review approved one commit, and a different commit merged | Unreviewed code on main | The merge is pinned to the head that the gate and the review saw |
| The orchestrator session hit its limit in the middle of a run | The work in progress was lost | obee resumes the run at the step where it stopped |

</details>

## Related projects

| Project | |
|---|---|
| [Archon](https://github.com/coleam00/Archon) [![stars](https://img.shields.io/github/stars/coleam00/Archon?style=social)](https://github.com/coleam00/Archon) | YAML workflows for coding agents. obee started from its idea. |
| [Gas Town](https://github.com/gastownhall/gastown) [![stars](https://img.shields.io/github/stars/gastownhall/gastown?style=social)](https://github.com/gastownhall/gastown) | Many agent sessions in tmux, with a merge queue. |
| [GitHub Agentic Workflows](https://github.com/github/gh-aw) [![stars](https://img.shields.io/github/stars/github/gh-aw?style=social)](https://github.com/github/gh-aw) | Agent steps inside GitHub Actions. |

obee differs in three ways: the process is code that a program runs, the merge is pinned to the reviewed commit, and it is one binary with no server.

## Status

obee is under construction, and the workflow format can still change. Claude Code works today. Codex, Copilot and a terminal interface are planned. `docs/architecture.md` describes the design.

## Build

```bash
make setup    # install the Go tools
make check    # the full gate
make build    # write bin/obee
```

## License

MIT
