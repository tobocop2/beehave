# A research workflow with no tracker and no forge: the item is a question, and the result is a report file.
# Add an item with `obee item add`, then run it with `obee run <item> --workflow workflow.star`.
#
# Two variables of the environment name the places that the workflow needs:
#   RESEARCH_TOOLS is the absolute path of the folder examples/research/tools. It holds check.sh and report.sh.
#   RESEARCH_OUT is the absolute path of the folder that receives the report.
#
# Steps: scope (an agent writes the sub-questions), research (one agent for each sub-question), check (a command that reads the notes),
# summary (an agent writes a short summary), review (a fresh agent tries to refute the notes and the summary), report (a command that writes
# the report file). A revise step fixes the notes, and the summary step then writes the summary again with the findings, so the report holds
# only a summary that a review has read.
#
# Outcomes:
#   Approved: the report is RESEARCH_OUT/<item id>.md. It starts with the summary.
#   Three reviews found open findings: the report is RESEARCH_OUT/<item id>-unreviewed.md and starts with the open findings. The report step
#     exits with code 3, which is in its stop_on_rc, so `obee status` shows "stopped at command:report#<n> rc 3" and not a plain success.
#   Three checks found a claim with no source or a URL that does not answer: the item stops at the check, and `obee status` shows its output.
#   The check cannot run (no network, no curl, a notes file that has no sub-question, the total time passed before a source failed): the item
#     stops at the check with code 2.
#
# The library review loop of the engine (review_fix.star) does not fit this work. Its fix step needs an implementer agent and resumes that session
# (the notes come from one agent for each sub-question), it sets its own model tiers and its own list of forbidden commands for code,
# and its loop goes back to one fix step, while here a failed check goes back to the research step of each file that failed.
# This workflow uses the same review call as the library, with output_tries = 2.

PASSES = 3
MAX_QUESTIONS = 5
NO_TEXT = "The reviewer left a finding open and wrote no text for it."

def capped(step, name, item):
    """Stops the item when a cap ended the agent step. The output `stop` is "turns" or "budget", and the notes of a capped run may be half done."""
    if step.stop != "":
        fail("REFUSE: the %s step stopped at the %s cap before it finished. Raise the cap or split the item, then run obee resume %s." % (name, step.stop, item.id))

def notes_file(number):
    return "notes/%d.md" % number

def research(item, number, question, findings):
    step = agent(
        name = "research",
        prompt = "prompts/research.md",
        model = "sonnet",
        inputs = {"item": item, "number": str(number), "question": question, "findings": findings},
    )
    capped(step, "research", item)

def summarize(item, findings):
    """Writes the summary of the notes as they are now. The review reads it, and the findings of the review say what to fix in it."""
    step = agent(
        name = "summary",
        prompt = "prompts/conclude.md",
        model = "sonnet",
        outputs = ["summary"],
        edits = False,
        inputs = {"item": item, "findings": findings},
    )
    capped(step, "summary", item)

    return step.summary

def check_notes(item, questions):
    """Checks the notes. A file with a problem goes back to its sub-question. The check stops the item at its last pass."""
    for attempt in range(1, PASSES + 1):
        check = command(
            name = "check",
            run = ["sh", env("RESEARCH_TOOLS") + "/check.sh", "notes"],
            env = {"CHECK_QUESTIONS": str(len(questions))},
            stop_on_rc = [2] if attempt < PASSES else [1, 2],  # 2 means the check could not run. 1 on the last pass means the notes still fail.
        )

        if check.rc == 0:
            return

        lines = check.stdout.split("\n")

        for number, question in enumerate(questions, 1):
            found = [line for line in lines if line.startswith("FAIL " + notes_file(number) + ":")]

            if found:
                research(item, number, question, "\n".join(found))

def main(item):
    scope = agent(
        name = "scope",
        prompt = "prompts/scope.md",
        model = "sonnet",
        outputs = ["questions"],
        edits = False,
        inputs = {"item": item},
    )
    capped(scope, "scope", item)

    questions = [line.strip() for line in scope.questions.split("\n") if line.strip() != ""][:MAX_QUESTIONS]

    for number, question in enumerate(questions, 1):
        research(item, number, question, "")

    check_notes(item, questions)

    # The reviewer starts fresh in each pass. The follow_ups setting makes any open finding a verdict of changes, so the loop ends at zero findings.
    approved = False
    findings = ""
    summary = summarize(item, "")

    for attempt in range(1, PASSES + 1):
        review = agent(
            name = "review",
            prompt = "prompts/review.md",
            model = "opus",
            outputs = ["verdict", "open"],
            follow_ups = "forbid",
            output_tries = 2,
            edits = False,
            inputs = {"item": item, "summary": summary},
        )
        capped(review, "review", item)

        # The text of the open findings is the output `open` of the verdict file, not the last message of the reviewer.
        findings = review.open if review.open != "" else NO_TEXT

        if review.verdict == "approve":
            approved = True
            break

        if attempt == PASSES:
            break

        revise = agent(name = "revise", prompt = "prompts/revise.md", model = "sonnet", inputs = {"item": item, "findings": findings})
        capped(revise, "revise", item)
        check_notes(item, questions)
        summary = summarize(item, findings)

    command(
        name = "report",
        run = ["sh", env("RESEARCH_TOOLS") + "/report.sh", "notes", env("RESEARCH_OUT") + "/" + item.id + ("" if approved else "-unreviewed") + ".md"],
        env = {"TITLE": item.title, "SUMMARY": summary, "OPEN": "" if approved else findings},
        stop_on_rc = [] if approved else [3],  # 3 means the report is written and the review left findings open.
    )
