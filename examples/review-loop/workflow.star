# Two agents improve one draft by messages. The author writes the draft, and the reviewer judges it.
# The author starts the reviewer as a child run and sends it each draft. The reviewer replies to "parent" with a verdict and notes.
# The loop stops when the reviewer approves, or after ROUNDS drafts.
# Add an item with `beehave item add`, then run it with `beehave run <item> <project> --workflow workflow.star`.

ROUNDS = 3
REPLY_TIMEOUT = "30m"
MODEL = "sonnet"

def capped(step, name):
    """Stops the run when a cap ended the agent step, because the output of a capped step can be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished." % (name, step.stop))

def reviewer(item):
    """The child run. It judges each draft that the author sends, and replies with the verdict and the notes."""
    for _ in range(ROUNDS):
        got = recv("draft", timeout = REPLY_TIMEOUT, name = "draft")
        if got.timed_out:
            fail("REFUSE: the author sent no draft in " + REPLY_TIMEOUT)
        review = agent(
            name = "review",
            prompt = "prompts/review.md",
            model = MODEL,
            outputs = ["verdict", "notes"],
            edits = False,
            sandbox = sandbox(),
            inputs = {"item": item, "draft": got.value},
        )
        capped(review, "review")
        send(to = "parent", topic = "verdict", value = {"verdict": review.verdict, "notes": review.notes}, name = "verdict")
        if review.verdict == "approve":
            return

def main(item):
    made = item_create(name = "reviewer", key = "reviewer", title = "Review: " + item.title, body = item.body, parent = item.id)
    child = spawn(items = [made.id], entry = "reviewer", name = "reviewer")

    first = agent(name = "draft", prompt = "prompts/draft.md", model = MODEL, outputs = ["draft"], edits = False, sandbox = sandbox(), inputs = {"item": item})
    capped(first, "draft")
    text = first.draft
    verdict = ""

    for n in range(1, ROUNDS + 1):
        send(to = child.runs[0], topic = "draft", value = text, name = "draft")
        reply = recv("verdict", timeout = REPLY_TIMEOUT, name = "verdict")
        if reply.timed_out:
            fail("REFUSE: the reviewer sent no verdict in " + REPLY_TIMEOUT)
        verdict = reply.value["verdict"]
        if verdict == "approve" or n == ROUNDS:
            break
        revise = agent(
            name = "revise",
            prompt = "prompts/revise.md",
            model = MODEL,
            outputs = ["draft"],
            edits = False,
            sandbox = sandbox(),
            inputs = {"item": item, "draft": text, "notes": reply.value["notes"]},
        )
        capped(revise, "revise")
        text = revise.draft

    gather(child, name = "reviewer")
    if verdict != "approve":
        fail("REFUSE: the reviewer did not approve a draft in %d rounds. Change the item and run it again." % ROUNDS)
    item_comment(name = "draft", id = item.id, key = "draft", text = text)
