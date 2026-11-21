# A planner and its workers. Each worker publishes where it is, and the planner reads that without waiting for the worker to end (part 1).
# At the check-in the planner cancels each worker that is still open and gives its task to a new worker, and one deadline bounds the new workers (part 3).
# Run: TASKS="21 22" beehave run <item> <project> --workflow workflow.star
# TASKS lists the item ids of the workers, split by spaces. CHECK_IN and DEADLINE are durations, such as "10m".

KEY = "progress"
MODEL = "sonnet"
PEEKS = 60
PAUSE = "1"
CHECK_IN = env("CHECK_IN", default = "10m")
DEADLINE = env("DEADLINE", default = "1h")

def worker(item):
    """One worker run. It publishes a progress value before and after its agent step."""
    publish(KEY, {"stage": "started"}, name = "started")
    work = agent(name = "work", prompt = "prompts/work.md", model = MODEL, edits = False, inputs = {"item": item})
    publish(KEY, {"stage": "done", "cost": work.cost_usd}, name = "done")
    return {"stage": "done"}

def stages(seen):
    """The stage of each worker as text, such as 21=started 22=started. A worker that published nothing shows as none."""
    return " ".join(["%s=%s" % (one.item, one.value["stage"] if one.set else "none") for one in seen])

def outcomes(results):
    """The outcome of each task as text, such as 21:ok 22:deadline."""
    return " ".join([one.item + ":" + ("ok" if one.ok else one.error) for one in results])

def main(item):
    runs = spawn(items = env("TASKS").split(" "), entry = "worker", name = "workers")

    # progress does not block: the planner asks again, after a pause, until each worker has published once.
    seen = progress(runs, KEY, name = "peek")
    for _ in range(PEEKS):
        if all([one.set for one in seen]):
            break
        command(name = "pause", run = ["sleep", PAUSE])
        seen = progress(runs, KEY, name = "peek")

    command(name = "peeked", run = ["echo", "while the workers run:", stages(seen)])

    # A worker that is still open at the check-in is stalled. The planner cancels it and gives its task to a new worker.
    checked = gather(runs, timeout = CHECK_IN, on_timeout = "return", name = "check-in").results
    stalled = [runs.runs[i] for i in range(len(checked)) if checked[i].open]
    for run in stalled:
        cancel(run, name = "stalled-" + run.item)
    again = spawn(items = [run.item for run in stalled], entry = "worker", name = "reassign")

    # One deadline bounds the new workers. At the deadline the planner cancels each one that is still open.
    late = gather(again, timeout = DEADLINE, on_timeout = "cancel", name = "deadline").results
    done = [one for one in checked if not one.open] + late

    final = progress(runs, KEY, name = "final")
    cost = 0.0
    for one in done:
        cost += one.cost_usd
    command(name = "report", run = ["echo", "when the workers ended:", stages(final), outcomes(done), "cost_usd=%s" % cost])
