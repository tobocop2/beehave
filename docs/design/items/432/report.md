# Item 432, design lane: report

Measured at the branch tip a9148468 (branch `432`) and at the base 74f5f1bf (`origin/integration/r1`, local ref).
The fetch of `origin` was refused by the sandbox (github.com is not on the allowlist), so the base ref may be stale.
`internal/config/files.go`, `internal/config/projects.go` and `internal/model/host.go` are byte-equal at both commits.

## Before writing

- Pull requests of the item: `pulls` in the data file is `[]`. Checked at the start and again before writing the plan.
- Repeat-offender check: `git log -S'trailing dot' --all` finds no commit for this item. `git log -S clusterName` finds
  1954655e, d9883a81 and 1c7ce504 (#368, which added dots). There is no closed pull request.
- Review notes of #368: not read. The forge is not reachable from this host (`~/.config/gh` is denied, github.com is not on the allowlist). OWED.
- Build-versus-buy: the check is one RE2 pattern from the standard library `regexp`. RE2 has no lookahead, but the rule fits a
  plain pattern, `^[A-Za-z](?:[A-Za-z0-9-]|\.[A-Za-z0-9])*$`. The control below measured it. No dependency in `go.mod` checks a host name.
- Tempting wrong fix: three `strings` checks added after the pattern in `hostName`, and a refusal text left unchanged.
  That puts a second judgement beside the pattern, and the text no longer describes what it refuses. The fix belongs in
  the one pattern `clusterName`, which every name already passes through. A second wrong fix is to tighten `link.wordPattern`.
  That pattern also checks addresses and users, and the issue does not name it.
- Skill `blast-radius-review`: the Skill tool answered `Unknown skill`. Its rule in `.ai-rulez/rules/review-and-smells.md:57-62`
  was followed: the sibling entry points are listed under Siblings.

## Measurements

| run | tree | mode | inputs | cells | red | differ from base | rc |
|---|---|---|---|---|---|---|---|
| base | 74f5f1bf | regex (full could not build) | 19628 | 19628 | 529 (trailing-dot 316, `..` 83, `.-` 130) | 0 | 1 |
| tip | a9148468 | regex | 19628 | 19628 | 529 (same) | 0 | 1 |
| control: candidate fix pattern | base + pattern swap | regex | 19628 | 19628 | 0 | 529 (named classes only) | 0 |
| control: over-strict mutant `^[A-Za-z][A-Za-z0-9]*$` | base + pattern swap | regex | 19628 | 19628 | 807 (valid 485, open classes 322) | 1336 | 1 |
| full mode | base | full | n/a | n/a | n/a | n/a | go test setup failed: proxy.golang.org is not on the allowlist, and no module cache can be read |

Every run included the instrument control: `"gpu"` accepted and `"a b"` refused.
The "differ from base 0" at the base is the control that the oracle matches the code at the base.
Absence check of named forms in the shipped `build_host` values, with a planted `"a."` control in the same call: only the planted line matched.

Discarded arm: full mode with a private `GOMODCACHE` in `$TMPDIR`. It cannot download, so it never ran.
OWED: run `MODE=full GATE=1 acceptance.sh` on the build host, at the base, to get the form×context rows and the gate at the base.

## Claims

| claim | evidence | head |
|---|---|---|
| The base accepts a trailing dot, `..` and `.-` | result-base.txt: 529 red rows, all in the named classes | 74f5f1bf, a9148468 |
| The base check is one pattern, used only by `hostName` | `grep -rn clusterName internal cmd` gives files.go:34 and projects.go:395 | a9148468 |
| The run-time host choice checks membership only | internal/model/host.go:17-31 `slices.Contains(declared, cluster)` | a9148468 |
| The harness turns green on a correct fix and red on an over-strict one | control rows above | base + swap |
| No shipped `build_host` value uses a named form | absence check with planted control | a9148468 |

## Hunks

No production hunk: this is a design lane. Files added: `docs/design/items/432/{acceptance.sh,gen.go.in,accept_test.go.in,regex_main.go.in,rulings.md,result-base.txt,report.md}`.
The `.go.in` files are not Go files to the toolchain. `go list ./...`, vet and lint do not see them.

## Inputs

| input class | base | head (expected) | test |
|---|---|---|---|
| valid name (letters, digits, single dots between labels, hyphens) | acc | acc | rows `valid` |
| `local` as string | acc | acc | row `"local"` |
| `local` in a list | ref | ref | full mode list rows |
| trailing dot | acc | ref | rows `named:trailing-dot` (316) |
| `..` | acc | ref | rows `named:dotdot` (83) |
| `.-` | acc | ref | rows `named:dotdash` (130) |
| trailing hyphen, `-.`, `--` | acc | acc (open question 1) | rows `open:*` |
| leading non-letter, `_`, space, empty | ref | ref | rows `base-refused` |
| each form in string / list-first / list-later × skypilot / ssh / slurm | as above | as above | full mode (OWED run) |

## Siblings

| site | same behavior? | changed |
|---|---|---|
| `internal/host/link/link.go:80` `wordPattern` (ssh target, address, user) | accepts all three forms | no (owner question 3) |
| `internal/host/skypilot/skypilot.go:29` `wordPattern` (cluster name to `sky`) | accepts all three forms | no (owner question 3) |
| `internal/host/slurm/parse.go:28` `nodePattern` | parses node names that slurm prints, not config input | no |
| `model.Project.HostFor` (run-time `--host` choice) | membership in the config list | inherits the config rule |

Regressions open at the head planned from: 0.

## Plan pass (step plan_231), measured at af7c1352

- Pull requests of the item: `pulls` in the data file is `[]` (read with `jq` at the start and again before this note).
- Repeat-offender: `git log -S'trailing dot' --all` gives only af7c1352 (this design lane). No closed pull request.
- Cited sites re-derived at af7c1352: `internal/config/files.go:34` pattern `^[A-Za-z][A-Za-z0-9.-]*$`, `projects.go:389-424`
  `hostName`/`buildHost`, test table `TestLoadFolder_BuildHostRefusals` at `buildhosts_test.go:25-49`. `git diff a9148468 af7c1352 -- internal cmd` is empty.
- Regex mode at af7c1352 (engine logs `acc-pre.log`, `acc-post.log`, summary lines only): rc 1, inputs 19628, cells 19628,
  red 529 (trailing-dot 316, `..` 83, `.-` 130), differ from base 0, control `"gpu"` acc, `"a b"` ref.
- Full mode retried twice: (1) default toolchain: `go: download go1.27.4 ... operation not permitted` (module cache denied);
  (2) `GOTOOLCHAIN=local GOMODCACHE=$TMPDIR/gomod`: `go: downloading github.com/stretchr/testify` and then
  `deny network-outbound proxy.golang.org:443`. The session allowlist cannot be widened. So the form×context rows and the gate at the base are still OWED on the build host.
- Skill `blast-radius-review`: Skill tool answered `Unknown skill` again. Rule `.ai-rulez/rules/review-and-smells.md` "Cross-cutting changes"
  followed: the sibling entry points (`link.wordPattern`, `skypilot.wordPattern`, `HostFor`) are in Siblings, and owner question 3 stands open.
- Absence check of named forms in `build_host` values (grep for `build_host = ...` with `."`, `..` or `.-`) with a planted `build_host = "a."` control in the same call:
  control 1 hit; tree 2 hits, both error-text strings (`projects.go:637`, `projects_source_test.go:139`) where the sentence ends in `."`, not values.
- Review notes of #368: still not read (forge not reachable). OWED.

Regressions open at the head planned from (af7c1352): 0.
