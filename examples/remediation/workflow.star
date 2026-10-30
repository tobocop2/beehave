# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file, in the review loop it loads, or in a prompt next to it. Go holds none of it.

load("@obee//review_fix.star", "review_fix")

# A hook refuses a command of these kinds before the call. A command that still ran is read from the output stream and recorded in the violations of the agent. The step does not fail.
# The review loop reads the violations of the latest run of the implementer: a hit sends the run back to a fix round.
FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command"]

def main(item):
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
    # `forbid_text` holds the patterns of the attribution rule of the merge gate, each case-insensitive.
    merge(
        name = "ship",
        head = loop.gate.head,
        tree = loop.gate.tree,
        merge = env("MERGE") == "true",
        repo = env("REPO"),
        base = env("BASE"),
        hold = "hold",
        own = {"logins": [env("OWNER_LOGIN")], "emails": [env("OWNER_EMAIL")]},
        approved = loop.review.verdict == "approve",
        forbid_text = ["(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"],
        children = "retarget",
    )
