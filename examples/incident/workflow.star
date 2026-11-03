# An incident desk: an alert opens an incident, an analyst proposes one mitigation from a runbook, an on-call person approves it before a deadline,
# and the desk runs it, checks the health of the service, and restores the earlier state if the service is not healthy.
# main is a watch. It takes the alerts from the spool and starts one run of incident for each alert. incident does the work:
#   1. It pages the first role and reads the diagnostics (GET calls only, scrubbed).
#   2. The analyst proposes a mitigation. The desk checks the proposal against the runbook table in policy.star.
#   3. The roles of the chain are paged one after the other. Each role has a deadline for its answer, set with the timeout of `wait`.
#   4. A safe row runs when nobody answers. A risky row never does, and the case goes to the commander.
#   5. The desk saves the earlier state, runs the mitigation under a key, and reads it back. Then it checks the health of the service.
#      A failure at any step restores the earlier state and holds the incident for a person.
#   6. The analyst drafts the postmortem from the timeline file that the desk wrote.
# Set INCIDENT_DIR to the absolute path of this folder. The incident home (default ~/.incident) holds the config, the credential header, the spool and the timelines.
# The ops API (OPS_API) is the one part that this example does not ship. It stands for the pager, the metrics store and the deploy control of one company.
# Set INCIDENT_MINUTE_SECONDS, INCIDENT_ROUNDS and INCIDENT_ROUND_SECONDS only to shorten a trial run. See policy.star.
load("//policy.star", "RISK", "ROLE", "ROUNDS", "ROUND_SECONDS", "RUNBOOK", "MINUTE_SECONDS", "chain_for", "check_mitigation")

# INCIDENT_DIR is the absolute path of this folder. A command runs outside a work folder, so the scripts are found by absolute path.
HERE = env("INCIDENT_DIR")
SCRIPT = struct(
    ACK = HERE + "/scripts/ack.sh",
    DIAGNOSE = HERE + "/scripts/diagnose.sh",
    INTAKE = HERE + "/scripts/intake.sh",
    MITIGATE = HERE + "/scripts/mitigate.sh",
    PAGE = HERE + "/scripts/page.sh",
    POSTMORTEM_SAVE = HERE + "/scripts/postmortem-save.sh",
    ROLLBACK = HERE + "/scripts/rollback.sh",
    SET_STATE = HERE + "/scripts/status.sh",
    SNAPSHOT = HERE + "/scripts/snapshot.sh",
    TIMELINE = HERE + "/scripts/timeline.sh",
    VERIFY = HERE + "/scripts/verify.sh",
)
ITEM_STATUS = struct(CLOSED = "closed", HOLD = "hold")
DECISION = struct(PROPOSE = "propose", NO_ACTION = "no-action", HUMAN = "human")
STATE = struct(MITIGATED = "mitigated", REJECTED = "rejected", NO_ACTION = "no-action")
PAGE = struct(HEADS_UP = "heads-up", HANDOFF = "handoff")
ANSWER = struct(APPROVED = "approved", REJECTED = "rejected", NONE = "none", UNREACHED = "unreached")

MODEL = "sonnet"
INTAKE_GRACE_SECONDS = 60
VERIFY_GRACE_MINUTES = 2
ANSWER_APPROVE = "approve"
DIAGNOSE_TIMEOUT = "3m"
ANALYST_TIMEOUT = "6m"
MITIGATE_TIMEOUT = "5m"
POSTMORTEM_TIMEOUT = "10m"
POLICY_APPROVER = "policy: no answer after the whole chain, safe row"
AGENT_FORBIDDEN = ["git-write", "remote-delete", "local-delete", "opaque-command"]
ANALYST_OUTPUTS = ["decision", "mitigation", "summary", "confidence", "procedure_note"]

HALT = struct(done = True)
UNANSWERED = struct(kind = ANSWER.NONE, role = "", text = "", raw = "")
UNREACHED = struct(kind = ANSWER.UNREACHED, role = "", text = "", raw = "")

def go(**fields):
    return struct(done = False, **fields)

def script(path, *args):
    return ["sh", path] + list(args)

def close_item(item):
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

def hold_item(item):
    item_status(name = "hold", id = item.id, status = ITEM_STATUS.HOLD)

def open_item(item, round_number, position, alert):
    made = item_create(
        name = "incident-%d-%d" % (round_number, position),
        title = "%s: %s" % (alert["severity"], alert["title"]),
        key = alert["key"],
        body = json.encode(alert),
        parent = item.id,
    )
    return made.id

def open_items(item, round_number, intake_lines):
    ids = []
    for line in intake_lines.splitlines():
        if line.strip() != "":
            ids.append(open_item(item, round_number, len(ids), json.decode(line)))
    return ids

def watch_round(item, round_number):
    got = command(
        name = "intake-%d" % round_number,
        run = script(SCRIPT.INTAKE, str(ROUND_SECONDS)),
        timeout = "%ds" % (ROUND_SECONDS + INTAKE_GRACE_SECONDS),
    )
    ids = open_items(item, round_number, got.stdout)
    if len(ids) == 0:
        return struct(runs = [], count = 0)
    command(name = "ack-%d" % round_number, run = script(SCRIPT.ACK), stdin = got.stdout)
    started = spawn(ids, entry = "incident", name = "start-%d" % round_number)
    return struct(runs = started.runs, count = len(ids))

def watch(item):
    runs = []
    count = 0
    for round_number in range(ROUNDS):
        result = watch_round(item, round_number)
        runs += result.runs
        count += result.count
    return struct(runs = runs, count = count)

def close_watch(item, text):
    item_comment(name = "report", id = item.id, key = "report", text = text)
    close_item(item)

def await_incidents(watched):
    done = gather(watched.runs, fail = "return", name = "wait-incidents")
    finished = len([result for result in done.results if result.ok])
    return "%d incidents: %d runs finished, %d need a person. See the child items." % (watched.count, finished, watched.count - finished)

def main(item):
    watched = watch(item)
    if len(watched.runs) == 0:
        return close_watch(item, "No incidents in this watch.")
    close_watch(item, await_incidents(watched))

def note(item, label, text):
    command(name = "timeline-" + label, run = script(SCRIPT.TIMELINE, "add", item.id, text))

def hand_to_person(item, why):
    note(item, "handoff", "HANDOFF: " + why)
    paged = command(name = "page-commander", run = script(SCRIPT.PAGE, item.id, ROLE.COMMANDER, PAGE.HANDOFF), stdin = why)
    if paged.rc != 0:
        note(item, "page-commander-failed", "the handoff page to the commander failed (rc %d); the incident stays held" % paged.rc)
    item_comment(name = "reason", id = item.id, key = "reason", text = why)
    hold_item(item)

def halt_to_person(item, why):
    hand_to_person(item, why)
    return HALT

def restore(item, service, key):
    back = command(name = "rollback", run = script(SCRIPT.ROLLBACK, item.id, service, key))
    note(item, "rollback", "restore of the earlier state: rc %d" % back.rc)
    return back.rc == 0

def read_case(item):
    alert = json.decode(item.body)
    return struct(severity = alert["severity"], title = alert["title"], service = alert["service"])

def open_incident(item, case, chain):
    note(item, "opened", "opened: %s (%s) on %s" % (case.title, case.severity, case.service))
    command(name = "page-first", run = script(SCRIPT.PAGE, item.id, chain.heads_up, PAGE.HEADS_UP), stdin = case.title)

def diagnose(item):
    diag = command(name = "diagnose", run = script(SCRIPT.DIAGNOSE), stdin = item.body, timeout = DIAGNOSE_TIMEOUT)
    if diag.rc != 0:
        return halt_to_person(item, "the diagnostics could not be read (rc %d)" % diag.rc)
    return go(text = diag.stdout)

def incident_brief(item, diagnostics):
    return item.body + "\n\nDiagnostics:\n" + diagnostics

def ask_analyst(brief):
    return agent(
        name = "analyst",
        prompt = "prompts/analyst.md",
        model = MODEL,
        edits = False,
        forbid_commands = AGENT_FORBIDDEN,
        inputs = {"incident": brief, "runbook": json.encode(sorted(RUNBOOK.keys()))},
        outputs = ANALYST_OUTPUTS,
        timeout = ANALYST_TIMEOUT,
    )

def analyst_problem(analysis):
    if analysis.stop != "":
        return "the analyst stopped at the %s cap" % analysis.stop
    if analysis.violations != "":
        return "the analyst tried a refused command: %s" % analysis.violations
    return ""

def record_analysis(item, analysis):
    note(item, "analysis", "analyst: %s, confidence %s. %s" % (analysis.decision, analysis.confidence, analysis.summary))
    if analysis.procedure_note != "":
        item_create(name = "procedure", title = "[procedure-owner] %s" % item.title, key = "proc-" + item.id, body = analysis.procedure_note, parent = item.id)

def finish_without_action(item, analysis):
    command(name = "status-clear", run = script(SCRIPT.SET_STATE, item.id, STATE.NO_ACTION), stdin = analysis.summary)
    close_item(item)
    return HALT

def ask_for_person(item, analysis):
    return halt_to_person(item, "the analyst asks for a person: %s" % analysis.summary)

DECISION_ENDINGS = {
    DECISION.NO_ACTION: finish_without_action,
    DECISION.HUMAN: ask_for_person,
}

def route_decision(item, analysis):
    if analysis.decision == DECISION.PROPOSE:
        return go(verdict = analysis)
    return DECISION_ENDINGS.get(analysis.decision, ask_for_person)(item, analysis)

def analyze(item, brief):
    analysis = ask_analyst(brief)
    problem = analyst_problem(analysis)
    if problem != "":
        return halt_to_person(item, problem)
    record_analysis(item, analysis)
    return route_decision(item, analysis)

def plan_mitigation(item, case, analysis, brief):
    checked = check_mitigation(analysis.mitigation)
    if checked.problem != "":
        return halt_to_person(item, "the proposal is not valid: %s" % checked.problem)
    proposal = checked.mitigation
    kind = proposal["kind"]
    return go(
        kind = kind,
        args = proposal["args"],
        row = RUNBOOK[kind],
        proposal = json.encode(proposal),
        service = proposal["args"].get("service", case.service),
        key = "%s-%s" % (item.id, kind),
        summary = analysis.summary,
        brief = brief,
    )

def investigate(item, case):
    found = diagnose(item)
    if found.done:
        return found
    brief = incident_brief(item, found.text)
    analysis = analyze(item, brief)
    if analysis.done:
        return analysis
    return plan_mitigation(item, case, analysis.verdict, brief)

def approver_name(text):
    return text[len(ANSWER_APPROVE):].lstrip(": ").strip() or "an unnamed person"

def approved_answer(item, level, text):
    by = "%s (%s)" % (approver_name(text), level.role)
    note(item, "approved", "approved by " + by)
    return struct(kind = ANSWER.APPROVED, role = level.role, text = by, raw = text)

def rejected_answer(item, level, text):
    note(item, "rejected", "rejected by %s: %s" % (level.role, text))
    return struct(kind = ANSWER.REJECTED, role = level.role, text = text, raw = text)

def unanswered(item, level):
    note(item, "unanswered-" + level.role, "no answer from %s inside %d minutes" % (level.role, level.minutes))
    return UNANSWERED

def question_for(level, plan):
    return "[%s] Approve %s? %s\nProposal: %s\nAnswer \"approve\" and your name, or any other text to reject. The answer is due in %d minutes." % (
        level.role,
        plan.kind,
        plan.summary,
        plan.proposal,
        level.minutes,
    )

# The deadline of a role is the timeout of the wait. The wait is durable: a restart of the desk keeps the question and the deadline.
def wait_for_answer(item, level, plan):
    ruling = wait(question_for(level, plan), name = "approve-" + level.role, timeout = "%ds" % (level.minutes * MINUTE_SECONDS))
    if ruling.timed_out:
        return unanswered(item, level)
    if ruling.answer.startswith(ANSWER_APPROVE):
        return approved_answer(item, level, ruling.answer)
    return rejected_answer(item, level, ruling.answer)

def ask_level(item, level, plan):
    paged = command(name = "page-" + level.role, run = script(SCRIPT.PAGE, item.id, level.role, "approve? " + plan.summary), stdin = plan.proposal)
    if paged.rc != 0:
        note(item, "page-failed-" + level.role, "the page to %s failed (rc %d)" % (level.role, paged.rc))
        return UNREACHED
    note(item, "paged-" + level.role, "paged %s; the answer is due in %d minutes" % (level.role, level.minutes))
    return wait_for_answer(item, level, plan)

# "Nobody answered" needs at least one person who was paged. If every page failed, the result is UNREACHED and the desk fails closed.
def collect_answer(item, chain, plan):
    reached = False
    for level in chain.levels:
        answer = ask_level(item, level, plan)
        if answer.kind == ANSWER.NONE:
            reached = True
        elif answer.kind != ANSWER.UNREACHED:
            return answer
    return UNANSWERED if reached else UNREACHED

def reject(item, answer):
    command(name = "status-rejected", run = script(SCRIPT.SET_STATE, item.id, STATE.REJECTED), stdin = answer.raw)
    item_comment(name = "rejected", id = item.id, key = "rejected", text = "Rejected by %s: %s" % (answer.role, answer.text))
    hold_item(item)
    return HALT

def approve_by_policy(item, plan):
    if plan.row.risk != RISK.SAFE:
        return halt_to_person(item, "nobody answered the whole chain, and %s is not a safe mitigation" % plan.kind)
    note(item, "no-answer", "nobody answered; %s is safe, so the desk runs it" % plan.kind)
    told = command(name = "page-commander-fyi", run = script(SCRIPT.PAGE, item.id, ROLE.COMMANDER, "no answer; running " + plan.kind), stdin = plan.proposal)
    if told.rc != 0:
        return halt_to_person(item, "nobody answered, and the commander could not be told (rc %d), so the desk did not run %s" % (told.rc, plan.kind))
    return go(by = POLICY_APPROVER)

def approve(item, chain, plan):
    answer = collect_answer(item, chain, plan)
    if answer.kind == ANSWER.REJECTED:
        return reject(item, answer)
    if answer.kind == ANSWER.APPROVED:
        return go(by = answer.text)
    if answer.kind == ANSWER.UNREACHED:
        return halt_to_person(item, "nobody could be paged, so the desk did not run %s" % plan.kind)
    return approve_by_policy(item, plan)

def save_state(item, plan):
    snap = command(name = "snapshot", run = script(SCRIPT.SNAPSHOT, item.id, plan.service))
    if snap.rc != 0:
        return halt_to_person(item, "the earlier state of %s could not be saved, so the desk did not run %s" % (plan.service, plan.kind))
    return go()

def write_mitigation(item, plan):
    note(item, "mitigate", "running %s %s" % (plan.kind, json.encode(plan.args)))
    wrote = command(name = "mitigate", run = script(SCRIPT.MITIGATE, plan.kind, plan.key), stdin = json.encode(plan.args), timeout = MITIGATE_TIMEOUT)
    if wrote.rc != 0:
        restored = restore(item, plan.service, plan.key)
        return halt_to_person(item, "%s failed (rc %d); %s" % (plan.kind, wrote.rc, "the desk restored the earlier state" if restored else "the earlier state was NOT restored"))
    return go()

def check_health(item, plan):
    minutes = plan.row.verify_minutes
    healthy = command(name = "verify", run = script(SCRIPT.VERIFY, plan.service, str(minutes)), timeout = "%dm" % (minutes + VERIFY_GRACE_MINUTES))
    if healthy.rc != 0:
        restored = restore(item, plan.service, plan.key)
        return halt_to_person(item, "%s did not restore health in %d minutes; earlier state restored: %s" % (plan.kind, minutes, restored))
    return go()

def run_saga(item, plan, approved_by):
    saved = save_state(item, plan)
    if saved.done:
        return saved
    wrote = write_mitigation(item, plan)
    if wrote.done:
        return wrote
    healthy = check_health(item, plan)
    if healthy.done:
        return healthy
    note(item, "healthy", "healthy after %s; approved by %s" % (plan.kind, approved_by))
    command(name = "status-mitigated", run = script(SCRIPT.SET_STATE, item.id, STATE.MITIGATED), stdin = "%s done under key %s" % (plan.kind, plan.key))
    return go()

def draft_postmortem(item, plan):
    timeline = command(name = "timeline-show", run = script(SCRIPT.TIMELINE, "show", item.id))
    written = agent(
        name = "postmortem",
        prompt = "prompts/postmortem.md",
        model = MODEL,
        edits = False,
        forbid_commands = AGENT_FORBIDDEN,
        inputs = {"timeline": timeline.stdout, "incident": plan.brief, "summary": plan.summary},
        outputs = ["draft"],
        timeout = POSTMORTEM_TIMEOUT,
    )
    return written.draft

def publish_postmortem(item, plan, approved_by, draft):
    saved = command(name = "postmortem-save", run = script(SCRIPT.POSTMORTEM_SAVE, item.id), stdin = draft)
    if saved.rc != 0:
        item_comment(name = "pm-failed", id = item.id, key = "pm-failed", text = "The postmortem draft was not saved (rc %d)." % saved.rc)
        return hold_item(item)
    item_comment(name = "done", id = item.id, key = "done", text = "%s done under key %s. Approved by %s. Postmortem draft: %s" % (plan.kind, plan.key, approved_by, saved.stdout.strip()))
    close_item(item)

def incident(item):
    case = read_case(item)
    chain = chain_for(case.severity)
    open_incident(item, case, chain)
    plan = investigate(item, case)
    if plan.done:
        return
    approval = approve(item, chain, plan)
    if approval.done:
        return
    saga = run_saga(item, plan, approval.by)
    if saga.done:
        return
    publish_postmortem(item, plan, approval.by, draft_postmortem(item, plan))
