# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file, in the review loop it loads, or in a prompt next to it. Go holds none of it.

load("@obee//review_fix.star", "review_fix")

# A pull request is stale after this many hours with no push, comment, review or answer from the author. The same values are the idle threshold and the grace period that the take-over passes to the tool as flags.
IDLE_HOURS = "1"
GRACE_HOURS = "1"

# The severities in the order of the queue, most severe first. A severity that is not in the list sorts last.
SEVERITIES = ["P0", "P1", "P2", "P3", "P4"]

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

# A hook refuses a command of these kinds before the call. A command that still ran is read from the output stream and recorded in the violations of the agent. The step does not fail.
# The review loop reads the violations of the latest run of the implementer: a hit sends the run back to a fix round.
FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "protected-path", "zsh-expansion", "laptop-copy"]

# The agent works in the worktree of the item and must not write in the checkout of the owner. The environment variable SOURCE_CHECKOUT names that checkout.
# The kind protected-path refuses a write there, and a git command there that is not a read. A command that reads the checkout passes.
def protected():
    return [env("SOURCE_CHECKOUT")]

def lane_scope():
    """Returns the allow_paths setting of the lane, as a dict to pass with `**`.
    The environment variable LANE_KIND names the kind of lane: impl (the default), papers or design.
    An impl lane may change any path. A papers or design lane may change only the globs in ALLOW_PATHS, split by spaces.
    The implement step and every fix step fail, and the message names each path outside the globs, when they change another path."""
    kind = env("LANE_KIND", default = "impl")
    if kind == "impl":
        return {}

    if kind not in ["papers", "design"]:
        fail("REFUSE: LANE_KIND is %r. The kinds are impl, papers or design" % kind)

    globs = env("ALLOW_PATHS", default = "").split()
    if len(globs) == 0:
        fail("REFUSE: a %s lane changes no source path, so ALLOW_PATHS must list the globs it may change, split by spaces" % kind)

    return {"allow_paths": globs}

# The most fix lanes that one pull request gets. The review loop runs one more pass than it: the last pass reviews the last fix.
# The environment variable PATCH_ROUNDS can lower the cap for a run. It cannot raise it.
MAX_PATCH_ROUNDS = 5

def patch_rounds():
    """Returns the number of fix lanes that one pull request gets: the value of PATCH_ROUNDS from 1 to MAX_PATCH_ROUNDS, and MAX_PATCH_ROUNDS when it is not set.
    A value that is not a whole number in that range stops the run before any agent starts."""
    text = env("PATCH_ROUNDS", default = str(MAX_PATCH_ROUNDS))
    if not text.isdigit() or int(text) < 1 or int(text) > MAX_PATCH_ROUNDS:
        fail("REFUSE: PATCH_ROUNDS is %r. Use a whole number from 1 to %d: the fix lanes that one pull request gets" % (text, MAX_PATCH_ROUNDS))

    return int(text)

def regressions(value):
    """Returns the regression count that the output `regressions` of a report holds, or None when the count is unknown.
    The output is a text. Spaces around the number are allowed. A text that is not a whole number gives None: an unknown count is never a pass."""
    number = value.strip()
    return int(number) if number.isdigit() else None

def regression_rise(start, end):
    """Returns the reason to hold the item, and an empty text when the regression count did not rise from the output `regressions` of the plan (`start`) to the one of the last review (`end`).
    Both counts are the regressions open at the head of the report, so they compare like with like: a defect that an earlier round found and a later round fixed is in neither.
    A count that is unknown in either output holds the item: the prompts ask both lanes for a whole number."""
    before = regressions(start)
    after = regressions(end)
    if before == None:
        return "The report of the plan has no regression count that can be read. An unknown count blocks the item: a person reads the reports and rules."

    if after == None:
        return "The report of the last review has no regression count that can be read. An unknown count blocks the item: a person reads the reports and rules."

    if after <= before:
        return ""

    return "The regression count rose from %d to %d between the plan and the last review. A rise blocks the item: a person reads the reports and rules." % (before, after)

def land(item, pins):
    """Lands the ready items as one batch: one gate run on the union of their heads, and a split of the union when the gate is red.
    Each entry of `pins` is id=sha: the item and the full SHA the owner reviewed, as EXPECT_HEAD is for one item.
    The MERGE switch, the stops before the work and the guards are those of the pipeline of one item. The branch of each pull request has the id of its item."""
    if env("MERGE") != "true":
        return

    prs = []
    stopped = []

    for pin in pins:
        pair = pin.split("=")
        if len(pair) != 2 or pair[0] == "" or pair[1] == "":
            fail("LAND entry %r is not id=sha. Give the full SHA that the owner reviewed for each item" % pin)

        id, head = pair[0], pair[1]

        # The stops of the pipeline of one item, for each item: merged, closed, branch gone, hold, draft, base, and a head that is not the reviewed one.
        # An item that stops is reported with its reason, and the other items still land.
        check = preflight(name = "intake-" + id, id = id, head = head, base = env("BASE"), hold = "hold", own = {"logins": [env("OWNER_LOGIN")]})
        if check.ok:
            prs.append(struct(item = id, head = head))
        else:
            stopped.append(struct(item = id, reason = check.reason))

    for skipped in stopped:
        item_comment(name = "stop-" + skipped.item, id = skipped.item, key = "rejected", text = skipped.reason)
        item_status(name = "stop-hold-" + skipped.item, id = skipped.item, status = "hold")

    if len(prs) == 0:
        return

    out = batch_merge(
        name = "land",
        prs = prs,
        base = env("BASE"),
        repo = env("REPO"),
        hold = "hold",
        own = {"logins": [env("OWNER_LOGIN")], "emails": [env("OWNER_EMAIL")]},
        forbid_text = ["(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"],
        integration = int(env("INTEGRATION_PR", default = "0")),
    )

    # A rejected item gets the reason as a comment and the hold status, so it does not come back until a person reads it.
    for rejected in out.rejected:
        item_comment(name = "no-" + rejected.item, id = rejected.item, key = "rejected", text = rejected.reason)
        item_status(name = "hold-" + rejected.item, id = rejected.item, status = "hold")

    # The read of the issues after the batch. A note means the read did not run, which is not a pass: the owner sees it as a comment.
    # A finding stops the run. The batch has landed, so the owner fixes the state of the issues by hand.
    if out.note != "":
        print("the issue check did not run: " + out.note)
        item_comment(name = "issue-check", id = item.id, key = "issue-check", text = "the issue check did not run: " + out.note)

    if len(out.findings) > 0:
        fail("REFUSE: the batch landed, but the issues are not as they must be. Fix the state of each issue by hand:\n" + "\n".join(out.findings))

def sweep(item):
    """Starts the ready items in the order of the queue, up to the free slots, and waits for the runs.
    QUEUE_SWEEP="true" selects this pass. The queue is read again here, so a pull request that arrived since the last pass shows in the order.
    The runs are the pipeline of one item each. The rank rule is above."""
    # The run of the sweep claims the item id it is given, as every run does. That is wanted: the claim lets only one pass run for that id, so two passes cannot start the same items twice. Give the sweep an id that is no real item.
    q = queue(name = "sweep-queue", hold = "hold")
    take = [r.item for r in rank(q.rows, env("OWNER_LOGIN"))][:q.free]
    if len(take) == 0:
        return

    gather(spawn(items = take, entry = "pipeline", name = "sweep-spawn").runs, name = "sweep-gather")

def take_over(item, pr):
    """Takes over the pull request `pr` of somebody else, in this order: the review, the notice, the grace period, the take.
    1. A review agent posts the findings as a changes-requested review. The notice is refused until the review is on the pull request.
    2. The tool posts the notice that asks the author for an answer, and refuses when the pull request is not stale (IDLE_HOURS) or is ours.
    3. The grace period (GRACE_HOURS) is a clock rule: the author has that long to answer KEEP or RELEASE. KEEP stops the item. RELEASE or no answer when the time is over goes on to the take. TAKE_AUTHOR names the author.
    4. The tool takes the pull request only when no one forbade it. It checks the clock itself as a second guard, and a take with replies that carry neither token is refused: the item then holds with the reason of the tool, and a person reads the replies.
    The run of the item holds the claim, so the take writes none. A refusal of the tool (exit code 2) ends the item with the lines it printed."""
    me = env("OWNER_LOGIN")
    author = env("TAKE_AUTHOR")
    base = ["remedy", "take", pr, "--me", me, "--idle", IDLE_HOURS, "--grace", GRACE_HOURS, "--item", item.id]

    # This run is the take-over run, so this is its one read of the facts, before its one agent.
    project = facts()

    agent(
        name = "take-review",
        prompt = "prompts/take_review.md",
        model = "opus",
        edits = False,
        kind = "review",
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "pr": pr, "facts": project},
    )

    command(name = "take-notice", run = base + ["--apply"], stop_on_rc = [2])

    grace = wait(
        "The notice is on pull request %s. Answer KEEP to keep it, or RELEASE to hand it over. With no answer in %s hours, it is taken." % (pr, GRACE_HOURS),
        name = "take-grace",
        timeout = GRACE_HOURS + "h",
        to = [author],
        choices = ["KEEP", "RELEASE"],
    )

    if grace.answer == "KEEP":
        return

    command(name = "take-claim", run = base + ["--take"], stop_on_rc = [2])

def _count(line, name):
    """Returns the number after `name=` in a line of the recount tool. A line with no such figure fails the step: an unknown count never turns the assert off."""
    for word in line.split():
        if word.startswith(name + "=") and word[len(name) + 1:].isdigit():
            return int(word[len(name) + 1:])

    fail("REFUSE: the line of recount-before has no `%s=<n>` figure, so the step cannot assert that the count moved. The line is %r" % (name, line))

def record_merge(item, merged):
    """Records the merge of the pull request `merged` in the integration pull request: its row, a closing row for each issue it resolves, the recount of the heading and the title, and the close of each resolved issue with the evidence.
    INTEGRATION_PR names the integration pull request. "0" or "none" means the round tracks no rows, and the step does nothing. CLOSES lists the issues that the merge resolves, split by spaces.
    The recount tool reads the pull request before and after the agent. It fails the step when the heading or the title says another number than the rows, when the row count did not change, and, when CLOSES is not empty, when the issue count did not change.
    The method never uses the merge queue of GitHub: the merge step above runs one pinned merge of the head."""
    integration = env("INTEGRATION_PR", default = "0")
    if integration in ["0", "none", ""]:
        return

    closes = env("CLOSES", default = "")
    before = command(name = "recount-before", run = ["remedy", "recount", "--pr", integration], stop_on_rc = [2])

    # A before-line with no figure fails here, before the agent runs: the assert after it never runs without a known count.
    was = ["--was-rows", str(_count(before.stdout, "rows"))]
    if closes != "":
        was += ["--was-issues", str(_count(before.stdout, "issues"))]

    # This run is the integration run, so this is its one read of the facts, before its one agent.
    project = facts()

    agent(
        name = "integrate",
        prompt = "prompts/integrate.md",
        model = "sonnet",
        edits = False,
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "pr": merged, "integration": integration, "closes": closes if closes != "" else "none", "facts": project},
    )

    command(name = "recount-after", run = ["remedy", "recount", "--pr", integration] + was, stop_on_rc = [1, 2])

def next_round(item):
    """Cuts the next round after the last pull request of the old round merged. NEXT_BASE names the new round branch. BASE is the old one, and INTEGRATION_PR is the integration pull request.
    The cut tool refuses while the integration pull request is not merged (exit code 2), so no round starts before the one before it ended."""
    command(
        name = "next-round",
        run = ["remedy", "cut", "--old", env("BASE"), "--new", env("NEXT_BASE"), "--pr", env("INTEGRATION_PR"), "--trunk", env("TRUNK", default = "main")],
        stop_on_rc = [2],
    )

def record_base_reds(item):
    """Writes the one record of the known base reds: the reds that BASE_REDS lists (split by spaces), the time and the tip of the base. Each new write replaces the file, so the file holds one record.
    BASE_REDS_FILE names the file (base-reds.md by default). The prompts tell each lane to read this file and not to copy the list."""
    tip = command(name = "base-tip", run = ["git", "rev-parse", "origin/" + env("BASE")], stop_on_rc = [2]).stdout.strip()
    reds = env("BASE_REDS").split()
    text = "# Known base reds\n\nMeasured at %s on the tip %s of %s.\n\n%s\n" % (now(), tip, env("BASE"), "\n".join(["- " + red for red in reds]))

    write(name = "base-reds", run = ["tee", env("BASE_REDS_FILE", default = "base-reds.md")], stdin = text)

def round_step(item):
    """Runs the round step that the environment names and returns True, or returns False when no round step is named.
    ROUND_STEP names the step, so a run with no round step reads one variable: "merge" runs record_merge for the pull request in MERGED_PR, "cut" runs next_round, and "reds" runs record_base_reds."""
    step = env("ROUND_STEP", default = "")
    if step == "":
        return False

    if step == "merge":
        record_merge(item, env("MERGED_PR"))
    elif step == "cut":
        next_round(item)
    elif step == "reds":
        record_base_reds(item)
    else:
        fail("REFUSE: ROUND_STEP is %r. The steps are merge, cut or reds" % step)

    return True

def main(item):
    # QUEUE_SWEEP="true" runs one pass over the queue and starts no host: each run it starts is the pipeline of one item.
    if env("QUEUE_SWEEP", default = "") == "true":
        sweep(item)
        return

    # TAKE_PR names a pull request of somebody else to take over. The run does the take-over steps and then stops.
    pr = env("TAKE_PR", default = "")
    if pr != "":
        take_over(item, pr)
        return

    # ROUND_STEP names a step of the round: the record of a merge, the cut of the next round, or the record of the base reds.
    if round_step(item):
        return

    # HOST_UP="true" launches the build host when it is absent. Without it the run starts no host, so a run that needs none costs nothing.
    # A project with no host, or a host that is already up, starts nothing. The host deletes itself when idle (host.idle_minutes).
    if env("HOST_UP", default = "") == "true":
        host_up(name = "host")

    pipeline(item)

def finish(item):
    """The engine calls finish after main on every end of the run except a cancel: success, error and command stop.
    It stops the build host when HOST_TEARDOWN is "true" and this run launched the host (host_up started it). A host that was already up is never stopped: another run may use it.
    HOST_TEARDOWN is for one item on the host: the stop deletes the host, so a second item on it would lose its agent. Two runs that start together can both report `started`, and then both stop the host. Teardown for a shared host is not done.
    The stop banks the work on the host first. A host that cannot bank its work stays up, and the item gets a comment with the reason, so the cost is not silent."""
    if env("HOST_TEARDOWN", default = "") != "true":
        return

    stop = host_teardown(name = "teardown")
    if not stop.down:
        item_comment(name = "host-stays-up", id = item.id, key = "host-stays-up", text = "The build host stays up. " + stop.reason)

def pipeline(item):
    # LAND lists ready items as id=sha, split by spaces. A run with LAND set starts no agent: it lands those items with one batch_merge. Without LAND, the run is the pipeline of one item.
    ready = env("LAND", default = "").split()
    if len(ready) > 0:
        land(item, ready)
        return

    # Before any agent starts, the pull request of the item must be as the owner left it: not merged, not closed, its branch still on the remote,
    # no hold label, not a draft, the head the owner named in EXPECT_HEAD (a full SHA; when it is not set, no head is checked), and the base BASE.
    # A stop is a result: the run ends here and starts no agent. The result of the run holds the stop as `stopped`: the step, the exit code and the reason.
    # An item with no pull request yet passes. An open pull request by a login that is not in `own` claims the item: somebody else is on it, and no agent starts.
    intake = preflight(name = "intake", head = env("EXPECT_HEAD", default = ""), base = env("BASE"), hold = "hold", own = {"logins": [env("OWNER_LOGIN")]})
    if not intake.ok:
        return

    scope = lane_scope()
    rounds = patch_rounds() + 1

    # The facts of the project file (people, rules, limits, traps and the identity keys). The step records them, so a resume keeps the facts the run started with.
    # Each agent of the example gets them as the input `facts`; no agent reads a path for them. The take-over run and the integration run read them once, before their one agent.
    project = facts()

    plan = agent(
        name = "plan",
        prompt = "prompts/plan.md",
        model = "opus",
        edits = False,
        outputs = ["regressions"],
        # The repeat-offender check in the prompt reads pull requests with gh, so the shell reaches the forge and nothing else.
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "facts": project},
    )

    if plan.stop != "":
        fail("REFUSE: the plan step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (plan.stop, item.id))

    implement = agent(
        name = "implement",
        prompt = "prompts/implement.md",
        model = "sonnet",
        require_skills = ["blast-radius-review"],
        forbid_commands = FORBID,
        protect_paths = protected(),
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "plan": plan.text, "facts": project},
        **scope
    )

    # A cap ended the run (the output `stop` is "turns" or "budget"), so the work may be half done. The item stops here, before the gate and the review.
    if implement.stop != "":
        fail("REFUSE: the implement step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (implement.stop, item.id))

    # The first checkpoint the engine can act on: the plan step makes no commit and one agent step is opaque, so the draft opens after the implement step, on the branch that the agent pushed.
    # The engine opens the draft pull request of the item once (a resume finds the open one) and,
    # when PARENT names the item this one depends on, links it into the native stack of the pull request of that item. The call owns only the closing line of the body:
    # `Closes #N` when the base is the default branch and `Addresses #N` on any other. An item id that is not an issue number gets no line, and a body that exists is kept.
    pr_open(
        name = "draft",
        title = item.title,
        base = env("BASE"),
        issue = int(item.id) if item.id.isdigit() else 0,
        parent = env("PARENT", default = ""),
    )

    # Five fix rounds need six passes: the sixth reviews the fifth fix.
    # The reviewer starts fresh in each round and judges the commit the gate ran on.
    # Rounds 1 to 3 resume the implementer on a cheaper model. Rounds 4 and 5 start fresh, one tier up.
    # This method has no follow-ups: a review that leaves any finding open gets the verdict "changes", and the loop runs a fix round.
    # Before each review, design and fix agent, the loop reads the pull request again, with the head the gate ran on (the head of the worktree), the base, the hold label and the own logins.
    # A stop in the loop ends the run as the stop at the intake does: no more agents start, and the result holds the step, the exit code and the reason.
    guard = {"base": env("BASE"), "hold": "hold", "own": {"logins": [env("OWNER_LOGIN")]}}
    loop = review_fix(item, implement, "prompts/review.md", "prompts/fix.md", follow_ups = "forbid", protect = protected(), design_prompt = "prompts/design.md", guard = guard, allow = scope.get("allow_paths", []), rounds = rounds, review_outputs = ["regressions"], facts = project)

    if loop.stopped:
        return

    # A reject verdict ends the loop with no fix round. The item gets the text of the review as the reason and the hold status.
    if loop.rejected:
        item_comment(name = "rejected", id = item.id, key = "rejected", text = loop.review.text)
        item_status(name = "hold-rejected", id = item.id, status = "hold")
        return

    # A rise of the regression count, from the report of the plan to the report of the last review, holds the item with the reason. So does a count that is unknown in either report.
    # This check runs before the design and the exhausted branches: a held item gets no owner ruling and no settle step. Nothing merges.
    # The last gate of an exhausted loop can be red. Then there is no review and no count; the item goes on to the ruling of the owner, and nothing merges after the settle step.
    if loop.review != None:
        rise = regression_rise(plan.regressions, loop.review.regressions)
        if rise != "":
            item_comment(name = "regression-rise", id = item.id, key = "regression-rise", text = rise)
            item_status(name = "hold-regression-rise", id = item.id, status = "hold")
            return

    # After two fix rounds on one mechanism, a design step decides whether the mechanism should exist. A replace or a delete ends the loop:
    # the item gets the decision and the reason as the comment and the hold status, and a person rules on it.
    if loop.design != None:
        item_comment(
            name = "design",
            id = item.id,
            key = "design",
            text = "Design decision for %s: %s. %s" % (loop.design.mechanism, loop.design.decision, loop.design.reason),
        )
        item_status(name = "hold-design", id = item.id, status = "hold")
        return

    # After the last round, no fix lane starts. A person rules on each open finding, and the settle step applies the ruling once: it is the only edit after the last round, no review follows it, and nothing merges.
    if loop.exhausted:
        ruling = wait(
            "The review loop used all of its rounds. Rule on each open finding: fix it now, or park it with the cost if it is wrong.",
            name = "adjudicate",
        )

        # The ruling can take days. The settle agent is a dispatch like the others, so the pull request is read again first, with the head the last gate ran on.
        if not preflight(name = "before-settle", id = item.id, head = loop.gate.head, **guard).ok:
            return

        settle = agent(
            name = "settle",
            prompt = "prompts/settle.md",
            model = "sonnet",
            inputs = {"ruling": ruling.answer, "facts": project},
            **scope
        )

        if settle.stop != "":
            fail("REFUSE: the settle step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (settle.stop, item.id))

        return

    # An approved head is marked ready. The call merges it when MERGE is "true". With MERGE set to anything else, the call stops after the mark
    # and the owner merges, so a dry run stays possible. The guards of the merge run on every merge: reviews, change requests, open threads,
    # own work, authors, draft, state and the pin of the head. The repository, the base, the switch and the owner identity come from the environment,
    # so the file holds no login or address.
    # `base` is the integration branch of the repository, never the default branch.
    # `approved` is the verdict of the review, which is not the agent that wrote the code. Our own pull requests merge only when it is true.
    # The call passes `tree = loop.gate.tree`, so the merge step proves the merged tree after the merge. A gate that records no tree stops the run at the call. The proof runs after the merge:
    # a stop does not undo it, and the item ends failed with the pull request merged.
    # The call also passes `steps = loop.gate.steps`: it merges only when every step of the gate at the head is a pass. A step that is unknown is not a pass. A call that can merge and leaves out `steps` is refused by `obee check workflow` and again at the call.
    # `forbid_text` holds the patterns of the attribution rule of the merge gate, each case-insensitive.
    merge(
        name = "ship",
        head = loop.gate.head,
        tree = loop.gate.tree,
        steps = loop.gate.steps,
        merge = env("MERGE") == "true",
        repo = env("REPO"),
        base = env("BASE"),
        hold = "hold",
        own = {"logins": [env("OWNER_LOGIN")], "emails": [env("OWNER_EMAIL")]},
        approved = loop.review.verdict == "approve",
        forbid_text = ["(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"],
        children = "retarget",
    )
