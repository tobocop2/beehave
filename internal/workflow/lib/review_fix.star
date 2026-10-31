# The review loop of the remediation method: a gate, a review, and a fix, until the head is approved or the rounds are used up.
# Rounds 1 to 3 resume the implementer on a cheaper model. Later rounds start a fresh session one tier up.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//review_fix.star", "review_fix").

FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "zsh-expansion"]

# A reviewer is read-only: it must not fix, commit or push. The kind git-write refuses every git command that changes a repository.
REVIEW_FORBID = FORBID + ["git-write"]

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the work of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (name, step.stop, item.id))

DECISIONS = ["keep", "replace", "delete"]

# The fix rounds on one mechanism after which the next round decides whether the mechanism should exist.
DESIGN_AFTER = 2

def review_fix(item, impl, review_prompt, fix_prompt, rounds = 6, follow_ups = "allow", protect = [], design_prompt = ""):
    """Runs the loop and returns a struct with exhausted, rejected, design, gate and review.

    protect lists the directories that no command of the review or the fix may write in, such as the checkout of the owner.
    A non-empty list adds the kind protected-path to both calls. An empty list adds nothing.

    The review verdict is approve, changes or reject. A reject ends the loop at once: rejected is true and no fix round runs.

    The design round is opt-in. With the default design_prompt of an empty text, the review declares only the output `verdict`, no design step runs,
    and the loop is the plain loop. With a design_prompt, the review also writes the output `mechanism`: the file path that its findings are about
    (it may add `:function`; the loop keys on the part before the first colon), or an empty text. The loop counts the fix steps
    of each mechanism. When a mechanism had two fix steps and the review still asks for changes on it, the next step is the design step, not a fix.
    The design step writes a decision (keep, replace or delete) and a reason. Keep runs the fix with the reason, and the design step does not run
    again for that mechanism. Replace and delete end the loop at once: design holds the mechanism, the decision and the reason, and no fix round runs.
    A finding on another mechanism, and a finding with no mechanism, keep the normal fix rounds.

    follow_ups is passed to each review call. With "forbid", a review that leaves any finding open gets the verdict "changes",
    and the review prompt must ask the agent to write a `findings` list beside the verdict.
    """
    extra = ["protected-path"] if protect else []
    violations = impl.violations
    check = None
    review = None
    fixes = {}
    decided = {}

    for round in range(1, rounds + 1):
        check = gate(name = "check")
        review = None

        if check.passed:
            review = agent(
                name = "review",
                prompt = review_prompt,
                model = "opus",
                pin = check.head,
                outputs = ["verdict", "mechanism"] if design_prompt != "" else ["verdict"],
                forbid_commands = REVIEW_FORBID + extra,
                protect_paths = protect,
                follow_ups = follow_ups,
                output_tries = 2,
                inputs = {"item": item, "violations": violations},
            )
            capped(review, "review", item)

        if check.passed and review.verdict == "approve" and violations == "":
            return struct(exhausted = False, rejected = False, design = None, gate = check, review = review)

        # A reject ends the loop with no fix round, whatever the gate says. The review runs only on a passing gate, so a missing review is no reject.
        # The caller holds the item with the text of the review as the reason.
        if review != None and review.verdict == "reject":
            return struct(exhausted = False, rejected = True, design = None, gate = check, review = review)

        if round == rounds:
            break

        findings = review.text if check.passed else check.report
        if violations != "":
            findings = violations + "\n\n" + findings

        # A failed gate has no review, so its fix names no mechanism. The key is the file: the text before the first colon.
        mechanism = ""
        if design_prompt != "" and review != None:
            mechanism = review.mechanism.split(":")[0].strip()

        if mechanism != "" and fixes.get(mechanism, 0) >= DESIGN_AFTER and mechanism not in decided:
            design = agent(
                name = "design",
                prompt = design_prompt,
                model = "opus",
                outputs = ["decision", "reason"],
                forbid_commands = REVIEW_FORBID + extra,
                protect_paths = protect,
                output_tries = 2,
                inputs = {"item": item, "mechanism": mechanism, "fixes": fixes[mechanism], "findings": findings},
            )
            capped(design, "design", item)

            if design.decision not in DECISIONS:
                fail("REFUSE: the design step wrote the decision %r. Use one of: %s. Run obee resume %s." % (design.decision, ", ".join(DECISIONS), item.id))

            if design.decision != "keep":
                return struct(
                    exhausted = False,
                    rejected = False,
                    design = struct(mechanism = mechanism, decision = design.decision, reason = design.reason),
                    gate = check,
                    review = review,
                )

            decided[mechanism] = True
            findings = "Design decision for %s: keep and patch. %s\n\n%s" % (mechanism, design.reason, findings)

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

        if mechanism != "":
            fixes[mechanism] = fixes.get(mechanism, 0) + 1

    return struct(exhausted = True, rejected = False, design = None, gate = check, review = review)
