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

`examples/remediation` takes issues from beads and GitHub to reviewed pull requests.

`examples/local` needs no tracker and no forge. The items are in `local.db`, and the result is a branch in a git repository on this machine. Copy `examples/local/project.star` to `<config folder>/projects/<name>.star`, set `source`, and run:

```bash
obee item add <name> fix-login --title "Fix the login" --body-file task.md
obee run fix-login <name> --workflow examples/local/workflow.star
```

## Claude Code plugin

`plugins/claude-code` adds five slash commands to Claude Code: `/obee:queue`, `/obee:run`, `/obee:status`, `/obee:answer` and `/obee:cancel`. Each one runs an obee command. To try it, run `claude --plugin-dir plugins/claude-code`. `docs/claude-code-plugin.md` has the install steps.

## Design

`docs/architecture.md` describes the packages, their layers and the durable loop. The loop runs on DBOS Transact Go with SQLite. The terminal screens use Bubble Tea.

## License

MIT. See `LICENSE`.
