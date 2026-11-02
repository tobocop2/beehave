# obee

**Stop babysitting your AI agents.**

obee runs them through your own process (plan, build, test, review) and comes to you only for the decisions that need a person.

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Go 1.27](https://img.shields.io/badge/go-1.27-00ADD8.svg)
![Status: under construction](https://img.shields.io/badge/status-under%20construction-orange.svg)

<!-- demo: terminal recording of a dry run, a kill and a resume -->

```bash
git clone https://github.com/tobocop2/obee && cd obee && make build   # writes bin/obee
```

## Runs survive a crash

Each step result goes into a local SQLite database. Kill obee at step 6 of 9, run the same `obee run` again, and the run continues at the step that was running. A finished agent step does not run again, so it does not bill again.

## The process is code

The process is one Starlark file with loops, gates, reviewers, waits for a person and cost caps. `obee check workflow` finds mistakes before a run. `obee check workflow --dry-run` walks the whole file over fakes and calls no agent, forge, tracker or host.

## Not only for code

The same engine runs research and desk workflows. `examples/research` answers a question with notes and sources. A project still needs a git repository today. Issue #245 removes that need.

## Quickstart

```bash
bin/obee check workflow examples/local/workflow.star --dry-run
mkdir -p ~/.config/obee/projects && cp examples/local/project.star ~/.config/obee/projects/myrepo.star   # set source to your repository
bin/obee item add myrepo fix-login --title "Fix the login" --body-file task.md
bin/obee run fix-login myrepo --workflow examples/local/workflow.star
```

The result is a branch named `fix-login` in your repository. If the process stops, run the same `obee run` again. Use `obee status` to list the runs and the questions that wait for you.

To work a whole backlog, use [`examples/queue`](examples/queue): it runs the open items of a project until no work remains.

## A workflow

This one plans, implements, then gates and reviews up to three times. It pushes only an approved commit. The reviewer reads the commit that the gate ran on (`pin = check.head`).

```starlark
# workflow.star
def main(item):
    plan = agent(name = "plan", model = "sonnet", prompt = "prompts/plan.md", inputs = {"item": item})
    work = agent(name = "implement", model = "sonnet", prompt = "prompts/implement.md", inputs = {"item": item, "plan": plan.text})

    approved = False
    for _ in range(3):
        check = gate(name = "check")  # runs the gate command of the project
        if check.passed:
            review = agent(name = "review", model = "sonnet", prompt = "prompts/review.md", pin = check.head, outputs = ["verdict"], inputs = {"item": item})
            approved = review.verdict == "approve"
            if approved:
                break
            findings = review.text
        else:
            findings = check.report
        agent(name = "fix", model = "sonnet", prompt = "prompts/fix.md", inputs = {"item": item, "findings": findings})

    if approved:
        command(name = "push", run = ["git", "push", "origin", "HEAD:refs/heads/" + item.id])
```

## Examples

| Folder | What it does |
|---|---|
| [`examples/local`](examples/local) | Implement, gate, review. One item, no tracker, no forge. The result is a branch. |
| [`examples/remediation`](examples/remediation) | Issues from beads and GitHub to reviewed pull requests, with the merge guards. |
| [`examples/research`](examples/research) | Answer a question with notes and sources. No code. |
| [`examples/queue`](examples/queue) | Run the open items of a project until no work remains. |
| [`examples/swarm`](examples/swarm) | Split a job into tasks, one agent for each, and land them with one batch merge. |

The settings of each example are in [`docs/examples.md`](docs/examples.md).

## Status

obee is private and under construction. The engine, the CLI, the examples and the merge guards exist today. Claude Code is the only agent today. OpenCode, pi, Hermes, Codex, Copilot and Gemini CLI are planned. Expect changes to the workflow API.

## Docs

- [`docs/examples.md`](docs/examples.md): the settings of each example.
- [`docs/architecture.md`](docs/architecture.md): the packages, the durable loop and the reasons for DBOS and Starlark.
- [`docs/claude-code-plugin.md`](docs/claude-code-plugin.md): the Claude Code plugin in `plugins/claude-code`, with `/obee:queue`, `/obee:run`, `/obee:status`, `/obee:answer` and `/obee:cancel`.

## Build

obee needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/obee
```

## License

MIT. See `LICENSE`.
