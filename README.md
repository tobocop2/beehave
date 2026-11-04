# beehave

beehave takes a backlog of issues to reviewed, gated pull requests with any coding agent. Implementer agents build and prove the change. Reviewer agents check it at a pinned commit. beehave merges only the head that was reviewed.

## Status

beehave is under construction. The command line tool builds and prints its version. The packages land one milestone at a time; `docs/architecture.md` lists them.

## Build

beehave needs Go 1.27 and Make.

```bash
make setup    # install the Go tools and generate the agent files
make check    # the full gate, the same as CI
make build    # write bin/beehave
```

`make check` runs on Linux and cannot compile the macOS code. A change under `internal/sandbox/seatbelt` must pass `make check-darwin` on a Mac before it merges. That target runs the Seatbelt tests and the contract suite under the real `sandbox-exec`.

## Examples

`examples/remediation` takes issues from beads and GitHub to reviewed pull requests. Set `MERGE` to `true` to let the workflow merge an approved pull request through the merge guards. With any other value, the owner merges. A run fails if `MERGE` is not set. Set `SOURCE_CHECKOUT` to the absolute path of the checkout of the owner: the implement, review and fix agents cannot write in it, and a git command there that is not a read is refused. A run that starts an agent fails if `SOURCE_CHECKOUT` is not set. Set `EXPECT_HEAD` to the full SHA the owner reviewed, and the run stops before any agent starts if the head of the pull request moved. When `EXPECT_HEAD` is not set, no head is checked. Set `LANE_KIND` to `papers` or `design` for a lane that changes no source path, and set `ALLOW_PATHS` to the globs it may change, split by spaces: the implement step and each fix step fail when they change any other path. The default `LANE_KIND` is `impl`, which has no path limit. A stop of this kind shows in the result of the run as `stopped`, with the step, the exit code and the reason. Set `LAND` to `id=sha` pairs, split by spaces, and the run starts no agent. Each `sha` is the full SHA the owner reviewed for that item. The run applies the stops of one item to each item, then lands the items that pass with one batch merge: one gate run, the same guards, and a split of the union if the gate is red. An item that stops, or whose head moved since the review, gets its reason as a comment and the hold status, and the other items still land. In that mode, `MERGE` works as for one item: a run fails if `MERGE` is not set, and a `MERGE` that is not `true` lands nothing.

`examples/local` needs no tracker and no forge. The items are in `local.db`, and the result is a branch in a git repository on this machine. Copy `examples/local/project.star` to `<config folder>/projects/<name>.star`, set `source`, and run:

```bash
beehave item add <name> fix-login --title "Fix the login" --body-file task.md
beehave run fix-login <name> --workflow examples/local/workflow.star
```

`beehave run` prints a line when the run starts and when each step starts and ends. To see a run later, list the runs, then name one:

```bash
beehave status
beehave status item-fix-login-r1
```

The first command prints one line for each run, starting with the run id. The second prints one line for each step of that run: the name, the start time, the duration, the outcome and the fields that decide the flow. The last line gives the state of the run and, for a failed run, its error. Add `--json` to the second command for one JSON object with the fields `run`, `state`, `error` and `steps`. `beehave run` writes its progress lines to stderr.

`examples/research` answers a question with notes and sources instead of code. It needs no tracker and no forge, and the result is a report file. Set `RESEARCH_TOOLS` to the absolute path of `examples/research/tools` and `RESEARCH_OUT` to the folder for the report.

`examples/incident` is an incident desk. An alert starts a run. An analyst agent proposes one mitigation from a runbook table, and the on-call roles approve it in order, each before a deadline. A safe mitigation runs when someone was paged and nobody answered. A risky one never does. If no page reaches a person, the desk holds the incident. The desk checks the health of the service and restores the earlier state if the check fails. Set `INCIDENT_DIR` to the absolute path of `examples/incident` and `OPS_API` to your ops API.

`examples/research-desk` turns a question into a cited report on a wiki. A planner splits the question into sections, one child run reads each section, and a script tests every quote against the page. A judge agent grades the claims and a person approves the draft before the publish. Set `RESEARCH_DESK_DIR` to the absolute path of the folder, `RESEARCH_HOME` to the folder of the store, `SEARCH_API` to a search server and `WIKI_API` to the page store. The review and the budget pause end with a failed run if nobody answers in time. Run `beehave resume <item>` and then `beehave run` again to continue at the same question.

`examples/ticket-pipeline` loads the support tickets of each day to a warehouse, with an agent that labels the tickets. It covers a backfill of many days in chunks, the retry of one day, a re-run of a range, an idempotent load, a cost budget that waits for a person, and a version change in the middle of a backfill. Set `PIPE_DIR` to the absolute path of the folder and `PIPE_HOME` to the data folder. The folder holds its own README with the engine gaps.

`examples/approvals` is an approval desk. A document needs sign-offs from roles, such as legal, security and product. A person signs each sign-off with an ssh key, and the desk checks the signature. A sign-off binds to one version of the document, and a new version makes the earlier sign-offs stale. The quorum is data: each role has members, a count and a required flag, and `min_roles` sets how many roles must be satisfied. A member can delegate to a person until a time. A run has a deadline, reminders and one extension. Every step is a row in an append-only SQLite ledger, and each row holds the hash of the row before it. `audit.star` checks the chain and the signatures. Set `APPROVALS_DIR` to the absolute path of `examples/approvals` and `NOTIFY_API` to your notifier.

`examples/swarm` runs one backlog with many agents at once. Put one task on each line of the body of the parent item: a priority (`P0` to `P3`), a tab and a title. The parent starts the tasks by priority, no more than the free slots, and lands the pull requests with one `batch_merge` for each priority, `P0` first. A conflict gets a fix run, at most twice, and then a person reads the item. Set `REPO`, `BASE`, `OWNER_LOGIN`, `OWNER_EMAIL`, `SWARM`, `SWARM_DIR` and `CAP_USD`. The folder holds its own README with the engine gaps.

`examples/maintenance` bumps the dependencies of a list of repositories once a week. A scheduler (launchd or cron) starts `scripts/tick.sh`, which skips a repository with nothing new, with the same updates as last week, or with a bump pull request still open, and runs `bump.star` for the others. The workflow updates, runs the gate, lets an agent fix the code for at most two rounds, and opens a draft pull request. If the tests stay red, the run waits for the answer `retry` or `hold`. A weekly report counts every outcome and posts once. Copy the folder to `~/.upkeep`, or set `UPKEEP_HOME`. The README of the folder lists the gaps of the engine.

`examples/queue` runs the open items of a project until no work remains. The parent reads `queue`, starts the ready items in the free slots, and starts the next item when any run ends. A failed item gets a comment and the status `hold`, and the loop goes on. Set `BASE` to the integration branch, and `OWNER_LOGIN` to your own login: each item reads the forge before its agent starts, and an open pull request by another login holds the item. One login runs one agent step at a time. The pick of a login prefers a login with no agent step running or queued, so the items spread over your logins. The parent run holds no login. With one login, the items run one at a time.

## Claude Code plugin

`plugins/claude-code` adds five slash commands to Claude Code: `/beehave:queue`, `/beehave:run`, `/beehave:status`, `/beehave:answer` and `/beehave:cancel`. Each one runs an beehave command. To try it, run `claude --plugin-dir plugins/claude-code`. `docs/claude-code-plugin.md` has the install steps.

## Design

`docs/architecture.md` describes the packages, their layers and the durable loop. The loop runs on DBOS Transact Go with SQLite. The terminal screens use Bubble Tea.

## License

MIT. See `LICENSE`.
