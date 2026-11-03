# The sign-off rules. One row per kind of document.
# open.sh copies the row of the request into the ledger, so the record of a request shows the rule that decided it,
# even after this file changes. Each run also pins the SHA-256 of this file.
#
# roles:        who may sign for a role (members), how many different people it needs (need), and whether it is
#               required. A required role is a veto: one reject from a member of it ends the request.
# min_roles:    how many roles must be satisfied. Required roles always count.
# window:       the seconds from the opening of the request to its deadline.
# extend:       the seconds that one extension adds. A request can be extended once. A person has the same time to answer the extension question.
# remind_at:    percent of the window at which the missing roles get a reminder.
# escalate_to:  the people who get the question when the deadline passes.

# APPROVALS_WINDOW_SECONDS sets every window and every extension to that many seconds. Only a trial run sets it.
# No window is shorter than 60 seconds, so that a person has time to answer.
MIN_WINDOW_SECONDS = 60
WINDOW_OVERRIDE = int(env("APPROVALS_WINDOW_SECONDS", "0"))

def hours(count):
    if WINDOW_OVERRIDE != 0 and WINDOW_OVERRIDE < MIN_WINDOW_SECONDS:
        fail("APPROVALS_WINDOW_SECONDS must be at least %d" % MIN_WINDOW_SECONDS)
    return WINDOW_OVERRIDE or count * 3600

KINDS = {
    "release": {
        "window": hours(72),
        "extend": hours(24),
        "remind_at": [50, 90],
        "min_roles": 2,
        "escalate_to": ["rita"],
        "roles": {
            "security": {"members": ["sam", "sue"], "need": 1, "required": True},
            "product": {"members": ["pat", "paula"], "need": 1, "required": True},
            "legal": {"members": ["lee", "lou"], "need": 1, "required": False},
        },
    },
    "contract": {
        "window": hours(168),
        "extend": hours(48),
        "remind_at": [50, 90],
        "min_roles": 3,
        "escalate_to": ["rita"],
        "roles": {
            "legal": {"members": ["lee", "lou", "luz"], "need": 2, "required": True},
            "security": {"members": ["sam", "sue"], "need": 1, "required": True},
            "product": {"members": ["pat", "paula"], "need": 1, "required": True},
        },
    },
}

# One round is one wait of at most ROUND_SECONDS for a change in the ledger. A change ends the round at once.
# The loop of a run is finite, so ROUNDS must cover the longest window plus its extension, and every change
# uses one round. 400 rounds cover a 168 hour window plus 48 hours, with room for about 180 changes.
ROUND_SECONDS = 3600
ROUNDS = 400
