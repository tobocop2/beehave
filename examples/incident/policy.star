RISK = struct(SAFE = "safe", RISKY = "risky")
FIELD = struct(STRING = "string", INT = "int")
SEVERITY = struct(SEV1 = "sev1", SEV2 = "sev2", SEV3 = "sev3")
ROLE = struct(PRIMARY = "primary", SECONDARY = "secondary", MANAGER = "manager", COMMANDER = "commander")

# The watch takes alerts for ROUNDS rounds of ROUND_SECONDS (twelve hours). A role has `minutes` minutes of MINUTE_SECONDS seconds each.
# The three variables exist only so that a trial run can be short. A real desk leaves them unset.
ROUND_SECONDS = int(env("INCIDENT_ROUND_SECONDS", "60"))
ROUNDS = int(env("INCIDENT_ROUNDS", "720"))
MINUTE_SECONDS = int(env("INCIDENT_MINUTE_SECONDS", "60"))

RUNBOOK = {
    "rollback": struct(
        fields = {"service": FIELD.STRING, "to_version": FIELD.STRING},
        risk = RISK.SAFE,
        verify_minutes = 10,
    ),
    "restart": struct(
        fields = {"service": FIELD.STRING},
        risk = RISK.SAFE,
        verify_minutes = 5,
    ),
    "scale": struct(
        fields = {"service": FIELD.STRING, "replicas": FIELD.INT},
        risk = RISK.RISKY,
        verify_minutes = 10,
    ),
    "flag_off": struct(
        fields = {"flag": FIELD.STRING},
        risk = RISK.RISKY,
        verify_minutes = 5,
    ),
}

CHAINS = {
    SEVERITY.SEV1: struct(
        heads_up = ROLE.PRIMARY,
        levels = [
            struct(role = ROLE.PRIMARY, minutes = 3),
            struct(role = ROLE.SECONDARY, minutes = 3),
            struct(role = ROLE.MANAGER, minutes = 5),
        ],
    ),
    SEVERITY.SEV2: struct(
        heads_up = ROLE.PRIMARY,
        levels = [
            struct(role = ROLE.PRIMARY, minutes = 10),
            struct(role = ROLE.SECONDARY, minutes = 10),
            struct(role = ROLE.MANAGER, minutes = 15),
        ],
    ),
    SEVERITY.SEV3: struct(
        heads_up = ROLE.PRIMARY,
        levels = [struct(role = ROLE.PRIMARY, minutes = 30)],
    ),
}

def chain_for(severity):
    return CHAINS.get(severity, CHAINS[SEVERITY.SEV1])

def refuse(problem):
    return struct(mitigation = None, problem = problem)

def int_problem(name, value):
    if type(value) != "int" or value < 1:
        return "field %s is not a whole number of one or more" % name
    return ""

def string_problem(name, value):
    if type(value) != "string" or value == "":
        return "field %s is missing" % name
    return ""

FIELD_CHECKS = {FIELD.INT: int_problem, FIELD.STRING: string_problem}

def missing_problem(row, args):
    for name, kind in row.fields.items():
        problem = FIELD_CHECKS[kind](name, args.get(name))
        if problem != "":
            return problem
    return ""

def extra_problem(row, args):
    for name in args.keys():
        if name not in row.fields:
            return "field %s is not in the runbook row" % name
    return ""

def args_problem(row, args):
    return missing_problem(row, args) or extra_problem(row, args)

def body_problem(proposal, row):
    if row == None:
        return "unknown mitigation kind %s" % proposal.get("kind", "")
    args = proposal.get("args", {})
    if type(args) != "dict":
        return "args is not an object"
    return args_problem(row, args)

def check_mitigation(text):
    proposal = json.decode(text)
    if type(proposal) != "dict":
        return refuse("the mitigation is not an object")
    problem = body_problem(proposal, RUNBOOK.get(proposal.get("kind", "")))
    if problem != "":
        return refuse(problem)
    return struct(mitigation = proposal, problem = "")
