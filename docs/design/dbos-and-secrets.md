# DBOS and secrets

Part of the [beehave architecture](../architecture.md).

The DBOS rules live in `.ai-rulez/rules/dbos-and-secrets.md`.

## DBOS and secrets

### The state folder

The state folder is created at 0700 under `umask 077` before DBOS opens `dbos.db`. A test checks the file modes.

### The DBOS config

`flow` builds the DBOS config with:

- an empty Conductor URL and key,
- no schedule,
- the JSON serializer,
- a pinned application version.

`flow` refuses to start if any `DBOS__*` variable is set.

### Logs and step errors

A custom `slog` logger writes to a 0600 file and redacts every record when it writes it.

DBOS stores the text of a step error as it is. So each step error passes through the same redactor before it returns to DBOS.

### Workflow data

No secret passes through a workflow input, a step output, an event or a message.

### Retention

At each start, `beehave run` deletes the finished runs that are older than `agents.retention_days` (default 7), with their phases. It uses `DeleteWorkflows`.

- With `agents.retention_days = 0`, it deletes none.
- Failed and cancelled runs stay, because `beehave resume` can reopen them.
- A kept run is the record that `earlier_calls` counts.

### Dependencies

Two `depguard` rules apply:

- The enterprise DBOS module is never imported.
- DBOS is imported only in `flow`.

`make deps` checks that `go list -deps ./...` has no `go.dbos.dev` or viper.

DBOS links `crypto/tls` through its Postgres driver. It links `net/http` through an enterprise entitlement check that runs only when auto-upgrade is on. So both are allowed in dependencies.

- The `depguard` rule `no-http-client` keeps `net/http` out of beehave's own code.
- `TestRun_OpensNoNetworkSocket` proves that `beehave` opens no network connection.
- cobra is allowed: its core imports neither.

`make vuln` runs govulncheck. `go.mod` pins `toolchain go1.27.3` or later.
