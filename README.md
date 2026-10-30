# obee

obee takes a backlog of issues to reviewed, gated pull requests with any coding agent. Implementer agents build and prove the change. Reviewer agents check it at a pinned commit. obee merges only the head that was reviewed.

## Status

obee is under construction. The command line tool builds and prints its version. The packages land one milestone at a time; `docs/architecture.md` lists them.

## Build

obee needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/obee
```

## Examples

`examples/remediation` takes issues from beads and GitHub to reviewed pull requests. Set `MERGE` to `true` to let the workflow merge an approved pull request through the merge guards. With any other value, the owner merges. A run fails if `MERGE` is not set. Set `EXPECT_HEAD` to the full SHA the owner reviewed, and the run stops before any agent starts if the head of the pull request moved. When `EXPECT_HEAD` is not set, no head is checked. A stop of this kind shows in the result of the run as `stopped`, with the step, the exit code and the reason. Set `LAND` to `id=sha` pairs, split by spaces, and the run starts no agent. Each `sha` is the full SHA the owner reviewed for that item. The run applies the stops of one item to each item, then lands the items that pass with one batch merge: one gate run, the same guards, and a split of the union if the gate is red. An item that stops, or whose head moved since the review, gets its reason as a comment and the hold status, and the other items still land. In that mode, `MERGE` works as for one item: a run fails if `MERGE` is not set, and a `MERGE` that is not `true` lands nothing.

`examples/local` needs no tracker and no forge. The items are in `local.db`, and the result is a branch in a git repository on this machine. Copy `examples/local/project.star` to `<config folder>/projects/<name>.star`, set `source`, and run:

```bash
obee item add <name> fix-login --title "Fix the login" --body-file task.md
obee run fix-login <name> --workflow examples/local/workflow.star
```

`examples/research` answers a question with notes and sources instead of code. It needs no tracker and no forge, and the result is a report file. Set `RESEARCH_TOOLS` to the absolute path of `examples/research/tools` and `RESEARCH_OUT` to the folder for the report.

`examples/swarm` splits a job into tasks, runs one agent for each task, and lands the pull requests with one `batch_merge` and one gate run. Put one task on each line of the body of the parent item. Set `REPO`, `BASE`, `OWNER_LOGIN` and `OWNER_EMAIL` to name your repository and your identity. A pull request that the gate or a guard rejects gets a comment and the status `hold`.

## Claude Code plugin

`plugins/claude-code` adds five slash commands to Claude Code: `/obee:queue`, `/obee:run`, `/obee:status`, `/obee:answer` and `/obee:cancel`. Each one runs an obee command. To try it, run `claude --plugin-dir plugins/claude-code`. `docs/claude-code-plugin.md` has the install steps.

## Design

`docs/architecture.md` describes the packages, their layers and the durable loop. The loop runs on DBOS Transact Go with SQLite. The terminal screens use Bubble Tea.

## License

MIT. See `LICENSE`.
