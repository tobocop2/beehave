# Runtime checks

Part of the [beehave architecture](../architecture.md).

This part covers the lane ledger, the pick of a runner, the machine check (`beehave doctor`), the pull request check before an action, the review poll and the scope check. The ledger and the scope check are tools of the remediation example. The others are part of the engine.

## Ledger

`ledger` is a tool of the remediation example, in `examples/remediation/tools/ledger`. It is not part of the engine: no file under `internal/` or `cmd/beehave/` names it. The command is `remedy ledger`.

### Storage

`ledger` keeps one row for each lane of each pull request. The rows are a SQLite file in the state folder, with mode 0600. There are two tables:

- One table holds the lanes.
- A second table records the issues that the first review of a key closes.

The time of a row is kept to the minute, in UTC.

`ledger` imports `examples/remediation/tools/check` and the SQLite driver `modernc.org/sqlite`. The print lines of a command line are not here: `Add` returns what it did, and the command prints it.

### Writes

`Add` writes in one `BEGIN IMMEDIATE` transaction. So two writers never lose a row, and a reader sees a whole transaction or none of it.

- An `Add` waits up to 30 seconds for another writer, then refuses with the locked error.
- A read that meets the journal of a crashed writer rolls it back with a read-write connection. In a folder that cannot be written, it refuses.
- `Add` records a lane once. A key that has a row for the lane is left as it is.
- A request with no time is refused.

### Closed issues of the first review

The first review of a key also records the issue numbers that the pull request closes, in the same transaction as the lane row. The numbers come from the `Closing` function of the request. It is read before the transaction starts. The first writer to commit the closes of a key wins.

The closing-keyword pattern spells each letter in both cases, because Go case folding maps the long s to `s`.

### Queries

A fix lane is a fix, a rework or a relay.

- `Rounds` counts the fix and rework rows on one mechanism since the last design row.
- `Since` returns whole days from the latest row of a kind.

### The state folder

The state folder is a flag. Every `remedy ledger` verb and `remedy scope` take `--state <folder>`. `remedy admit` takes the same flag. The ledger file is `lane-ledger.db` in that folder.

The tool does not guess the folder from the working folder, and it reads no environment variable for it. The workflow passes the folder that it keeps for the run.

### Commands

`remedy ledger add <key> <lane> <kind>` records a lane:

- A fix lane takes `--mechanism`, `--model` and `--round`.
- An `owner-add` row takes the issue number as the lane, and `--words`.
- The first review of a key needs the issues that the pull request closes. `--body-file <path>` takes the closing keywords of a file. `--pr <number>` reads them through the forge of the `origin` remote with the read of `remedy scope`, and only when the ledger asks for it.
- The time of the row is the time of the run.
- The verb prints the issues that a first review recorded.

`remedy ledger count <key> <kinds>`, `rounds <key> <mechanism>`, `prior <key> <lane>` and `closes <key>` print the answer of the ledger.

A refusal of the ledger and a failed read are exit code 2.

## Runner pool

`pool` picks the runner of an agent that has room.

### The pick

A runner has room when both windows are under their caps from `config.Pool`. The pick order is:

1. the lowest seven-day use,
2. then the lowest five-hour use,
3. then the first runner in config order.

A runner whose usage is unknown is never picked.

### Busy logins

The engine gives `Select` the logins that have an agent step running or queued. The pick prefers a runner that is not among them. When every runner with room is busy, the pick is the same as without the list.

The engine reads those logins from the open phases of the `agents` queue and from the open runs that a spawn or the queue started. Those runs hold their login from the start. A run that `beehave run` started holds its login only while one of its phases is open. Between phases its login reads free, and a second step on it waits for the per-login limit.

### No runner has room

When runners exist and none has room, `Select` returns `NoneQualifiesError`. The error holds the earliest reset time that the pool knows, and `ResetAt` returns it. The run does not move to another agent.

The refusal ends with "Wait for it." only when a reset time is known or no runner needs a login.

A reset time prints with its offset and, when it has a fraction, six digits of it (`05:00:00.500000+00:00`).

### The caps and `beehave run`

`beehave run` never refuses a run because of the caps, and it exits with no code for them. The run starts. A run with no agent step never reads the usage.

The first `agent` step that names no `runner` picks the login, as a recorded step named `login#<n>`.

| `Select` result | What the step does |
|---|---|
| `NoneQualifiesError` with a reset time | Sleeps with the durable sleep of DBOS until that time, then picks again as a new recorded step. A reset time in the past waits one minute. |
| `NoneQualifiesError` with no reset time | Fails with the same text as before. `beehave resume` picks again. |

`beehave run --over-cap` changes only the case where no runner has room. Then the step takes the runner with the lowest use, from `SelectOverCap`, and its record holds `over_cap` true. A runner under the caps is taken as usual. A runner with unknown usage is never taken.

### Reading usage

`pool` reads no usage. It takes a `Reader` function value and reads all runners at the same time, so the whole read takes one timeout. A reader that panics gives an unknown reading and leaves the others.

The window types `Window`, `Usage` and `Reading` live in `model`, because the reader is a layer 1 package and layer 1 cannot import `pool`. A `Reading` holds usage or a reason. Its zero value is unknown.

### The Claude reader

`agent/claude` has the reader. It starts the CLI with stream-json in the folder `/`, sends one `get_usage` request and stops at its answer. The CLI gets the allowlisted variables and the login of the runner only (`agent.LoginEnv`), never a token.

The read has three limits:

- 30 seconds from the context,
- 4 MB of output,
- one process group that dies at the end.

The reader maps a reply to a reading as follows:

| Reply | Reading |
|---|---|
| The CLI cannot start, exits without an answer, or the reply has no usable window | Unknown, with the reason |
| A utilization outside 0 to 100 | Unknown |
| A window with the value null | Not open now: 0 percent with no reset time |
| A window that is missing, or is not an object | Unknown |
| A success reply with `rate_limits_available` false | Unknown, and needs a login (see below) |
| A success reply with no `rate_limits` and no such flag | Unknown |
| A reply line that is not valid JSON, such as one with `NaN` | Skipped |

A success reply with `rate_limits_available` false has no Claude plan limits: the profile is not logged in, or it uses an API key. Its reason names the runner and gives the command `CLAUDE_CONFIG_DIR=<dir> claude`, then `/login`.

## Doctor

`beehave doctor` checks that the machine can run beehave. It prints one line for each check: `ok`, or `FAIL` with the fix. It exits 1 when any check fails.

### Checks

The checks are a table of a name and a function that returns an error, the fix, or nil. The programs come from the projects:

- `git` always,
- `bd` when a project names beads for the tracker or the claims,
- `gh` when a project names GitHub for the tracker or the forge.

The checks run in this order:

1. The config folder loads.
2. `git`, `bd` and `gh` are on the path.
3. The bd version is new enough. This check runs only when `bd` is on the path. A version error with no text is still a failure.
4. For each Claude runner with a `config_dir`:
   - the folder exists,
   - the usage read shows plan limits (a runner with no login fails with the login command),
   - its token is readable, when it has a Keychain entry.

### Wiring

The doctor command lives in `cli`. `cli` does not import a backend. `BuildInfo.Doctor` holds two function values that `cmd/beehave` fills:

- the Keychain reader, which runs `security` and never returns the token. A call of `security` stops after 10 seconds.
- the bd version check of the beads adapter.

Both are required. The command refuses a `Doctor` with a nil value and runs no check.

## Preflight

`preflight` asks whether the pull request that a caller is about to act on is still the one it expects. It reads the pull request once and its head branch once, through a two-method `Reader` that the forge fills, and returns a `Result`.

### Stops

`Stop` is a closed set with a `Code`:

| Code | Stop |
|---|---|
| 0 | unchanged |
| 2 | refused |
| 3 | merged |
| 4 | closed |
| 5 | head moved |
| 6 | base changed |
| 7 | branch gone |
| 8 | draft |
| 9 | held |

`Check` tests them in this order: merged, closed, branch gone, hold, draft, head, base. A merged pull request still shows a head that looks fine, so the state comes first. A hold outranks a draft.

Each stop carries lines that name the problem and the recovery.

### Refusals and read errors

- The expected head must be a full 40-character SHA. Any other length is refused and never compared.
- A failed read is a `ReadError`.
- A pull request with no state, draft flag, head branch or label list is a `ReadError`, because a null would pass as an open and ready pull request.

Only a missing branch counts as branch gone. That is when the ref read answers HTTP 404 and a second read of the repository succeeds, because a missing repository gives the same 404. Any other failed read of the branch is a `ReadError`.

## Review poll

`review` answers one question for a reviewer: what needs me now? It declares the small interfaces it reads (`Reader`, `Own`, `Stacks`), and the forge fills them. It imports only `model`.

### Poll rows

`Poll` returns one `Row` for each thing that needs an answer.

A pull request is read once when others ask us to review it, when we reviewed it, or when it has other activity. It gets a kind from our last verdict: `NEW`, `CURRENT`, `WAITING`, `FOLLOW-UP` or `RE-REVIEW`.

- A `CURRENT` or `WAITING` pull request with a reply after our verdict is `REPLIED`.
- A review request on a `CURRENT` pull request makes it `RE-REVIEW`.
- A comment, a dismissed review and a draft review are not a verdict.

The poll also lists:

| Row | Meaning |
|---|---|
| `REPLY` | An issue or pull request where others spoke after us |
| `CI-RED`, `CI-NOT-RUN` | Our ready pull requests with a failed check |
| `OWN` | Our pull requests with a fresh review, an open thread or a standing change request |

A search that fills its limit is an error, because the list may be cut off.

### The verdict ledger in JSON

A row with a verdict of ours is also a ledger in the JSON output:

| Field | Meaning |
|---|---|
| `ours` | The state |
| `ours_at` | The commit that the review read |
| `ours_when` | The time it was submitted |
| `still_head` | True when that commit is the head |

The poll derives the ledger again each time and stores nothing.

### Sweep

`Sweep` lists our open pull requests with their change requests, open threads and base branch. A base branch that is not the integration branch gets a flag.

The sweep and the `OWN` rows read the forge once, through `OwnPulls`. A pull request with a list longer than one page is read again with the per-pull-request reads, so the batched answer and the single reads give the same rows. A list that the single read also refuses for length prints `?` in the sweep and `unknown` in the poll.

A failed read stops the poll with an `UnreadError` that names the read and its cause. A page of unknown rows would hide the cause.

## Scope

`scope` is a tool of the remediation example, in `examples/remediation/tools/scope`. It refuses a pull request that started to close a new issue after its first review.

### Command

The command is `remedy scope <pr> --state <folder> [--key <key>]`. It reads the issues that the pull request closes and the ledger in the state folder. `--key` names the ledger key when it is not the pull request number.

- A pass is exit code 0.
- A refusal, a bad argument and a failed read are exit code 2.

### How the closed issues are read

| Pull request | Read |
|---|---|
| Into the default branch, GitHub forge | The `closingIssuesReferences` field of GraphQL. The list holds the issues linked in the sidebar as well as the ones named by keywords in the body. |
| Into any other branch (such as an integration branch) | The closing keywords of the body |
| A forge with no such read | The closing keywords of the body, in every case |

GitHub links a closing keyword only for a pull request into the default branch ([linking a pull request to an issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)).

More than 100 issues is a refusal. The first review row of the ledger records its issues with this same read.

For a pull request into the default branch, one GraphQL read gives the list, the base branch and the default branch:

- A base branch or default branch that the answer lacks is a refusal.
- The forge keeps an issue when its repository is the base repository of the same answer. It refuses when it drops every issue of a non-empty list.

For the closing keywords of the body, a keyword must start at an ASCII word boundary, a rule of this tool. So `unfixed #3` closes nothing. Two spellings of one number, such as `12` and `012`, name one issue.

### The check

The first review of a key records the issues that the pull request closes. `Check` reads the issues now and compares the two lists.

`Check` reads the forge only when the ledger holds a first review for the key. With no first review, it fixes the scope under the key and passes.

`Check` imports `ledger`. It reads the ledger and the forge through the small `Records` and `Reader` interfaces, and returns a `Result` with an exit code and the lines to show.

### Owner-added issues

A new issue passes only when the ledger holds an `owner-add` row for the key and the issue. The lane column of that row is the issue number. The words column is the reason of the owner.

The refusal names the command that adds the row: `remedy ledger add <key> <issue> owner-add --state <folder> --words <words>`.

An `owner-add` row never fixes a lane, so `Count`, `Prior`, `Rounds` and `Since` do not see it.
