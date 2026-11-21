# The remediation workflow: plan, implement, then review and fix until the gate passes and the review approves.
# Every method value is in this file, in the review loop it loads, or in a prompt next to it. Go holds none of it.

load("@beehave//review_fix.star", "DESIGN_AFTER", "FIX_ESCALATE_AFTER", "HOST_LOSS_LIMIT", "VOID_ASKS", "VOID_RERUNS", "candidates_spent", "capped", "is_host_void", "kept_history", "restart_steps", "review_fix")

# The model of each step of the method. This table is the one place a model is named. A project overrides the model or the runner of a step with vars.round.steps.
# fix runs the fix rounds up to vars.round.escalate_after, and fix_late runs the rounds after them.
STEP_DEFAULTS = {
    "plan": "opus",
    "implement": "sonnet",
    "review": "opus",
    "fix": "sonnet",
    "fix_late": "opus",
    "design": "opus",
    "take_review": "opus",
    "sync_review": "opus",
    "sync_resolve": "opus",
    "queue_audit": "sonnet",
    "settle": "sonnet",
}

# The steps that review_fix takes. The other steps of STEP_DEFAULTS are agent calls of this file.
LOOP_STEPS = ["implement", "review", "fix", "fix_late", "design"]

# A pull request is stale after this many hours with no push, comment, review or answer from the author. The same values are the idle threshold and the grace period that the take-over passes to the tool as flags.
IDLE_HOURS = "1"
GRACE_HOURS = "1"

# The severities in the order of the queue, most severe first. A severity that is not in the list sorts last.
SEVERITIES = ["P0", "P1", "P2", "P3", "P4"]

# The attribution patterns, each case-insensitive. No line of a pull request body, a commit message, an issue, a comment or a pull request title may match them.
# The merge adds the pattern of the tracker id of each item (id_patterns); the posting steps use only this list, because their text names the item.
ATTRIBUTION = ["(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"]

# A line of the mutation output is a finding only when it starts with this text. The log of the tool prints other lines, such as the line of a killed mutant that gremlins writes as KILLED TYPE at file:line:col.
FINDING_PREFIX = "finding: "

def _severity(row):
    if row.severity in SEVERITIES:
        return SEVERITIES.index(row.severity)

    return len(SEVERITIES)

def _group(row, me):
    # The work of others is a pull request whose author is not `me`. A review request does not decide it. It stays first whatever its review state is, so a pull request of another author with requested changes is here and not in group 1.
    if row.pr > 0 and row.author != "" and row.author != me:
        return 0

    if row.changes_requested:
        return 1

    if row.pr > 0:
        return 2

    return 3

def rank(rows, me):
    """Returns the ready rows in the order of work: first the pull requests of other authors than `me` (the work of others, whatever their review state),
    then our pull requests with requested changes, then our other work in flight (an open pull request), then the new rows.
    Inside each group, the rows of a container come before the loose rows, then the rows go by severity, then by item id. A row that is not ready is not offered.
    Two rows with the same keys keep the order of their item ids, so the input order does not change the result."""
    ready = [r for r in rows if r.state == "ready"]
    return sorted(ready, key = lambda r: (_group(r, me), 0 if r.parent != "" else 1, _severity(r), r.item))

# A hook refuses a command of these kinds before the call. A command that still ran is read from the output stream and recorded in the violations of the agent. The step does not fail.
# The review loop reads the violations of the latest run of the implementer: a hit sends the run back to a fix round.
FORBID = ["bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "protected-path", "zsh-expansion", "laptop-copy"]

# The agent works in the worktree of the item and must not write in the checkout of the owner. The setting vars.round.source_checkout names that checkout.
# The kind protected-path refuses a write there, and a git command there that is not a read. A command that reads the checkout passes.
def protected(round_settings):
    if round_settings["source_checkout"] == "":
        return []

    return [round_settings["source_checkout"]]

def lane_scope(round_settings):
    """Returns the allow_paths setting of the lane, as a dict to pass with `**`.
    The setting vars.round.lane_kind names the kind of lane: impl (the default), papers or design. The settings function refuses any other kind, and a papers or design round with no allow_paths.
    An impl lane may change any path. A papers or design lane may change only the globs in vars.round.allow_paths.
    The implement step and every fix step fail, and the message names each path outside the globs, when they change another path."""
    if round_settings["lane_kind"] in ["papers", "design"]:
        return {"allow_paths": round_settings["allow_paths"]}

    return {}

# The settings of a round are the dict `round` of the project file vars: vars = {"round": {...}}. Each key has its type and the fix that a refusal names.
ROUND_KEYS = {
    "base": ("string", "the branch of the round, such as integration/r2"),
    "owner": ("string", "the login of the owner"),
    "lane_cap": ("int", "the number of lanes that run at once, 1 or more"),
    "fix_rounds": ("int", "the number of fix rounds for one item, 1 or more"),
    "label": ("string", "the tracker label that selects the items of the round"),
    "items": ("list", "the item ids of the round, in place of the label"),
    "next_base": ("string", "the branch of the next round, or an empty text for a round that does not cut"),
    "owner_email": ("string", "the email of the owner"),
    "source_checkout": ("string", "the checkout of the owner"),
    "integration_pr": ("int", "the number of the integration pull request, or 0 to let the round find it"),
    "merge": ("bool", "true to land approved items, false to land none"),
    "hand_off": ("bool", "true when the owner merges the approved pull requests: the round marks them ready and waits, and lands none itself"),
    "acceptance": ("string", "the acceptance script, or an empty text for none"),
    "union_gate": ("list", "the command that gates the union of a batch, a list of words, or an empty list for the gate of the project"),
    "base_result": ("string", "the file of the base result, or an empty text for none"),
    "base_reds": ("list", "the known base reds"),
    "base_reds_file": ("string", "the file of the known base reds"),
    "lane_kind": ("string", "impl, papers or design"),
    "allow_paths": ("list", "the paths that a papers or design lane may change"),
    "wait_hours": ("int", "the hours that the round waits for the owner, 1 or more"),
    "steps": ("dict", "a dict from a step name to {\"model\": text, \"runner\": text}, for example {\"review\": {\"model\": \"your-model\"}}"),
    "design_after": ("int", "the fixes on one mechanism before the design step, 1 to 100"),
    "void_reruns": ("int", "the reruns of a void gate before the loop asks the owner, 0 to 100"),
    "void_asks": ("int", "the questions to the owner about a void gate, 0 to 100"),
    "escalate_after": ("int", "the last fix round that resumes the implementer, 0 to 100"),
}

# The keys that a round cannot go without, and the value of each other key that the file leaves out.
ROUND_REQUIRED = ["base", "owner", "lane_cap", "fix_rounds"]
ROUND_DEFAULTS = {
    "label": "",
    "items": [],
    "next_base": "",
    "owner_email": "",
    "source_checkout": "",
    "integration_pr": 0,
    "merge": False,
    "hand_off": False,
    "acceptance": "",
    "union_gate": [],
    "base_result": "",
    "base_reds": [],
    "base_reds_file": "",
    "lane_kind": "impl",
    "allow_paths": [],
    "wait_hours": 24,
    "steps": {},
    "design_after": DESIGN_AFTER,
    "void_reruns": VOID_RERUNS,
    "void_asks": VOID_ASKS,
    "escalate_after": FIX_ESCALATE_AFTER,
}

# The most that a limit takes.
LIMIT_MOST = 100

# The time that one write of a record file and one run of the acceptance script may take.
WRITE_TIMEOUT = "1m"
ACCEPTANCE_TIMEOUT = "30m"

ALNUM = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

def _made_of(text, allowed):
    for c in text.elems():
        if c not in allowed:
            return False

    return True

def _valid_base(text):
    # A branch name that can be part of a round id: segments of letters, digits, "_", "." and "-" joined by "/". Each segment starts with a letter or a digit.
    if ".." in text:
        return False

    for segment in text.split("/"):
        if segment == "" or segment[0] not in ALNUM or not _made_of(segment[1:], ALNUM + "_.-"):
            return False

    return True

def _valid_item(text):
    # An item id is a number, or a beads id such as bd-12.
    if text != "" and _made_of(text, "0123456789"):
        return True

    prefix, found, rest = text.partition("-")
    return found != "" and prefix != "" and prefix[0] in ALNUM and _made_of(prefix, ALNUM + "_") and rest != "" and _made_of(rest, ALNUM + "_.-")

def _round_refuse(key, why):
    fail("REFUSE: vars.round.%s %s" % (key, why))

def _round_value(block, key):
    kind, fix = ROUND_KEYS[key]
    if key not in block:
        return ROUND_DEFAULTS[key]

    value = block[key]
    if type(value) != kind:
        _round_refuse(key, "is %r, of type %s. Write %s." % (value, type(value), fix))

    return value

def _round_lists(got):
    for key in ["base_reds", "allow_paths"]:
        for entry in got[key]:
            if type(entry) != "string" or entry.strip() == "":
                _round_refuse(key, "lists a blank string. Write the entry or remove it.")

def _round_numbers(got):
    for key, least in [("lane_cap", 1), ("fix_rounds", 1), ("wait_hours", 1), ("integration_pr", 0)]:
        if got[key] < least:
            _round_refuse(key, "is %d. Write %s." % (got[key], ROUND_KEYS[key][1]))

    for key, least in [("design_after", 1), ("void_reruns", 0), ("void_asks", 0), ("escalate_after", 0)]:
        if got[key] < least or got[key] > LIMIT_MOST:
            _round_refuse(key, "is %d. Write %s." % (got[key], ROUND_KEYS[key][1]))

def _step_entry(name, entry):
    # One entry of vars.round.steps: a dict of a model (non-blank text) and a runner (text). Returns the entry with the default model of the step.
    path = "steps." + name
    if type(entry) != "dict":
        _round_refuse(path, "is %r, of type %s. Write a dict with a model, a runner or both, for example {\"model\": \"your-model\"}." % (entry, type(entry)))

    for field in entry:
        if field not in ["model", "runner"]:
            _round_refuse(path + "." + field, "is not a step field. The fields are: model, runner.")

    model = entry.get("model", STEP_DEFAULTS[name])
    if type(model) != "string" or model.strip() == "":
        _round_refuse(path + ".model", "is %r. Write the name of a model, or leave the key out for the default." % (model,))

    runner = entry.get("runner", "")
    if type(runner) != "string":
        _round_refuse(path + ".runner", "is %r, of type %s. Write the name of a runner, or an empty text for the pool." % (runner, type(runner)))

    return {"model": model, "runner": runner}

def _round_steps(block):
    # Returns every step of STEP_DEFAULTS with its model and runner. A step that the project leaves out keeps its default model and the pool.
    for name in block:
        if name not in STEP_DEFAULTS:
            _round_refuse("steps." + name, "is not a step. The steps are: %s." % ", ".join(sorted(STEP_DEFAULTS)))

    steps = {name: _step_entry(name, block[name]) if name in block else {"model": STEP_DEFAULTS[name], "runner": ""} for name in STEP_DEFAULTS}

    # A review on the model of the implementer judges its own work with the same blind spots. One model for every step has no other model to take, so it is accepted.
    models = {steps[name]["model"]: True for name in steps}
    if len(models) > 1 and steps["review"]["model"] == steps["implement"]["model"]:
        _round_refuse("steps.review.model", "is %r, the model of the implement step. Review on another model: set vars.round.steps.review.model or vars.round.steps.implement.model." % (steps["review"]["model"],))

    return steps

def _model(round_settings, name):
    return round_settings["steps"][name]["model"]

def _runner(round_settings, name):
    return round_settings["steps"][name]["runner"]

def _round_lane(got):
    if got["lane_kind"] not in ["impl", "papers", "design"]:
        _round_refuse("lane_kind", "is %r. Write impl, papers or design." % got["lane_kind"])

    if got["lane_kind"] != "impl" and len(got["allow_paths"]) == 0:
        _round_refuse("lane_kind", "is %r and vars.round.allow_paths is empty. List the paths that the lane may change." % got["lane_kind"])

def _round_selector(block, got):
    has_label = "label" in block
    has_items = len(got["items"]) > 0
    if has_label and has_items:
        fail("REFUSE: a round takes one item selector: set vars.round.label or vars.round.items, not both.")

    if not has_label and not has_items:
        fail("REFUSE: a round needs an item selector: set vars.round.label or vars.round.items.")

    if has_label and got["label"].strip() == "":
        _round_refuse("label", "is blank. Write the tracker label that selects the items.")

    for item in got["items"]:
        if type(item) != "string" or not _valid_item(item):
            _round_refuse("items", "lists %r. Write an item id: a number, or a beads id such as bd-12." % item)

def settings(project):
    """Returns the settings of the round as a dict, with the default of each key that the project vars leave out.
    The engine reads no setting. This function is the one check of them: it refuses project vars with no `round`, a missing key, a key of the wrong type, a value out of range and a key that is no setting,
    and it refuses a method setting of the vars (the lists, the dicts, the gate map, the changelog and the board) of the wrong type or form, each with a message that names the key and the fix. `project` is the value of facts(). Every entry of the file is a round or a child of one, so every entry reads them."""
    if "round" not in project.vars:
        fail("REFUSE: the project vars hold no round. Add the settings of the round to the project file: vars = {\"round\": {\"base\": \"integration/r2\", \"owner\": \"your-login\", \"lane_cap\": 4, \"fix_rounds\": 5, \"label\": \"beehave-round\"}}.")

    block = project.vars["round"]
    if type(block) != "dict":
        fail("REFUSE: vars.round is %r, of type %s. Write a dict of the round settings, for example {\"base\": \"integration/r2\"}." % (block, type(block)))

    for key in block:
        if key not in ROUND_KEYS:
            _round_refuse(key, "is not a round setting. The settings are: %s." % ", ".join(sorted(ROUND_KEYS)))

    for key in ROUND_REQUIRED:
        if key not in block:
            _round_refuse(key, "is missing. Add it: %s." % ROUND_KEYS[key][1])

    got = {key: _round_value(block, key) for key in ROUND_KEYS}

    for key in ["base", "owner"]:
        if got[key].strip() == "":
            _round_refuse(key, "is blank. Write %s." % ROUND_KEYS[key][1])

    if not _valid_base(got["base"]):
        _round_refuse("base", "is %r. Write a branch name such as integration/r2: letters, digits, `.`, `_` and `-`, with `/` between parts." % got["base"])

    if "base_reds_file" not in block:
        got["base_reds_file"] = "../base-reds-" + got["base"].replace("/", "_2F") + ".md"
    elif got["base_reds_file"].strip() == "":
        _round_refuse("base_reds_file", "is blank. Write the file of the known base reds, or leave the key out for ../base-reds-<base>.md, one file for each round.")
    elif _leaves_project(got["base_reds_file"]):
        _round_refuse("base_reds_file", "is %r, which leaves the folder of the project. Write a path from the folder of the round that stays in the project folder, such as ../base-reds.md, or an absolute path." % got["base_reds_file"])

    _round_numbers(got)
    got["steps"] = _round_steps(got["steps"])
    _round_lists(got)
    _round_lane(got)
    _round_selector(block, got)
    _method_vars(project.vars)

    return got

def _leaves_project(path):
    """Reports whether a relative path from the folder of the round leaves the folder of the project, its parent: its clean form starts with two ".." parts or more. An absolute path is the choice of the owner."""
    if path.startswith("/"):
        return False
    depth = 0
    for part in path.split("/"):
        if part == "..":
            depth -= 1
        elif part not in ["", "."]:
            depth += 1
        if depth < -1:
            return True
    return False

# The method settings of the project vars that the example and its prompts read. Each key is optional. The lists hold text, the dicts map text to text.
METHOD_LISTS = ["gate_files", "conventions", "brief_rules", "stacked_rules", "limits", "traps", "token_rules"]
METHOD_DICTS = ["people", "epics"]
BOARD_KEYS = ["number", "started", "merged", "owner"]

def _vars_refuse(key, why):
    fail("REFUSE: vars.%s %s" % (key, why))

def _is_text(value):
    return type(value) == "string" and value.strip() != ""

def _method_vars(vars):
    """Refuses a method setting of the project vars that has the wrong type or a blank entry, before any step runs. The engine keeps vars a free map, so this function is the one check of these keys.
    The keys are the lists and dicts of facts, the gate map, the changelog path and the board. A key that the project leaves out passes. Each refusal names the key and the fix."""
    for key in METHOD_LISTS:
        if key in vars:
            _method_list(key, vars[key])

    for key in METHOD_DICTS:
        if key in vars:
            _method_dict(key, vars[key])

    if "gate_map" in vars:
        _method_gate_map(vars["gate_map"])

    if "changelog" in vars:
        _method_changelog(vars["changelog"])

    if "board" in vars:
        _method_board(vars["board"])

    if "clean_command" in vars:
        _method_words("clean_command", vars["clean_command"])

def _method_words(key, value):
    if type(value) != "list" or len(value) == 0 or [word for word in value if not _is_text(word)] != []:
        _vars_refuse(key, "is %r. Write the command as a list of words that are not blank, for example [\"sh\", \"scripts/clean-lane.sh\"], or leave the key out." % (value,))

def _method_list(key, value):
    if type(value) != "list":
        _vars_refuse(key, "is %r, of type %s. Write a list of text, for example [\"a fact\"]." % (value, type(value)))

    for entry in value:
        if not _is_text(entry):
            _vars_refuse(key, "lists %r. Write each fact as text that is not blank, or remove the item." % (entry,))

def _method_dict(key, value):
    if type(value) != "dict":
        _vars_refuse(key, "is %r, of type %s. Write a dict of text to text, for example {\"name\": \"value\"}." % (value, type(value)))

    for name, entry in value.items():
        if not _is_text(name) or not _is_text(entry):
            _vars_refuse(key, "holds a blank or non-text name or value (%r: %r). Write both as text or remove the entry." % (name, entry))

def _method_gate_map(value):
    if type(value) != "dict":
        _vars_refuse("gate_map", "is %r, of type %s. Write a dict of each CI command to a list of gate_steps names, for example {\"make test\": [\"test\"], \"go install x@v1\": []}." % (value, type(value)))

    for command_text, names in value.items():
        if not _is_text(command_text) or type(names) != "list":
            _vars_refuse("gate_map", "holds %r: %r. Write the CI command as text and the gate_steps names that run it as a list, or [] when it is not a check." % (command_text, names))

        for name in names:
            if not _is_text(name):
                _vars_refuse("gate_map", "lists %r for the command %r. Write each gate_steps name as text that is not blank." % (name, command_text))

def _method_changelog(value):
    # The check compares the path with the lines that git prints, so only the exact form of such a line matches.
    if not _is_text(value):
        _vars_refuse("changelog", "is %r. Write the path of the changelog, a relative path to a file of the repository, or leave the key out." % (value,))

    if value.startswith("/") or value.startswith("./") or value.endswith("/") or ".." in value.split("/"):
        _vars_refuse("changelog", "is %r. The changelog is a file of the repository, and the check compares it with the paths that git prints. Use a relative path to a file, with no `..`, no leading `/` or `./` and no trailing `/`." % (value,))

def _method_board(value):
    if type(value) != "dict":
        _vars_refuse("board", "is %r, of type %s. Write a dict, for example {\"number\": 3, \"started\": \"In Progress\", \"merged\": \"Done\"}; \"owner\" is optional." % (value, type(value)))

    for key in value:
        if key not in BOARD_KEYS:
            _vars_refuse("board." + key, "is not a board setting. The settings are: %s." % ", ".join(BOARD_KEYS))

    for key in ["number", "started", "merged"]:
        if key not in value:
            _vars_refuse("board", "has no %s. Write vars = {\"board\": {\"number\": 3, \"started\": \"In Progress\", \"merged\": \"Done\"}}; \"owner\" is optional." % key)

    if type(value["number"]) != "int" or value["number"] <= 0:
        _vars_refuse("board.number", "is %r. Write the number of the GitHub Project, a whole number above 0." % (value["number"],))

    for key in ["started", "merged"]:
        if not _is_text(value[key]):
            _vars_refuse("board." + key, "is %r. Write the name of the column, text that is not blank." % (value[key],))

    if "owner" in value and type(value["owner"]) != "string":
        _vars_refuse("board.owner", "is %r. Write the login of the owner of the project as text, or leave the key out." % (value["owner"],))

def regressions(value):
    """Returns the regression count that the output `regressions` of a report holds, or None when the count is unknown.
    The output is a text. Spaces around the number are allowed. A text that is not a whole number gives None: an unknown count is never a pass."""
    number = value.strip()
    return int(number) if number.isdigit() else None

def regression_rise(start, end):
    """Returns the reason to hold the item, and an empty text when the regression count did not rise from the output `regressions` of the plan (`start`) to the one of the full review that is checked (`end`). The loop checks after each full review.
    Both counts are the regressions open at the head of the report, so they compare like with like: a defect that an earlier round found and a later round fixed is in neither.
    A count that is unknown in either output holds the item: the prompts ask both lanes for a whole number."""
    before = regressions(start)
    after = regressions(end)
    if before == None:
        return "The report of the plan has no regression count that can be read. An unknown count blocks the item: a person reads the reports and rules."

    if after == None:
        return "The report of the review has no regression count that can be read. An unknown count blocks the item: a person reads the reports and rules."

    if after <= before:
        return ""

    return "The regression count rose from %d to %d between the plan and a review. A rise blocks the item: a person reads the reports and rules." % (before, after)

def mutation_command(project):
    """Returns the mutation command of the project: the list of words in the key mutation_command of its vars. The key is required, and the run is refused before any agent starts when it is missing or is not a list of words.
    The command runs on the build host in the slot class mutation. The workflow adds one word at the end, the base ref (origin/ and vars.round.base), so the command mutates only the lines that differ from it."""
    words = project.vars.get("mutation_command")
    if type(words) != "list" or len(words) == 0 or [w for w in words if type(w) != "string" or w == ""] != []:
        fail("REFUSE: vars.mutation_command of the project file is %r. Set it to the command that runs the mutation tool, a list of words such as [\"sh\", \"scripts/mutation-pr.sh\"]. See docs/design/workflow-files.md, the mutation step" % (words,))

    return words

def require_review_skill_named(project):
    """Stops the run before any agent starts when the project vars do not name the review skill of the sweep step. The key vars.review_skill is required: the workflow names no skill itself."""
    name = project.vars.get("review_skill")
    if type(name) != "string" or name.strip() == "":
        fail("REFUSE: vars.review_skill of the project file is %r. Set it to the name of the skill that the sweep step loads after the implement step, such as \"your-review-skill\". See docs/design/workflow-files.md, the review skill of the sweep step" % (name,))

def require_runner_that_loads_skills(project, round_settings, step):
    """Stops the run before any agent starts when the step is on a runner whose agent CLI has no line that loads a skill before the first turn, because the sweep step on that runner cannot start.
    A step on the pool needs no check here: the sweep step itself refuses the runner that the pool picks. A runner that the facts do not list is left to the agent step, which refuses an unknown runner."""
    runner = _runner(round_settings, step)
    if runner == "":
        return

    known = project.runners.get(runner)
    if known != None and not known["loads_skills"]:
        fail("REFUSE: runner %s of step %s has no line that loads a skill before the first turn, so the sweep step cannot start on it. The step did not start. Set vars.round.steps.%s.runner to a runner of an agent that has one, or to an empty text for the pool." % (runner, step, step))

def check_mutation(words, base):
    """Runs the mutation command of the project on the build host, in the slot class mutation, and returns the findings as text. An empty text means the head is clean.
    `base` is vars.round.base: the last word of the command is origin/<base>.
    The command prints one finding per line, such as finding: path/file.go:12: a mutant lived: ARITHMETIC_BASE, and exits with a code other than 0 when the run fails.
    A run that applied no mutant is a failed run: the command says so itself. A diff that holds nothing to mutate exits with 0, as "not applicable" or "no candidate".
    Exit code 0 is a pass, and its output is ignored.
    The output of a failed host job is the end of its log, so it holds the noise of the tool too. A failed run gives each line that starts with "finding: " as one finding, without the prefix.
    A failed run with no such line gives each non-blank line, the last 20 at most, as one finding. A failed run that printed no line gives one finding with its exit code."""
    run = command(name = "mutation", run = words + ["origin/" + base], host = "build", slots = "mutation")
    if run.rc == 0:
        return ""

    lines = [line.strip() for line in run.stdout.split("\n") if line.strip() != ""]
    found = [line[len(FINDING_PREFIX):] for line in lines if line.startswith(FINDING_PREFIX)]
    lines = found if len(found) > 0 else lines[-20:]
    if len(lines) == 0:
        return "The mutation command exited with code %d and printed no finding. A run that fails is a finding. Read its log, then fix the cause." % run.rc

    return "The mutation run failed. Each line is one finding:\n" + "\n".join(["- " + line for line in lines])

def lane_report(lane):
    """Checks the report that a lane wrote, as the command step candidate-lane-report, and returns the findings as text. An empty text means the report is whole.
    `remedy report` reads the report from its stdin and judges it against the head of the lane: the four tables, the head and the line about background commands.
    Exit code 2 means the check could not run, and it ends the item. Every other code except 0, such as 1, 3 or 127, is a defect of the lane: its output is the findings, and they send the lane back to a fix."""
    done = command(name = "candidate-lane-report", run = ["remedy", "report", "/dev/stdin", "--head", lane.head_sha], stdin = lane.report, stop_on_rc = [2])
    if done.rc == 0:
        return ""

    return "The report of the lane has a defect. Each line is one finding:\n" + done.stdout.strip()

def id_patterns(ids):
    """Returns the forbid_text patterns of a merge for the ids of its items, to add to the attribution patterns. No line of a pull request body or of a commit message may match them.
    A text names a change by its problem, fix and result, and never by a tracker id. An id that is only digits is the number of a GitHub issue, which a body may cite as #N, so it adds no pattern."""
    patterns = []
    for id in ids:
        if not id.isdigit():
            patterns.append("(?i)\\b" + id.replace(".", "\\.") + "\\b")

    return patterns

def severity_problem(severity, operator):
    """Returns why a filed issue may not have this severity, or an empty text. An issue carries one of P0 to P3. Severity is operator-first:
    an issue that an operator hits in normal use is never below P2, and any other issue is never above P2."""
    if severity not in SEVERITIES[:4]:
        return "severity %r is not one label of P0, P1, P2 or P3" % severity

    rank = SEVERITIES.index(severity)
    if operator and rank > 2:
        return "an issue that an operator hits in normal use is never below P2, and this one is %s" % severity

    if not operator and rank < 2:
        return "an issue that an operator does not hit in normal use is never above P2, and this one is %s" % severity

    return ""

FINDING_FIELDS = ["id", "title", "body", "found", "severity", "operator"]

def changelog_missing(changelog, base, head, id, fetch = False):
    """Returns the reason to refuse a change, or "" when it may merge: the project declares a changelog and the change from the base to `head` does not touch that file.
    A project that declares none passes. The paths come from git in the folder of the item. Both merge paths call this, so the rule has one place.
    `fetch` is true for a head that may not be in the folder yet (the members of a batch): the head is fetched before it is read."""
    if changelog == "":
        return ""

    if fetch:
        got = command(name = "changelog-fetch-" + id, run = ["git", "fetch", "origin", head], stop_on_rc = [2])
        if got.rc != 0:
            return "REFUSE: the head %s of item %s could not be fetched, so the changelog %s could not be checked: %s" % (head, id, changelog, got.stderr.strip())

    paths = command(name = "changed-paths-" + id, run = ["git", "diff", "--name-only", "origin/" + base + "..." + head], stop_on_rc = [2])
    if paths.rc != 0:
        return "REFUSE: the changed paths of item %s could not be read, so the changelog %s could not be checked: %s" % (id, changelog, paths.stderr.strip())

    if changelog not in paths.stdout.split("\n"):
        return "REFUSE: the project declares the changelog %s and this change does not touch it. Add the entry for the change to the file." % changelog

    return ""

def decode_output(text, what, empty):
    """Decodes the JSON text of an agent output. `what` names the output in a refusal, and `empty` is the value to write when there is nothing to report. A text that is not JSON is refused with `what` and the first 200 characters of the text."""
    decoded = json.decode(text, default = ("invalid",))
    if type(decoded) == "tuple":
        points = list(text.codepoints())
        shown = text if len(points) <= 200 else "".join(points[:200]) + "..."
        fail("REFUSE: %s is not valid JSON: %r. Write %s when there is nothing to report" % (what, shown, empty))

    return decoded

def uncaused_text(implement, fixes):
    """The JSON list of the reds that the lanes did not cause: the output `uncaused` of the implement lane and of each fix lane, joined. An entry whose id an earlier lane already listed is left out, so two lanes that report one red file one item.
    An entry that is no object stays in the list, and file_parked refuses it. A text that is not JSON is refused with the name of the lane and the text itself."""
    joined = []
    seen = []
    lanes = [implement] + fixes
    for place in range(len(lanes)):
        lane = lanes[place]
        name = "the implement lane" if place == 0 else "fix lane %d" % place
        listed = decode_output(lane.uncaused, "the output uncaused of " + name, "[]")
        if type(listed) != "list":
            fail("REFUSE: the output uncaused of a lane is not a JSON list. Write [] when the lane found no red that it did not cause")

        for entry in listed:
            id = entry.get("id") if type(entry) == "dict" else None
            if id != None and id in seen:
                continue

            seen.append(id)
            joined.append(entry)

    return json.encode(joined)

def file_parked(parked, output = "parked"):
    """Files one issue for each finding that the review loop leaves open. `parked` is the output of the settle step, or the joined outputs `uncaused` of the lanes: a JSON list. `output` names it in a refusal, and a text that is not JSON is refused with that name and the start of the text.
    Each finding has an id (words in lower case joined by hyphens, the same for the same finding), a title, a body in the template of the repository,
    `found` (how the finding was found, with the commit and the exact command), a severity of P0 to P3, and `operator` (true when an operator hits it in normal use).
    The issue has no assignee, so the queue can give it out. The key is the id of the finding: a resume that files again returns the issue that exists.
    Every finding is checked before the first issue is filed, so a bad entry files nothing."""
    findings = decode_output(parked, "the output " + output, "[]")
    if type(findings) != "list":
        fail("REFUSE: the output %s is not a JSON list. Write [] when no finding stays open" % output)

    seen = []
    for entry in findings:
        if type(entry) != "dict":
            fail("REFUSE: an entry of %s is not a JSON object: %r" % (output, entry))

        for field in FINDING_FIELDS:
            if field not in entry:
                fail("REFUSE: an entry of %s has no %r: %r" % (output, field, entry))

        id = entry["id"]
        if id == "" or id != id.lower() or not id.replace("-", "").isalnum() or id in seen:
            fail("REFUSE: the id %r of a finding in %s is empty, repeated or not words in lower case joined by hyphens" % (id, output))

        seen.append(id)

        for field in ["title", "body", "found"]:
            if type(entry[field]) != "string" or entry[field].strip() == "":
                fail("REFUSE: the finding %s has an empty %s" % (id, field))

        problem = severity_problem(entry["severity"], entry["operator"] == True)
        if problem != "":
            fail("REFUSE: the finding %s: %s" % (id, problem))

    for entry in findings:
        item_create(
            name = "file-" + entry["id"],
            key = "finding-" + entry["id"],
            title = entry["title"],
            body = entry["body"].rstrip() + "\n\n## How it was found\n\n" + entry["found"].strip(),
            severity = entry["severity"],
            operator = entry["operator"] == True,
            forbid_text = ATTRIBUTION,
        )

def land(approved, project, round_settings, n):
    """Lands the approved items of a pass as one batch named round-land-<n>: one gate run on the union of their heads, and a split of the union when the gate is red.
    Each entry of `approved` has an item id, the full SHA that the review approved, and the verdict of the child run (`approved`). Each member of the batch carries that verdict, so our own pull request
    lands only on the approval of its own review, and the approval of one member never reaches another. The setting vars.round.merge, the stops before the work and the guards are those of the pipeline of one item.
    The branch of each pull request has the id of its item. Each item that the batch merged is recorded with record_merge.
    Returns the host_losses of the batch call, the count of the engine for the tree of the run. It is 0 when the pass lands nothing."""
    if not round_settings["merge"]:
        return 0

    base = round_settings["base"]
    prs = []
    stopped = []

    for entry in approved:
        id, head = entry.item, entry.head

        # The stops of the pipeline of one item, for each item: merged, closed, branch gone, hold, draft, base, and a head that is not the reviewed one.
        # An item that stops is reported with its reason, and the other items still land.
        # The pull request of an approved item is still the draft that its run opened: the merge marks it ready, after this call. The number of that draft is no stop.
        # A child value with no draft number keeps the stop.
        own_draft = {"own_draft": entry.pr} if entry.pr else {}
        check = preflight(name = "intake-" + id, id = id, head = head, base = base, hold = "hold", own = {"logins": [round_settings["owner"]]}, **own_draft)
        if not check.ok:
            stopped.append(struct(item = id, reason = check.reason))
            continue

        # A project that declares a changelog lands no item without an entry, as the merge of one item does.
        missing = changelog_missing(project.vars.get("changelog", ""), base, head, id, fetch = True)
        if missing != "":
            stopped.append(struct(item = id, reason = missing))
            continue

        # The merge accepts the draft that the run opened, and marks it ready after its guards. The hand-off call reads no draft.
        prs.append(struct(item = id, head = head, approved = entry.approved, **own_draft))

    for skipped in stopped:
        item_comment(name = "stop-" + skipped.item, id = skipped.item, key = "rejected", text = skipped.reason, forbid_text = ATTRIBUTION)
        item_status(name = "stop-hold-" + skipped.item, id = skipped.item, status = "hold")

    if len(prs) == 0:
        return 0

    if round_settings["hand_off"]:
        # The owner merges: the call marks each pull request ready at its reviewed head, tells the owner, and waits until each one is merged, closed or a draft again.
        out = merge(name = "round-land-%d" % n, prs = prs, base = base, repo = project.repo, hand_off = True)
    else:
        out = merge(
            name = "round-land-%d" % n,
            prs = prs,
            base = base,
            repo = project.repo,
            hold = "hold",
            own = {"logins": [round_settings["owner"]], "emails": [round_settings["owner_email"]]},
            gate_run = round_settings["union_gate"],
            forbid_text = ATTRIBUTION + id_patterns([pr.item for pr in prs]),
            integration = round_settings["integration_pr"],
        )

    # A rejected item gets the reason as a comment and the hold status, so it does not come back until a person reads it.
    # With hand_off, a pull request that is closed, whose head moved after the ready mark, or that has a reason to wait is rejected, and a ready one is a draft again.
    for rejected in out.rejected:
        item_comment(name = "no-" + rejected.item, id = rejected.item, key = "rejected", text = rejected.reason, forbid_text = ATTRIBUTION)
        item_status(name = "hold-" + rejected.item, id = rejected.item, status = "hold")

    # Each merged item moves its card to the merged column, as the pipeline of one item does. The item ids of a project with a board are issue numbers.
    heads = {entry.item: entry.head for entry in approved}
    for landed in out.merged:
        _move_card(project, "merged", issue = int(landed))
        record_merge(landed, heads[landed], base)

    # The read of the issues after the batch. A note means the read did not run, which is not a pass: the round prints it in the run record, because the round has no item issue for a comment.
    # A finding stops the run. The batch has landed, so the owner fixes the state of the issues by hand.
    if out.note != "":
        print("the issue check did not run: " + out.note)

    # A merged item named in unproven is not on the base, and not on the vehicle commit. The owner reads the lines before any issue closes.
    if len(out.unproven) > 0:
        fail("REFUSE: the batch merged, but a member is not proven on the base. Check each one by hand:\n" + "\n".join(["item %s: %s" % (u.item, u.reason) for u in out.unproven]))

    if len(out.findings) > 0:
        fail("REFUSE: the batch landed, but the issues are not as they must be. Fix the state of each issue by hand:\n" + "\n".join(out.findings))

    # The acceptance script runs on the build host against the round branch after each batch that landed something. A red script stops the run, as a finding does.
    if round_settings["acceptance"] != "" and len(out.merged) > 0:
        accepted = gate(name = "round-acceptance-%d" % n, run = ["sh", round_settings["acceptance"]], host = "build", timeout = ACCEPTANCE_TIMEOUT)
        if not accepted.passed:
            fail("REFUSE: the batch landed, but the acceptance script %s failed on %s. Read its output, then fix the round branch by hand:\n%s" % (round_settings["acceptance"], base, accepted.report))

    return out.host_losses

def take_over(item, pr, author, project, round_settings):
    """Takes over the pull request `pr` of somebody else, in this order: the review, the notice, the grace period, the take.
    No entry calls it yet: the engine refuses every foreign pull request until the sandbox default lands. `author` is the author of the pull request, from the queue row.
    1. A review agent posts the findings as a changes-requested review. The notice is refused until the review is on the pull request.
    2. The tool posts the notice that asks the author for an answer, and refuses when the pull request is not stale (IDLE_HOURS) or is ours.
    3. The grace period (GRACE_HOURS) is a clock rule: the author has that long to answer KEEP or RELEASE. KEEP stops the item. RELEASE or no answer when the time is over goes on to the take.
    4. The tool takes the pull request only when no one forbade it. It checks the clock itself as a second guard, and a take with replies that carry neither token is refused: the item then holds with the reason of the tool, and a person reads the replies.
    The run of the item holds the claim, so the take writes none. A refusal of the tool (exit code 2) ends the item with the lines it printed."""
    base = ["remedy", "take", pr, "--me", round_settings["owner"], "--idle", IDLE_HOURS, "--grace", GRACE_HOURS, "--item", item.id]

    agent(
        name = "take-review",
        prompt = "prompts/take_review.md",
        model = _model(round_settings, "take_review"),
        runner = _runner(round_settings, "take_review"),
        edits = False,
        label = "take review",
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"item": item, "pr": pr, "facts": project},
    )

    command(name = "take-notice", run = base + ["--apply"], stop_on_rc = [2])

    grace = wait(
        "The notice is on pull request %s. Answer KEEP to keep it, or RELEASE to hand it over. With no answer in %s hours, it is taken." % (pr, GRACE_HOURS),
        name = "take-grace",
        timeout = GRACE_HOURS + "h",
        to = [author],
        choices = ["KEEP", "RELEASE"],
    )

    if grace.answer == "KEEP":
        return

    command(name = "take-claim", run = base + ["--take"], stop_on_rc = [2])

def foreign_followup(recorded, project):
    """Files one follow-up issue for the findings that a foreign review left open, once the reviewed pull request merged, and returns its id. It returns "" when it files nothing.
    No entry calls it yet: the engine refuses every foreign pull request until the sandbox default lands (#438), and that change wires it in.
    `recorded` is the value of the foreign review run: `pr` is the number that the intake preflight gave, and `findings` the open findings of the review, each with a `class` and a `text`.
    `project` is the value of facts().
    The step reads the state of that number. It does not look the pull request up again, so a branch that is not the item id and a base that is not vars.round.base change nothing.
    A review with no open findings files nothing, and so does a pull request that is still open or closed without a merge.
    The key holds the repository and the number and is shared by every run, so a second run on this host for the same pull request files no second issue.
    Two hosts that share one tracker can still file two issues for one pull request."""
    findings = recorded["findings"]
    if len(findings) == 0:
        return ""

    pr = recorded["pr"]
    if pr_state(name = "foreign-state", pr = pr).state != "MERGED":
        return ""

    lines = ["- %s: %s" % (finding["class"], finding["text"]) for finding in findings]
    return item_create(
        name = "foreign-followup",
        key = "foreign:%s:%d" % (project.repo, pr),
        shared = True,
        title = "Open review findings of the merged pull request #%d" % pr,
        body = "Pull request #%d merged with these review findings open:\n\n%s\n" % (pr, "\n".join(lines)),
        forbid_text = ATTRIBUTION,
    ).id

def record_merge(id, head, base):
    """Closes the issue of the item `id` that a batch merged into the round branch `base`, with the evidence: a comment, then the status closed. The tracker builtins do both,
    so the close does not depend on an agent, on the gh tool or on an integration pull request that exists (vars.round.integration_pr may be 0).
    The count rows of the integration pull request are written by round_integration, not here."""
    item_comment(
        name = "merged-" + id,
        id = id,
        key = "merged",
        text = "Merged into %s at %s. The gate passed at that head and the review approved it." % (base, head),
        forbid_text = ATTRIBUTION + id_patterns([id]),
    )
    item_status(name = "closed-" + id, id = id, status = "closed")

def line_fields(text):
    """Returns the `name=value` words of the line of a tool as a dict. A word with no `=` is left out."""
    fields = {}
    for word in text.split():
        pair = word.split("=", 1)
        if len(pair) == 2:
            fields[pair[0]] = pair[1]

    return fields

# The fields that each state of a line of `remedy sync` must carry. The round acts on these states only.
# A line with no state reads as current: the tool prints a state on every exit 0, and a dry run prints nothing.
SYNC_FIELDS = {
    "current": [],
    "synced": ["branch", "pr", "head", "base_tip"],
    "conflict": ["branch", "source"],
    "refused": [],
}

# The states of `remedy sync land` after which the pass goes on. A moved base or a moved head lands nothing, and the next pass syncs again.
SYNC_LAND_STATES = ["landed", "stale", "head-moved"]

# Each reason that ends a pass before its queue audit, with what the owner does before the answer CONTINUE.
SYNC_REASONS = {
    "sync-conflict": "The agent did not resolve the conflict on the sync branch. Merge the branch that the sync step names into the sync branch, resolve and push.",
    "sync-refused": "The forge refused the merge for the workflow permission. Merge the trunk into the round branch by hand.",
    "sync-red": "The gate failed on the head of the sync branch. Read the gate of the sync pull request.",
    "sync-review": "The review did not approve the sync pull request. Its findings are in the reply of the step sync-review of the last pass: beehave status with the run id of the round lists the steps.",
    "host-loss": "The build host lost gate jobs, and no gate gave a verdict about the code. Check the host and its link.",
}

def sync_end(reason, host_losses = 0):
    """The end of a sync: the reason that holds the round, or an empty text, and the highest host_losses that a gate of the sync read."""
    return struct(reason = reason, host_losses = host_losses)

def sync_check(name, run):
    """Runs one check of `remedy sync` as the command step `name` and returns the fields of its line, with the state set.
    Exit code 2 (a forge failure or a bad argument) stops the round with the line of the tool. Any other exit code but 0, a state the round does not know, or a field that its state needs and lacks, stops the run."""
    out = command(name = name, run = run, stop_on_rc = [2])
    if out.rc != 0:
        fail("REFUSE: the check %s ended with the exit code %d and the line %r. Check the remedy tool of the round, then run the round again." % (name, out.rc, out.stdout))

    fields = line_fields(out.stdout)
    state = fields.get("state", "current")
    if state not in SYNC_FIELDS:
        fail("REFUSE: the line of %s has the state %r, which the round does not know. Check the remedy tool of the round, then run the round again." % (name, state))

    missing = [key for key in SYNC_FIELDS[state] if fields.get(key, "") == ""]
    if missing:
        fail("REFUSE: the %s line of %s has no %s. The fields are %r. Check the remedy tool of the round, then run the round again." % (state, name, ", ".join(missing), fields))

    fields["state"] = state
    return fields

def sync_land(item, project, round_settings, line, n):
    """Puts the folder of the round at the head of a synced line, runs the gate command on it, reviews the sync pull request and lands it pinned to that head.
    The gate has no base, so it runs the gate command only: no new-tests check and no mutation step. Its pin_base is the round base, so it reads the pin file there.
    A void gate with a host cause runs again as sync-gate-<n>-host-<k>, until the engine counts HOST_LOSS_LIMIT host losses in the tree of the run.
    Returns sync_end with the reason sync-red, sync-review or host-loss when the pass must end, else an empty reason."""
    head = line["head"]
    for step, run in [("sync-fetch-%d" % n, ["git", "fetch", "origin", line["branch"]]), ("sync-checkout-%d" % n, ["git", "checkout", "--detach", head])]:
        if command(name = step, run = run).rc != 0:
            fail("REFUSE: the step %s could not put the folder of the round at the head %s of %s. Check the folder of the round. %s" % (step, head, line["branch"], restart_steps(item)))

    check = gate(name = "sync-gate-%d" % n, head = head, pin_base = round_settings["base"])
    losses = check.host_losses
    for k in range(1, HOST_LOSS_LIMIT + 1):
        if not is_host_void(check) or check.host_losses >= HOST_LOSS_LIMIT:
            break

        check = gate(name = "sync-gate-%d-host-%d" % (n, k), head = head, pin_base = round_settings["base"])
        losses = max(losses, check.host_losses)

    if is_host_void(check):
        return sync_end("host-loss", losses)

    if not check.passed:
        return sync_end("sync-red", losses)

    review = agent(
        name = "sync-review-%d" % n,
        prompt = "prompts/sync_review.md",
        model = _model(round_settings, "sync_review"),
        runner = _runner(round_settings, "sync_review"),
        label = "sync review",
        edits = False,
        pin = head,
        outputs = ["verdict"],
        output_tries = 2,
        forbid_commands = FORBID + ["git-write"],
        protect_paths = protected(round_settings),
        sandbox = sandbox(network = ["github.com", "api.github.com"]),
        inputs = {"pr": line["pr"], "branch": line["branch"], "head": head, "base": round_settings["base"], "facts": project},
    )
    capped(review, "sync-review", item)
    if review.verdict != "approve":
        return sync_end("sync-review", losses)

    landed = line_fields(command(name = "sync-land-%d" % n, run = ["remedy", "sync", "land", "--pr", line["pr"], "--head", head, "--base-tip", line["base_tip"]]).stdout)
    if landed.get("state", "") not in SYNC_LAND_STATES:
        fail("REFUSE: the landing of the sync pull request %s gave the fields %r. Read the pull request. %s" % (line["pr"], landed, restart_steps(item)))

    if landed["state"] == "landed":
        round_integration(project, round_settings, "round-integration-sync-%d" % n)

    return sync_end("", losses)

def sync_trunk(item, project, round_settings, n):
    """Merges a moved trunk into the round branch at the start of pass `n`, before its queue audit. Returns sync_end: a reason of SYNC_REASONS that ends the pass, or an empty reason, and the host_losses that its gate read.
    A conflict gets one agent step, sync-resolve-<n>, on the sync branch, the push of its head by the engine, and then one more check. The tool merges the forge side and writes no checkout."""
    run = ["remedy", "sync", "--base", round_settings["base"], "--trunk", project.base_branch]
    line = sync_check("sync-%d" % n, run)

    if line["state"] == "conflict":
        resolve = agent(
            name = "sync-resolve-%d" % n,
            prompt = "prompts/sync.md",
            model = _model(round_settings, "sync_resolve"),
            runner = _runner(round_settings, "sync_resolve"),
            forbid_commands = FORBID,
            protect_paths = protected(round_settings),
            sandbox = sandbox(network = ["github.com", "api.github.com"]),
            inputs = {"branch": line["branch"], "source": line["source"], "base": round_settings["base"], "facts": project},
        )
        capped(resolve, "sync-resolve", item)

        # The agent has no credential, so the engine pushes the merge commit to the sync branch. With no lease, git takes the merge only as a fast forward of the sync branch.
        # A resolve that made no commit ends on the head it started on, which is not a commit of the sync branch, so a push of it would move the branch off its commits.
        if resolve.committed:
            push(name = "push-sync-%d" % n, branch = line["branch"], head = resolve.head_sha, expect = "")
        line = sync_check("sync-again-%d" % n, run)

    state = line["state"]
    if state == "conflict":
        return sync_end("sync-conflict")

    if state == "refused":
        return sync_end("sync-refused")

    if state == "synced":
        return sync_land(item, project, round_settings, line, n)

    return sync_end("")

def host_hold(seen, acked):
    """Returns host-loss when the engine counted HOST_LOSS_LIMIT host losses or more in the tree of the run, and a loss came after the last answer of the owner. Else an empty text."""
    if seen >= HOST_LOSS_LIMIT and seen > acked:
        return "host-loss"

    return ""

def round_question(reason, seen, idle):
    """The question of round-wait: the reason that held the round when there is one, `seen`, the host losses that the round read, and `idle`, the text that names why no item can move."""
    if reason == "":
        return "The round has no item it can move. %s Then answer CONTINUE to check the queue again, or STOP to end the round." % idle

    if reason == "host-loss":
        return "The engine counted %d host losses in this round. %s Then answer CONTINUE to go on. The round asks again after a newer loss." % (seen, SYNC_REASONS[reason])

    return "The round did not merge the trunk into the round branch: %s. %s Then answer CONTINUE to check the trunk again." % (reason, SYNC_REASONS[reason])

def round_integration(project, round_settings, name = "round-integration"):
    """Finds or creates the integration pull request from the round branch into the trunk, and returns its number and state. `name` is the name of the step.
    The tool `remedy integration` uses vars.round.integration_pr when it is not 0, and prints `pr=<n> state=<state>`. The tool makes the whole body again from the pull requests merged into the round branch, so a merge of an item or of a sync is never missing from it.
    The round runs it after each landing of items (round-integration-land-<n>), after each landing of a sync (round-integration-sync-<n>) and when the round has nothing to move."""
    out = command(
        name = name,
        run = ["remedy", "integration", "--base", round_settings["base"], "--into", project.base_branch, "--pr", str(round_settings["integration_pr"])],
        stop_on_rc = [2],
    )
    fields = line_fields(out.stdout)

    if not fields.get("pr", "").isdigit():
        fail("REFUSE: the line of round-integration has no `pr=<n>` figure. The line is %r" % out.stdout)

    return struct(pr = int(fields["pr"]), merged = fields.get("state", "") == "MERGED")

def next_round(project, round_settings, pr):
    """Cuts the next round after the integration pull request `pr` of the old round merged. vars.round.next_base names the new round branch. vars.round.base is the old one.
    `pr` is the number that round_integration returned, so a round with vars.round.integration_pr 0 cuts with the pull request that the tool found.
    The cut tool refuses while the integration pull request is not merged (exit code 2), so no round starts before the one before it ended."""
    command(
        name = "round-cut",
        run = ["remedy", "cut", "--old", round_settings["base"], "--new", round_settings["next_base"], "--pr", str(pr), "--trunk", project.base_branch],
        stop_on_rc = [2],
    )

def record_base_reds(round_settings):
    """Writes the one record of the known base reds: the reds of vars.round.base_reds, the time and the tip of the base. Each new write replaces the file, so the file holds one record.
    vars.round.base_reds_file names the file, as a path from the folder of the round. The default is ../base-reds-<base>.md, with each "/" of the base written as _2F. It lies in the folder of the project, outside every item folder, so the gate sees a clean folder, and each round has its own file.
    The file is the record for the owner. A lane reads the same list from the data file of its step, because its sandbox cannot read this file."""
    base = round_settings["base"]
    tip = command(name = "base-tip", run = ["git", "rev-parse", "origin/" + base], stop_on_rc = [2]).stdout.strip()
    text = "# Known base reds\n\nMeasured at %s on the tip %s of %s.\n\n%s\n" % (now(), tip, base, "\n".join(["- " + red for red in round_settings["base_reds"]]))

    # Every lane reads the file, so the record is world-readable whatever the umask of the engine is. The path goes in the environment, so a path with a space stays one word.
    write(
        name = "base-reds",
        run = ["sh", "-c", 'umask 022 && exec tee -- "$BASE_REDS_FILE"'],
        env = {"BASE_REDS_FILE": round_settings["base_reds_file"]},
        stdin = text,
        timeout = WRITE_TIMEOUT,
    )

def derive_gate_list(project):
    """Derives the gate list of the round from the CI workflow files of vars.gate_files, and holds gate_steps to it by vars.gate_map. A project with no gate_files derives nothing.
    `remedy gatelist` stops the round (exit code 2) when the files give no step, or when gate_map does not map each derived command exactly to gate_steps names.
    See the gate list section of docs/design/agent-runs.md."""
    files = project.vars.get("gate_files", [])

    if len(files) == 0:
        return

    if len(project.gate_steps) == 0:
        _vars_refuse("gate_files", "is set, but the project has no gate_steps. Set gate_steps in the project file, one named step for each command of the gate, and map each CI command to them in vars.gate_map.")

    steps = []
    for name in project.gate_steps:
        steps += ["--steps", name]

    command(name = "gate-list", run = ["remedy", "gatelist"] + steps + ["--map", json.encode(project.vars.get("gate_map", {}))] + files, stop_on_rc = [2])

# A bound for the loop of the round, because Starlark has no while. A pass ends one run or starts one, so the bound is far above the work of any real round.
MAX_PASSES = 500

def queue_audit(item, project, round_settings, n, q):
    """Runs the agent step queue-audit-<n> after the queue read `q` of the pass and before every spawn, also when the queue is empty.
    The agent audits the rows and the open pull requests that the engine read, from the data file: it holds no forge tool and reads no forge itself."""
    agent(
        name = "queue-audit-%d" % n,
        prompt = "prompts/queue.md",
        model = _model(round_settings, "queue_audit"),
        runner = _runner(round_settings, "queue_audit"),
        edits = False,
        label = "queue audit",
        sandbox = sandbox(),
        inputs = {"item": item, "pass": n, "base": round_settings["base"], "facts": project},
        data = {"rows": q.rows, "pulls": q.pulls},
    )

def ready_items(q, round_settings):
    """Returns the ids of the rows of the queue `q` that this pass may start, in the order of the rank rule, up to the free slots and vars.round.lane_cap.
    The queue read applies vars.round.label. The item list vars.round.items is applied here, to the rows."""
    listed = round_settings["items"]
    rows = [r for r in q.rows if len(listed) == 0 or r.item in listed]
    return [r.item for r in rank(rows, round_settings["owner"])][:min(q.free, round_settings["lane_cap"])]

# Each reason that a listed item does not move, by the state of its row, with the step of the owner. missing is an item with no row. A ready row shows here only when the items queue has no free slot.
ITEM_CLAUSES = {
    "failed": "Listed item {item} is failed: run beehave resume {item}. If the resume refuses the run, run beehave cancel {item}, and the next pass starts a new attempt.",
    "hold": "Listed item {item} is on hold: take the hold off the item or its pull request.",
    "done": "Listed item {item} is done.",
    "running": "Listed item {item} has an open run outside this round: beehave status shows it.",
    "ready": "Listed item {item} is ready, and the items queue has no free slot.",
    "missing": "Listed item {item} is not in the queue: it is closed, or it does not carry vars.round.label.",
}

def idle_reason(q, round_settings):
    """Returns the text that names why the pass of the queue read `q` moved no item: one clause for each listed item with its state and the step of the owner, or the count of the rows in each state when the round has no item list."""
    listed = round_settings["items"]
    if len(listed) == 0:
        counts = {}
        for r in q.rows:
            counts[r.state] = counts.get(r.state, 0) + 1

        head = "The queue has no ready row." if counts.get("ready", 0) == 0 else "The items queue has no free slot."
        failed = [r.item for r in q.rows if r.state == "failed"]
        steps = " Failed: %s. Run beehave resume for each, or beehave cancel when the resume refuses the run." % ", ".join(failed) if len(failed) > 0 else ""
        return "%s Rows: %d ready, %d failed, %d on hold, %d done, %d running.%s" % (head, counts.get("ready", 0), counts.get("failed", 0), counts.get("hold", 0), counts.get("done", 0), counts.get("running", 0), steps)

    rows = {r.item: r.state for r in q.rows}
    clauses = []
    for item in listed:
        clauses.append(ITEM_CLAUSES[rows.get(item, "missing")].format(item = item))

    return " ".join(clauses)

def round(item):
    """The round: one entry for the whole round, with one child run (the entry pipeline) for each item. `item` is the round itself, an id that no tracker row uses.
    A pass is sync-<n> (sync_trunk), round-queue-<n>, queue-audit-<n> on the rows of that read, the cap, round-spawn-<n>, round-gather-<n> (first_of: waits until a run ends and takes every run that ended in the same read) and round-land-<n>. The round never returns while a child run is open, but a step that stops the run, such as an exit code 2 of a sync check, ends it with the open runs.
    A sync that ends with a reason holds the round: the later passes start no sync and no item, and only gather the open runs. With none open, the round waits in round-wait with the reason.
    The host losses hold it too. `seen` is the highest host_losses that a child result, the landing or the sync gate gave: the count of the engine for the tree of the run, and no count of the round.
    At HOST_LOSS_LIMIT or more, and above `acked`, the reason is host-loss. CONTINUE sets `acked` to `seen`, so the round asks again only after a newer loss.
    It stops when no ready row can move and no child run is open: then it runs round-integration and waits for the owner in round-wait. CONTINUE starts another pass, which checks the trunk again.
    A STOP or the timeout returns the state waiting with the reason, an empty text when no sync held the round. When the integration pull request is merged and vars.round.next_base is set, the round cuts the next round."""
    project = facts()
    round_settings = settings(project)

    # The round starts its host once. The child entry pipeline calls no host builtin, so a child cannot stop it. finish stops it.
    host_up(name = "round-host", host = project.build_host)
    record_base_reds(round_settings)
    derive_gate_list(project)

    runs = []
    approved = []
    held = ""
    seen = 0
    acked = 0

    for n in range(1, MAX_PASSES + 1):
        if held == "":
            held = host_hold(seen, acked)

        if held == "":
            synced = sync_trunk(item, project, round_settings, n)
            held = synced.reason
            seen = max(seen, synced.host_losses)

        if held == "":
            q = queue(name = "round-queue-%d" % n, hold = "hold", label = round_settings["label"])
            queue_audit(item, project, round_settings, n, q)
            take = ready_items(q, round_settings)
            if len(take) > 0:
                runs = runs + spawn(items = take, entry = "pipeline", base = round_settings["base"], name = "round-spawn-%d" % n).runs

        if len(runs) > 0:
            ended = [e.outcome for e in first_of(runs = runs, name = "round-gather-%d" % n).runs]
            ended_items = [e.item for e in ended]
            runs = [r for r in runs if r.item not in ended_items]
            for result in ended:
                if result.ok and result.value != None:
                    seen = max(seen, result.value["host_losses"])
                    if result.value["approved"]:
                        approved.append(struct(item = result.item, head = result.value["head"], approved = result.value["approved"], pr = result.value.get("pr", 0)))

            if len(approved) > 0:
                seen = max(seen, land(approved, project, round_settings, n))
                approved = []
                round_integration(project, round_settings, "round-integration-land-%d" % n)

            continue

        # No ready row can move and no child run of this round is open.
        integration = round_integration(project, round_settings)
        answer = wait(
            round_question(held, seen, idle_reason(q, round_settings) if held == "" else ""),
            name = "round-wait",
            timeout = "%dh" % round_settings["wait_hours"],
            to = [round_settings["owner"]],
            choices = ["CONTINUE", "STOP"],
        )
        if answer.timed_out or answer.answer == "STOP":
            return {"state": "waiting", "base": round_settings["base"], "pr": integration.pr, "reason": held}

        if held == "host-loss":
            acked = seen

        held = ""

        if integration.merged and round_settings["next_base"] != "":
            next_round(project, round_settings, integration.pr)
            return {"state": "cut", "base": round_settings["next_base"]}

    return {"state": "waiting", "base": round_settings["base"], "pr": 0, "reason": held}

def main(item):
    round(item)

def finish(item):
    """The engine calls finish after main on every end of the run except a cancel: success, error and command stop. A child run never calls it.
    It stops the build host once. A host that this round did not launch (host_up started it) is never stopped: another run may use it.
    The stop banks the work on the host first. A host that cannot bank its work stays up and finish prints the reason: the round has no item issue for a comment."""
    stop = host_teardown(name = "round-teardown", host = facts().build_host)
    if not stop.down:
        print("the build host stays up: " + stop.reason)

def _move_card(project, stage, pr = 0, issue = 0):
    """Moves the card to the column that `vars.board` of the project names for `stage` ("started" or "merged"), and reads it back.
    `issue` names the issue card of a landed item, and `pr` the pull request of the item of the run. A project with no `vars.board` makes no call.
    The `remedy board` command puts the card on the board, sets the column and reads the card back. Exit code 2 (a refusal, or a read-back that shows another column) stops the run."""
    board = project.vars.get("board")
    if board == None:
        return

    card = ["--issue", str(issue)] if issue != 0 else ["--pr", str(pr)]
    owner = ["--owner", board["owner"]] if board.get("owner", "") != "" else []

    command(
        name = "board-" + stage + ("-" + str(issue) if issue != 0 else ""),
        run = ["remedy", "board", "--board", str(board["number"]), "--status", board[stage]] + owner + card,
        stop_on_rc = [2],
    )

def item_value(item, head, approved, reason, host_losses, pr = 0):
    """The value that a child run returns to the round: the item, the head it reached, whether the review approved it, why not when it did not,
    the highest host_losses that a gate of the child read, and the number of the draft that the child opened (0 when it opened none)."""
    return {"item": item.id, "head": head, "approved": approved, "reason": reason, "host_losses": host_losses, "pr": pr}

def sweep(project):
    """Runs the sweep of a lane on the build host and returns its result as text: the project command `vars.clean_command` (it deletes the target directory of the lane).
    A project with no clean command runs nothing, and the text says so. A failed step shows its exit code in the text, and a step that cannot run (a host that is gone)
    shows its error: failure = "return" gives the error as a result. Neither fails the lane."""
    words = project.vars.get("clean_command")
    if words == None:
        return "clean: not run, vars.clean_command is not set"

    clean = command(name = "sweep-clean", run = words, host = "build", failure = "return")

    return _swept("clean", clean)

def _swept(step, result):
    """One line of the sweep text: the exit code of the step, and its error when the step could not run."""
    if result.error:
        return "%s: rc=%d error: %s" % (step, result.rc, result.error)

    return "%s: rc=%d" % (step, result.rc)

def pipeline(item):
    """The child entry: the pipeline of one item. The round starts one child for each item with spawn(entry = "pipeline").
    It returns item_value to the round, which lands the approved heads in one batch. The child never merges.
    The sweep runs when the lane ends: after each verdict, and before each of the two fail calls of the lane. The value that the lane returns carries the sweep result as `sweep`.
    A sweep step that cannot run returns its error in the sweep text. A lane that an error of an earlier builtin ends (a host that is gone at the mutation run) gets no sweep:
    Starlark has no way to catch an error."""
    # The facts of the project file (the plumbing fields and the vars that hold the people, rules, limits, traps and the round settings). The step records them, so a resume keeps the facts the run started with.
    # Each agent of the example gets them as the input `facts`; no agent reads a path for them.
    project = facts()
    round_settings = settings(project)
    value = _lane(item, project, round_settings)
    value["sweep"] = sweep(project)

    return value

def _lane(item, project, round_settings):
    """The steps of one lane, from the intake to the verdict of the review. It returns item_value."""
    base = round_settings["base"]

    # Before any agent starts, the pull request of the item must be as the owner left it: not merged, not closed, its branch still on the remote,
    # no hold label, not a draft, and the base vars.round.base.
    # A stop is a result: the run ends here and starts no agent. The result of the run holds the stop as `stopped`: the step, the exit code and the reason.
    # An item with no pull request yet passes. An open pull request by a login that is not in `own` claims the item: somebody else is on it, and no agent starts.
    intake = preflight(name = "intake", head = "", base = base, hold = "hold", own = {"logins": [round_settings["owner"]]})
    if not intake.ok:
        return item_value(item, "", False, "stopped", 0)

    # The engine reads the pull requests of the item in every state, each closed or merged one with its closing comment, before any agent starts.
    # The plan and the implementer read them from their data file for the repeat-offender check: no sandboxed agent reads the forge.
    history = pr_history(name = "history")

    scope = lane_scope(round_settings)

    # The cap is for the pull request, not for one run: the candidates that earlier runs of this item started count against it. A candidate counts when its first agent starts.
    # The engine keeps the record of each run, so the count survives a restart. The first candidate reviews the implementer, so fix_rounds fix rounds need one candidate more.
    # After the cap, a run gets no candidate: no review and no fix lane start, and the owner rules on the open findings.
    rounds = max(round_settings["fix_rounds"] + 1 - candidates_spent(), 0)

    # The mutation command is read before any agent starts, so a project with no vars.mutation_command is refused here.
    mutation = mutation_command(project)

    # The sweep step below starts with the review skill, so the project must name it. The check runs here, before any agent starts.
    require_review_skill_named(project)
    require_runner_that_loads_skills(project, round_settings, "implement")

    # The item this one depends on is the parent of its row. An item with no parent has none.
    parent = item_read(name = "parent", of = "parent")

    plan = agent(
        name = "plan",
        prompt = "prompts/plan.md",
        model = _model(round_settings, "plan"),
        runner = _runner(round_settings, "plan"),
        edits = False,
        outputs = ["regressions"],
        # The plan reads the earlier pull requests of the item from its data file, so its shell reaches no host.
        sandbox = sandbox(),
        inputs = {"item": item, "facts": project},
        data = {"pulls": history.pulls},
    )

    capped(plan, "plan", item)

    # The implementer, each fix and each review read the known base reds of the round from the data file that the engine writes, not from the record of the owner.
    lane_data = {"base_reds": round_settings["base_reds"], "base": base}

    # The implement step and its sweep share one sandbox: the forge is the only host that they reach.
    lane_box = sandbox(network = ["github.com", "api.github.com"])

    implement = agent(
        name = "implement",
        prompt = "prompts/implement.md",
        model = _model(round_settings, "implement"),
        runner = _runner(round_settings, "implement"),
        label = "implement",
        outputs = ["report", "uncaused"],
        forbid_commands = FORBID,
        protect_paths = protected(round_settings),
        sandbox = lane_box,
        inputs = {"item": item, "plan": plan.text, "facts": project, "base": base, "acceptance": round_settings["acceptance"], "base_result": round_settings["base_result"]},
        data = dict(lane_data, pulls = history.pulls),
        **scope
    )

    # A cap ended the run (the output `stop` is "turns" or "budget"), so the work may be half done. The item stops here, before the sweep, the gate and the review.
    capped(implement, "implement", item)
    kept_history(implement, "implement", item)

    # The review sweep is a step of its own that the engine starts: the agent CLI loads the review skill before the first turn of the model, so no choice of the model is left to check.
    # It resumes the session of the implement step on the same runner, in the same sandbox and with the same limits. It may edit, and it commits each fix.
    sweep_step = agent(
        name = "implement-sweep",
        prompt = "prompts/sweep.md",
        model = _model(round_settings, "implement"),
        runner = _runner(round_settings, "implement"),
        resume = implement.session,
        skill = project.vars["review_skill"],
        label = "implement-sweep",
        outputs = ["report"],
        forbid_commands = FORBID,
        protect_paths = protected(round_settings),
        sandbox = lane_box,
        **scope
    )

    capped(sweep_step, "implement-sweep", item)

    # The agent of a sandboxed step has no credential, so the engine pushes its head to the branch of the item, the branch of its worktree. This is the first push of the branch in the run:
    # with no lease, git takes only a new branch or a fast forward. Every gate clones the forge, so the push comes before the first gate. The head is the head of the sweep, which holds the commits of the implement step.
    push(name = "push-implement", branch = item.id, head = sweep_step.head_sha, expect = "")

    # The first checkpoint the engine can act on: the plan step makes no commit and one agent step is opaque, so the draft opens after the implement step, on the branch that the engine pushed.
    # The engine opens the draft pull request of the item once (a resume finds the open one) and,
    # when the item has a parent, links it into the native stack of the pull request of that item. The call owns only the closing line of the body:
    # `Closes #N` when the base is the default branch and `Addresses #N` on any other. An item id that is not an issue number gets no line, and a body that exists is kept.
    # The body is the text of the item. Only the pull request into the trunk may close an issue, so a closing keyword in the text is allowed when the round branch is the trunk (integration)
    # and the call refuses it when the base is another branch.
    trunk = base == project.base_branch
    draft = pr_open(
        name = "draft",
        title = item.title,
        body = item.body,
        integration = trunk,
        base = base,
        issue = int(item.id) if item.id.isdigit() else 0,
        parent = parent.id,
        forbid_text = ATTRIBUTION,
    )

    # The board is a project choice: the key `board` of the project vars. With none, the run makes no board call.
    # With one, the card of the item moves to the started column now and to the merged column after the merge, and each call reads the card back.
    _move_card(project, "started", pr = draft.number)

    # Five fix rounds need six candidates: the sixth checks the fifth fix. A candidate gets the gate on the base, the checks before review, the mutation run and a full review once.
    # Between candidates, scoped fixes and scoped reviews run with no gate. Each reviewer starts fresh and judges the commit it is pinned to.
    # The fixes after candidate k are fix round k. The rounds up to vars.round.escalate_after run on the step fix and resume the implementer when both steps use the same runner; later rounds start fresh on the step fix_late.
    # This method has no follow-ups: a review that leaves any finding open gets the verdict "changes", and the loop runs a fix.
    # Before each review, design and fix agent, the loop reads the pull request again, with the head of the worktree, the base, the hold label and the own logins.
    # A stop in the loop ends the run as the stop at the intake does: no more agents start, and the result holds the step, the exit code and the reason.
    # The loop accepts the draft that this run opened (own_draft is its number) and stops at any other draft.
    guard = {"base": base, "hold": "hold", "own": {"logins": [round_settings["owner"]]}, "own_draft": draft.number}
    # The loop calls rise_after_review after each full review, so a rise ends the loop before the next fix lane starts. A scoped review writes no count.
    def rise_after_review(review):
        return regression_rise(plan.regressions, review.regressions)

    # The mutation run follows the checks before review of each candidate and comes before the full review: a lived mutant is a finding for the fix lane, and the review never judges a head that has one.
    def mutants(gate):
        return check_mutation(mutation, base)

    # The loop starts from the sweep as the lane of the implementer. The violations of the implement step go with it, so its first fix still answers them.
    starting = struct(session = sweep_step.session, head_sha = sweep_step.head_sha, report = sweep_step.report, rewrote = sweep_step.rewrote, violations = "\n".join([text for text in [implement.violations, sweep_step.violations] if text]))
    loop = review_fix(item, starting, "prompts/review.md", "prompts/fix.md", base = base, follow_ups = "forbid", protect = protected(round_settings), design_prompt = "prompts/design.md", guard = guard, allow = scope.get("allow_paths"), rounds = rounds, review_outputs = ["regressions"], fix_outputs = ["report", "uncaused"], lane_check = lane_report, facts = project, data = lane_data, review_check = rise_after_review, checks = [mutants], steps = {name: round_settings["steps"][name] for name in LOOP_STEPS}, design_after = round_settings["design_after"], void_reruns = round_settings["void_reruns"], void_asks = round_settings["void_asks"], escalate_after = round_settings["escalate_after"], push_branch = item.id)

    if loop.stopped:
        return item_value(item, "", False, "stopped", loop.host_losses)

    # The limit of host losses stopped the loop. The host is at fault and not the item, so the item gets no comment and no hold status, and a later run starts it again.
    if loop.host_lost:
        return item_value(item, loop.head, False, "host-loss", loop.host_losses)

    # Each red that a lane says it did not cause is filed as its own item before any verdict, so no approval exists without the item. A refused filing ends the item with no approval.
    file_parked(uncaused_text(implement, loop.lanes), "uncaused")

    # A reject verdict ends the loop with no fix round. The item gets the text of the review as the reason and the hold status.
    # A scoped review can reject before the first gate, so an item that the loop holds reports loop.head, the commit that the loop judged last.
    if loop.rejected:
        item_comment(name = "rejected", id = item.id, key = "rejected", text = loop.review.text, forbid_text = ATTRIBUTION)
        item_status(name = "hold-rejected", id = item.id, status = "hold")
        return item_value(item, loop.head, False, "rejected", loop.host_losses)

    # A rise of the regression count, from the report of the plan to the report of any full review, holds the item with the reason. So does a count that is unknown in either report.
    # The loop stops at the review that shows the rise: a held item gets no more fix lane, no owner ruling and no settle step. Nothing merges.
    # The last gate of an exhausted loop can be red. Then there is no review and no count; the item goes on to the ruling of the owner, and nothing merges after the settle step.
    if loop.held != "":
        item_comment(name = "regression-rise", id = item.id, key = "regression-rise", text = loop.held, forbid_text = ATTRIBUTION)
        item_status(name = "hold-regression-rise", id = item.id, status = "hold")
        return item_value(item, loop.head, False, "regression-rise", loop.host_losses)

    # After two fixes on one mechanism, a design step decides whether the mechanism should exist. A replace or a delete ends the loop:
    # the item gets the decision and the reason as the comment and the hold status, and a person rules on it.
    if loop.design != None:
        item_comment(
            name = "design",
            id = item.id,
            key = "design",
            text = "Design decision for %s: %s. %s" % (loop.design.mechanism, loop.design.decision, loop.design.reason),
            forbid_text = ATTRIBUTION,
        )
        item_status(name = "hold-design", id = item.id, status = "hold")
        return item_value(item, loop.head, False, "design", loop.host_losses)

    # After the last round, no fix lane starts. A person rules on each open finding, and the settle step applies the ruling once: it is the only edit after the last round, no review follows it, and nothing merges.
    if loop.exhausted:
        ruling = wait(
            "The review loop used all of its rounds. Rule on each open finding: fix it now, or park it with the cost if it is wrong.",
            name = "adjudicate",
        )

        # The ruling can take days. The settle agent is a dispatch like the others, so the pull request is read again first, with the head that the loop judged last.
        # A run that got no candidate has no gate: its head is the head of the sweep.
        if not preflight(name = "before-settle", id = item.id, head = loop.head, **guard).ok:
            return item_value(item, loop.head, False, "stopped", loop.host_losses)

        settle = agent(
            name = "settle",
            prompt = "prompts/settle.md",
            model = _model(round_settings, "settle"),
            runner = _runner(round_settings, "settle"),
            outputs = ["parked"],
            inputs = {"ruling": ruling.answer, "facts": project},
            **scope
        )

        capped(settle, "settle", item)

        # The settle agent commits the changes of the ruling and has no credential, so the engine pushes its head. The lease is the head that the loop pushed last.
        push(name = "push-settle", branch = item.id, head = settle.head_sha, expect = loop.head)

        # Each finding that the owner parked stays open after the last round: it gets its own issue, so it is not lost with the pull request.
        file_parked(settle.parked, "parked of the settle step of item " + item.id)

        return item_value(item, settle.head_sha, False, "settled", loop.host_losses)

    # The child never merges. It returns the head that the gate ran on and the verdict of the review, and the round lands every approved head of a pass in one batch (land).
    # `approved` is the verdict of the review, which is not the agent that wrote the code. The batch lands our own pull requests only when it is true.
    missing = changelog_missing(project.vars.get("changelog", ""), base, loop.gate.head, item.id)
    if missing != "":
        sweep(project)
        fail(missing)

    # The approval is on the pull request, with the plan that the review judged the change against. The key makes a replay after a crash post it once.
    if loop.verdict == "approve":
        pr_comment(
            name = "approved",
            key = "approved",
            text = "The review approved the head %s.\n\nThe plan:\n\n%s" % (loop.gate.head, plan.text),
            integration = trunk,
            forbid_text = ATTRIBUTION,
        )

    return item_value(item, loop.gate.head, loop.verdict == "approve", "" if loop.verdict == "approve" else loop.verdict, loop.host_losses, pr = draft.number)
