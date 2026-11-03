load("//policy.star", "FIELD", "POLICY")

DECISION = struct(PROPOSE = "propose", DECLINE = "decline", ESCALATE = "escalate")

# DESK_DIR is the absolute path of this folder.
PARSE_INTENT = env("DESK_DIR") + "/gateway/parse-intent.sh"
CLERK_MODEL = "sonnet"
CLERK_TIMEOUT = "10m"
READ_ONLY_FORBIDDEN = ["git-write", "local-delete", "remote-delete", "opaque-command"]
CLERK_OUTPUTS = ["decision", "intent", "reply", "note", "procedure_note"]
FIELD_PROBLEM = {FIELD.INT: "is not a whole number of zero or more", FIELD.STRING: "is missing"}

# A fresh session for one case, with masked text, no file edits and no write commands.
def clerk(item, case_text):
    return agent(
        name = "clerk",
        prompt = "prompts/clerk.md",
        model = CLERK_MODEL,
        edits = False,
        forbid_commands = READ_ONLY_FORBIDDEN,
        inputs = {"case": case_text, "intents": json.encode(sorted(POLICY.keys()))},
        outputs = CLERK_OUTPUTS,
        timeout = CLERK_TIMEOUT,
    )

def valid_count(value):
    return type(value) == "int" and value >= 0

def valid_text(value):
    return type(value) == "string" and value != ""

FIELD_VALID = {FIELD.INT: valid_count, FIELD.STRING: valid_text}

def shape_problem(intent):
    if type(intent) != "dict":
        return "the intent is not an object"
    kind = intent.get("kind", "")
    if type(kind) != "string" or kind not in POLICY:
        return "unknown intent kind %s" % kind
    if type(intent.get("args", {})) != "dict":
        return "args is not an object"
    return ""

def bad_field(row, args):
    for field, kind in row.fields.items():
        if not FIELD_VALID[kind](args.get(field)):
            return "field %s %s" % (field, FIELD_PROBLEM[kind])
    return ""

def extra_field(row, args):
    for field in args.keys():
        if field not in row.fields:
            return "field %s is not in the policy row" % field
    return ""

def refuse_intent(problem):
    return struct(intent = None, problem = problem)

# The schema check of a write intent. The result holds the intent, or the problem text.
def check_intent(text):
    parsed = command(name = "parse-intent", run = ["sh", PARSE_INTENT], stdin = text)
    if parsed.rc != 0:
        return refuse_intent("the intent is not one JSON object")
    intent = json.decode(parsed.stdout)
    problem = shape_problem(intent)
    if problem != "":
        return refuse_intent(problem)
    row = POLICY[intent["kind"]]
    args = intent.get("args", {})
    problem = bad_field(row, args) or extra_field(row, args)
    if problem != "":
        return refuse_intent(problem)
    return struct(intent = intent, problem = "")
