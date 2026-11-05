# Contributing

## Build

beehave needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/beehave
```

## macOS code

`make check` runs on Linux and cannot compile the macOS code. A change under `internal/sandbox/seatbelt` must pass `make check-darwin` on a Mac before it merges. That target runs the Seatbelt tests and the contract suite under the real `sandbox-exec`.
