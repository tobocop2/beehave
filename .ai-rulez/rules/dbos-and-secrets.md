---
type: Decision
title: Dbos And Secrets
x-ai-rulez:
  kind: rule
  id: dbos-and-secrets
  metadata:
    priority: critical
---

# DBOS configuration and secrets

DBOS Transact Go stores workflow state in SQLite. Set it up as follows. Pin the DBOS version in `go.mod` and keep `go.sum`.

## Enforced now

- `depguard` denies `go.dbos.dev/dbos-enterprise-go` everywhere. It denies `github.com/dbos-inc/dbos-transact-golang` outside `internal/flow`.
- `make deps` runs `go list -deps ./...` and fails if the list has `go.dbos.dev`, `mvdan.cc/sh/v3/interp`, `github.com/spf13/viper`, `net/http` or `crypto/tls`. cobra is allowed: it is the CLI framework, and its core imports neither `net/http` nor `crypto/tls`. The DBOS security report names cobra as a dependency to avoid; this rule keeps the part of that rule that closes the network and drops the rest.
- `make vuln` runs `govulncheck`. A reachable finding fails the gate. `go.mod` pins `toolchain go1.27.3` or later for the standard-library fixes.

## Process rules (each waits for a test in the PR that adds `internal/flow`)

- Set `umask 077` and create the state folder with `os.MkdirAll(stateDir, 0o700)` before `dbos.NewContext`. A test checks the modes of `dbos.db`, `-wal` and `-shm`.
- Pass an absolute `sqlite:` URL inside that folder. Leave `ConductorURL` and `ConductorAPIKey` empty, so DBOS makes no outgoing connection. Register no schedule. Use the default JSON serializer.
- Set `ApplicationVersion` to a string obee owns, not the binary hash. Set `ExecutorID` to `local`. Set `EnablePatching` to false. Set `SkipMigrations` to false, except for a read-only inspector that must not create the file.
- At start, refuse to start if any `DBOS__*` variable is set, and refuse for `DBOS_DOMAIN` and `DBOS_SYSTEM_DATABASE_URL`. The report lists `DBOS__CLOUD`, `DBOS__CONDUCTOR_APP_NAME`, `DBOS__CONDUCTOR_KEY`, `DBOS__CONDUCTOR_URL`, `DBOS__APPVERSION`, `DBOS__VMID` and `DBOS__APPID`. A test sets each and expects the refusal.
- Do not run the `dbos` CLI against the state file.

## Secrets (prose only until a test can check them)

- Never pass or return a secret through a workflow input, a step output, an event, a message or an error text. A secret stays in the process that reads it from the environment or the keychain.
- Pass the DBOS logger a `*slog.Logger` that writes to a 0600 file at level INFO or higher and redacts step errors. Wrap each step error with a scrubbed message before it returns to DBOS. A test feeds a step error that holds a token and checks the log file.
- Delete finished workflows with `DeleteWorkflows`. The pause window is the retention.
