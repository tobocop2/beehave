HERE = env("APPROVALS_DIR")
AUDIT_TIMEOUT = "2m"

# The item body is the request id, the approved hash (or - for a request that was not approved) and the head from the outcome
# comment of the request: "REQ-1 <hash> <seq>:<row_hash>". The run checks the ledger chain, every signature, the close at that
# hash and the head, and posts the trail. A body without the head is refused, because a cut tail would pass (see scripts/audit.sh).
def main(item):
    words = item.body.split()
    if len(words) != 3:
        fail("REFUSE: the body must be: <request> <hash or -> <head>. A body without the head cannot see a cut tail.")
    audit = command(name = "audit", run = ["sh", HERE + "/scripts/audit.sh"] + words, timeout = AUDIT_TIMEOUT)
    if audit.rc != 0:
        fail("REFUSE: the ledger does not verify:\n%s" % audit.stdout)
    item_comment(name = "trail", id = item.id, key = "trail", text = audit.stdout)
    item_status(name = "close", id = item.id, status = "closed")
