---
type: Decision
title: Go Code Style
x-ai-rulez:
  kind: rule
  id: go-code-style
  metadata:
    priority: high
---

# Go code style

## Objects and composition

- Model a thing with a struct and methods. Use composition and embedding, never a type hierarchy.
- Declare a small interface (1 to 5 methods) in the package that uses it. Do not declare it next to the implementation. Accept interfaces, return concrete types.
- Add an interface only when a consumer needs a seam for a fake or a second implementation.
- A constructor `NewX(deps...)` takes every dependency as a parameter. It does not read globals, the environment or the clock.
- One composition root, `cmd/beehave/main.go`, builds the object graph once and passes it down.

## Functions

- Keep a function to about 20 lines (`funlen` fails at 20 lines or 15 statements), cyclomatic complexity 10, two levels of nesting. Extract a named helper when it grows.
- `context.Context` is the first parameter of every function that blocks, does I/O or starts work. Never store a context in a struct.
- Use a `map` or a `switch` on a typed constant for dispatch. Do not write repeated `if` chains.
- No naked returns. No positional magic indexes; use named constants or `range`.

## Imports

- Imports sit at the top of the file in three groups: standard library, third party, then `github.com/tobocop2/beehave` (`goimports` with `local-prefixes` sorts them).
- No dot imports. A blank import is allowed only for a driver that registers itself, with a comment that says which (`revive` enforces both).
- Keep start-up cheap by not doing work in package scope.
- A type assertion uses the two-value form (`errcheck` enforces it) and a comment that says why the assertion is needed (untyped payload, SDK value).

## Names and constants

- Name constants for magic numbers and magic strings. Search the repo for the raw value before you add a constant.
- A closed set of values is a named type with constants (`type Verdict string`), a parser that rejects unknown input, and `switch` statements that the `exhaustive` linter checks. A `kind string` parameter for a closed set is a defect.
- Keep package-level constants together and functions together. Do not interleave them.
- Descriptive names: `pendingLanes`, not `cur`.

## Errors

- Wrap with context: `fmt.Errorf("claim %s: %w", id, err)`. Check with `errors.Is` and `errors.As`. Never compare error strings.
- Define sentinel errors as `var ErrX = errors.New(...)` and typed errors as structs. Do not add `panic` for expected failures.
- Handle an error once: return it or log it, not both.
- An error message is for the user. Name the problem and the fix. Do not leak internal names such as "dispatch" or "resolve".
- No bare `_ =` on an error without a comment that says why it is safe.

## Logging and output

- Use `log/slog`. Pass a `*slog.Logger` in. Do not call `slog.Default()` in library code.
- `fmt.Print*`, `os.Exit`, `panic` and `os.Stdout`/`os.Stderr` live only in `internal/cli` and `cmd/beehave` (`forbidigo` enforces this). Other packages write to an `io.Writer` they receive.

## Text

- No em dashes. No divider comments (`// ------`).
- A comment states what or a constraint, in one line. Default to no comment. Doc comments describe what the code is, not how it got there.
- Design rationale lives in `docs/architecture.md` and the topic files under `docs/design/`, not in code comments. A package comment is 3 lines or fewer.
- Use literal unicode characters, not escapes, and name them as constants.
