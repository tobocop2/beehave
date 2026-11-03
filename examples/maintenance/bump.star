# The weekly bump of one repository. The body of the item is {"repo", "week", "base", "fingerprint", "table"}.
# tick.sh already decided: it files this item only for a repository that has news. A skip never reaches this file.
#   1. A small agent runs apply.sh, which updates the dependencies and commits the manifest files. The agent step is there for the worktree:
#      the engine makes the worktree of an item at its first agent step, and a command or gate step has none before that.
#   3. The gate tests the result. If it fails, an agent fixes the code, for at most two rounds.
#   4. If the tests still fail, the run waits for a person: "retry" runs two more fix rounds, "hold" (or no answer before the deadline) holds the item.
#      Nothing is pushed while the tests fail.
#   5. pr.sh pushes the branch and opens a draft pull request.
# Set UPKEEP_HOME to the folder of the example (default ~/.upkeep). Set RED_TIMEOUT only to shorten a trial run.

UPKEEP_FOLDER = env("UPKEEP_HOME", default = env("HOME") + "/.upkeep")
SCRIPTS = UPKEEP_FOLDER + "/scripts/"
FIX_ATTEMPTS = 2
RETRY_ASKS = 2
RED_TIMEOUT = env("RED_TIMEOUT", default = "72h")
TIMEOUT = struct(SCRIPT = "10m", UPDATE = "20m", FIX = "20m", PR = "5m")
OUTCOME = struct(OPENED = "opened", SKIPPED = "skipped", RED = "red", ERROR = "error")
STATUS = struct(CLOSED = "closed", HOLD = "hold")
VERDICT = struct(FIXED = "fixed", GAVE_UP = "gave-up", UPDATED = "updated", UNCHANGED = "unchanged")
UPDATE_FORBID = ["bulk-staging", "local-delete", "remote-delete", "git-author", "git-bypass", "config-read"]
ANSWER = struct(RETRY = "retry", HOLD = "hold")
FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "config-read", "protected-path"]

def sh(name, script, args = [], stdin = "", timeout = TIMEOUT.SCRIPT):
    return command(name = name, run = ["sh", SCRIPTS + script] + args, stdin = stdin, timeout = timeout)

def stop(outcome, detail, status, text = ""):
    return struct(outcome = outcome, detail = detail, status = status, text = text)

def read_job(item):
    body = json.decode(item.body)
    return struct(id = item.id, repo = body["repo"], week = body["week"], base = body["base"], fingerprint = body["fingerprint"], table = body["table"])

def update_deps(job):
    """The update runs through an agent step, because only an agent step makes the worktree of the item. The agent runs one script and reports its result."""
    update = agent(
        name = "update",
        prompt = "prompts/update.md",
        model = "haiku",
        forbid_commands = UPDATE_FORBID,
        inputs = {"command": "sh %sapply.sh %s" % (SCRIPTS, job.week)},
        outputs = ["verdict"],
        timeout = TIMEOUT.UPDATE,
    )
    if update.stop != "":
        return stop(OUTCOME.ERROR, "the update agent stopped: " + update.stop, STATUS.HOLD)
    if update.verdict == VERDICT.UNCHANGED:
        return stop(OUTCOME.SKIPPED, "the update changed no manifest file", STATUS.CLOSED)
    if update.verdict != VERDICT.UPDATED:
        return stop(OUTCOME.ERROR, "the update command failed", STATUS.HOLD)
    return None

def fix_attempt(tag, attempt, table, report):
    fix = agent(
        name = "fix-%s-%d" % (tag, attempt),
        prompt = "prompts/fix.md",
        model = "sonnet",
        forbid_commands = FORBID,
        protect_paths = [UPKEEP_FOLDER],
        inputs = {"updates": table, "report": report, "round": str(attempt)},
        outputs = ["verdict"],
        timeout = TIMEOUT.FIX,
    )
    return fix.stop == "" and fix.verdict != VERDICT.GAVE_UP

def fix_until_green(tag, table):
    check = gate(name = "check-%s-0" % tag)
    attempts = 0
    for attempt in range(1, FIX_ATTEMPTS + 1):
        if check.passed:
            break
        attempts = attempt
        if not fix_attempt(tag, attempt, table, check.report):
            break
        check = gate(name = "check-%s-%d" % (tag, attempt))
    return struct(check = check, attempts = attempts)

def ask_for_retry(job, ask, report):
    """The tests are red. The run waits for a person, and a restart keeps the question and the deadline."""
    text = "The tests of %s fail after the bump of %s and %d fix rounds. Answer retry for more fix rounds, or hold.\n%s" % (job.repo, job.week, FIX_ATTEMPTS, report)
    ruling = wait(text, name = "red-%d" % ask, timeout = RED_TIMEOUT, choices = [ANSWER.RETRY, ANSWER.HOLD])
    return (not ruling.timed_out) and ruling.answer == ANSWER.RETRY

def verify(job, table):
    result = fix_until_green("first", table)
    attempts = result.attempts
    for ask in range(RETRY_ASKS):
        if result.check.passed or not ask_for_retry(job, ask, result.check.report):
            break
        result = fix_until_green("retry-%d" % ask, table)
        attempts += result.attempts
    if result.check.passed:
        return struct(stop = None, attempts = attempts)
    return struct(stop = stop(OUTCOME.RED, "the tests fail after the bump", STATUS.HOLD, result.check.report), attempts = attempts)

def pull_request_text(job, table, attempts):
    return "Weekly bump of %s for %s.\n\nUpdates (ecosystem, name, from, to):\n\n%s\n\nThe gate passed after %d fix rounds." % (job.repo, job.week, table, attempts)

def publish(job, attempts):
    text = pull_request_text(job, job.table, attempts)
    pr = sh("pr", "pr.sh", [job.repo, job.week, job.fingerprint, job.base], stdin = text, timeout = TIMEOUT.PR)
    if pr.rc != 0:
        return stop(OUTCOME.ERROR, "the push or the pull request failed", STATUS.HOLD)
    return stop(OUTCOME.OPENED, pr.stdout.strip(), STATUS.CLOSED)

def conclude(item, job, ending):
    if ending.outcome != OUTCOME.OPENED:
        sh("log", "log.sh", [job.week, job.repo, ending.outcome, ending.detail])
    item_comment(name = "outcome", id = item.id, key = "outcome", text = "%s: %s\n%s" % (ending.outcome, ending.detail, ending.text))
    item_status(name = "end", id = item.id, status = ending.status)

def main(item):
    job = read_job(item)
    halted = update_deps(job)
    if halted:
        return conclude(item, job, halted)
    checked = verify(job, job.table)
    if checked.stop:
        return conclude(item, job, checked.stop)
    conclude(item, job, publish(job, checked.attempts))
