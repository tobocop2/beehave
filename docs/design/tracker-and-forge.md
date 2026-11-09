# Tracker, claims and forge

Part of the [beehave architecture](../architecture.md).

This part describes the tracker backends and their writes, the claim policy, the claim leases in beads, the forge adapter and the shared command runner.

## Store and claim

`store` holds the `Tracker` and `ClaimRows` contracts and the decode helpers.

### Backends

| Backend | Reaches | Notes |
|---|---|---|
| `store/beads` | `bd --json` | The CLI is the contract |
| `store/github` | GitHub issues through `gh` | The CLI is the contract. It holds no lease |
| `store/local` | SQLite | It starts no program |

For `store/beads` and `store/github`, each payload decodes once into a named struct. A payload of another shape is a refusal that names the command. Unknown fields are kept, because `bd` and `gh` send many fields the code does not read.

### The Writer contract

`store.Writer` is a second contract beside `Tracker`. A workflow can change a tracker that implements it.

| Method | Meaning |
|---|---|
| `Create(key, item)` | Files an item. It also takes an optional assignee. |
| `SetStatus(id, status)` | Sets `open`, `closed` or `hold`. In progress is the claim and is not a status. |
| `Comment(id, key, text)` | Adds a comment. |
| `Assign(id, assignee)` | Sets one assignee. It is safe to repeat. |

The assignee, in `Create` and in `Assign`:

- The `github` adapter uses `--assignee` and `--add-assignee`.
- The `beads` adapter uses `--assignee` and `bd update --assignee`.
- The `local` adapter refuses both.

A tracker with no `Writer` stays valid. A workflow that writes to it fails with an error that names the tracker.

### Keys make writes safe to repeat

`Create` and `Comment` are safe to repeat for one key. The key is the id of the run, a colon and the `key` of the call, so two runs never share a key. The adapters keep the key in the data of the tracker, and look it up before they write.

| Adapter | Create | Comment | SetStatus |
|---|---|---|---|
| `local` | A row in `item_keys` for each key, filed in the same `BEGIN IMMEDIATE` transaction as the item. | A row in `comments` with the primary key project, item and key. | An update of the state. `hold` shows as `Hold` in `OpenItems`. `closed` leaves the list. |
| `github` | The line `beehave-key: <key>` ends the body. `gh issue list --state all --search` finds it first, and the adapter matches the whole line. The search index of GitHub can lag. When the search finds nothing, a second `gh issue list --state all --limit 100` with no `--search` reads the newest issues, and the adapter matches the whole line again. A parent is `--parent`, which needs gh 2.101 or newer. | The same line ends the comment. The adapter first reads every comment of the issue, with `gh api --paginate --slurp` on the comments of the issue (https://cli.github.com/manual/gh_api). | `gh issue view` reads the state and labels. Only the needed command runs: `close`, `reopen`, or `edit` with the `hold` label. The repository must already have a label named `hold`. gh does not create it, and `hold` without it is a refusal that names the label. |
| `beads` | The line `beehave-key: <key>` ends the description. `bd list --all --desc-contains` finds it first. `bd create --silent` prints the new id. | The same line ends the comment, after `--`, so that text which starts with `-` is not read as a flag. `bd comments <id> --json` is read first. | `bd show` reads the status and labels. Only the needed command runs: `close`, `reopen`, or `update` with the `hold` label. |

The engine records each call as a step, so a replay does not write again. The key covers the one gap that the step record cannot: a crash between the write and the record.

### The claim package

`claim` holds the claim policy and no command line:

- the read of the winner,
- one reclaim of an ended lease,
- the refusal of a live holder.

It declares the small `Rows` interface it needs and does not import `store`. Beads holds one claim row for each item.

## Claims in beads

A project with `claims = "beads"` keeps its claims in beads, whatever tracker the project uses. Beads has native claim leases and works across machines. GitHub labels have no write that tests the old value, and no lease. For these projects, bd grants, renews and ends the lease.

A project with `claims = "local"` (the default) keeps its claim rows in `local.db` and needs no `bd`.

### Lease operations

- **Row.** A forge number has no beads row, so each item gets a task titled `lock:<kind>-<item>`. A row left with a stale assignee or a closed status is reset to open first, guarded by the values that were read.
- **Take.** `bd update <row> --claim --actor <holder>` claims an open row with no assignee in one write and grants the lease to the actor. The holder is the actor and the assignee. A refused take exits 1 with the text `issue already claimed` or `issue not claimable`. The adapter reads that exit as a lost take, and `claim` reads the row to learn the winner. Any other exit 1 is an error.
- **Beat.** `bd heartbeat <row> --actor <holder>` renews the lease of each row of the holder.
  - A row that fails does not stop the renewal of the other rows: the errors join.
  - A holder with no row gets the text that the claim is gone.
  - A row that another holder took after the list gets the same text, because bd refuses the heartbeat with `issue already claimed`.
  - Any other failure is an error.
  - The lease lasts 5 minutes, and bd has no flag to change it.
- **Reclaim.** `bd reclaim --id <row> --older-than 0s` frees a row only when its lease ended. bd ends no lease by itself: a late beat before the reclaim revives the lease. A row set to `in_progress` by hand has no lease, and bd never reclaims it.
- **Release.** `bd unclaim <row> --if-assignee <holder>` sets the status back to open and ends the lease. A mismatch exits 1 with the text `assignee mismatch`, and `claim` refuses with code 3. A closed row holds nothing and counts as free.
- **Version.** The leases and the guards exist from bd 1.3.2. The claim store asks `bd --version` once before its first write, and refuses an older bd before it writes anything. `beehave check config` runs the same check.

### One dolt server for each project

The lease table of bd is node-local: it is not committed to Dolt and not replicated. All the clients of one `dolt sql-server` see the same leases, and a second server has its own. Keep one server for each project.

### The claim rules

- A holder is live while the end of its lease is after the clock.
- The end comes from bd and the clock from beehave. A clock skew between the host of bd and the host of beehave can make `EnsureFree` and `Who` disagree with `Take`. `EnsureFree` only prechecks, and `bd reclaim` decides.
- A holder with no lease never expires, because bd never reclaims it.
- A read that fails, does not parse or fills a page is an error, and `claim` turns it into a refusal.
- `Take` is bd's atomic claim. When another holder wins, `claim` runs one reclaim, then claims again, then refuses with code 3.

### Shared helpers

Backends copy small helpers, such as `derefText` and the argument constants, rather than share them through the parent. A shared helper would add an export that only backends use.

### The fake bd and the contract test

The package `store/beads/beadstest` holds the fake `bd` that the tests of `store/beads` and `cmd/beehave` share, as `net/http/httptest` holds a fake server. The fake follows bd 1.3.2. The same package holds the helpers that start and stop the private dolt server of the real-bd contract tests, so `internal/store/beads` and `cmd/beehave` share one copy.

The contract test `TestLeaseContract` in `internal/store/beads` runs the same scenarios against the fake and against a real bd 1.3.2 on a private dolt server. The real run is off by default. To run it:

1. Set `BEEHAVE_BD_132` to the path of a bd 1.3.2 binary. Check its checksum against the release file.
2. Put `dolt` on `PATH`.
3. Run `go test ./internal/store/beads -run TestLeaseContract`.

The test starts its own dolt server on a free port, refuses ports 3306 and 3307, and stops the server at the end. It ends a lease with `dolt sql` instead of a wait of 5 minutes.

`cmd/beehave` runs `claim` over the real claim store, because only the composition root may import `store` and `claim` together.

## Forge and the command runner

`forge` holds the `Forge` contract. Its backend `forge/github` runs `gh` and has one decoder for each payload shape. The pull request, review, comment, thread and check types live in `model`, so layer 2 declares its own interfaces over them without importing `forge`.

### Writes

The writes are `PostComment`, `SetDraft`, `MergePR`, `CreateBranch`, `SetBase` and `SetBody`.

`MergePR` runs `gh pr merge --merge --match-head-commit`. It refuses a pin that is not a full commit id before `gh` runs.

`TokenScopes` reads `gh auth status --active`.

### Conversation methods

Seven methods read the conversation and move the pull request.

| Method | Does |
|---|---|
| `Comments` | Returns the conversation comments, all pages joined |
| `ReviewComments` | Returns the inline comments, all pages joined |
| `Search` | Returns the numbers of the open issues or pull requests that involve a user and changed since a date. It refuses an answer that fills its limit |
| `DefaultBranch` | Returns the default branch. It refuses a repository that names none |
| `BranchRef` | Returns the tip of a branch. A branch that does not exist gives `found` false and no error. Every other failed read is an error |
| `PostComment` | Adds a comment |
| `SetDraft` | Converts a pull request to a draft or marks it ready |

`forgetest` runs each method in its conformance suite. The suite checks that every method fails on a failed command.

### The command runner

`proc` holds the one command runner, the `Runner` interface. `proc/proctest` holds the scripted fake of it. `model.Decode` is the one JSON decoder for command answers. `store` and `forge` share them.

The runner starts each command in its own process group with a timeout. It reports these exit codes:

| Exit code | Meaning |
|---|---|
| 124 | A deadline |
| 127 | A missing command |
| 130 | A cancel |
