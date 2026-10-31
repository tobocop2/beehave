# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file, in the review loop it loads, or in a prompt next to it. Go holds none of it.

load("@obee//review_fix.star", "review_fix")

# A hook refuses a command of these kinds before the call. A command that still ran is read from the output stream and recorded in the violations of the agent. The step does not fail.
# The review loop reads the violations of the latest run of the implementer: a hit sends the run back to a fix round.
FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read"]

def land(pins):
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
    )

    # A rejected item gets the reason as a comment and the hold status, so it does not come back until a person reads it.
    for rejected in out.rejected:
        item_comment(name = "no-" + rejected.item, id = rejected.item, key = "rejected", text = rejected.reason)
        item_status(name = "hold-" + rejected.item, id = rejected.item, status = "hold")

def main(item):
    pipeline(item)

def finish(item):
    """The engine calls finish after main on every end of the run except a cancel: success, error and command stop.
    It stops the build host when HOST_TEARDOWN is "true". A run with no host sets nothing and calls nothing.
    HOST_TEARDOWN is for a run with one item on the host: the stop deletes the host, so a second item on it would lose its agent. Teardown for a shared host is not done.
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
        land(ready)
        return

    # Before any agent starts, the pull request of the item must be as the owner left it: not merged, not closed, its branch still on the remote,
    # no hold label, not a draft, the head the owner named in EXPECT_HEAD (a full SHA; when it is not set, no head is checked), and the base BASE.
    # A stop is a result: the run ends here and starts no agent. The result of the run holds the stop as `stopped`: the step, the exit code and the reason.
    # An item with no pull request yet passes. An open pull request by a login that is not in `own` claims the item: somebody else is on it, and no agent starts.
    intake = preflight(name = "intake", head = env("EXPECT_HEAD", default = ""), base = env("BASE"), hold = "hold", own = {"logins": [env("OWNER_LOGIN")]})
    if not intake.ok:
        return

    plan = agent(
        name = "plan",
        prompt = "prompts/plan.md",
        model = "opus",
        inputs = {"item": item},
    )

    implement = agent(
        name = "implement",
        prompt = "prompts/implement.md",
        model = "sonnet",
        require_skills = ["blast-radius-review"],
        forbid_commands = FORBID,
        inputs = {"item": item, "plan": plan.text},
    )

    # Five fix rounds need six passes: the sixth reviews the fifth fix.
    # The reviewer starts fresh in each round and judges the commit the gate ran on.
    # Rounds 1 to 3 resume the implementer on a cheaper model. Rounds 4 and 5 start fresh, one tier up.
    # This method has no follow-ups: a review that leaves any finding open gets the verdict "changes", and the loop runs a fix round.
    loop = review_fix(item, implement, "prompts/review.md", "prompts/fix.md", follow_ups = "forbid")

    # A reject verdict ends the loop with no fix round. The item gets the text of the review as the reason and the hold status.
    if loop.rejected:
        item_comment(name = "rejected", id = item.id, key = "rejected", text = loop.review.text)
        item_status(name = "hold-rejected", id = item.id, status = "hold")
        return

    # After the last round, a person rules on each open finding. The ruling is applied once. No review follows it.
    if loop.exhausted:
        ruling = wait(
            "The review loop used all of its rounds. Rule on each open finding: fix it now, or park it with the cost if it is wrong.",
            name = "adjudicate",
        )
        agent(
            name = "settle",
            prompt = "prompts/settle.md",
            model = "sonnet",
            inputs = {"ruling": ruling.answer},
        )
        return

    # An approved head is marked ready. The call merges it when MERGE is "true". With MERGE set to anything else, the call stops after the mark
    # and the owner merges, so a dry run stays possible. The guards of the merge run on every merge: reviews, change requests, open threads,
    # own work, authors, draft, state and the pin of the head. The repository, the base, the switch and the owner identity come from the environment,
    # so the file holds no login or address.
    # `base` is the integration branch of the repository, never the default branch.
    # `approved` is the verdict of the review, which is not the agent that wrote the code. Our own pull requests merge only when it is true.
    # The call passes `tree = loop.gate.tree`, so the merge step proves the merged tree after the merge. A gate that records no tree stops the run at the call. The proof runs after the merge:
    # a stop does not undo it, and the item ends failed with the pull request merged.
    # The call also passes `steps = loop.gate.steps`: it merges only when every step of the gate at the head is a pass. A step that is unknown is not a pass.
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
