# Planner

A planner starts one worker run for each task. Each worker tells the planner how far it got, and the planner reads that while the workers run. A worker that stalls is canceled, and its task goes to a new worker.

This has three parts. Part 1 reads progress. Part 2 waits for the first of a worker end, a `stall` message and the end of the check-in. Part 3 cancels a stalled worker and bounds the new workers with a deadline.

## How it runs

- **Spawn.** The planner calls `spawn` with the item ids of the variable `TASKS`. Each item runs the function `worker`.
- **Publish.** A worker calls `publish("progress", value)` before and after its agent step. A second call with the same key replaces the value.
- **Progress.** The planner calls `progress(runs, "progress")`. The call does not wait. A worker that has not published yet reads `set = False` and `value = None`. The planner asks again after a pause, until each worker has published once.
- **Watch.** The planner calls `first_of(runs = open_runs, topics = ["stall"], timeout = CHECK_IN)` in a loop. The call returns `kind = "run"` when a worker ended, `kind = "message"` when a message came on the topic `stall`, and `kind = "timer"` when `CHECK_IN` passed with no event. Each call starts the check-in again, so a worker end or a message gives the other workers a full `CHECK_IN` more. The field `runs` lists every worker that ended in the same read; each keeps its outcome and leaves the list of open workers.
- **Stall by hand.** Run `beehave send <item> --topic stall 22` to stall the worker of item 22. The planner cancels that worker at once. At `kind = "timer"` the planner cancels every worker that is still open.
- **Reassign.** The planner starts a new worker for the task of each stalled worker. A worker that is stalled was canceled with `cancel`. A canceled worker is ended for good, so `spawn` starts the next attempt of the item.
- **Deadline.** One `gather(timeout = DEADLINE, on_timeout = "cancel")` bounds the new workers. At the deadline the planner cancels each one that is still open, and its result reads `error = "deadline"`.
- **Report.** The last step prints the last stage of each first worker, each task with `ok` or its error, and the cost. Each result has `cost_usd`: the spend of the worker run and of every run below it.
- **Status.** While a worker runs, `beehave status <run id> --events` prints the keys and values that the worker published.

A restart of the planner keeps each deadline that it recorded: a parent that stops during a wait and starts again does not wait longer.

## Files

- `workflow.star`: the planner (`main`) and the worker entry.
- `prompts/work.md`: the prompt of the worker agent.

## Variables

`TASKS`: the item ids of the workers, split by spaces. Optional: `CHECK_IN` (default `10m`) and `DEADLINE` (default `1h`).

## Engine gaps

- **A stall is a missed check-in or a message.** The planner treats a worker as stalled when `CHECK_IN` passes with no event, or when a user sends the `stall` message. It does not yet use the progress that the worker published to decide.
