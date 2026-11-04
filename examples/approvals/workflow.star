# An approval desk. One item is one request for sign-offs on a document. The run follows it to approval, rejection or expiry.
# The sign-offs are signed files that people put in the inbox of the desk; scripts/intake.sh verifies them (ssh-keygen -Y verify).
# The rules are data in policy.star: the roles, how many people each needs, which roles are required (a veto), and min_roles.
# The deadline and the reminders are ledger facts. The run waits in rounds (scripts/await.sh).
# The extension is a wait addressed to the people in escalate_to, with the choices extend and cancel. The ledger row names the person.
# The engine does not prove the name; the signed inbox is why the sign-offs do not use a wait.
# Set APPROVALS_DIR to the absolute path of this folder. The desk folder (default ~/.approval) holds the ledger and the keys.
load("//policy.star", "KINDS", "ROUNDS", "ROUND_SECONDS")

HERE = env("APPROVALS_DIR")
STATUS = struct(PENDING = "pending", APPROVED = "approved", REJECTED = "rejected", EXPIRED = "expired", CLOSED = "closed")
ITEM_STATUS = struct(OPEN = "open", CLOSED = "closed", HOLD = "hold")
RC = struct(CHANGED = 4)

# An approved request closes its item. A rejected or expired request holds it, so a person looks at it.
FINAL_STATUS = {
    STATUS.APPROVED: ITEM_STATUS.CLOSED,
    STATUS.REJECTED: ITEM_STATUS.HOLD,
    STATUS.EXPIRED: ITEM_STATUS.HOLD,
}

DEFAULT_TIMEOUT = "5m"
AWAIT_SLACK_SECONDS = 120
EXTENSIONS_ALLOWED = 1
ANSWER_EXTEND = "extend"
ANSWER_CANCEL = "cancel"
ESCALATE_ROLE = "@escalate"
AUTHOR_ROLE = "@author"
BRIEF_MODEL = "sonnet"
BRIEF_TIMEOUT = "10m"
BRIEF_FORBIDDEN = ["config-read", "opaque-command", "git-write", "delete-text"]

# The item is one request: {"doc": "msa-acme", "kind": "contract", "author": "ann"}. One run follows it to its end.
def main(item):
    request = parse_request(item)
    open_request(item, request)
    memory = struct(seen = "", reminded = 0, done = False)
    for number in range(ROUNDS):
        memory = run_round(item, request, memory, number)
        if memory.done:
            return
    give_up(item)

def parse_request(item):
    body = json.decode(item.body)
    row = KINDS.get(body["kind"])
    if row == None:
        fail("REFUSE: the kind %s is not in policy.star" % body["kind"])
    return struct(doc = body["doc"], kind = body["kind"], author = body["author"], policy = struct(**row))

def run_script(name, step, args, stdin = "", timeout = DEFAULT_TIMEOUT):
    return command(name = step, run = ["sh", "%s/scripts/%s.sh" % (HERE, name)] + args, stdin = stdin, timeout = timeout)

def open_request(item, request):
    args = [item.id, request.doc, request.kind, request.author]
    opened = run_script("open", "open", args, stdin = json.encode(request.policy))
    if opened.rc != 0:
        fail("REFUSE: the request did not open: %s" % opened.stdout)

def read_state(item, number):
    tally = run_script("tally", "tally-%d" % number, [item.id])
    if tally.rc != 0:
        fail("REFUSE: the tally failed: %s" % tally.stdout)
    return struct(**json.decode(tally.stdout))

def run_round(item, request, memory, number):
    state = read_state(item, number)
    return HANDLERS[state.status](item, request, state, memory, number)

def with_seen(memory, seen):
    return struct(seen = seen, reminded = memory.reminded, done = memory.done)

def with_reminded(memory, reminded):
    return struct(seen = memory.seen, reminded = reminded, done = memory.done)

def finished(memory):
    return struct(seen = memory.seen, reminded = memory.reminded, done = True)

def keep_waiting(item, request, state, memory, number):
    memory = announce_version(item, request, state, memory)
    if memory.done:
        return memory
    memory = send_reminders(item, request, state, memory)
    if memory.done:
        return memory
    seconds = round_seconds(request, state, memory)
    run_script("await", "await-%d" % number, [item.id, str(seconds), str(state.seq), state.hash], timeout = "%ds" % (seconds + AWAIT_SLACK_SECONDS))
    return memory

# A round ends at a change, at the next reminder, at the deadline or after ROUND_SECONDS, whichever comes first.
def round_seconds(request, state, memory):
    seconds = min(ROUND_SECONDS, max(state.left, 1))
    for level in request.policy.remind_at[memory.reminded:]:
        seconds = min(seconds, max(state.opened + (state.deadline - state.opened) * level // 100 - state.now, 1))
        break
    return seconds

def roles_to_ask(state):
    return state.stale_roles + [role for role in state.missing if role not in state.stale_roles]

# Tell the roles about each version once. A key from the item and the hash makes a repeat after a restart send nothing.
# A notice that cannot be sent holds the item: a request nobody was told about would expire with no one asked.
def tell(item, step, key, to, text, memory):
    sent = run_script("notify", step, [item.id, key, to], stdin = text)
    if sent.rc == 0:
        return memory
    hold_item(item, "Not sent: the notice %s failed. %s" % (key, sent.stderr.strip()))
    return finished(memory)

def hold_item(item, why):
    item_comment(name = "reason", id = item.id, key = "reason", text = why)
    item_status(name = "hold", id = item.id, status = ITEM_STATUS.HOLD)

def announce_version(item, request, state, memory):
    if memory.seen == state.hash:
        return memory
    memory = with_seen(memory, state.hash)
    key = "version-%s-%s" % (item.id, state.hash)
    if len(state.stale) == 0:
        text = "A sign-off is wanted on %s (%s), version %s, by %d seconds from now." % (request.doc, request.kind, state.hash[:8], state.left)
        return tell(item, "notify-version-" + state.hash[:8], key, ",".join(state.missing), text, memory)
    change = run_script("diff", "diff-" + state.hash[:8], [request.doc, state.stale_from, state.hash])
    note = brief(request, state, change.stdout)
    return tell(item, "notify-reset-" + state.hash[:8], key, ",".join(roles_to_ask(state)), note.summary, memory)

def brief(request, state, change):
    inputs = {"doc": "%s (%s)" % (request.doc, request.kind), "diff": change, "roles": json.encode(roles_to_ask(state))}
    return agent(
        name = "brief-" + state.hash[:8],
        prompt = "prompts/brief.md",
        model = BRIEF_MODEL,
        edits = False,
        forbid_commands = BRIEF_FORBIDDEN,
        inputs = inputs,
        outputs = ["summary"],
        timeout = BRIEF_TIMEOUT,
    )

# Send every reminder that is owed, in order, one notice per remind_at level. A round that wakes late (past more than one level, or past the
# deadline) still sends each level it missed. A level is never skipped, so the key remind-<item>-<n> exists for every n up to the last owed.
def send_reminders(item, request, state, memory):
    for number, level in enumerate(request.policy.remind_at):
        if state.pct < level or number < memory.reminded:
            continue
        text = "A sign-off is still wanted. %d percent of the window has passed. Missing: %s." % (level, ", ".join(state.missing))
        memory = tell(item, "remind-%d" % (number + 1), "remind-%s-%d" % (item.id, number + 1), ",".join(state.missing), text, with_reminded(memory, number + 1))
        if memory.done:
            return memory
    return memory

def close_as_state(item, request, state, memory, number):
    closed = run_script("finalize", "finalize-%d" % number, [item.id, state.status])
    if closed.rc == RC.CHANGED:
        return memory
    if closed.rc != 0:
        fail("REFUSE: the request did not close: %s" % closed.stdout)
    report_outcome(item, state, closed.stdout.strip().split("\n"))
    return finished(memory)

def rejection_lines(state):
    return "".join([" %s (%s): %s." % (found["who"], found["role"], found["reason"]) for found in state.rejections])

# The head is the last row of the ledger at the close. It goes in the item, outside the ledger file, so that audit.sh can see a cut tail.
def report_outcome(item, state, lines):
    text = "%s at version %s.%s Certificate: %s. Ledger %s." % (state.status, state.hash, rejection_lines(state), lines[0], lines[1])
    item_comment(name = "outcome", id = item.id, key = "outcome", text = text)
    run_script("notify", "tell-author", [item.id, "outcome-" + item.id, AUTHOR_ROLE], stdin = text)
    item_status(name = "close", id = item.id, status = FINAL_STATUS[state.status])

def handle_expiry(item, request, state, memory, number):
    if state.extensions >= EXTENSIONS_ALLOWED:
        return close_as_state(item, request, state, memory, number)
    memory = send_reminders(item, request, state, memory)
    if memory.done:
        return memory
    memory = tell(item, "escalate", "escalate-" + item.id, ESCALATE_ROLE, "The deadline passed. Missing: %s." % ", ".join(state.missing), memory)
    if memory.done:
        return memory
    question = "The deadline passed. Missing: %s. Answer extend for %d more seconds, or cancel." % (", ".join(state.missing), request.policy.extend)
    ruling = wait(question, name = "extend-%d" % number, to = state.escalate_to, choices = [ANSWER_EXTEND, ANSWER_CANCEL], timeout = "%ds" % request.policy.extend)
    if not ruling.timed_out and ruling.answer == ANSWER_EXTEND:
        return extend(item, request, sorted(ruling.answers.keys())[0], memory, number)
    return close_as_state(item, request, state, memory, number)

# The wait refuses a name that is not in escalate_to, and the engine does not prove the name. The ledger row records the name given.
def extend(item, request, who, memory, number):
    run_script("extend", "extend-%d" % number, [item.id, str(request.policy.extend), who, "answered " + ANSWER_EXTEND])
    return memory

def already_closed(item, request, state, memory, number):
    item_status(name = "close", id = item.id, status = FINAL_STATUS[state.outcome])
    return finished(memory)

def give_up(item):
    item_comment(name = "rounds", id = item.id, key = "rounds", text = "The rounds ran out before the request ended. Run beehave resume.")
    item_status(name = "hold", id = item.id, status = ITEM_STATUS.HOLD)

HANDLERS = {
    STATUS.PENDING: keep_waiting,
    STATUS.APPROVED: close_as_state,
    STATUS.REJECTED: close_as_state,
    STATUS.EXPIRED: handle_expiry,
    STATUS.CLOSED: already_closed,
}
