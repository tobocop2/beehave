# A workflow for a project with no tracker and no forge: the items are in local.db, and the result is a branch in the source repository.
# Add an item with `obee item add`, then run it with `obee run <item> --workflow workflow.star`.

def main(item):
    plan = agent(name = "plan", prompt = "prompts/plan.md", model = "sonnet", inputs = {"item": item})
    agent(name = "implement", prompt = "prompts/implement.md", model = "sonnet", inputs = {"item": item, "plan": plan.text})

    # A pass is one gate, one review and, when the head is not approved, one fix. The loop stops at the first approved head.
    approved = False

    for _ in range(3):
        check = gate(name = "check")  # The gate command is the one in the project file.
        findings = check.report

        if check.passed:
            review = agent(
                name = "review",
                prompt = "prompts/review.md",
                model = "sonnet",
                pin = check.head,
                outputs = ["verdict"],
                inputs = {"item": item},
            )
            approved = review.verdict == "approve"
            findings = review.text

        if approved:
            break

        agent(name = "fix", prompt = "prompts/fix.md", model = "sonnet", inputs = {"item": item, "findings": findings})

    # The branch goes into the source repository under the id of the item. An item that used all of its passes is not pushed.
    # A command gets no token. For a remote that needs one, add env = {"GH_TOKEN": env("GH_TOKEN")}.
    if approved:
        command(name = "push", run = ["git", "push", "origin", "HEAD:refs/heads/" + item.id])
