# A planner and its workers. Each worker publishes where it is, and the planner reads that without waiting for the worker to end (part 1).
# The planner waits for the first of a worker end, a "stall" message and the end of CHECK_IN (part 2).
# It cancels each stalled worker and gives its task to a new worker, and one deadline bounds the new workers (part 3).
# Run: TASKS="21 22" beehest run <item> <project> --workflow workflow.star
# TASKS lists the item ids of the workers, split by spaces. CHECK_IN and DEADLINE are durations, such as "10m".
# To stall a worker by hand, run: beehest send <item> --topic stall 22. The text is the item id of the worker.

KEY = "progress"
MODEL = "sonnet"
PEEKS = 60
MAX_EVENTS = 100
PAUSE = "1"
CHECK_IN = env("CHECK_IN", default = "10m")
DEADLINE = env("DEADLINE", default = "1h")

def worker(item):
    """One worker run. It publishes a progress value before and after its agent step."""
    publish(KEY, {"stage": "started"}, name = "started")
    work = agent(name = "work", prompt = "prompts/work.md", model = MODEL, edits = False, sandbox = sandbox(), inputs = {"item": item})
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

    command(name = "peeked", run = ["echo", "while the workers run:", stages(seen)], sandbox = sandbox())

    # Part 2: each turn the planner waits for the first of three events: a worker ends, a user sends the message "stall" with the item id of a worker, or
    # CHECK_IN passes with no event. A stall message stalls that worker. At the end of CHECK_IN every worker that is still open is stalled.
    open_runs = [r for r in runs.runs]
    ended = []
    stalled = []
    for turn in range(MAX_EVENTS):
        if not open_runs:
            break
        event = first_of(runs = open_runs, topics = ["stall"], timeout = CHECK_IN, name = "watch-%d" % turn)
        if event.kind == "run":
            ended += [e.outcome for e in event.runs]
            gone = [e.item for e in event.runs]
            open_runs = [r for r in open_runs if r.item not in gone]
        elif event.kind == "message":
            hit = [r for r in open_runs if r.item == event.message]
            stalled += hit
            open_runs = [r for r in open_runs if r.item != event.message]
            for run in hit:
                cancel(run, name = "named-" + run.item)
        else:
            stalled += open_runs
            for run in open_runs:
                cancel(run, name = "stalled-" + run.item)
            open_runs = []

    # Part 3: the planner gives the task of each stalled worker to a new worker.
    again = spawn(items = [run.item for run in stalled], entry = "worker", on_open = "replace", name = "reassign")

    # One deadline bounds the new workers. At the deadline the planner cancels each one that is still open.
    late = gather(again, timeout = DEADLINE, on_timeout = "cancel", name = "deadline").results
    done = sorted(ended, key = lambda one: one.item) + late

    final = progress(runs, KEY, name = "final")
    cost = 0.0
    for one in done:
        cost += one.cost_usd
    command(name = "report", run = ["echo", "when the workers ended:", stages(final), outcomes(done), "cost_usd=%s" % cost])
