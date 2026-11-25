# Swarm

A swarm runs one backlog with many agents at once. Each line of the parent item becomes a task item. The parent starts the tasks by priority, no more than the free slots. It lands the finished pull requests with a merge queue that takes the highest priority first.

## How it runs

- **Backlog.** Each line of the body of the parent item is a priority (`P0` to `P3`), a tab, a title, and optionally a tab and a body. A bad line refuses the whole backlog.
- **Priority at the door.** The parent calls `queue`, sorts the ready items by priority, and calls `spawn` for no more than `queue.free` items. `agents(max_concurrent)` in `runners.star` sets the slots. If a run ended and the parent has not gathered it, nothing starts in that pass. The parent gathers the run first. If a waiting pull request has a higher priority than the first ready item, the parent lands it at once, even if the batch is not full and other agents run. So a fix item that the landing files starts before any task of lower priority.
- **Merge queue.** Finished tasks wait until `BATCH` pull requests are ready or no agent runs, or until a waiting pull request outranks the ready items. Then one `merge` runs for each priority, `P0` first, so a high priority lands before a low one and one gate run covers each union.
- **Deferred landing.** If the forge defers a pull request in an early landing, the parent does not land it early again. The next regular landing (full batch or no agent runs) tries it again.
- **Conflicts.** A pull request that conflicts gets a fix item, at most twice. The fix agent merges the base into the branch and pushes it. After two tries, or for any other reason, the item gets a comment and the status `hold`.
- **Limits.** The setting `agents.max_tree_budget_usd` caps the dollars of the whole swarm. At the cap, the engine holds each agent step that has not started, so the swarm waits until you raise the cap and restart beehave. A file `$HOME/.swarm/<swarm>/STOP` stops new runs. `DEADLINE_MIN` stops new runs after that many minutes. `STOP` and `DEADLINE_MIN` never stop a run that already started. The cap never stops an agent step that already started.

## Files

- `workflow.star`: the parent and the two child entries, `task` and `resolve`.
- `project.star`: the project file to copy to `projects/<name>.star`. The tracker must accept new items. The forge is required.
- `prompts/task.md`, `prompts/resolve.md`: the prompts of the two entries.

## Variables

`REPO`, `BASE`, `OWNER_LOGIN`, `OWNER_EMAIL`, `SWARM` (the id of the parent item). Optional: `REMOTE` (the default is the GitHub address of `REPO`, so a private repository needs credentials for `git ls-remote`), `DEADLINE_MIN`, `BATCH`.

## Credentials and the base

`project.star` has no `credentials` key, so `merge` keeps the guards of a project with no writer. The base needs no ruleset. If the base requires `beehave/gate` or `beehave/review`, `merge` refuses and says that the project needs credentials.

When the project gets a `credentials` entry for the `writer` role, `merge` refuses unless the base requires a pull request and the statuses `beehave/gate` and `beehave/review`. Post the ruleset first (docs/design/trust.md, section "The ruleset"). The ruleset file names `integration/**`, so change its branch pattern to the value of `BASE`. A later change will remove the no-credentials path, and this example then needs both the key and the ruleset.

## Engine gaps

Each gap is true on main now.

- **The clock is text.** `now()` returns an RFC 3339 string. The deadline parses it in Starlark.
- **One priority for each call.** `merge` folds its members in the order of the item id, so priority needs one call for each tier.
- **An item has no priority.** `item_create` takes no severity or priority argument, and `queue` sorts rows by severity. The example keeps the priority of each item in its own map and sorts the ready items itself.
- **The branch read is a command.** The parent has no worktree, so it reads the branch of a task with `git ls-remote` in a `command` step.
