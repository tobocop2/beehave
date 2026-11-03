# A swarm: one parent item splits a job into tasks, an agent works on each task as its own item and opens a pull request,
# and one batch_merge lands all the pull requests that pass one gate run on the union of their heads.
# Add the parent item with `obee item add` and put one task on each line of its body. Then run it with `obee run <item> --workflow workflow.star`.
#
# Four variables of the environment name your repository and your identity. Set all four before the run:
#   REPO         the repository, as owner/name, for example your-org/your-repo.
#   BASE         the integration branch that receives the pull requests. It is never the default branch of the repository.
#   OWNER_LOGIN  your login on the forge. A pull request of this login lands only if a step approved it, so the guard sees the agents as you.
#   OWNER_EMAIL  the email of your commits, for the same guard.
# One optional variable names the integration pull request:
#   INTEGRATION_PR  the number of the pull request from BASE into the default branch. With it, the read of the issues after the batch runs once that pull request is merged. Without it, the read runs only when BASE is the default branch.
#
# Steps: create (one item for each task), fan (spawn starts one run of `task` for each item and gather waits for all of them),
# head (a command that reads the head of each pull request branch), land (one batch_merge).
# Outcomes:
#   A member that passes the gate lands. The run lists it in `merged`.
#   A member that is red alone, conflicts with the base, or fails a guard is in `rejected`. The parent comments the reason on its item and sets it to hold.
#   A task that did not finish is also commented and held, and it is not in the union.
#   A member whose mergeable state stays unknown, or that a passing cause stopped (a forge read failed, the base moved), is in `deferred`.
#   It stays open with no comment, and the next run of the parent puts it in the next batch.

# The label of the forge that blocks a merge. Change it to the label that your repository uses.
HOLD = "hold"

# The comment for a task that left no pull request branch. Change the text to match your conventions.
NOT_DONE = "The task did not finish, so its pull request is not in the batch."

def task(item):
    """Runs for each task item. The agent works in its own work folder, pushes the branch that has the id of the item, and opens the pull request."""
    implement = agent(
        name = "implement",
        prompt = "prompts/task.md",
        model = "sonnet",
        # Add more kinds from the list of the engine if your repository needs them.
        forbid_commands = ["bulk-staging"],
        # The shell pushes the branch and opens the pull request, so it reaches the forge and nothing else. A step with no list has no network.
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "base": env("BASE")},
    )

    # A cap ended the run (the output `stop` is "turns" or "budget"), so the pull request may be half done. The task fails with the reason.
    if implement.stop != "":
        fail("REFUSE: the implement step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (implement.stop, item.id))

def tasks_of(item):
    """The tasks are the non-empty lines of the body of the parent item."""
    return [line.strip() for line in item.body.split("\n") if line.strip() != ""]

def main(item):
    # One item for each task. The key makes the call safe to repeat, so a resume does not file the same task twice.
    ids = []

    for number, text in enumerate(tasks_of(item), 1):
        made = item_create(name = "create-%d" % number, key = "task-%d" % number, title = text, body = text, parent = item.id)
        ids.append(made.id)

    if len(ids) == 0:
        return

    # Each item runs the function `task` as a child run. The wait ends when every child run has ended, with success or not.
    runs = spawn(items = ids, entry = "task", name = "fan")
    results = gather(runs, name = "fan").results

    prs = []
    unfinished = []

    for result in results:
        if not result.ok:
            unfinished.append(result.item)
            continue

        # The branch of the pull request has the id of the item. Its tip is the head that the gate and the guards pin.
        # The parent has no work folder, so the command reads the repository by its address, not by a remote of a local clone.
        # For a private repository, git needs a credential helper that knows the forge login, for example `gh auth setup-git`.
        # A missing branch gives no output and code 0. Any other code means the read failed, and the run stops rather than hold a finished task.
        found = command(name = "head-" + result.item, run = ["git", "ls-remote", "https://github.com/" + env("REPO") + ".git", "refs/heads/" + result.item])
        if found.rc != 0:
            fail("cannot read the branch %s of %s: git ls-remote ended with code %d" % (result.item, env("REPO"), found.rc))
        head = found.stdout.split()[0] if found.stdout.strip() != "" else ""

        if head == "":
            unfinished.append(result.item)
            continue

        prs.append(struct(item = result.item, head = head))

    for id in unfinished:
        item_comment(name = "unfinished-" + id, id = id, key = "unfinished", text = NOT_DONE)
        item_status(name = "hold-" + id, id = id, status = "hold")

    if len(prs) == 0:
        return

    # One gate run covers the union of all heads. A red union is split in halves until the red member is alone, and the rest land.
    # `own` lists the identities of the owner and the agents. Their pull requests land only because the guards accept the review of another step.
    out = batch_merge(
        name = "land",
        prs = prs,
        base = env("BASE"),
        repo = env("REPO"),
        hold = HOLD,
        own = {"logins": [env("OWNER_LOGIN")], "emails": [env("OWNER_EMAIL")]},
        integration = int(env("INTEGRATION_PR", default = "0")),
    )

    # A rejected member gets the reason as a comment and the hold status, so it does not come back in the next batch until a person reads it.
    for rejected in out.rejected:
        item_comment(name = "no-" + rejected.item, id = rejected.item, key = "rejected", text = rejected.reason)
        item_status(name = "hold-" + rejected.item, id = rejected.item, status = "hold")

    # The read of the issues after the batch. A note means the read did not run, which is not a pass: the owner sees it as a comment on the parent.
    # A finding stops the run. The batch has landed, so the owner fixes the state of the issues by hand.
    if out.note != "":
        print("the issue check did not run: " + out.note)
        item_comment(name = "issue-check", id = item.id, key = "issue-check", text = "the issue check did not run: " + out.note)

    if len(out.findings) > 0:
        fail("REFUSE: the batch landed, but the issues are not as they must be. Fix the state of each issue by hand:\n" + "\n".join(out.findings))
