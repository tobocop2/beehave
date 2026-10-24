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

## Design

`docs/architecture.md` describes the packages, their layers and the durable loop. The loop runs on DBOS Transact Go with SQLite. The terminal screens use Bubble Tea.

## License

MIT. See `LICENSE`.
