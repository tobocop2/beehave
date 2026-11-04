# The review loop of the remediation method: a gate, a review, and a fix, until the head is approved or the rounds are used up.
# Rounds 1 to 3 resume the implementer on a cheaper model. Later rounds start a fresh session one tier up.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//review_fix.star", "review_fix").

FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "zsh-expansion", "laptop-copy"]

# A reviewer is read-only: it must not fix, commit or push. The kind git-write refuses every git command that changes a repository.
REVIEW_FORBID = FORBID + ["git-write"]

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the work of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (name, step.stop, item.id))

def kept_history(step, name, item):
    """Stops the item when an agent step rewrote history (amend, squash, reset). The reviewed commits must stay, and a rewrite can reach the remote only through a force-push."""
    if step.rewrote:
        fail("REFUSE: the %s step rewrote the history of the item folder. A step adds commits and never amends, squashes or resets. Check the folder, then run obee resume %s." % (name, item.id))

DECISIONS = ["keep", "replace", "delete"]

# The fix rounds on one mechanism after which the next round decides whether the mechanism should exist.
DESIGN_AFTER = 2

def stopped_before(item, name, head, guard):
    """Asks the forge about the pull request of the item right before an agent of the loop starts.
    The head is the one the gate ran on, which the caller read from the worktree. A pull request that is merged, closed, held, a draft,
    on another base, without its branch or at another head is a stop: it returns the result of the call, and the loop starts no agent."""
    if not guard:
        return None

    check = preflight(name = name, id = item.id, head = head, **guard)
    return None if check.ok else check

# A gate with no failed step and an unknown step gave no verdict about the code: it is void, not red. The loop reruns a void gate
# VOID_RERUNS times at once. Each rerun is a new gate step, so the engine records each, and a replay never reads an old void as the answer.
# Then it asks the owner up to VOID_ASKS times; an answer reruns the gate. The wait is the way back: a resume would replay the recorded void.
VOID_RERUNS = 2
VOID_ASKS = 3

def is_void(check):
    return not check.passed and not [s for s in check.steps if s["status"] == "fail"] and [s for s in check.steps if s["status"] == "unknown"] != []

def void_names(check):
    names = [s["name"] for s in check.steps if s["status"] == "unknown"]
    return ", ".join(names) if names else "unknown"

def settled_gate(item):
    """Runs the gate and reruns it while it is void. Returns the first result that is not void, or the last void result after the bound."""
    check = gate(name = "check")
    for attempt in range(1, VOID_RERUNS + VOID_ASKS + 1):
        if not is_void(check):
            return check

        if attempt > VOID_RERUNS:
            wait(
                "The gate gave no result for %s (steps with no result: %s). %s Fix the cause, then answer to run the gate again." % (item.id, void_names(check), check.report),
                name = "gate-void-%d" % attempt,
            )

        check = gate(name = "check-void-%d" % attempt)

    return check

def review_fix(item, impl, review_prompt, fix_prompt, rounds = 6, follow_ups = "allow", protect = [], design_prompt = "", guard = {}, allow = [], review_outputs = [], facts = {}):
    """Runs the loop and returns a struct with stopped, exhausted, rejected, design, gate and review.

    guard holds the settings of the preflight call that runs before every review, design and fix agent: base, hold and own.
    The call compares the forge head with the head the gate ran on, which is the head of the worktree. The loop therefore needs each fix
    pushed before the next gate. A stop is a result: stopped is true, the step of the stop is the reason of the run, and the loop starts no more agents.
    The caller returns when stopped is true. An item with no pull request passes.

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

    allow lists the globs that a fix may change, as allow_paths does for an agent step. A fix that changes any other path fails the step.
    An empty list adds no check.

    review_outputs lists more outputs that the review writes beside the verdict, for a workflow that reads a field of the review, such as a count.
    The review prompt must ask the agent to write each one, and the returned review holds them. An empty list adds none.
    facts is the value of the facts() builtin, or an empty dict. The loop passes it as the input `facts` of each review, design and fix agent, so a prompt can state the facts of the project.

    follow_ups is passed to each review call. With "forbid", a review that leaves any finding open gets the verdict "changes",
    and the review prompt must ask the agent to write a `findings` list beside the verdict. A reject needs the list too: a call that writes the verdict reject and no `findings` fails the step. With "audit", any open finding of any class
    gives the verdict "findings"; the method sets no threshold, so the prompt decides what the agent lists.
    """
    extra = ["protected-path"] if protect else []
    scope = {"allow_paths": allow} if allow else {}
    violations = impl.violations
    check = None
    review = None
    fixes = {}
    decided = {}

    kept_history(impl, "implement", item)

    for round in range(1, rounds + 1):
        check = settled_gate(item)
        review = None

        # A gate that is still void after the reruns and the asks gave no verdict about the code. No fix agent reads its report.
        if is_void(check):
            fail("REFUSE: the gate gave no result for %s after %d reruns and %d questions (steps with no result: %s). %s" % (item.id, VOID_RERUNS, VOID_ASKS, void_names(check), check.report))

        if check.passed:
            if stopped_before(item, "before-review-%d" % round, check.head, guard) != None:
                return struct(stopped = True, exhausted = False, rejected = False, design = None, gate = check, review = None)

            review = agent(
                name = "review",
                lane = "review",
                rounds = rounds,
                prompt = review_prompt,
                model = "opus",
                pin = check.head,
                outputs = (["verdict", "mechanism"] if design_prompt != "" else ["verdict"]) + review_outputs,
                forbid_commands = REVIEW_FORBID + extra,
                protect_paths = protect,
                follow_ups = follow_ups,
                output_tries = 2,
                edits = False,
                inputs = {"item": item, "violations": violations, "facts": facts},
            )
            capped(review, "review", item)

        # A read-only step that changed the folder is a violation of this round: the approve of that review is not clean.
        review_violations = review.violations if review != None else ""

        if check.passed and review.verdict == "approve" and violations == "" and review_violations == "":
            return struct(stopped = False, exhausted = False, rejected = False, design = None, gate = check, review = review)

        # A reject ends the loop with no fix round, whatever the gate says. The review runs only on a passing gate, so a missing review is no reject.
        # The caller holds the item with the text of the review as the reason.
        if review != None and review.verdict == "reject":
            return struct(stopped = False, exhausted = False, rejected = True, design = None, gate = check, review = review)

        if round == rounds:
            break

        findings = review.text if check.passed else check.report
        if review_violations != "":
            findings = review_violations + "\n\n" + findings

        if violations != "":
            findings = violations + "\n\n" + findings

        # A failed gate has no review, so its fix names no mechanism. The key is the file: the text before the first colon.
        mechanism = ""
        if design_prompt != "" and review != None:
            mechanism = review.mechanism.split(":")[0].strip()

        if mechanism != "" and fixes.get(mechanism, 0) >= DESIGN_AFTER and mechanism not in decided:
            if stopped_before(item, "before-design-%d" % round, check.head, guard) != None:
                return struct(stopped = True, exhausted = False, rejected = False, design = None, gate = check, review = review)

            design = agent(
                name = "design",
                lane = "design",
                prompt = design_prompt,
                model = "opus",
                outputs = ["decision", "reason"],
                forbid_commands = REVIEW_FORBID + extra,
                protect_paths = protect,
                output_tries = 2,
                edits = False,
                inputs = {"item": item, "mechanism": mechanism, "fixes": fixes[mechanism], "findings": findings, "facts": facts},
            )
            capped(design, "design", item)

            if design.violations != "":
                fail("REFUSE: the design step changed the item folder, and it is read-only. %s Check the folder, then run obee resume %s." % (design.violations, item.id))

            if design.decision not in DECISIONS:
                fail("REFUSE: the design step wrote the decision %r. Use one of: %s. Run obee resume %s." % (design.decision, ", ".join(DECISIONS), item.id))

            if design.decision != "keep":
                return struct(
                    stopped = False,
                    exhausted = False,
                    rejected = False,
                    design = struct(mechanism = mechanism, decision = design.decision, reason = design.reason),
                    gate = check,
                    review = review,
                )

            decided[mechanism] = True
            findings = "Design decision for %s: keep and patch. %s\n\n%s" % (mechanism, design.reason, findings)

        if stopped_before(item, "before-fix-%d" % round, check.head, guard) != None:
            return struct(stopped = True, exhausted = False, rejected = False, design = None, gate = check, review = review)

        fix = agent(
            name = "fix",
            lane = "rework",
            rounds = rounds - 1,
            prompt = fix_prompt,
            model = "sonnet" if round <= 3 else "opus",
            resume = impl.session if round <= 3 else "",
            forbid_commands = FORBID + extra,
            protect_paths = protect,
            inputs = {"item": item, "round": round, "findings": findings, "facts": facts},
            **scope
        )
        capped(fix, "fix", item)
        kept_history(fix, "fix", item)
        violations = fix.violations

        if mechanism != "":
            fixes[mechanism] = fixes.get(mechanism, 0) + 1

    return struct(stopped = False, exhausted = True, rejected = False, design = None, gate = check, review = review)
