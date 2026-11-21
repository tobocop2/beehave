# Planner

A planner starts one worker run for each task. Each worker tells the planner how far it got, and the planner reads that while the workers run. A worker that stalls is canceled, and its task goes to a new worker.

This is part 1 and part 3. Part 1 reads progress. Part 3 cancels a stalled worker and bounds the new workers with a deadline. Part 2 adds the wait for the first event.

## How it runs

- **Spawn.** The planner calls `spawn` with the item ids of the variable `TASKS`. Each item runs the function `worker`.
- **Publish.** A worker calls `publish("progress", value)` before and after its agent step. A second call with the same key replaces the value.
- **Progress.** The planner calls `progress(runs, "progress")`. The call does not wait. A worker that has not published yet reads `set = False` and `value = None`. The planner asks again after a pause, until each worker has published once.
- **Check-in.** The planner waits for the workers until `CHECK_IN` with `gather(timeout = CHECK_IN, on_timeout = "return")`. A worker that ended keeps its outcome. A worker that is still open has `open = True`.
- **Reassign.** The planner cancels each open worker with `cancel`, and starts a new worker for its task. A canceled worker is ended for good, so `spawn` starts the next attempt of the item.
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

- **A stall is a missed check-in.** The planner treats a worker that is still open at the check-in as stalled. It does not yet use the progress that the worker published to decide.
