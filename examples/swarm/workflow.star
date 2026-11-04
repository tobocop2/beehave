# A swarm: one backlog, many tasks, many agents at once, and a merge queue that lands pull requests by priority.
# Run: SWARM=<item> obee run <item> <project> --workflow workflow.star
# The body of the parent item holds one task on each line: a priority (P0 to P3), a tab, the title, and optionally a tab and a body.
#
# Variables of the environment:
#   REPO, BASE, OWNER_LOGIN, OWNER_EMAIL  the repository as owner/name, the integration branch, and the identity for the merge guard.
#   SWARM        the id of the parent item. The spend ledger and the STOP file are named by it.
#   SWARM_DIR    the absolute path of the folder that holds this file. The ledger script runs from there.
#   CAP_USD      the most the swarm spends. The parent starts no new run at the cap.
#   REMOTE       optional address of the repository for the branch reads. Default: https://github.com/<REPO>.git
#   DEADLINE_MIN optional, default 480: the most minutes that the swarm starts new runs.
#   BATCH        optional, default 8: the number of finished pull requests that starts a landing pass.

HOLD = "hold"
TIERS = ["P0", "P1", "P2", "P3"]
RESOLVES = 2
MAX_PASSES = 900
FORBIDDEN = ["bulk-staging", "git-bypass", "git-author"]
MODEL = "sonnet"
IDLE_TIMEOUT = "10m"
SHORT_TIMEOUT = "1m"
CONFLICT_PREFIXES = ["conflict", "red on top"]
TASK = "task"
RESOLVE = "resolve"

def sh(verb, args):
    return ["sh", env("SWARM_DIR") + "/scripts/ledger.sh", verb, env("SWARM")] + args

def seconds(stamp):
    """Seconds since 1970 of an RFC 3339 UTC time such as 2026-11-03T10:20:30Z (days from civil date)."""
    year, month, day = int(stamp[0:4]), int(stamp[5:7]), int(stamp[8:10])
    year = year - 1 if month <= 2 else year
    era = year // 400
    yoe = year - era * 400
    doy = (153 * ((month + 9) % 12) + 2) // 5 + day - 1
    days = era * 146097 + yoe * 365 + yoe // 4 - yoe // 100 + doy - 719468
    return days * 86400 + int(stamp[11:13]) * 3600 + int(stamp[14:16]) * 60 + int(stamp[17:19])

def run_step(item, name, kind, prompt, inputs):
    """One agent step of a child run. Its cost goes to the ledger before any check of the step."""
    done = agent(
        name = name,
        prompt = prompt,
        model = MODEL,
        forbid_commands = FORBIDDEN,
        inputs = inputs,
        idle_timeout = IDLE_TIMEOUT,
        max_budget_usd = 6,
    )
    command(name = "cost-" + name, run = sh("add", [item.id, kind, str(done.cost_usd)]), timeout = SHORT_TIMEOUT)
    if done.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished." % (name, done.stop))
    if done.violations != "":
        fail("REFUSE: the agent tried a refused command: " + done.violations)

def task(item):
    """One task as a child run. The agent pushes the branch that has the id of the item."""
    run_step(item, "implement", TASK, "prompts/task.md", {"item": item, "base": env("BASE")})

def resolve(item):
    """A merge fix as a child run. The body of the item names the original item."""
    note = json.decode(item.body)
    run_step(item, "resolve", RESOLVE, "prompts/resolve.md", {"item": item, "branch": note["orig"], "reason": note["reason"], "base": env("BASE")})

def tip(name, branch):
    remote = env("REMOTE", default = "https://github.com/" + env("REPO") + ".git")
    found = command(name = name, run = ["git", "ls-remote", remote, "refs/heads/" + branch], timeout = SHORT_TIMEOUT)
    if found.rc != 0:
        fail("cannot read the branch %s of %s" % (branch, remote))
    return found.stdout.split()[0] if found.stdout.strip() != "" else ""

def new_swarm():
    return struct(prio = {}, kind = {}, origin = {}, tries = {}, waiting = {}, live = [], held = [], landed = [], batches = [], deferred = [], stop = {"why": ""})

def halt(swarm, why):
    swarm.stop["why"] = why

def file_backlog(item, swarm):
    """One item for each line. The whole backlog is refused if one line is bad."""
    lines = [line for line in item.body.split("\n") if line.strip() != "" and not line.startswith("#")]
    if len(lines) == 0:
        fail("REFUSE: the backlog has no task")
    for number, line in enumerate(lines, 1):
        fields = line.split("\t")
        if len(fields) < 2 or fields[0].strip() not in TIERS or fields[1].strip() == "":
            fail("REFUSE: task %d needs a priority P0 to P3, a tab and a title" % number)
        made = item_create(name = "create-%d" % number, key = "task-%d" % number, title = "[%s] %s" % (fields[0].strip(), fields[1].strip()), body = "\t".join(fields[2:]), parent = item.id)
        swarm.prio[made.id] = fields[0].strip()
        swarm.kind[made.id] = TASK

def check_limits(swarm, p, started):
    """A limit stops new runs only. Runs that already started finish."""
    if swarm.stop["why"] != "":
        return
    over = command(name = "budget-%d" % p, run = sh("check", [env("CAP_USD")]), timeout = SHORT_TIMEOUT)
    if over.rc != 0:
        halt(swarm, "no new runs: " + over.stdout.strip())
    elif seconds(now()) - started > int(env("DEADLINE_MIN", default = "480")) * 60:
        halt(swarm, "the swarm ran for more than %s minutes" % env("DEADLINE_MIN", default = "480"))

def ready_ids(swarm, rows):
    """The ready items, highest priority first. The key puts the priority before the id."""
    open_ids = [run.item for run in swarm.live]
    by_key = {swarm.prio[r.item] + " " + r.item: r.item for r in rows if r.state == "ready" and r.item in swarm.kind and r.item not in open_ids}
    return [by_key[key] for key in sorted(by_key.keys())]

def read_ready(swarm, name):
    """The free slots and the ready items. Stale is True when a live run has ended but is not gathered yet."""
    queued = queue(name = name, hold = HOLD)
    running = [r.item for r in queued.rows if r.state == "running"]
    return struct(free = queued.free, ready = ready_ids(swarm, queued.rows), stale = any([run.item not in running for run in swarm.live]))

def outranked(swarm, ready):
    """True when a pull request waits to land and its priority is higher than that of the first ready item.
    A pull request that the last landing deferred does not count: the next landing, early or not, tries it again."""
    tries = [swarm.prio[i] for i in swarm.waiting.keys() if i not in swarm.deferred]
    return len(tries) > 0 and len(ready) > 0 and min(tries) < swarm.prio[ready[0]]

def start_ready(item, swarm, p):
    """Starts the ready items by priority, no more than the free slots. Returns True when nothing is ready, running or waiting."""
    queued = read_ready(swarm, "q-%d" % p)
    # A run that ended but is not gathered yet still counts as live here, and a slot of it looks free. Its landing can file a fix item of priority P0.
    # So nothing starts until the pass has gathered it and landed it: the pass goes on to wait_for_one, which returns at once.
    if queued.stale:
        return False
    # A waiting pull request that outranks every ready item lands now, and does not wait for a full batch. Its fix item, if it conflicts, then starts first.
    if outranked(swarm, queued.ready):
        if land_pass(item, swarm):
            return False
        queued = read_ready(swarm, "q-%d-landed" % p)
        if queued.stale:
            return False
    ready = queued.ready
    for kind in [TASK, RESOLVE]:
        chosen = [i for i in ready[:queued.free] if swarm.kind[i] == kind]
        if len(chosen) > 0:
            swarm.live.extend(spawn(items = chosen, entry = kind, name = "%s-%d" % (kind, p)).runs)
    return len(ready) == 0 and len(swarm.live) == 0 and len(swarm.waiting) == 0

def park(swarm, p, item_id, key, text):
    """Puts an item on hold with a comment, so that a person reads it."""
    item_comment(name = "%s-%d-%s" % (key, p, item_id), id = item_id, key = key, text = text)
    item_status(name = "hold-%d-%s" % (p, item_id), id = item_id, status = "hold")
    swarm.held.append(item_id)

def wait_for_one(swarm, p):
    if len(swarm.live) == 0:
        return
    results = gather(swarm.live, until = "any", name = "g-%d" % p).results
    gone = [r.item for r in results]
    keep = [run for run in swarm.live if run.item not in gone]
    swarm.live.clear()
    swarm.live.extend(keep)
    for ended in results:
        orig = swarm.origin.get(ended.item, ended.item)
        if not ended.ok:
            park(swarm, p, ended.item, "failed", "The run of this item failed: " + ended.error)
            if orig != ended.item:
                swarm.held.append(orig)
                swarm.waiting.pop(orig, None)
            continue
        head = tip("head-%d-%s" % (p, ended.item), orig)
        if head == "":
            park(swarm, p, ended.item, "nobranch", "The task ended and left no branch, so it is not in the batch.")
        else:
            swarm.waiting[orig] = head

def land(tag, swarm):
    """One batch_merge for each priority, P0 first. batch_merge folds its members in the order of the item id, so one call for each priority makes P0 win."""
    total = struct(merged = [], rejected = [], deferred = [], unproven = [])
    for tier in TIERS:
        group = [struct(item = i, head = swarm.waiting[i]) for i in sorted(swarm.waiting.keys()) if swarm.prio[i] == tier]
        if len(group) == 0:
            continue
        out = batch_merge(
            name = "land-%s-%s" % (tag, tier),
            prs = group,
            base = env("BASE"),
            repo = env("REPO"),
            hold = HOLD,
            own = {"logins": [env("OWNER_LOGIN")], "emails": [env("OWNER_EMAIL")]},
        )
        total.merged.extend(out.merged)
        total.rejected.extend(out.rejected)
        total.deferred.extend(out.deferred)
        total.unproven.extend(out.unproven)
    return total

def is_conflict(reason):
    return any([reason.startswith(prefix) for prefix in CONFLICT_PREFIXES])

def settle_rejected(item, swarm, number, rejected):
    """A conflict gets a fix item, at most twice. After that, or for any other reason, a person reads the item."""
    orig = rejected.item
    swarm.waiting.pop(orig)
    if is_conflict(rejected.reason) and swarm.tries.get(orig, 0) < RESOLVES:
        swarm.tries[orig] = swarm.tries.get(orig, 0) + 1
        made = item_create(
            name = "resolve-%d-%s" % (number, orig),
            key = "resolve-%s-%d" % (orig, swarm.tries[orig]),
            title = "[%s] Merge %s with %s" % (swarm.prio[orig], env("BASE"), orig),
            body = json.encode({"orig": orig, "reason": rejected.reason}),
            parent = item.id,
        )
        swarm.prio[made.id] = swarm.prio[orig]
        swarm.kind[made.id] = RESOLVE
        swarm.origin[made.id] = orig
    else:
        park(swarm, number, orig, "rejected", "Not landed: " + rejected.reason)

def land_pass(item, swarm):
    """Lands the waiting pull requests. Returns True when the pass stalled."""
    number = len(swarm.batches) + 1
    out = land("%d" % number, swarm)
    swarm.batches.append(out)
    swarm.deferred.clear()
    swarm.deferred.extend([d.item for d in out.deferred])
    unproven = {u.item: u.reason for u in out.unproven}
    for merged_id in out.merged:
        if merged_id in unproven:
            swarm.waiting.pop(merged_id)
            park(swarm, number, merged_id, "unproven", "The batch merged this item, but its landing on %s is not proven, so the item stays open. Check it by hand: %s" % (env("BASE"), unproven[merged_id]))
            continue
        item_comment(name = "merged-%d-%s" % (number, merged_id), id = merged_id, key = "merged", text = "Landed on %s in batch %d." % (env("BASE"), number))
        item_status(name = "close-%d-%s" % (number, merged_id), id = merged_id, status = "closed")
        swarm.landed.append(merged_id)
        swarm.waiting.pop(merged_id)
        for fix_id in [k for k in swarm.origin.keys() if swarm.origin[k] == merged_id]:
            item_status(name = "close-%d-%s" % (number, fix_id), id = fix_id, status = "closed")
    for rejected in out.rejected:
        settle_rejected(item, swarm, number, rejected)
    stalled = len(out.deferred) > 0 and len(swarm.live) == 0 and len(out.merged) + len(out.rejected) == 0
    if stalled:
        halt(swarm, "the forge could not settle %d pull requests" % len(out.deferred))
    return stalled

def run_pass(item, swarm, p, started):
    """One pass. Returns False when the loop ends."""
    check_limits(swarm, p, started)
    if swarm.stop["why"] != "":
        if len(swarm.live) == 0 and len(swarm.waiting) == 0:
            return False
    elif start_ready(item, swarm, p):
        return False
    wait_for_one(swarm, p)
    if len(swarm.waiting) >= int(env("BATCH", default = "8")) or (len(swarm.live) == 0 and len(swarm.waiting) > 0):
        return not land_pass(item, swarm)
    return True

def main(item):
    swarm = new_swarm()
    file_backlog(item, swarm)
    started = seconds(now())
    for p in range(MAX_PASSES):
        if not run_pass(item, swarm, p, started):
            break
    left = [i for i in swarm.kind.keys() if swarm.kind[i] == TASK and i not in swarm.landed and i not in swarm.held]
    spent = command(name = "ledger-report", run = sh("report", []), timeout = SHORT_TIMEOUT).stdout.strip()
    stopped = ("Stopped: " + swarm.stop["why"]) if swarm.stop["why"] != "" else ""
    item_comment(
        name = "report",
        id = item.id,
        key = "report",
        text = "%d landed, %d need a person, %d not started. %s Spend: %s" % (len(swarm.landed), len(swarm.held), len(left), stopped, spent),
    )
    if swarm.stop["why"] == "" and len(left) == 0 and len(swarm.held) == 0:
        item_status(name = "close", id = item.id, status = "closed")
