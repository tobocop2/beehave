# A workflow for a project with no tracker and no forge: the items are in local.db, and the result is a branch in the source repository.
# Add an item with `beehave item add`, then run it with `beehave run <item> --workflow workflow.star`.

load("@beehave//review_fix.star", "is_void", "void_names")

# The gates in a row that may give no verdict before the item stops.
VOID_LIMIT = 3

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the work of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item. A plain beehave resume replays this stop. To start the item again, run beehave resume %s, then beehave cancel %s, then beehave run %s." % (name, step.stop, item.id, item.id, item.id))

def judged(item, rerun_void):
    """Runs the gate and returns the first result that judged the code. A gate that failed a step is red and judged the code.
    A gate with an unknown step and no failed step is void: the host killed it, or it did not run. It gave no verdict, so the gate runs again.
    After VOID_LIMIT void results in a row the item stops: the code was never judged, and a fix agent would change working code."""
    for _ in range(VOID_LIMIT):
        check = gate(name = "check")  # The gate command is the one in the project file.

        if not rerun_void or not is_void(check):
            return check

    fail("REFUSE: the gate gave no verdict %d times in a row. The steps with no result: %s. The code was not judged. Fix the host or the gate command, then run beehave run %s again." % (VOID_LIMIT, void_names(check), item.id))

def main(item):
    # Every agent step runs in the sandbox with no network: the steps read and edit the work folder, and the gate runs the tests.
    plan = agent(name = "plan", prompt = "prompts/plan.md", model = "sonnet", edits = False, sandbox = sandbox(), inputs = {"item": item})
    capped(plan, "plan", item)

    # The implement step sits behind patched. The true branch is the new code: it reads the facts and gives the implement step the base branch as the input base.
    # A new run, and an open run that did not reach this call, take it. The false branch is the old code, unchanged: a run that already recorded the implement step replays it.
    if patched("implement-base"):
        implement = agent(name = "implement", prompt = "prompts/implement.md", model = "sonnet", sandbox = sandbox(), inputs = {"item": item, "plan": plan.text, "base": facts().base_branch})
    else:
        implement = agent(name = "implement", prompt = "prompts/implement.md", model = "sonnet", sandbox = sandbox(), inputs = {"item": item, "plan": plan.text})
    capped(implement, "implement", item)

    # The rerun of a void gate sits behind patched. A run that already recorded a void gate and the fix step after it replays them as they were.
    rerun_void = patched("void-gate")

    # A pass is one gate, one review and, when the head is not approved, one fix. The loop stops at the first approved head.
    approved = False

    for _ in range(3):
        check = judged(item, rerun_void)
        findings = check.report

        if check.passed:
            review = agent(
                name = "review",
                prompt = "prompts/review.md",
                model = "sonnet",
                pin = check.head,
                outputs = ["verdict"],
                edits = False,
                sandbox = sandbox(),
                inputs = {"item": item},
            )
            capped(review, "review", item)
            approved = review.verdict == "approve"
            findings = review.text

        if approved:
            break

        fix = agent(name = "fix", prompt = "prompts/fix.md", model = "sonnet", sandbox = sandbox(), inputs = {"item": item, "findings": findings})
        capped(fix, "fix", item)

    # The branch goes into the source repository under the id of the item. An item that used all of its passes is not pushed.
    # A command gets no token and no SSH agent, but it keeps HOME, so ssh still finds a key file in ~/.ssh. This push suits a source on this machine, which needs no key.
    # The push builtin needs a project with a forge, and a forge makes the gate look up pull requests.
    if approved:
        command(name = "push", run = ["git", "push", "origin", "HEAD:refs/heads/" + item.id])
