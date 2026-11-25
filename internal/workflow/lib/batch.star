# The batch of a round: fold the members onto the base tip with git, gate the union once, split a red union by halves, and land a green union with one merge call.
# batch_merge returns the result of the batch, and issue_audit is the read of the issues after it.
# A project that wants another policy copies this file into its workflow folder and loads it with load("//batch.star", "batch_merge").

# The reasons that a member is rejected or deferred.
REASON_CONFLICT = "conflict"
REASON_RED = "red"
REASON_RED_ON_TOP = "red on top of the merged members"
REASON_MOVED = "the base moved while the batch ran; run the batch again"

# The note of a batch that landed nothing, so no issue was read.
NO_LANDING_NOTE = "issue states: no member landed, so no read ran."

# The number of lost gate jobs with a proof that end a call: the first loss gates the union again, and the second ends the call.
LOST_JOBS_OF_A_CALL = 2

# The author and committer of the union commits. They exist in the object database of the worktree only, and no forge sees them.
IDENTITY = ["-c", "user.name=beehave", "-c", "user.email=beehave@localhost"]

# git cat-file -e exits with 1 or 128 for an object that the repository does not hold, and git merge-tree exits with 1 for a conflict.
NO_OBJECT = [1, 128]
CONFLICT = 1

def _git(st, args, allowed):
    """Runs git in the worktree of the item and returns its result. An exit code that is not 0 and not in allowed ends the call with the output of git."""
    out = command(name = st["name"] + ".git", run = ["git"] + IDENTITY + args)
    if out.rc != 0 and out.rc not in allowed:
        fail("batch %s: git %s failed with exit code %d: %s" % (st["name"], args[0], out.rc, out.stderr.strip()))
    return out

def _merge_tree(st, ours, theirs):
    """Merges theirs into ours without a working copy: the tree of the merge, and whether it conflicts."""
    out = _git(st, ["merge-tree", "--write-tree", ours, theirs], [CONFLICT])
    return struct(tree = out.stdout.split("\n")[0].strip(), conflict = out.rc == CONFLICT)

def _members(name, prs):
    """The members in item order. Each needs an item and a head, and no item is listed twice."""
    if name == "":
        fail("batch_merge needs a `name` that is not empty. The name makes the step names of the call unique")
    members = sorted(prs, key = lambda m: m.item)
    for i, m in enumerate(members):
        if m.item == "" or m.head == "":
            fail("batch %s: a batch member needs an item and a head commit" % name)
        if i > 0 and members[i - 1].item == m.item:
            fail("batch %s: the batch lists %s twice" % (name, m.item))
    return members

def _reject(st, item, reason):
    st["decided"][item] = True
    st["rejected"].append(struct(item = item, reason = reason))

def _stop(st, reason):
    """Ends the batch. The members not decided so far are deferred with the reason."""
    st["stopped"] = True
    st["stop_reason"] = reason

def _present(st, members):
    """Rejects each member whose head is not in the repository, and returns the others."""
    present = []
    for m in members:
        if _git(st, ["cat-file", "-e", m.head + "^{commit}"], NO_OBJECT).rc != 0:
            _reject(st, m.item, "head %s of %s is not in the repository; fetch it" % (m.head, m.item))
        else:
            present.append(m)
    return present

def _culprit(st, included, m):
    """The first earlier member that conflicts with m by itself. When only the fold of several conflicts, the last earlier member.
    A member that conflicts with the fold and not with the tip alone has an earlier member."""
    for earlier in included:
        if _merge_tree(st, earlier.head, m.head).conflict:
            return earlier.item
    return included[-1].item

def _union(st, members):
    """Folds the members in order onto the base tip. A member that conflicts with the tip alone is a conflict, and one that conflicts only with an earlier member is held."""
    tip = st["tip"]
    commit, tree = tip.commit, tip.tree
    included, held, conflicts = [], [], []
    for m in members:
        made = _merge_tree(st, commit, m.head)
        if not made.conflict:
            commit = _git(st, ["commit-tree", made.tree, "-m", "beehave batch union", "-p", commit, "-p", m.head], []).stdout.strip()
            tree = made.tree
            included.append(m)
        elif _merge_tree(st, tip.commit, m.head).conflict:
            conflicts.append(m)
        else:
            held.append(struct(member = m, blame = _culprit(st, included, m)))
    return struct(commit = commit, tree = tree, included = included, held = held, conflicts = conflicts)

def _sort(st, u):
    """Routes the members that a union left out. In the first round a conflict with the base is rejected and a conflict with an earlier member is held for the retry.
    In the retry no member is held again, and a conflict names the earlier member that landed, or the base."""
    for m in u.conflicts:
        reason = REASON_CONFLICT
        if st["retry"]:
            blame = st["with"].get(m.item, "")
            reason = "conflicts with " + (blame if blame in st["merged"] else "base")
        _reject(st, m.item, reason)
    for h in u.held:
        if st["retry"]:
            _reject(st, h.member.item, "conflicts with " + h.blame)
        else:
            st["with"][h.member.item] = h.blame
            st["held"].append(h.member)

def _void(g):
    """The reason of a gate that gave no result about the code: no step failed and a step is unknown. Empty for a gate with a result."""
    statuses = [s["status"] for s in g.steps]
    if "fail" in statuses or "unknown" not in statuses:
        return ""
    return g.report.strip() or "a step of the gate is unknown"

def _gate_once(st, u):
    """Checks the union out in the worktree of the item and gates it. A lost job with a proof gates the same union again, and the second loss of the call is a void gate."""
    for _ in range(LOST_JOBS_OF_A_CALL):
        _git(st, ["checkout", "-q", "--detach", u.commit], [])
        if st["gate_run"]:
            g = gate(name = st["name"] + ".gate", run = st["gate_run"])
        else:
            g = gate(name = st["name"] + ".gate", pin_base = st["base"])
        if not g.host_loss:
            return struct(gate = g, void = _void(g))
        st["losses"].append(g.host_loss)
        st["host_losses"] = g.host_losses
        if len(st["losses"]) == LOST_JOBS_OF_A_CALL:
            first, second = st["losses"][0], st["losses"][1]
            return struct(gate = g, void = "the host lost two gate jobs of this batch: the gate job %s was lost on %s and the proof is %s; the gate job %s was lost on %s and the proof is %s" % (
                first["job"],
                first["host"],
                first["proof"],
                second["job"],
                second["host"],
                second["proof"],
            ))

def _verdict(st, u):
    """Gates a union, unless a union with the same tree was red before. The gate must report the tree of the union."""
    if u.tree in st["red"]:
        return struct(passed = False, gate = None)
    st["gates"] += 1
    ran = _gate_once(st, u)
    if ran.void != "":
        fail("the gate gave no result: %s. No member is blamed. Fix the cause and run the batch again" % ran.void.strip().rstrip("."))
    g = ran.gate
    if g.tree != u.tree:
        fail("batch %s: the gate ran on tree %s, and the union is tree %s" % (st["name"], g.tree, u.tree))
    passed = g.passed and len(g.steps) > 0 and [s for s in g.steps if s["status"] != "pass"] == []
    if not passed:
        st["red"][u.tree] = True
    return struct(passed = passed, gate = g)

def _land_union(st, u, g):
    """Merges the members of a green union in one merge call. A refused member is rejected and the members after it come back to be gated again.
    A base that moved, or a member that the forge cannot settle, stops the batch."""
    st["merges"] += 1
    out = merge(
        name = "%s.merge#%d" % (st["name"], st["merges"]),
        members = u.included,
        base = st["base"],
        repo = st["repo"],
        gate = g,
        hold = st["hold"],
        own = st["own"],
        forbid_text = st["forbid_text"],
    )
    st["tip"] = out.tip
    for item in out.merged:
        st["merged"].append(item)
        st["decided"][item] = True
    for r in out.rejected:
        _reject(st, r.item, r.reason)
    if out.moved:
        _stop(st, REASON_MOVED)
    elif len(out.deferred) > 0:
        _stop(st, out.deferred[0].reason)
    return [[m for m in u.included if m.item in out.rest]]

def _step(st, members):
    """Gates one set and lands it, or splits it in halves when the gate is red. Returns the sets to land next, in order."""
    u = _union(st, members)
    _sort(st, u)
    if len(u.included) == 0:
        return []
    seen = _verdict(st, u)
    if seen.passed:
        return _land_union(st, u, seen.gate)
    if len(u.included) == 1:
        _reject(st, u.included[0].item, REASON_RED_ON_TOP if len(st["merged"]) > 0 else REASON_RED)
        return []
    half = (len(u.included) + 1) // 2
    return [u.included[:half], u.included[half:]]

def _land(st, members):
    """Lands the members with a work stack in place of the recursion of a split. A split of n members takes at most 2n - 1 sets, and each refusal adds one."""
    stack = [members]
    for _ in range(4 * len(members) + 4):
        if len(stack) == 0 or st["stopped"]:
            return
        todo = stack.pop()
        if len(todo) > 0:
            stack.extend(reversed(_step(st, todo)))

def issue_audit(landed, integration = 0, name = ""):
    """Reads the issue of each landed item and, when integration is not 0, the issues that the integration pull request closes.
    An issue of a landed item that is still open is a finding, and so is an issue that the integration pull request closes and no landed item names.
    The id of an item of the GitHub tracker is the number of its issue. Returns struct(findings)."""
    findings, claimed = [], {}
    for item in landed:
        if item == "" or item.lstrip("0123456789") != "" or item.startswith("0"):
            findings.append("the id \"%s\" of a landed item is not a plain issue number, so its issue was not read" % item)
            continue
        number = int(item)
        claimed[number] = True
        state = issue_state(number = number, name = "%s.issue#%s" % (name, item)).state
        if state != "CLOSED":
            findings.append("issue #%d of item %s is not closed after the batch landed (the forge says \"%s\")" % (number, item, state))
    if integration > 0:
        refs = pr_closing(pr = integration, name = name + ".closing").issues
        for number in sorted([int(ref[1:]) for ref in refs if ref.startswith("#")]):
            if number not in claimed:
                findings.append("pull request #%d closes issue #%d and no landed item names it" % (integration, number))
    return struct(findings = findings)

def batch_merge(name, prs, base, repo, hold = "", own = {}, forbid_text = [], gate_run = [], integration = 0):
    """Lands the pull requests of prs with one gate run for the whole set. The members fold in item order onto the base tip, the union is gated, and a red union splits in halves.
    A member that conflicts only with an earlier member is retried once on the new tip. gate_run replaces the project gate with a program.
    Returns struct(merged, rejected, deferred, tip, tree, gates, findings, note, unproven, moved, host_loss, host_losses)."""
    members = _members(name, prs)
    st = {
        "name": name,
        "base": base,
        "repo": repo,
        "hold": hold,
        "own": own,
        "forbid_text": forbid_text,
        "gate_run": gate_run,
        "merged": [],
        "rejected": [],
        "decided": {},
        "with": {},
        "held": [],
        "red": {},
        "retry": False,
        "stopped": False,
        "stop_reason": "",
        "gates": 0,
        "merges": 0,
        "losses": [],
        "host_losses": 0,
    }
    present = _present(st, members)
    start = base_tip(base = base, name = name + ".tip")
    st["tip"] = struct(commit = start.commit, tree = start.tree)
    _land(st, present)
    if not st["stopped"] and len(st["held"]) > 0:
        st["retry"] = True
        _land(st, sorted(st["held"], key = lambda m: m.item))

    deferred = [struct(item = m.item, reason = st["stop_reason"]) for m in members if m.item not in st["decided"]]
    landed = [struct(item = m.item, head = m.head) for m in prs if m.item in st["merged"]]
    unproven = []
    findings, note = [], NO_LANDING_NOTE
    if len(landed) > 0:
        unproven = landing_proof(base = base, start = start, end = st["tip"], landed = landed, name = name + ".prove").unproven
        findings, note = issue_audit(landed = st["merged"], integration = integration, name = name).findings, ""

    return struct(
        merged = st["merged"],
        rejected = st["rejected"],
        deferred = deferred,
        tip = st["tip"].commit,
        tree = st["tip"].tree,
        gates = st["gates"],
        findings = findings,
        note = note,
        unproven = unproven,
        moved = [],
        host_loss = st["losses"],
        host_losses = st["host_losses"],
    )
