# The queue of the open work of a project: one row for each open item with its state, the open pull requests, and the free slots.
# The library reads with item_list, pr_list and run_read, and with pr_state for a done run that owes a visit. It joins the reads here.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//queue.star", "queue").

# The severities in the order of the queue, most severe first. A severity that is not in the list sorts last.
SEVERITIES = ["P0", "P1", "P2", "P3", "P4"]

def _idle(held):
    return "hold" if held else "ready"

def _foreign(value):
    """True when a run recorded a foreign review: the reason foreign, the number of the reviewed pull request and open findings."""
    if type(value) != "dict":
        return False
    if value.get("reason") != "foreign" or type(value.get("pr")) != "int" or value["pr"] == 0:
        return False
    return type(value.get("findings")) == "list" and len(value["findings"]) > 0

def _settle(run, held, name):
    """The state of a row from the last run of its item. A run that succeeded is done, unless its foreign review is owed another visit or the item is held."""
    state = run["state"]
    stopped = state == "stopped"
    value = None
    if state == "done":
        if _foreign(run["value"]) and pr_state(pr = run["value"]["pr"], name = "%s-state-%s" % (name, run["item"])).state == "MERGED":
            state = _idle(held)
        elif held:
            # A held item is not done: a landing that the round rejected puts the item on hold, and the next pass must not offer it again.
            state = "hold"
        else:
            value = run["value"]
    elif state not in ["running", "failed", "stopped"]:
        state = _idle(held)
    return struct(
        state = state,
        stop_step = run["stop_step"] if stopped else "",
        stop_reason = run["stop_reason"] if stopped else "",
        value = value,
    )

def _joined(pulls, hold):
    """What the open pull requests of each branch say about the item of that branch. Of many pull requests on one branch the lowest number counts, and one with the hold label holds the item."""
    joined = {}
    for p in pulls:
        one = joined.get(p["branch"], struct(number = 0, author = "", changes_requested = False, held = False))
        number, author, changes_requested = one.number, one.author, one.changes_requested
        if number == 0 or p["number"] < number:
            number, author, changes_requested = p["number"], p["author"], p["review_decision"] == "CHANGES_REQUESTED"
        held = one.held or (hold != "" and hold in p["labels"])
        joined[p["branch"]] = struct(number = number, author = author, changes_requested = changes_requested, held = held)
    return joined

def queue(name, hold = "", label = "", base = ""):
    """Reads the open items, the open pull requests and the last run of each item. Returns rows, free and pulls.
    A `label` other than "" keeps only the items that carry that tracker label. A `base` other than "" keeps only the pull requests on that base.
    A pull request that names an open item by its branch holds that item when it carries the `hold` label.
    The reads are recorded steps named <name>-items, <name>-pulls, <name>-runs and <name>-state-<item>, so a replay returns the first answers.
    The rows are sorted by severity and then by item id. free is the cap of the items queue less its open runs, read before the runs."""
    items = item_list(label = label, name = name + "-items").items

    # A pull request names an open item whatever the label of that item, so the label does not narrow this read.
    named = items if label == "" else item_list(name = name + "-all").items
    pulls = pr_list(base = base, name = name + "-pulls").pulls
    read = run_read(items = [i["id"] for i in items], name = name + "-runs")
    joined = _joined(pulls, hold)
    rows = []
    for item, run in zip(items, read.runs):
        pull = joined.get(item["id"], struct(number = 0, author = "", changes_requested = False, held = False))
        settled = _settle(run, item["hold"] or pull.held, name)
        rows.append(struct(
            item = item["id"],
            state = settled.state,
            severity = item["severity"],
            pr = pull.number,
            parent = item["parent"],
            author = pull.author,
            changes_requested = pull.changes_requested,
            labels = list(item["labels"]),
            stop_step = settled.stop_step,
            stop_reason = settled.stop_reason,
            value = settled.value,
        ))
    ids = [i["id"] for i in named]
    listed = [struct(number = p["number"], branch = p["branch"], base = p["base"], author = p["author"], draft = p["draft"], title = p["title"], item = p["branch"] if p["branch"] in ids else "") for p in pulls]
    return struct(rows = sorted(rows, key = lambda r: (r.severity, r.item)), free = read.free, pulls = listed)

def _severity(row):
    if row.severity in SEVERITIES:
        return SEVERITIES.index(row.severity)

    return len(SEVERITIES)

def _group(row, me):
    # The work of others is a pull request whose author is not `me`. A review request does not decide it. It stays first whatever its review state is, so a pull request of another author with requested changes is here and not in group 1.
    if row.pr > 0 and row.author != "" and row.author != me:
        return 0

    if row.changes_requested:
        return 1

    if row.pr > 0:
        return 2

    return 3

def rank(rows, me):
    """Returns the ready rows in the order of work: first the pull requests of other authors than `me` (the work of others, whatever their review state),
    then our pull requests with requested changes, then our other work in flight (an open pull request), then the new rows.
    Inside each group, the rows of a container come before the loose rows, then the rows go by severity, then by item id. A row that is not ready is not offered.
    Two rows with the same keys keep the order of their item ids, so the input order does not change the result."""
    ready = [r for r in rows if r.state == "ready"]
    return sorted(ready, key = lambda r: (_group(r, me), 0 if r.parent != "" else 1, _severity(r), r.item))
