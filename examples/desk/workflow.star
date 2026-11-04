# A refund desk for a travel company: stateless clerks propose, and the desk owns every write.
# A shift reads every channel (email and chat), masks the personal data, and makes one item for each new message. Each case then runs in its own run:
#   1. The clerk reads the masked case and the live facts through read-only scripts, and proposes an intent. The clerk writes nothing.
#   2. The desk checks the intent against the policy row, then asks the system of record whether the case is eligible.
#   3. The tier of the policy row decides the approval. Under the limit of a contain or transact tier the desk approves by policy. Above it, `wait` holds the case for a person.
#   4. The saga runs each write with the `write` call: a read-back first, so a replay never writes twice, and an undo for each write that has one.
#   5. A refusal at any step goes to the exception queue by its reason code. A failed saga fails the case run, and the engine runs the undos.
# Set DESK_DIR to the absolute path of this folder. The desk home (default ~/.desk) holds the config, the credential header, the vault, the sent log and the gold sets.
# Run a shift with `beehave run <shift> desk --workflow workflow.star`, where the item <shift> is an item you add with `beehave item add`.
# `beehave status` shows a case that waits for a person. Answer with `beehave answer <case> approve` (or any other text to reject), then run the shift again.
# The system of record (DESK_API) is the one part that this example does not ship: the scripts in gateway/ show the calls that it must answer.
# tests/run.sh runs every script against stand-ins for curl and sendmail.
load("//policy.star", "ITEM_STATUS", "NO_UNDO", "POLICY", "REASON", "ROUTES", "TEAM", "TIER")
load("//clerk.star", "DECISION", "check_intent", "clerk")

# DESK_DIR is the absolute path of this folder. A shift has no work folder, so the scripts are found by absolute path.
DESK = env("DESK_DIR")
SCRIPT = struct(
    INTAKE = DESK + "/gateway/intake.sh",
    ACK = DESK + "/gateway/ack.sh",
    READ_CASE = DESK + "/gateway/read-case.sh",
    ELIGIBLE = DESK + "/gateway/eligible.sh",
    WRITE = DESK + "/gateway/write.sh",
    READ_BACK = DESK + "/gateway/read-back.sh",
    REPLY = DESK + "/gateway/reply.sh",
)
RC = struct(OK = 0, NOT_ELIGIBLE = 1)
INTAKE_TIMEOUT = "5m"
ANSWER_APPROVE = "approve"

# One shift. The item is the shift, such as shift-2026-11-01-am. Each case runs as its own run of case().
def main(item):
    intake = command(name = "intake", run = ["sh", SCRIPT.INTAKE], timeout = INTAKE_TIMEOUT)
    if intake.rc != RC.OK:
        fail("REFUSE: the intake exited with code %d, so the shift cannot tell a quiet shift from a broken one. %s" % (intake.rc, intake.stderr))
    cases = make_case_items(item, intake.stdout)
    command(name = "ack", run = ["sh", SCRIPT.ACK], stdin = intake.stdout)
    if len(cases) == 0:
        return close_with_report(item, "No new cases.")
    results = run_one_by_one(cases)
    summary = "%d cases: %d finished, %d need a person. See the child items." % (len(cases), finished_count(results), held_count(results))
    close_with_report(item, summary)

# Each case is its own run, and the runs go one after the other. Two runs that start together both make a work folder from the clone of the desk
# repository, and the engine does not yet lock that step: the second one can fail on the lock of the git config (issue #241). When the engine locks it,
# start all the cases with one spawn call and one gather call.
def run_one_by_one(cases):
    results = []
    for case_id in cases:
        runs = spawn([case_id], entry = "case", name = "spawn-" + case_id)
        results += gather(runs.runs, fail = "return", name = "wait-" + case_id).results
    return results

def make_case_items(shift, intake_lines):
    cases = []
    for line in intake_lines.splitlines():
        if line.strip() != "":
            cases.append(make_case_item(shift, json.decode(line)))
    return cases

# The message id is the key, so a second intake of the same message makes no second item.
def make_case_item(shift, msg):
    made = item_create(
        name = "case",
        title = "%s: %s" % (msg["channel"], msg["subject"]),
        key = msg["key"],
        body = json.encode(msg),
        parent = shift.id,
    )
    return made.id

def close_with_report(item, text):
    item_comment(name = "report", id = item.id, key = "report", text = text)
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

def finished_count(results):
    return len([result for result in results if result.ok])

def held_count(results):
    return len([result for result in results if not result.ok or result.stopped])

def refusal(code, detail):
    return struct(code = code, detail = detail)

def answer_customer(item, msg, rejection):
    command(name = "reply-no", run = ["sh", SCRIPT.REPLY, msg["channel"], msg["key"] + "-no", msg["from"]], stdin = rejection.detail)
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

def hold_for_team(item, msg, rejection):
    item_create(
        name = "route",
        title = "[%s] %s" % (ROUTES[rejection.code], item.title),
        key = "route-" + item.id,
        body = "Reason %s. %s" % (rejection.code, rejection.detail),
        parent = item.id,
    )
    item_status(name = "hold", id = item.id, status = ITEM_STATUS.HOLD)

TEAM_HANDLERS = {TEAM.CUSTOMER: answer_customer}

# The exception queue: the reason code on the case, then the handler of the team that owns the code.
def route(item, msg, rejection):
    item_comment(name = "reason", id = item.id, key = "reason", text = "%s: %s" % (rejection.code, rejection.detail))
    TEAM_HANDLERS.get(ROUTES[rejection.code], hold_for_team)(item, msg, rejection)

def first_of(values):
    for value in values:
        return value
    return ""

def clerk_trouble(answer):
    if answer.stop != "":
        return refusal(REASON.SYSTEM_FAILURE, "the clerk stopped at the %s cap" % answer.stop)
    if len(answer.violations) > 0:
        return refusal(REASON.SYSTEM_FAILURE, "the clerk tried a refused command: %s" % first_of(answer.violations))
    return None

def file_procedure_note(item, answer):
    if answer.procedure_note != "":
        item_create(name = "procedure", title = "[procedure-owner] %s" % item.title, key = "proc-" + item.id, body = answer.procedure_note, parent = item.id)

def decision_refusal(answer):
    if answer.decision == DECISION.PROPOSE:
        return None
    code = REASON.CLERK_DECLINED if answer.decision == DECISION.DECLINE else REASON.NEEDS_JUDGMENT
    return refusal(code, answer.note)

def proposal(rejection, answer):
    return struct(refusal = rejection, clerk = answer)

# Live facts through the read-only gateway, then the clerk proposes. The clerk never writes.
def propose(item):
    facts = command(name = "read-case", run = ["sh", SCRIPT.READ_CASE], stdin = item.body)
    if facts.rc != RC.OK:
        return proposal(refusal(REASON.SYSTEM_FAILURE, "the gateway could not read the case"), None)
    answer = clerk(item, item.body + "\n\nFacts:\n" + facts.stdout)
    trouble = clerk_trouble(answer)
    if trouble != None:
        return proposal(trouble, answer)
    file_procedure_note(item, answer)
    return proposal(decision_refusal(answer), answer)

def eligibility_refusal(intent):
    eligible = command(name = "eligible", run = ["sh", SCRIPT.ELIGIBLE, intent["kind"]], stdin = json.encode(intent["args"]))
    if eligible.rc == RC.NOT_ELIGIBLE:
        return refusal(REASON.NOT_ELIGIBLE, eligible.stdout)
    if eligible.rc != RC.OK:
        return refusal(REASON.SYSTEM_FAILURE, "the eligibility check failed")
    return None

def plan(rejection, intent):
    row = POLICY[intent["kind"]] if intent != None else None
    return struct(refusal = rejection, intent = intent, row = row)

# The orchestrator checks the intent, then eligibility against the live system. Nothing here trusts the clerk.
def vet(answer):
    checked = check_intent(answer.intent)
    if checked.intent == None:
        return plan(refusal(REASON.BAD_INTENT, checked.problem), None)
    return plan(eligibility_refusal(checked.intent), checked.intent)

def approved(by):
    return struct(refusal = None, by = by)

def declined(detail):
    return struct(refusal = refusal(REASON.NEEDS_JUDGMENT, detail), by = "")

def tier_off(item, answer, case_plan):
    return declined("the %s intent is off" % case_plan.intent["kind"])

def tier_shadow(item, answer, case_plan):
    item_comment(name = "shadow", id = item.id, key = "shadow", text = answer.intent)
    return declined("shadow tier: a person works the case; the proposal is on the item")

def ask_a_person(item, answer, case_plan):
    row = case_plan.row
    question = "[%s] The clerk proposes %s and this reply:\n%s\nAnswer approve or reject, then a reason." % (row.approvers, answer.intent, answer.reply)
    ruling = wait(question, name = "approve")
    if not ruling.answer.startswith(ANSWER_APPROVE):
        return declined(ruling.answer)
    return approved("%s: %s" % (row.approvers, ruling.answer))

def approve_by_policy(item, answer, case_plan):
    return approved("policy: tier %s, limit %d" % (case_plan.row.tier, case_plan.row.auto_limit))

def contain(item, answer, case_plan):
    if case_plan.intent["args"].get("amount", 0) > case_plan.row.auto_limit:
        return ask_a_person(item, answer, case_plan)
    return approve_by_policy(item, answer, case_plan)

TIER_HANDLERS = {
    TIER.OFF: tier_off,
    TIER.SHADOW: tier_shadow,
    TIER.ASSIST: ask_a_person,
    TIER.CONTAIN: contain,
    TIER.TRANSACT: contain,
}

# The tier decides what happens to the proposal. The limit of the row holds at the contain tier and at the transact tier.
def approve(item, answer, case_plan):
    return TIER_HANDLERS[case_plan.row.tier](item, answer, case_plan)

# The engine owns the key, the read-back and the undo. The key is in BEEHAVE_WRITE_KEY for the write, the read-back and the undo, and it is the same
# on a replay, so a second run of the case cannot write twice. A read-back that exits 0 means the write is done, and the engine skips it.
def apply_step(step, payload):
    program = ["sh", SCRIPT.WRITE, step.write]
    back = ["sh", SCRIPT.READ_BACK, step.write]
    if step.undo == NO_UNDO:
        return write(name = step.write, run = program, read_back = back, stdin = payload)
    return write(name = step.write, run = program, read_back = back, undo = ["sh", SCRIPT.WRITE, step.undo], stdin = payload)

# The writes run in order. When a step fails, the case routes the failure and then fails, and the engine runs the undos of the earlier writes in reverse order.
def run_saga(row, payload):
    keys = []
    for step in row.saga:
        applied = apply_step(step, payload)
        if applied.rc != RC.OK:
            return struct(refusal = refusal(REASON.SYSTEM_FAILURE, "step %s failed after %d earlier steps; the earlier writes are undone" % (step.write, len(keys))), keys = keys)
        keys.append(applied.key)
    return struct(refusal = None, keys = keys)

def saga_payload(answer, case_plan, msg):
    return json.encode(struct(args = case_plan.intent["args"], reply = answer.reply, channel = msg["channel"], to = msg["from"]))

def settle(item, msg, answer, case_plan):
    approval = approve(item, answer, case_plan)
    if approval.refusal != None:
        return approval.refusal
    saga = run_saga(case_plan.row, saga_payload(answer, case_plan, msg))
    if saga.refusal != None:
        return saga.refusal
    done = "%s done under keys %s. Approved by %s." % (case_plan.intent["kind"], ", ".join(saga.keys), approval.by)
    close_with_done(item, done)
    return None

def close_with_done(item, text):
    item_comment(name = "done", id = item.id, key = "done", text = text)
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

# One case: propose, vet, then settle. A step that refuses sends the case to the exception queue.
def case(item):
    msg = json.decode(item.body)
    proposed = propose(item)
    if proposed.refusal != None:
        return route(item, msg, proposed.refusal)
    case_plan = vet(proposed.clerk)
    if case_plan.refusal != None:
        return route(item, msg, case_plan.refusal)
    rejection = settle(item, msg, proposed.clerk, case_plan)
    if rejection != None:
        route(item, msg, rejection)
        if rejection.code == REASON.SYSTEM_FAILURE:
            # The case run fails after the routing, so the engine undoes the writes that the saga made.
            fail("REFUSE: %s" % rejection.detail)
