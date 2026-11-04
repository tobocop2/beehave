---
type: Decision
title: Testing
x-ai-rulez:
  kind: rule
  id: testing
  metadata:
    priority: high
---

# Testing

- 100% statement coverage per package, enforced by `go-test-coverage` in `make coverage` (after `make test`). Only `cmd/beehave/main.go` is excluded; the smoke step in `make check` runs the built binary. Never lower the threshold.
- Write the test before or with the code. Every exported function has a test. Every test asserts something. A test that cannot fail is a defect: break the code and watch it go red.
- Use table-driven tests with named cases and `t.Run`. Name a case for the behavior (`rejects_unknown_key`), not a number.
- Use `t.Parallel()` where the test shares nothing. Use `t.TempDir()`, `t.Setenv`, `t.Cleanup` and `t.Helper()`. Never write to a real path or the real home folder.
- Fakes implement the consumer's small interface. Do not mock with a framework. Run with `-race`.
- Tests must run without a live `gh`, `bd` or agent. Put a fake binary first on `PATH` for adapter tests. A test that starts a real agent CLI is forbidden.
- A probe that turns on tracing or telemetry runs only in a container with no network.
- Integration tests test a real multi-package flow, not a mocked unit repeated. Mark one with a build tag name `integration` and a stated reason.
- Name tests for behavior: `TestExecute_PrintsVersion`, not `TestExecute3`.
- Run the tests you changed while you work (`go test ./internal/x/ -run Name`). Run the full gate once at the end. Kill a test that hangs past 2 minutes and find the hang.
- Before you delete code, make the tests pass with the new code, then delete the redundant tests. Never delete tests and code in one step.
- A mutation run (`make mutation`) is outside the gate. Run it on a package whose tests you doubt.
- Do not use `-run` filters, `t.Skip` or build tags to hide a failing test.

## TUI tests

- One Bubble Tea model per screen. Send `tea.Msg` values through `teatest` and assert on the final model or output. Do not call `Update` internals in tests of a screen.
- Styles live in one file. All user-facing text lives in one `messages` file. Inline strings in a view are forbidden.
- Reuse the confirm dialog model for yes/no questions. Do not write a second one.
