# Approval desk

A document needs sign-off from several roles. One item is one request. The run follows it to approval, rejection or expiry.

## How it works

- **Signed sign-offs.** A person signs a message with `ssh-keygen -Y sign` (`scripts/sign.sh`) and sends the two files to the inbox of the desk. `scripts/intake.sh` verifies the signature against `allowed_signers` (namespace `beehave-signoff`). The desk holds no private key. A message with a bad or unknown signature goes to `refused/`, and a taken message cannot be taken twice.
- **One version.** `scripts/submit.sh` stores a document in a git repository. A sign-off names the blob hash of the version it covers. A new version makes earlier sign-offs stale: the tally counts only the current hash, and the ledger keeps the old rows.
- **The quorum is data.** `policy.star` gives each role its members, the number of people it needs and a `required` flag. A reject from a member of a required role is a veto. `min_roles` is the number of roles that must be satisfied. `scripts/tally.jq` evaluates the rule over the ledger.
- **Delegation.** A member signs a delegation for a role, to a person, until a time. The tally counts the delegate while the delegation is in force.
- **Deadline, reminders, one extension.** The deadline is a ledger fact. The run sends a reminder to the missing roles at each `remind_at` percent of the window. A round that wakes late, even after the deadline, sends each reminder it owes, in order, before the escalation. No reminder is skipped. At the deadline it tells `escalate_to` and asks a person to extend or cancel. The question has the same window as the extension. No window is shorter than 60 seconds.
- **Audit.** The ledger is a SQLite table with triggers that refuse update and delete. Each row holds the hash of the row before it. `audit.star` and `scripts/audit.sh` check the chain and every signature.
- **Lock.** Every append takes `flock` on the lock file of the desk. A script cannot append without it.
- **Outcome.** An approved request closes its item. A rejected or expired request holds the item. A notice that the notifier refuses also holds the item.

## What the audit detects

- A row that was changed, a row removed from the middle and rows in another order: the chain breaks.
- A signed message that was changed or whose key left `allowed_signers`: the signature check fails.
- A cut tail: only with the head. At the close, `finalize.sh` prints the head (`seq:row_hash`) and the run writes it in the outcome comment of the item. `audit.sh REQ HASH HEAD` fails if that row is gone. The head is required: a call or an `audit.star` body without it is refused. Use `-` for the hash when the request was rejected or expired.
- Not detected: a person who can write the ledger file can rebuild the chain from the first change on. The head from the close limits this only if the verifier got it from the item and not from the desk.

## The extension question and the wait

The extension question is a wait with `to` (the people in `escalate_to`) and `choices` (`extend`, `cancel`). A person answers with `beehave answer <item> --as rita extend`. The wait refuses a stranger. The ledger row of the extension names the person. The engine does not prove the name: anyone who can run `beehave answer` can give a listed name. The sign-offs stay signed files, because a wait proves no name.

The sign-offs do not use a wait with `quorum`. A quorum wait counts names that the engine does not prove, so it would add a weaker second path next to the signed inbox. The rounds that poll the inbox stay.

## Files

`policy.star` (rules), `workflow.star` (the run), `audit.star` (the audit run), `prompts/brief.md` (the note after a reset), `scripts/` (every call to the outside world), `tests/run.sh` (the script tests, in a temp folder that the test deletes). `NOTIFY_API` is the one system that the example does not ship.

## Run it

1. Copy the folder to a git repository. Make `~/.approval` with `config` (`NOTIFY_API=...`), `credential.header` and `allowed_signers`.
2. Copy `config/projects/approvals.star` to the beehave config folder. Set `APPROVALS_DIR` to the absolute path of this folder.
3. `sh scripts/submit.sh msa msa.md ann`, then add an item with the body `{"doc": "msa", "kind": "release", "author": "ann"}` and run `beehave run <item> approvals --workflow workflow.star`.
4. Each signer runs `sh scripts/sign.sh <key> . s1 type=signoff req=<item> doc=msa hash=<hash> role=security by=sam decision=approve reason=ok` and sends the files to `~/.approval/inbox`.
5. A release pipeline calls `sh scripts/audit.sh <item> <hash> <head>`. The head is required.

## Limits of the tests

- The scripts need `flock`, which macOS does not have. On a host without it, the e2e scripts skip, so only a Linux gate proves them.
- A delegation row has the request `-`, so a delegation by a member covers that role for every request until its `until`. It has unit tests and no e2e script.
- `approvals-restart.txt` sleeps 65 seconds on purpose: the deadline must pass while no process runs.
- A regression in the veto or the deadline shows as a hang until the harness timeout, not as a failed assertion.
