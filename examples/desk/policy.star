TIER = struct(OFF = "off", SHADOW = "shadow", ASSIST = "assist", CONTAIN = "contain", TRANSACT = "transact")
TIERS = [TIER.OFF, TIER.SHADOW, TIER.ASSIST, TIER.CONTAIN, TIER.TRANSACT]

FIELD = struct(INT = "int", STRING = "string")
STEP = struct(CREDIT = "credit", UNCREDIT = "uncredit", REBOOK = "rebook", UNREBOOK = "unrebook", REPLY = "reply")
NO_UNDO = ""
ITEM_STATUS = struct(CLOSED = "closed", HOLD = "hold")

REASON = struct(
    NOT_ELIGIBLE = "not-eligible",
    NEEDS_JUDGMENT = "needs-judgment",
    CLERK_DECLINED = "clerk-declined",
    SYSTEM_FAILURE = "system-failure",
    BAD_INTENT = "bad-intent",
)
TEAM = struct(CUSTOMER = "customer", SUPERVISOR = "supervisor", IT = "it", PROCEDURE_OWNER = "procedure-owner")

# A reason code decides who gets a case that the desk does not finish.
ROUTES = {
    REASON.NOT_ELIGIBLE: TEAM.CUSTOMER,
    REASON.NEEDS_JUDGMENT: TEAM.SUPERVISOR,
    REASON.CLERK_DECLINED: TEAM.SUPERVISOR,
    REASON.SYSTEM_FAILURE: TEAM.IT,
    REASON.BAD_INTENT: TEAM.PROCEDURE_OWNER,
}

# One row per intent. A tier change is a reviewed edit of this file, and each run pins its SHA-256.
# auto_limit is the amount up to which the contain tier approves alone. promote_at and demote_at are mean costs per case.
POLICY = {
    "refund": struct(
        model = "sonnet",
        tier = TIER.CONTAIN,
        auto_limit = 100,
        approvers = "billing",
        fields = {"booking_id": FIELD.STRING, "amount": FIELD.INT, "reason": FIELD.STRING},
        saga = [
            struct(write = STEP.CREDIT, undo = STEP.UNCREDIT),
            struct(write = STEP.REPLY, undo = NO_UNDO),
        ],
        promote_at = 0.5,
        demote_at = 2.0,
    ),
    "rebook": struct(
        model = "sonnet",
        tier = TIER.SHADOW,
        auto_limit = 0,
        approvers = "travel-desk",
        fields = {"booking_id": FIELD.STRING, "new_date": FIELD.STRING},
        saga = [
            struct(write = STEP.REBOOK, undo = STEP.UNREBOOK),
            struct(write = STEP.REPLY, undo = NO_UNDO),
        ],
        promote_at = 0.5,
        demote_at = 2.0,
    ),
}
