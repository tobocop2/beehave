# A planner and its workers, part 1: each worker publishes where it is, and the planner reads that without waiting for the worker to end.
# Run: TASKS="21 22" beehave run <item> <project> --workflow workflow.star
# TASKS lists the item ids of the workers, split by spaces.

KEY = "progress"
MODEL = "sonnet"
PEEKS = 60
PAUSE = "1"

def worker(item):
    """One worker run. It publishes a progress value before and after its agent step."""
    publish(KEY, {"stage": "started"}, name = "started")
    work = agent(name = "work", prompt = "prompts/work.md", model = MODEL, edits = False, inputs = {"item": item})
    publish(KEY, {"stage": "done", "cost": work.cost_usd}, name = "done")
    return {"stage": "done"}

def stages(seen):
    """The stage of each worker as text, such as 21=started 22=started. A worker that published nothing shows as none."""
    return " ".join(["%s=%s" % (one.item, one.value["stage"] if one.set else "none") for one in seen])

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

    done = gather(runs, name = "all")
    final = progress(runs, KEY, name = "final")
    cost = 0.0
    for one in done.results:
        cost += one.cost_usd
    command(name = "report", run = ["echo", "when the workers ended:", stages(final), "cost_usd=%s" % cost])
