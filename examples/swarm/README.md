# Swarm

A swarm runs one backlog with many agents at once. Each line of the parent item becomes a task item. The parent starts the tasks by priority, no more than the free slots. It lands the finished pull requests with a merge queue that takes the highest priority first.

## How it runs

- **Backlog.** Each line of the body of the parent item is a priority (`P0` to `P3`), a tab, a title, and optionally a tab and a body. A bad line refuses the whole backlog.
- **Priority at the door.** The parent calls `queue`, sorts the ready items by priority, and calls `spawn` for no more than `queue.free` items. `agents(max_concurrent)` in `runners.star` sets the slots.
- **Merge queue.** Finished tasks wait until `BATCH` pull requests are ready or no agent runs. Then one `batch_merge` runs for each priority, `P0` first, so a high priority lands before a low one and one gate run covers each union.
- **Conflicts.** A pull request that conflicts gets a fix item, at most twice. The fix agent merges the base into the branch and pushes it. After two tries, or for any other reason, the item gets a comment and the status `hold`.
- **Limits.** `CAP_USD` stops new runs when the ledger reaches it. A file `$HOME/.swarm/<swarm>/STOP` stops new runs. `DEADLINE_MIN` stops new runs after that many minutes. A limit never stops a run that already started.

## Files

- `workflow.star`: the parent and the two child entries, `task` and `resolve`.
- `project.star`: the project file to copy to `projects/<name>.star`. The tracker must accept new items. The forge is required.
- `scripts/ledger.sh`: the spend ledger (`add`, `check`, `report`).
- `prompts/task.md`, `prompts/resolve.md`: the prompts of the two entries.

## Variables

`REPO`, `BASE`, `OWNER_LOGIN`, `OWNER_EMAIL`, `SWARM` (the id of the parent item), `SWARM_DIR` (the absolute path of this folder), `CAP_USD`. Optional: `REMOTE` (the default is the GitHub address of `REPO`, so a private repository needs credentials for `git ls-remote`), `DEADLINE_MIN`, `BATCH`.

## Engine gaps

Each gap is true on main now.

- **A swarm cap needs a ledger.** The results of `gather` carry no cost, and a spawned run has its own sum. The example writes the cost of each agent step to `scripts/ledger.sh` and reads the total before each pass. `docs/architecture.md` says that a cap for an item tree or a swarm is not covered.
- **The clock is text.** `now()` returns an RFC 3339 string. The deadline parses it in Starlark.
- **One priority for each call.** `batch_merge` folds its members in the order of the item id, so priority needs one call for each tier.
- **An item has no priority.** `item_create` takes no severity or priority argument, and `queue` sorts rows by severity. The example keeps the priority of each item in its own map and sorts the ready items itself.
- **The branch read is a command.** The parent has no worktree, so it reads the branch of a task with `git ls-remote` in a `command` step.
