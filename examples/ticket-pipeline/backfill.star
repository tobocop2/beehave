# A backfill of a range of days, or the daily run for one day. The item body is "FROM TO [BUDGET_CENTS]": two dates (YYYY-MM-DD), and a budget in cents for the agent calls.
#   1. scripts/plan.sh lists the days of the range that the warehouse does not hold at the current VERSION. A second run of the same range does only the rest.
#   2. The days go in chunks of CHUNK days. One chunk is one item and one child run (entry `chunk`) that loads its days one after the other.
#      The parent holds a few steps for each chunk and none for each day, and a child holds the steps of CHUNK days, so no run grows with the size of the range.
#   3. A day that fails or needs a person is left out with a comment, and the other days go on. Run the day alone with partition.star to retry it.
#   4. Before each day, scripts/budget.sh reserves the cost of the agent calls in one SQL statement. When the budget is spent, the run waits for a person:
#      answer the number of cents to add, or stop. The wait has a deadline (BUDGET_TIMEOUT), and a fail at the deadline keeps every loaded day.
# Set PIPE_DIR to the absolute path of this folder and PIPE_HOME to the folder of the data (config, credential.header, warehouse.db, stage/).
# Run: beehave run <item> pipeline --workflow backfill.star. A change of PIPE_VERSION takes effect for a new run: cancel the item, set the variable, run a new item.
load("//version.star", "VERSION", "CHUNK", "MAX_DAYS")
load("//partition.star", "DAY", "PIPE_DIR", "PIPE_HOME", "DEFAULT_BUDGET_CENTS", "STATUS", "load_day", "script_env")

BUDGET_TIMEOUT = env("BUDGET_TIMEOUT", default = "24h")
MAX_BUDGET_ASKS = 20
MIN_WORDS = 2
BUDGET_WORD = 2
USAGE = "REFUSE: the item body must be FROM TO [BUDGET_CENTS], two dates in YYYY-MM-DD form."
BUDGET_STOP = "BUDGET"
BUDGET_LEFT_RC = struct(LEFT = 0, SPENT = 3)

def read_request(item):
    words = item.body.split()
    if len(words) < MIN_WORDS:
        fail(USAGE)
    budget = words[BUDGET_WORD] if len(words) > BUDGET_WORD else str(DEFAULT_BUDGET_CENTS)
    return struct(first = words[0], last = words[1], budget = int(budget))

def plan_days(request, backfill, round):
    pipe_env = script_env(backfill, request.budget)
    pipe_env["PIPE_MAX_DAYS"] = str(MAX_DAYS)
    plan = command(name = "plan-%d" % round, run = ["sh", PIPE_DIR + "/scripts/plan.sh", request.first, request.last], env = pipe_env)
    if plan.rc != 0:
        fail("REFUSE: plan failed with code %d: %s %s" % (plan.rc, plan.stdout.strip(), plan.stderr.strip()))
    return plan.stdout.split()

# One chunk: the entry of a child run. The item body is JSON with the days, the backfill id and the budget in cents.
def chunk(item):
    spec = json.decode(item.body)
    problems = []
    loaded = 0
    budget_stop = ""
    for day in spec["days"]:
        outcome = load_day(day, spec["backfill"], spec["budget_cents"])
        if outcome.state in (DAY.LOADED, DAY.EMPTY, DAY.SKIPPED):
            loaded += 1
        elif outcome.state == DAY.BUDGET:
            budget_stop = outcome.note
            break
        else:
            problems.append("%s: %s" % (outcome.state, outcome.note))
    text = "%d of %d days done. %s" % (loaded, len(spec["days"]), "\n".join(problems))
    item_comment(name = "report", id = item.id, key = "report", text = text)
    item_status(name = "close", id = item.id, status = STATUS.CLOSED)
    if budget_stop != "":
        fail("%s: %s" % (BUDGET_STOP, budget_stop))

def budget_spent(request, backfill, label):
    left = command(name = "budget-left-" + label, run = ["sh", PIPE_DIR + "/scripts/budget-left.sh"], env = script_env(backfill, request.budget))
    if left.rc not in (BUDGET_LEFT_RC.LEFT, BUDGET_LEFT_RC.SPENT):
        fail("REFUSE: budget-left failed with code %d: %s" % (left.rc, left.stdout.strip()))
    return left.rc == BUDGET_LEFT_RC.SPENT

def run_chunk(item, request, days, label):
    made = item_create(
        name = "chunk-" + label,
        title = "ticket chunk %s to %s" % (days[0], days[-1]),
        key = "chunk-%s-%s-v%s-%s" % (days[0], days[-1], VERSION, label),
        body = json.encode({"days": days, "backfill": item.id, "budget_cents": request.budget}),
        parent = item.id,
    )
    spawned = spawn([made.id], entry = "chunk", name = "spawn-" + label)
    return gather(spawned.runs, fail = "return", name = "gather-" + label).results[0]

# Runs the chunks of the plan one after the other. Returns struct(stopped, problems): stopped is true when the budget ended the round.
def run_chunks(item, request, days, round):
    problems = []
    for start in range(0, len(days), CHUNK):
        label = "r%d-%d" % (round, start // CHUNK)
        if budget_spent(request, item.id, label):
            return struct(stopped = True, problems = problems)
        result = run_chunk(item, request, days[start:start + CHUNK], label)
        if result.ok:
            continue
        if result.error.startswith(BUDGET_STOP):
            return struct(stopped = True, problems = problems)
        problems.append("chunk starting %s: %s" % (days[start], result.error))
    return struct(stopped = False, problems = problems)

def main(item):
    request = read_request(item)
    problems = []
    planned = 0
    for round in range(MAX_BUDGET_ASKS):
        days = plan_days(request, item.id, round)
        if round == 0:
            planned = len(days)
        if len(days) == 0:
            break
        outcome = run_chunks(item, request, days, round)
        problems += outcome.problems
        if not outcome.stopped:
            break
        ruling = wait("The budget of %d cents is spent. Answer the cents to add, such as 500, or stop to end the backfill here." % request.budget, name = "budget-%d" % round, timeout = BUDGET_TIMEOUT)
        if ruling.timed_out:
            fail("REFUSE: no answer about the budget within %s. Every loaded day stays in the warehouse. Run the item again to continue." % BUDGET_TIMEOUT)
        if not ruling.answer.strip().isdigit():
            break
        request = struct(first = request.first, last = request.last, budget = request.budget + int(ruling.answer.strip()))
    table = command(name = "report-table", run = ["sh", PIPE_DIR + "/scripts/report.sh", request.first, request.last], env = script_env(item.id, request.budget))
    text = "%d days planned at version %s, %d problems.\n%s\n%s" % (planned, VERSION, len(problems), "\n".join(problems), table.stdout)
    item_comment(name = "report", id = item.id, key = "report", text = text)
    item_status(name = "close", id = item.id, status = STATUS.CLOSED)
