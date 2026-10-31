# The review loop of the remediation method: a gate, a review, and a fix, until the head is approved or the rounds are used up.
# Rounds 1 to 3 resume the implementer on a cheaper model. Later rounds start a fresh session one tier up.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//review_fix.star", "review_fix").

FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read"]

# A reviewer is read-only: it must not fix, commit or push. The kind git-write refuses every git command that changes a repository.
REVIEW_FORBID = FORBID + ["git-write"]

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the work of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (name, step.stop, item.id))

def review_fix(item, impl, review_prompt, fix_prompt, rounds = 6, follow_ups = "allow", protect = []):
    """Runs the loop and returns a struct with exhausted, rejected, gate and review.

    protect lists the directories that no command of the review or the fix may write in, such as the checkout of the owner.
    A non-empty list adds the kind protected-path to both calls. An empty list adds nothing.

    The review verdict is approve, changes or reject. A reject ends the loop at once: rejected is true and no fix round runs.

    follow_ups is passed to each review call. With "forbid", a review that leaves any finding open gets the verdict "changes",
    and the review prompt must ask the agent to write a `findings` list beside the verdict.
    """
    extra = ["protected-path"] if protect else []
    violations = impl.violations
    check = None
    review = None

    for round in range(1, rounds + 1):
        check = gate(name = "check")
        review = None

        if check.passed:
            review = agent(
                name = "review",
                prompt = review_prompt,
                model = "opus",
                pin = check.head,
                outputs = ["verdict"],
                forbid_commands = REVIEW_FORBID + extra,
                protect_paths = protect,
                follow_ups = follow_ups,
                output_tries = 2,
                inputs = {"item": item, "violations": violations},
            )
            capped(review, "review", item)

        if check.passed and review.verdict == "approve" and violations == "":
            return struct(exhausted = False, rejected = False, gate = check, review = review)

        # A reject ends the loop with no fix round, whatever the gate says. The review runs only on a passing gate, so a missing review is no reject.
        # The caller holds the item with the text of the review as the reason.
        if review != None and review.verdict == "reject":
            return struct(exhausted = False, rejected = True, gate = check, review = review)

        if round == rounds:
            break

        findings = review.text if check.passed else check.report
        if violations != "":
            findings = violations + "\n\n" + findings

        fix = agent(
            name = "fix",
            prompt = fix_prompt,
            model = "sonnet" if round <= 3 else "opus",
            resume = impl.session if round <= 3 else "",
            forbid_commands = FORBID + extra,
            protect_paths = protect,
            inputs = {"item": item, "round": round, "findings": findings},
        )
        capped(fix, "fix", item)
        violations = fix.violations

    return struct(exhausted = True, rejected = False, gate = check, review = review)
