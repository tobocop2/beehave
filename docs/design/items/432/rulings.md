# Item 432: rulings and expected behavior

Rule of the item: the config check refuses a `build_host` name that ends with a dot, holds `..` or holds `.-`.
Nothing else changes. The rows below come from the issue and from the base (74f5f1bf, `origin/integration/r1`).

## Paths every later lane uses

- Harness: `docs/design/items/432/acceptance.sh` (`gen.go.in` is the generator and the oracle, `accept_test.go.in`
  is the full mode, `regex_main.go.in` is the regex mode).
  Run it as `MODE=full docs/design/items/432/acceptance.sh <tree>`. Add `GATE=1` to run `make check` after the rows.
- Build of the base: `git archive 74f5f1bf | tar -x -C "$TMPDIR/432-base-74f5f1bf"`, then
  `MODE=full docs/design/items/432/acceptance.sh "$TMPDIR/432-base-74f5f1bf"`.
- Recorded base result: `docs/design/items/432/result-base.txt` (regex mode, exit 1, 529 red rows).

## The code path

There is one site. `internal/config/files.go:34` `clusterName`, called from `hostName` (`internal/config/projects.go:389-404`),
and `buildHost` (`projects.go:407-424`) runs it on each name of the string or the list. The run-time choice of a host
(`model.Project.HostFor`, `internal/model/host.go:17`, from `cmd/beehave/flow.go:308` and `:325`, and `cmd/beehave/gates.go:183`)
checks only that the name is one the config declares, so it takes the new rule from the config check.

## Expected behavior

Forms: `string` is `build_host = "<s>"`, `list-first` is `["<s>", "gpu-z"]`, `list-later` is `["gpu-z", "<s>"]`.
Contexts: each form inside a `host` block of `skypilot`, `ssh` and `slurm`. A string `"local"` has no host block.
Every context has the same value as the skypilot row. The full mode prints one row for each cell (input × form × context).

| input | class | string base → head | list-first base → head | list-later base → head |
|---|---|---|---|---|
| `gpu`, `gpu-b`, `a.b`, `a-b`, `a.b.c`, `beehave-rig.us-central1-a.lance-bench-8389` | valid | acc → acc | acc → acc | acc → acc |
| `local` | valid | acc → acc | ref → ref (local with other hosts) | ref → ref |
| `a.`, `gpu.`, `a.b.`, `local.`, `beehave-rig….lance-bench-8389.` | trailing dot (named) | acc → **ref** | acc → **ref** | acc → **ref** |
| `a..b`, `gpu..b`, `a...b`, `a..` | `..` (named) | acc → **ref** | acc → **ref** | acc → **ref** |
| `a.-b`, `gpu.-b`, `a.-` | `.-` (named) | acc → **ref** | acc → **ref** | acc → **ref** |
| `a-` | trailing hyphen (not named) | acc → acc | acc → acc | acc → acc |
| `a-.b` | `-.` (not named) | acc → acc | acc → acc | acc → acc |
| `a--b` | `--` (not named) | acc → acc | acc → acc | acc → acc |
| `.a`, `-a`, `7a`, `a_b`, `a b`, `""` | refused at base | ref → ref | ref → ref | ref → ref |
| every generated string of length ≤ 5 over `a Z 7 . - _ space` | from the oracle | oracle base → oracle head | same | same |

The text of a refusal of a named form is not named by the issue. Two list cells change only the first problem they
print: `["local", "a."]` and `["a.", "a."]` are refused at base by the local rule and the twice rule, and at head by the
name rule, because `hostName` runs on every name before those rules (`projects.go:413-421`). Both stay refused.

## Owner questions (open; the harness keeps the base value)

1. A trailing hyphen (`a-`), `-.` (`a-.b`) and `--` are not valid DNS labels either. The issue names only the three
   dot forms, so the head value of these cells is the base value (accept). Should they be refused too?
2. The refusal text says "(a letter, then letters, digits, dots and hyphens)". A refused `a.` meets that description.
   The project rule says an error names the problem and the fix. Must the head text name the new rule?
3. Two sibling patterns accept the same three forms: `internal/host/link/link.go:80` `wordPattern` and
   `internal/host/skypilot/skypilot.go:29` `wordPattern`. The issue names the config check only, so they keep their base value.
