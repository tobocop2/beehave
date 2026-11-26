# Contributing

## Build

beehave needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/beehave only
make remedy   # write bin/remedy, the tool of the remediation example
make test-examples   # run the tests of the examples folder
```

## macOS code

`make check` runs on Linux and cannot compile the macOS code. A change under `internal/sandbox/seatbelt` must pass `make check-darwin` on a Mac before it merges. That target runs the Seatbelt tests and the contract suite under the real `sandbox-exec`.

## Release checklist

Run `make test-real-tools BEEHAVE_REAL_ROUND=1` and read it green before you tag. The command runs the sandbox round on the sandbox repository `tobocop2/beehave-real-tools`.

The command installs the pinned gh 2.101.0, bd 1.3.2 and claude 2.1.285. Set `GH_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN` for the rows that need them. A row without its token skips and names the token.

The gh contract in `internal/ghcontract` holds the fields and flags of gh 2.101.0. `make check` tests every call of beehave against it without a token. When you change the gh pin, run the real-tools suite: it fails when the table differs from the new gh. Then refresh the table and the checksum in `scripts/install-real-tools.sh`.
