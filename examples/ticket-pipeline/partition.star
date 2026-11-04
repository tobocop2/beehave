# One day of tickets, from the source to the warehouse. This file is also a workflow of its own: it runs one day alone, to retry a day that a backfill left out.
# The item body is JSON: {"day": "2026-01-31"}, and optionally "backfill" (the id that the budget counts under, default the item) and "budget_cents".
# A day that waits for a person (odd labels) asks with `wait`. Answer load or skip with `beehave answer <item> load`.
load("//version.star", "VERSION")

PIPE_DIR = env("PIPE_DIR")
PIPE_HOME = env("PIPE_HOME")
ODD_TIMEOUT = env("ODD_TIMEOUT", default = "72h")
DEFAULT_BUDGET_CENTS = 5000

FORBIDDEN_COMMANDS = ["config-read", "opaque-command", "local-delete", "remote-delete", "git-write", "git-bypass"]
STATUS = struct(CLOSED = "closed")
RC = struct(OK = 0, READY_OPEN = 2, BUDGET_SPENT = 3, VALIDATE_ODD = 1)

# The end states of a day. LOADED and EMPTY wrote to the warehouse. The others wrote nothing.
DAY = struct(LOADED = "loaded", EMPTY = "empty", SKIPPED = "skipped", OPEN = "open", BUDGET = "budget", ODD = "odd", FAILED = "failed")

CLASSIFY_MODEL = "sonnet"
CLASSIFY_TIMEOUT = "40m"
CLASSIFY_IDLE_TIMEOUT = "10m"
CLASSIFY_PROMPT = "prompts/classify.md"

def script_env(backfill, budget):
    return {"PIPE_HOME": PIPE_HOME, "PIPE_VERSION": VERSION, "PIPE_BACKFILL": backfill, "PIPE_BUDGET_CENTS": str(budget)}

def run_script(day, name, script, backfill, budget, stdin = ""):
    return command(name = "%s-%s" % (name, day), run = ["sh", PIPE_DIR + "/scripts/" + script, day], env = script_env(backfill, budget), stdin = stdin)

def failed(day, what, step):
    return struct(state = DAY.FAILED, note = "%s failed for %s with code %d: %s" % (what, day, step.rc, (step.stdout + step.stderr).strip()))

def classify(day):
    stage = PIPE_HOME + "/stage/" + day
    return agent(
        name = "classify-" + day,
        prompt = CLASSIFY_PROMPT,
        model = CLASSIFY_MODEL,
        edits = False,
        forbid_commands = FORBIDDEN_COMMANDS,
        inputs = {"day": day, "records": stage + "/records.jsonl", "allowed": PIPE_DIR + "/schema/labels.txt"},
        outputs = ["labels"],
        timeout = CLASSIFY_TIMEOUT,
        idle_timeout = CLASSIFY_IDLE_TIMEOUT,
    )

def ask_person(day, why, ask):
    """Ask a person to rule on a day with odd labels. A day in a chunk is not asked: it waits for a run of its own."""
    if not ask:
        return DAY.ODD
    ruling = wait("Day %s: %s\nAnswer load to load the day as it is, or skip to leave it out." % (day, why), name = "odd-" + day, choices = ["load", "skip"], timeout = ODD_TIMEOUT)
    if ruling.timed_out:
        fail("REFUSE: no answer about day %s within %s. The extract stays in the stage. Run beehave resume, then run it again." % (day, ODD_TIMEOUT))
    return ruling.answer

def load_day(day, backfill, budget, ask = False):
    """Takes one day from the source to the warehouse and returns struct(state, note). It fails only for a step that the engine refuses."""
    ready = run_script(day, "ready", "ready.sh", backfill, budget)
    if ready.rc == RC.READY_OPEN:
        return struct(state = DAY.OPEN, note = "the source has not closed " + day)
    if ready.rc != RC.OK:
        return failed(day, "ready", ready)
    extract = run_script(day, "extract", "extract.sh", backfill, budget)
    if extract.rc != RC.OK:
        return failed(day, "extract", extract)
    reserve = run_script(day, "budget", "budget.sh", backfill, budget)
    if reserve.rc == RC.BUDGET_SPENT:
        return struct(state = DAY.BUDGET, note = reserve.stdout.strip())
    if reserve.rc != RC.OK:
        return failed(day, "budget", reserve)
    if extract.stdout.strip().startswith("0 "):
        return finish_load(day, backfill, budget, DAY.EMPTY)
    labels = classify(day)
    if labels.stop != "":
        return struct(state = DAY.FAILED, note = "the classify step stopped at the %s cap for %s" % (labels.stop, day))
    if len(labels.violations) > 0:
        return struct(state = DAY.FAILED, note = "the classify step ran a refused command for %s: %s" % (day, labels.violations[0]))
    collect = run_script(day, "collect", "collect.sh", backfill, budget, stdin = labels.labels)
    if collect.rc != RC.OK:
        return failed(day, "collect", collect)
    check = run_script(day, "validate", "validate.sh", backfill, budget)
    if check.rc > RC.VALIDATE_ODD:
        return failed(day, "validate", check)
    if check.rc == RC.VALIDATE_ODD:
        answer = ask_person(day, check.stdout.strip(), ask)
        if answer == DAY.ODD:
            return struct(state = DAY.ODD, note = "%s: %s" % (day, check.stdout.strip()))
        if answer != "load":
            return struct(state = DAY.SKIPPED, note = "a person skipped " + day)
    return finish_load(day, backfill, budget, DAY.LOADED)

def finish_load(day, backfill, budget, state):
    loaded = run_script(day, "load", "load.sh", backfill, budget)
    if loaded.rc != RC.OK:
        return failed(day, "load", loaded)
    return struct(state = state, note = loaded.stdout.strip())

def main(item):
    job = json.decode(item.body)
    budget = int(job.get("budget_cents", DEFAULT_BUDGET_CENTS))
    day = load_day(job["day"], job.get("backfill", item.id), budget, ask = True)
    if day.state in (DAY.FAILED, DAY.OPEN, DAY.BUDGET):
        fail("REFUSE: %s. Fix the cause, run beehave resume %s, then run the item again." % (day.note, item.id))
    item_comment(name = "report", id = item.id, key = "report", text = "%s %s at version %s. %s" % (job["day"], day.state, VERSION, day.note))
    item_status(name = "close", id = item.id, status = STATUS.CLOSED)
