# An eval of one intent of the refund desk. The item body names the intent, such as refund. Set DESK_DIR to the absolute path of this folder.
# The clerk runs on each gold case as shadow work, and the judge grades each calibration reply. The scripts in evals/ score the run with a cost-weighted
# confusion matrix, and compare the judge with the two people who graded each reply. A good score proposes a tier change to a person. It never makes one.
# Run it with `beehave run <eval> desk --workflow eval.star`.
load("//policy.star", "ITEM_STATUS", "POLICY", "TIERS")
load("//clerk.star", "DECISION", "READ_ONLY_FORBIDDEN", "check_intent", "clerk")

# DESK_DIR is the absolute path of this folder. An eval run has no work folder, so the scripts are found by absolute path.
DESK = env("DESK_DIR")
SCRIPT = struct(
    GOLD = DESK + "/evals/gold.sh",
    CALIB = DESK + "/evals/calib.sh",
    SCORE = DESK + "/evals/score.sh",
    AGREEMENT = DESK + "/evals/agreement.sh",
    RECORD = DESK + "/evals/record.sh",
)
EXAM = struct(YES = "yes", NO = "no")
EXAM_LABEL = {EXAM.YES: "final exam"}
PRACTICE_LABEL = "practice"
GOLD_CASES = struct(item_name = "gold-case", label = "gold", key_mark = "")
CALIB_CASES = struct(item_name = "calib-case", label = "calib", key_mark = "c-")
RECORD_GROUP = struct(CASES = "cases", CALIB = "calib")
NOTHING_PROPOSED = struct(kind = "none", amount = 0)
ANSWER_YES = "yes"
JUDGE_MODEL = "sonnet"
JUDGE_OUTPUTS = ["grade"]

# One eval run of one intent. The item is the run, such as eval-refund-2026-11-01, and its body names the intent.
# Set EXAM=yes for a final exam: it scores the held-out slice, which a normal run never reads.
def main(item):
    run = read_run(item)
    cases = make_case_items(item, run)
    grade_all(cases)
    results = score_run(item, run)
    item_comment(name = "report", id = item.id, key = "report", text = results.report)
    propose_tier_change(item, run, results)
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

def read_run(item):
    kind = item.body.strip()
    if kind not in POLICY:
        fail("REFUSE: the item body must name an intent of policy.star, not %s" % kind)
    return struct(kind = kind, row = POLICY[kind], exam = env("EXAM", default = EXAM.NO))

def create_case_item(item, run, group, line):
    gold_case = json.decode(line)
    gold_case["run"] = item.id
    made = item_create(
        name = group.item_name,
        title = "%s %s %s" % (group.label, run.kind, gold_case["id"]),
        key = "%s-%s%s" % (item.id, group.key_mark, gold_case["id"]),
        body = json.encode(gold_case),
        parent = item.id,
    )
    return made.id

def create_case_items(item, run, group, lines):
    return [create_case_item(item, run, group, line) for line in lines.splitlines()]

# One item per gold case and one per calibration reply. The gold set lives outside every worktree, and its cases are masked.
def make_case_items(item, run):
    gold = command(name = "gold", run = ["sh", SCRIPT.GOLD, run.kind, run.exam])
    calib = command(name = "calib", run = ["sh", SCRIPT.CALIB, run.kind])
    return struct(
        gold = create_case_items(item, run, GOLD_CASES, gold.stdout),
        calib = create_case_items(item, run, CALIB_CASES, calib.stdout),
    )

# The clerk runs on every gold case as shadow work, and the judge on every calibration reply.
def grade_all(cases):
    for case_id in cases.gold:
        run_alone(case_id, "shadow")
    for case_id in cases.calib:
        run_alone(case_id, "calibrate")

# The runs go one after the other. Two runs that start together both make a work folder from the clone of the desk repository, and the engine does not yet
# lock that step: the second one can fail on the lock of the git config (issue #241). When the engine locks it, start the cases with one spawn call and one gather call.
def run_alone(case_id, entry):
    runs = spawn([case_id], entry = entry, name = "spawn-" + case_id)
    gather(runs.runs, fail = "return", name = "wait-" + case_id)

def last_line(text):
    lines = text.splitlines()
    return lines[len(lines) - 1]

# The score is a cost-weighted confusion matrix. The agreement shows where the judge may stand in for people.
def score_run(item, run):
    score = command(name = "score", run = ["sh", SCRIPT.SCORE, run.kind, item.id])
    agree = command(name = "agreement", run = ["sh", SCRIPT.AGREEMENT, run.kind, item.id])
    label = EXAM_LABEL.get(run.exam, PRACTICE_LABEL)
    report = "Eval of %s (%s):\n%s\n%s" % (run.kind, label, score.stdout, agree.stdout)
    return struct(report = report, cost = json.decode(last_line(score.stdout))["mean_cost"])

def earned_promotion(row, cost, level):
    return cost <= row.promote_at and level < len(TIERS) - 1

def earned_demotion(row, cost, level):
    return cost > row.demote_at and level > 0

def ask_for_promotion(item, run, results, level):
    if run.exam != EXAM.YES:
        item_comment(name = "next", id = item.id, key = "next", text = "The score allows a promotion. Run the final exam next (EXAM=yes).")
        return
    question = "[%s] %s passed the final exam at mean cost %s. Promote it to %s? Answer yes or no." % (run.row.approvers, run.kind, results.cost, TIERS[level + 1])
    ruling = wait(question, name = "promote")
    if ruling.answer.startswith(ANSWER_YES):
        item_create(name = "promote", title = "Promote %s to %s in policy.star" % (run.kind, TIERS[level + 1]), key = "promote-" + item.id, body = results.report, parent = item.id)

# A drop does not wait for a person. The tier change is still a reviewed edit.
def ask_for_demotion(item, run, results, level):
    item_create(name = "demote", title = "Demote %s to %s in policy.star" % (run.kind, TIERS[level - 1]), key = "demote-" + item.id, body = results.report, parent = item.id)

# A promotion or a demotion is a proposal to a person, never a change made here.
def propose_tier_change(item, run, results):
    level = TIERS.index(run.row.tier)
    if earned_promotion(run.row, results.cost, level):
        ask_for_promotion(item, run, results, level)
    elif earned_demotion(run.row, results.cost, level):
        ask_for_demotion(item, run, results, level)

def judge_reply(case_text, reply_text):
    return agent(
        name = "judge",
        prompt = "prompts/judge.md",
        model = JUDGE_MODEL,
        edits = False,
        forbid_commands = READ_ONLY_FORBIDDEN,
        inputs = {"case": case_text, "reply": reply_text},
        outputs = JUDGE_OUTPUTS,
    )

def record_result(run_id, group, case_id, result):
    command(name = "record", run = ["sh", SCRIPT.RECORD, run_id, group, case_id], stdin = json.encode(result))

def proposed_intent(answer):
    if answer.decision != DECISION.PROPOSE:
        return NOTHING_PROPOSED
    intent = check_intent(answer.intent).intent
    if intent == None:
        return NOTHING_PROPOSED
    return struct(kind = intent["kind"], amount = intent["args"].get("amount", 0))

# One gold case: the clerk proposes, the judge grades the reply, and both are written for the score.
def shadow(item):
    gold_case = json.decode(item.body)
    answer = clerk(item, gold_case["case"])
    proposed = proposed_intent(answer)
    judge = judge_reply(gold_case["case"], answer.reply)
    record = struct(id = gold_case["id"], decision = answer.decision, kind = proposed.kind, amount = proposed.amount, judge = judge.grade)
    record_result(gold_case["run"], RECORD_GROUP.CASES, gold_case["id"], record)
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)

# One calibration reply that two people graded. The judge grades it without their grades.
def calibrate(item):
    calib_case = json.decode(item.body)
    judge = judge_reply(calib_case["case"], calib_case["reply"])
    record_result(calib_case["run"], RECORD_GROUP.CALIB, calib_case["id"], struct(id = calib_case["id"], judge = judge.grade))
    item_status(name = "close", id = item.id, status = ITEM_STATUS.CLOSED)
