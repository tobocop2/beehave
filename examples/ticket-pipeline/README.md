# Ticket pipeline

A pipeline that loads one partition for each day. Each day, the pipeline reads the support tickets of that day from a source, an agent labels each ticket (billing, bug, how-to, account, other, unsure), a check validates the labels, and a load writes the day to a warehouse.

The warehouse is sqlite. Any SQL warehouse takes its place. The ticket source (`SOURCE_API`) is the one part that this example does not ship.

## What it covers

- **Backfill.** `backfill.star` takes an item whose body is `FROM TO [BUDGET_CENTS]`, for example `2025-01-01 2025-12-31 20000`. `scripts/plan.sh` lists the days of the range that the warehouse does not hold at the current version. The limit is `MAX_DAYS` in `version.star`.
- **Chunks.** The plan goes in chunks of `CHUNK` days. One chunk is one item and one child run that loads its days one after the other. The run of the backfill records a few steps for each chunk and none for a day. A child run records the steps of `CHUNK` days. The history of a run does not grow with the size of the range.
- **Retry of one failed day.** A day that fails, is not closed at the source or has odd labels is left out of the chunk, with a line in the report of the chunk. Add an item whose body is `{"day": "2026-01-31"}` and run it with `partition.star`. A day with odd labels then asks a person (`wait` with the choices `load` and `skip`).
- **Re-run of a range.** Start a new item for the same range. The plan holds only the days that are not loaded at the current version.
- **Idempotent load.** `scripts/load.sh` replaces a whole day in one transaction and reads it back. If the day is loaded at the same version with the same input, the load changes nothing. If the load fails, the transaction rolls back and the earlier rows stay.
- **Cost budget.** Before each day, `scripts/budget.sh` reserves the estimated cost of the day in one SQL statement. If the budget cannot cover the day, the chunk stops. The backfill then waits for a person: answer the cents to add, such as `500`, or `stop`. The wait has the deadline `BUDGET_TIMEOUT` (default 24h). A reservation stays if the day then fails, so a retry of that day reserves again and the budget counts it twice.
- **Change of the version in the middle of a backfill.** Cancel the item, set `PIPE_VERSION` (for example `PIPE_VERSION=3`) in the environment of the engine, and start a new item for the same range. The plan selects the days that are below the new version, and the load replaces their rows.
- **Daily run.** `scripts/daily.sh` starts the backfill for yesterday. Call it from cron or launchd.

## Files

```
version.star        VERSION (from PIPE_VERSION), CHUNK, MAX_DAYS
backfill.star       the range: plan, chunks, budget wait, report (entry chunk = one chunk of days)
partition.star      one day from source to warehouse; also the workflow that retries one day
prompts/classify.md the instructions of the agent
schema/             the allowed labels and the warehouse tables
scripts/            every call to the outside world
config/projects/pipeline.star   copy to <config folder>/projects/pipeline.star
```

The data folder (`PIPE_HOME`, default `~/.pipe`, mode 700) is outside the repository. It holds `config` (`SOURCE_API`, `COST_CENTS_PER_100`, `UNSURE_MAX_PCT`, `DRIFT_MAX_POINTS`), `credential.header` (the header line that the source needs), `warehouse.db` and `stage/<day>/`.

## How to run it

1. Make the data folder with the files `config` and `credential.header`.
2. Copy `config/projects/pipeline.star` to the config folder of beehave.
3. Set `PIPE_DIR` to the absolute path of this folder and `PIPE_HOME` to the data folder. Programs need `sqlite3`, `jq` and `curl`.
4. Add the item: `beehave item add pipeline bf-2025 --title 'Backfill 2025' --body-file body.txt`.
5. Run it: `beehave run bf-2025 pipeline --workflow backfill.star`.
6. If the run waits for the budget, `beehave status` shows the question. Answer with `beehave answer bf-2025 500`.

Tests need Go and the beehave source: `go test ./cmd/beehave -run TestScripts/pipe`. The scripts `pipe-*.txt` in `cmd/beehave/testdata/e2e` run the example with a stand-in for the source and a fake agent.

## Engine gaps

The example needs no engine change. These points need one to be better, and the example does not work around them with shell:

- **No bulk item creation.** `item_create` makes one item for each call, and each call is a recorded step of the parent. The example makes one item for each chunk to keep the history small. A `spawn` of a plan (a list of keys with bodies) would let one day be one child run with no step in the parent.
- **No cap for a tree of runs.** `agents.max_run_budget_usd` is a real cap for one run. It does not count the children of a run. The agent step returns `cost_usd`, so a workflow can add the real cost in Starlark. The SQL estimate is a choice: `budget.sh` reserves the cost of a day before the call, so the budget stops a day before it spends.
- **No structured stop.** `run_chunks` finds a budget stop with the text of the failure (`BUDGET`). The engine gives no coded stop reason.
- **No run of a range as a schedule.** Cron starts the daily run. The engine has no timer that starts a workflow.
