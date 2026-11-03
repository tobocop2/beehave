# A research desk: a question comes in, and a report goes to a wiki. The item is the question (its body). The project has no source and no repository.
#   1. A planner agent splits the question into sections. The plan is saved in the store; a run on an item that has a plan keeps it.
#   2. One child run for each section (entry `section`): search and fetch by script, a reader agent extracts claims with exact quotes, a script tests
#      each quote against the page snapshot, a fresh judge agent grades the claims, and a writer agent writes the section from the claims that passed.
#      The judge also gets one planted false claim. A judge that accepts it is void, and its verdicts are not recorded.
#      A program gate checks the prose: every paragraph cites a supported claim, and every number is in a cited quote.
#   3. An abstract agent writes the abstract of the sections.
#   4. A person reviews the draft with `wait`: "approve", or "redo N: a note". A redo runs section N again as a new generation, then the abstract again.
#   5. After the approval, a program gate reads every cited source again. A quote that left its page stops the item before the publish.
#   6. The publish script writes the page with a key from the run and the report hash, reads it back and compares the hash.
# Set RESEARCH_DESK_DIR to the absolute path of this folder, RESEARCH_HOME to the folder of the store, SEARCH_API to a SearXNG server and WIKI_API to the
# page store. MAX_AGENT_CALLS (default 40) caps the agent calls of one item across all its runs. The cap counts calls, not dollars.
# The sections run one after the other: the call counter and the order of the claim ids are then the same on every run.
# Run with `obee run <item> research-desk --workflow workflow.star`. Answer a wait with `obee answer <item> approve`.
load("//answer.star", "ANSWER", "parse_answer")

DESK = env("RESEARCH_DESK_DIR")
STORE = env("RESEARCH_HOME")
SEARCH_API = env("SEARCH_API", default = "")
WIKI_API = env("WIKI_API", default = "")
CREDENTIAL = env("RESEARCH_HEADER_FILE", default = "")
MAX_AGENT_CALLS = int(env("MAX_AGENT_CALLS", default = "40"))
MAX_SOURCES = 3
MAX_ROUNDS = 5
MAX_BUDGET_ASKS = 3
AGENT_TIMEOUT = "10m"
# The deadlines of the two waits. When nobody answers in time, `wait` returns timed_out and the run fails with the reason. The plan and the finished
# sections stay in the store. Both deadlines are set by the environment, for a shorter time in a test.
REVIEW_TIMEOUT = env("REVIEW_TIMEOUT", default = "72h")
BUDGET_TIMEOUT = env("BUDGET_TIMEOUT", default = "24h")
MODEL = "sonnet"
READ_ONLY_FORBIDDEN = ["git-write", "local-delete", "remote-delete", "opaque-command"]
ITEM_CLOSED = "closed"
NOT_SHOWN = "Not shown: %s"

def tool(label, script, args, stdin = ""):
    """Runs one script of tools/ with the store and the addresses in its environment."""
    return command(
        name = label,
        run = ["sh", DESK + "/tools/" + script] + args,
        stdin = stdin,
        env = {"RESEARCH_HOME": STORE, "SEARCH_API": SEARCH_API, "WIKI_API": WIKI_API, "RESEARCH_HEADER_FILE": CREDENTIAL},
    )

def program_gate(label, script, args):
    """A program gate: the exit code of the script is the verdict, and its output is the report. A gate call has no env argument, so the store is an argument."""
    return gate(name = label, run = ["sh", DESK + "/tools/" + script, STORE] + args)

def ok(label, result):
    if result.rc != 0:
        fail("REFUSE: %s exited with code %d. %s %s" % (label, result.rc, result.stdout, result.stderr))
    return result.stdout

def take_call(run, label):
    """Counts one agent call before the call. The counter is in the store, so it holds across the runs of one item."""
    for _ in range(MAX_BUDGET_ASKS):
        taken = tool("take-" + label, "store.sh", ["take-call", run, str(MAX_AGENT_CALLS)])
        if taken.rc == 0:
            return
        if taken.rc != 2:
            ok("take-call", taken)
        # The budget is used up. The run waits for a person up to the deadline, so a raise continues the same run.
        ruling = wait("%s Answer the number of calls to add, such as 10." % taken.stdout.strip(), name = "budget", timeout = BUDGET_TIMEOUT)
        if ruling.timed_out:
            fail("REFUSE: no answer to the budget question within %s. The plan and the finished sections stay in the store." % BUDGET_TIMEOUT)
        if ruling.answer.strip().isdigit():
            ok("raise-cap", tool("raise-cap", "store.sh", ["raise-cap", run, str(MAX_AGENT_CALLS), ruling.answer.strip()]))
    fail("REFUSE: the run used all its agent calls and the budget was not raised.")

def call_agent(run, label, prompt, inputs, outputs):
    take_call(run, label)
    answer = agent(
        name = label,
        prompt = prompt,
        model = MODEL,
        edits = False,
        forbid_commands = READ_ONLY_FORBIDDEN,
        inputs = inputs,
        outputs = outputs,
        timeout = AGENT_TIMEOUT,
    )
    if answer.stop != "":
        fail("REFUSE: the %s agent stopped at the %s cap" % (label, answer.stop))
    if len(answer.violations) > 0:
        fail("REFUSE: the %s agent tried a refused command: %s" % (label, answer.violations[0]))
    return answer

def plan_lines(run):
    got = tool("get-plan", "store.sh", ["get-plan", run])
    if got.rc != 0:
        return []
    return [line.split("\t") for line in got.stdout.splitlines() if line.strip() != ""]

def main(item):
    run = item.id
    ok("init", tool("init", "store.sh", ["init", run]))
    sections = plan_lines(run)
    if len(sections) == 0:
        planned = call_agent(run, "plan", "prompts/plan.md", {"question": item.body}, ["sections"])
        ok("put-plan", tool("put-plan", "store.sh", ["put-plan", run], stdin = planned.sections))
        sections = plan_lines(run)
    for line in sections:
        run_section(item, run, int(line[0]), ok("gen", tool("gen-" + line[0], "store.sh", ["gen", run, line[0]])).strip())
    write_abstract(item, run)
    review(item, run, len(sections))
    stale = program_gate("staleness", "staleness.sh", [run])
    if not stale.passed:
        fail("REFUSE: a quote left its page, so nothing is published. %s" % stale.report)
    published = tool("publish", "publish.sh", [run])
    if published.rc != 0:
        fail("REFUSE: the publish exited with code %d. %s" % (published.rc, published.stderr))
    item_comment(name = "report", id = item.id, key = "report", text = published.stdout.strip())
    item_status(name = "close", id = item.id, status = ITEM_CLOSED)

def run_section(item, run, n, gen):
    """One child run for the generation of a section. A generation that is done is not run again, so a restart keeps the finished sections."""
    if tool("status-%d-%s" % (n, gen), "store.sh", ["status", run, str(n), gen]).stdout.strip() == "done":
        return
    title_question = [line for line in plan_lines(run) if line[0] == str(n)][0]
    made = item_create(
        name = "section",
        title = "%s: %s" % (run, title_question[1]),
        key = "%s-s%d-g%s" % (run, n, gen),
        body = json.encode({"run": run, "n": n, "gen": gen, "title": title_question[1], "question": title_question[2]}),
        parent = item.id,
    )
    spawned = spawn([made.id], entry = "section", name = "spawn-%d-%s" % (n, gen))
    result = gather(spawned.runs, fail = "return", name = "wait-%d-%s" % (n, gen)).results[0]
    if not result.ok:
        fail("REFUSE: section %d stopped. See the child item %s. %s" % (n, made.id, result.error))

def write_abstract(item, run):
    text = ok("sections-text", tool("sections-text", "store.sh", ["sections-text", run]))
    written = call_agent(run, "abstract", "prompts/abstract.md", {"question": item.body, "sections": text}, ["abstract"])
    ok("set-abstract", tool("set-abstract", "store.sh", ["set-abstract", run], stdin = written.abstract))

def review(item, run, count):
    """The person reads the draft. A redo runs one section again as a new generation, then the abstract again. The rounds are capped."""
    problem = ""
    for round in range(1, MAX_ROUNDS + 1):
        draft = ok("assemble", tool("assemble", "assemble.sh", [run]))
        ruling = wait("%sReview the draft report of %s, round %d. Answer approve, or redo N: a note.\n\n%s" % (problem, run, round, draft), name = "review", timeout = REVIEW_TIMEOUT)
        if ruling.timed_out:
            fail("REFUSE: no answer to the review of the draft within %s. The plan and the finished sections stay in the store." % REVIEW_TIMEOUT)
        answer = parse_answer(ruling.answer, count)
        if answer.kind == ANSWER.APPROVE:
            return
        if answer.kind == ANSWER.BAD:
            problem = answer.problem + "\n"
            continue
        problem = ""
        gen = ok("bump-gen", tool("bump-gen-%d" % answer.n, "store.sh", ["bump-gen", run, str(answer.n)], stdin = answer.note)).strip()
        run_section(item, run, answer.n, gen)
        write_abstract(item, run)
    fail("REFUSE: the review used all %d rounds without an approval." % MAX_ROUNDS)

def not_shown(run, n, gen, why):
    ok("save-prose", tool("save-prose", "save-prose.sh", [run, str(n), gen], stdin = NOT_SHOWN % why))

def set_status(run, n, gen, status):
    ok("set-status", tool("set-status", "store.sh", ["status", run, str(n), gen, status]))

# One section: the entry of a child run. The item body names the run, the section, the generation, the title and the question.
def section(item):
    spec = json.decode(item.body)
    run = spec["run"]
    n = str(spec["n"])
    gen = spec["gen"]
    ok("reset", tool("reset", "claims.sh", ["reset", run, n, gen]))
    found = ok("search", tool("search", "search.sh", [spec["question"], str(MAX_SOURCES)]))
    pages = []
    for url in found.splitlines():
        if url.strip() != "":
            pages.append(ok("fetch", tool("fetch", "fetch.sh", [run, url.strip()])).strip())
    read = call_agent(run, "read", "prompts/read.md", {"question": spec["question"], "pages": "\n".join(pages)}, ["claims"])
    ok("add-claims", tool("add-claims", "claims.sh", ["add", run, n, gen], stdin = read.claims))
    if int(ok("count-quotes", tool("count-quotes", "claims.sh", ["count", run, n, gen, "quote"])).strip()) == 0:
        set_status(run, n, gen, "empty")
        return not_shown(run, n, gen, "no claim had a quote that the pages hold.")
    ok("plant", tool("plant", "claims.sh", ["plant", run, n, gen]))
    exported = ok("export", tool("export", "claims.sh", ["export", run, n, gen]))
    judged = call_agent(run, "judge", "prompts/judge.md", {"claims": exported}, ["verdicts"])
    recorded = tool("verdicts", "claims.sh", ["verdicts", run, n, gen], stdin = judged.verdicts)
    if recorded.rc == 3:
        set_status(run, n, gen, "judge-void")
        fail("REFUSE: " + recorded.stdout.strip())
    ok("verdicts", recorded)
    if int(ok("count-supported", tool("count-supported", "claims.sh", ["count", run, n, gen, "supported"])).strip()) == 0:
        set_status(run, n, gen, "thin")
        return not_shown(run, n, gen, "no claim passed the judge.")
    supported = ok("supported", tool("supported", "claims.sh", ["supported", run, n, gen]))
    note = ok("note", tool("note", "store.sh", ["note", run, n])).strip()
    written = call_agent(run, "write", "prompts/write.md", {"title": spec["title"], "question": spec["question"], "claims": supported, "note": note}, ["prose"])
    ok("save-prose", tool("save-prose", "save-prose.sh", [run, n, gen], stdin = written.prose))
    cites = program_gate("cites", "cites.sh", [run, n, gen])
    if not cites.passed:
        set_status(run, n, gen, "cites-failed")
        fail("REFUSE: the prose of section %s fails the citation check. %s" % (n, cites.report))
    set_status(run, n, gen, "done")
