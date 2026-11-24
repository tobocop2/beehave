# The claim stops of the remediation method: who else holds the item, whether a product decision holds it, and whether its pull request is a draft.
# The preflight builtin keeps the checks that protect a merge (state, branch, head and base). A caller runs it first, then claim_stop.

ASSIGNED = ("ASSIGNED", 11)
CLAIMED = ("CLAIMED", 10)
HELD = ("HELD", 9)
DRAFT = ("DRAFT", 8)

def _clear():
    return struct(ok = True, stop = "UNCHANGED", code = 0, reason = "")

def _is_own(login, logins):
    """An empty login is never own. Forge logins ignore case."""
    return login != "" and login.lower() in logins

def _foreign(assignees, logins):
    """The first assignee that is not own, or an empty text."""
    for login in assignees:
        if login != "" and not _is_own(login, logins):
            return login
    return ""

def _stop(name, id, kind, lines):
    reason = "; ".join(lines)
    item_stop(name = name, step = "claim_stop:" + name, code = kind[1], reason = reason, id = id)
    return struct(ok = False, stop = kind[0], code = kind[1], reason = reason)

def _assigned(name, id, pull, logins):
    """The stop for an item or a linked issue that a login outside own holds, or None. The pull request comes first, then each link in the order of the body."""
    holder = _foreign(pull.assignees, logins)
    where = "#%d" % pull.number
    if holder == "":
        for n, link in enumerate(pull.links):
            text = link["repo"] + "#" + str(link["number"])
            holder = _foreign(issue_assignees(name = "%s-%d" % (name, n + 1), repo = link["repo"], number = link["number"]).assignees, logins)
            if holder != "":
                where = "the linked issue " + text
                break
    if holder == "":
        return None
    knows = "yes" if _is_own(holder, [who.lower() for who in [pull.author] + pull.reviewers if who != ""]) else "no"
    return _stop(name, id, ASSIGNED, [
        "STOP: %s is assigned to '%s', who is not on the list of own logins. The item is claimed. knows=%s" % (where, holder, knows),
        "  Recovery: leave the item to the holder and move to the next item. To take it, ask the holder first.",
    ])

def claim_stop(name, id = "", hold = "", own = {}, own_draft = 0):
    """Reads the pull request of the item and returns the first claim stop, or a clear result. The result has the fields of preflight: ok, stop, code and reason.

    id is the item, and an empty id is the item of the run. A pull request that is not open, and an item with no pull request, give a clear result: the preflight call reports those.
    The order is assigned (the pull request, then each linked issue in the order of the body), claimed, held, draft. The codes are 11, 10, 9 and 8.
    own is {"logins": [...]}. A pull request of another author, and an assignee of the pull request or of a linked issue who is not in the list, claim the item. An empty list checks neither.
    hold is the label that holds an item, and an empty hold holds nothing. own_draft is the number of the draft that the run opened itself, and that draft is no stop. Zero means that every draft stops.
    name makes the step names of the call unique. A stop of the item of the run shows as the step claim_stop:<name> in the result of the run.
    A read that fails, or a read with no draft flag, label list or assignee list, ends the run with an error and never passes.
    """
    if type(own) != "dict":
        fail("REFUSE: claim_stop got own %r. Pass a dict such as {\"logins\": [\"owner\"]}." % (own,))
    if type(own_draft) != "int" or own_draft < 0:
        fail("REFUSE: claim_stop got own_draft %r. Pass the number of the draft that the run opened, or 0 for none." % (own_draft,))

    pull = pr_read(name = name, id = id)
    if not pull.found or pull.state != "OPEN":
        return _clear()

    logins = [login.lower() for login in own.get("logins", []) if login != ""]
    if logins:
        stop = _assigned(name, id, pull, logins)
        if stop:
            return stop
        if not _is_own(pull.author, logins):
            return _stop(name, id, CLAIMED, [
                "STOP: #%d is open and its author is '%s', who is not on the list of own logins. The item is claimed." % (pull.number, pull.author or "null"),
                "  Recovery: leave the item to its author and move to the next item.",
            ])
    if hold != "" and hold in pull.labels:
        return _stop(name, id, HELD, [
            "STOP: #%d now carries the '%s' label. A product decision is open; nothing merges." % (pull.number, hold),
            "  Recovery: write the judgement on the issue and move to the next item.",
        ])
    if pull.draft and pull.number != own_draft:
        return _stop(name, id, DRAFT, [
            "STOP: #%d is a DRAFT. It is not mergeable and may be mid-rework by its author." % pull.number,
            "  Recovery: review it if you were sent to review it, but do not merge and do not push",
            "  without saying so on the pull request first.",
        ])
    return _clear()
