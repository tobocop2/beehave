# Planner

A planner starts one worker run for each task. Each worker tells the planner how far it got, and the planner reads that while the workers run.

This is part 1. The planner only reads progress. Parts 2 and 3 add the wait for the first event and the cancel of a stalled worker.

## How it runs

- **Spawn.** The planner calls `spawn` with the item ids of the variable `TASKS`. Each item runs the function `worker`.
- **Publish.** A worker calls `publish("progress", value)` before and after its agent step. A second call with the same key replaces the value.
- **Progress.** The planner calls `progress(runs, "progress")`. The call does not wait. A worker that has not published yet reads `set = False` and `value = None`. The planner asks again after a pause, until each worker has published once.
- **Gather.** The planner calls `gather`. Each result has `cost_usd`: the spend of the worker run and of every run below it.
- **Status.** While a worker runs, `beehave status <run id> --events` prints the keys and values that the worker published.

## Files

- `workflow.star`: the planner (`main`) and the worker entry.
- `prompts/work.md`: the prompt of the worker agent.

## Variables

`TASKS`: the item ids of the workers, split by spaces.
