# A queue loop: one parent item reads the open work of the project, starts a run for each ready item up to the free slots, and starts the next item when any run ends.
# Run it with `beehave run <item> --workflow workflow.star`, where the item is a parent that has no work of its own.
# Set OWNER_LOGIN before the run. It names your own login on the forge; an open pull request by another login claims the item.
# Set BASE before the run. It names the integration branch that receives the pull requests.
# Set PAUSE_AFTER_FAILURE to the time that the loop sleeps after a pass that saw a failed run, such as "2m". The default is "30s".
#
# Each pass does four things:
#   1. queue() of the library reads the items and the pull requests on BASE again. A merge in an earlier pass shows here, so an item that the merge unblocked is ready now.
#   2. spawn starts the ready items that fit in the free slots. A held item is never ready, and a failed item is never ready.
#   3. first_of(runs = live) waits until an open run ends, and its field `runs` lists every run that ended in the same read. The other runs keep their slots, so one item that loops does not stop the rest.
#   4. The loop removes the ended runs from its list, holds each failed item with a comment, and sleeps before it starts runs after a pass that saw a failure.
# The loop ends when no item is ready and no run is open.
#
# One login runs one agent step at a time on each queue: a step with edits = False can run beside a step that edits on the same login. The pick of a login prefers a login with no agent step running or queued, so the items spread over your logins. The parent run holds no login. With one login, the items run one at a time.
#
# This file is separate from examples/remediation because main is the entry of the parent and of a child alike.
# The child function `work` is the one place to put the pipeline of one item: replace its agent call with the steps of the remediation example.

load("@beehave//claims.star", "claim_stop")
load("@beehave//queue.star", "queue")

# The label of the forge that holds an item. Change it to the label that your repository uses.
HOLD = "hold"

# A bound for the loop, because Starlark has no while. A pass ends one run or starts one, so the bound is far above the work of any real queue.
MAX_PASSES = 500

def work(item):
    """Runs for each item as a child run. The agent works in its own work folder, pushes the branch that has the id of the item, and opens the pull request."""

    # The forge is read here, at the start of the item, so a pull request that was opened after the pass read the queue still stops the item.
    # An open pull request of a login that is not OWNER_LOGIN claims the item. The failure holds the item with the reason, and no agent starts.
    intake = preflight(name = "intake", base = env("BASE"))
    if intake.ok:
        intake = claim_stop(name = "intake", hold = HOLD, own = {"logins": [env("OWNER_LOGIN")]})
    if not intake.ok:
        fail(intake.reason)

    implement = agent(
        name = "implement",
        prompt = "prompts/task.md",
        model = "sonnet",
        forbid_commands = ["bulk-staging"],
        # The shell pushes the branch and opens the pull request, so it reaches the forge and nothing else. A step with no list has no network.
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "base": env("BASE")},
    )

    # A cap ended the run (the output `stop` is "turns", "budget" or "context"), so the pull request may be half done. The item fails with the reason.
    if implement.stop != "":
        fail("REFUSE: the implement step stopped at the %s cap before it finished. Raise the cap or split the item. A plain beehave resume replays this stop. To start the item again, run beehave resume %s, then beehave cancel %s, then beehave run %s." % (implement.stop, item.id, item.id, item.id))

def main(item):
    # The runs that this parent started and has not seen end.
    live = []

    # True after a pass that saw a failed run.
    pause = False

    for n in range(MAX_PASSES):
        q = queue(name = "q-%d" % n, hold = HOLD, base = env("BASE"))

        # An item that has a run is `running`, `done` or `failed`, so only `ready` rows start. The list `live` guards the same rule on this side.
        open = [r.item for r in live]
        take = [r.item for r in q.rows if r.state == "ready" and r.item not in open][:q.free]

        if len(take) == 0 and len(live) == 0:
            break

        # A failure often has a shared cause, such as a provider outage. A durable sleep keeps the loop from starting more runs into it, and a restart during the sleep waits only for the time left.
        # The loop sleeps only when it is about to start runs, so a loop that has nothing left to start ends at once.
        if pause and len(take) > 0:
            sleep(env("PAUSE_AFTER_FAILURE", default = "30s"), name = "pause-%d" % n)

        if len(take) > 0:
            live = live + spawn(items = take, entry = "work", name = "s-%d" % n).runs

        # With no free slot and no new run, the wait still ends when a run ends, so the next pass starts the next item.
        ended = [e.outcome for e in first_of(runs = live, name = "g-%d" % n).runs]
        gone = [e.item for e in ended]
        live = [r for r in live if r.item not in gone]

        # A failed item is held, so it leaves the queue until a person reads the comment. The loop goes on with the other items.
        for e in ended:
            if not e.ok:
                item_comment(name = "fail-%d-%s" % (n, e.item), id = e.item, key = "failed", text = "The run of this item failed: " + e.error)
                item_status(name = "hold-%d-%s" % (n, e.item), id = e.item, status = "hold")

        pause = any([not e.ok for e in ended])
