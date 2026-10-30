# The review loop of the remediation method: a gate, a review, and a fix, until the head is approved or the rounds are used up.
# Rounds 1 to 3 resume the implementer on a cheaper model. Later rounds start a fresh session one tier up.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//review_fix.star", "review_fix").

FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read"]

def review_fix(item, impl, review_prompt, fix_prompt, rounds = 6, follow_ups = "allow"):
    """Runs the loop and returns a struct with exhausted, gate and review.

    follow_ups is passed to each review call. With "forbid", a review that leaves any finding open gets the verdict "changes",
    and the review prompt must ask the agent to write a `findings` list beside the verdict.
    """
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
                follow_ups = follow_ups,
                output_tries = 2,
                inputs = {"item": item, "violations": violations},
            )

        if check.passed and review.verdict == "approve" and violations == "":
            return struct(exhausted = False, gate = check, review = review)

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
            forbid_commands = FORBID,
            inputs = {"item": item, "round": round, "findings": findings},
        )
        violations = fix.violations

    return struct(exhausted = True, gate = check, review = review)
