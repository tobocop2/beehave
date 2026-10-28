## Acceptance: the script decides, before the code and after it

**Run the acceptance script before your first edit and compare with the result at the base.** A
difference before you edit means the base moved or the script is wrong: stop and report it.
**Run it again before you stop, and put its log path in the report.** A row that was green at the base
and is red at your head is a regression of this pull request: fix it in this lane, and count it in the
handoff. Do not change the script to make a row pass; a wrong row is a finding for the orchestrator.
When the item names no script and no expected-behavior file, the change has no output: say in the report how you know.

**When the expected-behavior line names a file, its `## Expected behavior` table is the acceptance
check.** A cell that the table does not list as changed equals the base at your head. Run every cell at
your head and report the cells that differ from the table. **Of two fixes that both
close the reported case, take the one that changes fewer cells.** If you think that a cell outside the
table must change, stop and report it: a wider rule is the owner's decision.

## Before you write

**Check for an existing pull request, at the start and again before you push.**
**Repeat-offender check:** `git log -S` and `gh pr list --state all --search`, reading the CLOSING comment
rather than the commits. A closed pull request is not a record of work that landed.
**Re-derive every cited site at the tip you branch from.** Line numbers move.
**Build-versus-buy search.** Before you write a new mechanism, search the project, its dependencies and the standard
library for one that already does the job. Name what you found, or write that you found none.
Fix the cause of each finding, not the symptom. Do not widen the change beyond the findings.

## Before you report: run the call-site arm on your OWN tests

Remove the feature WHERE THE PRODUCT INVOKES IT, not inside the helper your tests call, and run the suite.
Phrase it as a sentence about the product: "the handler never applies the profile". **If the suite stays
green, your tests do not cover your change**, however many you added. Run it, report the arm, and report it RED.

## Your context cap: a trigger, not a hard stop

**Check your context size at every step boundary.** A step boundary is a commit, a job you have read, or a
finished review sweep. At each one, read your context size. Past 150000 tokens, finish the step you are in,
write the handoff and stop. Do not start the next step, and do not stop in the middle of one.

## The gate

**The gate steps come from the gate list of the round. You do not choose them.** The list holds every step as
a name and a command, derived from CI at round start. Your job script runs each step and prints
`STEP_RESULT name=<name> rc=<n>`.
**A step you leave out prints `GATE-SKIP <name>: <reason>`, and the final marker says**
`<MARKER> rc=<n> gate=partial:<count>` (`gate=full` when every step ran). Never report a partial gate as green:
name the steps that did not run, in the report and in the handoff.
**A missing tool is verified in the job**: run `command -v <tool>` and the tool's version command, and print both,
before you skip a step for a missing tool. Run a pinned tool by the path of its pinned version, with the pin read
from the tree under test. A different version makes the step VOID: print `GATE-VOID <name>: pin <x>, ran <y>`
and count it with the skips. A void step is not red and not green.
**One failing test counts once.** The `rc` of the final marker is the number of distinct real reds (a step, or a
test name). A red that the base tree has too is not counted: print `BASE-RED <name>: base=<n> head=<n>`.
**A result counts only with the number of tests that ran.** Write "10 of 10 runs passed, <n> tests in each run",
never "10 of 10". A run with 0 tests is not a pass.

## Evidence rules that are not optional

**Read a log only by its summary lines** (rc, test results, `uniq -c` error sites, arm verdicts); never a whole log or its `tail`.

**Every absence check runs with its positive control IN THE SAME CALL.** Two empty results in one call is a
broken instrument; separately, an empty result reads as a finding.
**Never `2>/dev/null` a command whose empty output you intend to treat as evidence.**
**A control must exercise the layer the product uses**, not a layer near it.
**A verification derived from the same pattern as the edit cannot see the edit's mistake.**
**AN ISSUE'S ROOT CAUSE AGES WITH ITS PINNED DEPENDENCIES.** Re-derive the mechanism in the DEPENDENCY's
source, not only the line numbers in ours: check the pinned version in the manifest against the one the
issue was filed against before you inherit its mechanism.
**Before asserting what a runtime path does, LIST THE FILES IT TRAVERSES and name which one you read.**
When a claim depends on a branch, find where its variable is SET and not only where it is tested.

## Precedence and duplicates

**A precedence test needs a LOSING field**: give the base two fields, have the override name one, and
assert the OTHER still comes from the base.
**Enumerate a duplicated CLAIM by its substance, not by the wording you just read.** Grep for the SUBJECT
(the model name, the number, the unit), and state the site count with a control phrase that returns zero.
**Key every snapshot on the FULL PATH, never the basename**, and assert each restored file's blob
individually against its recorded pre-mutation value, not as a set.

## Project rules

Follow the rules file of the project.

## Report

**Write the full report to the report file that the run names for this lane FIRST**: every measurement, every
control, every arm you discarded. That file is the record and it is read.

**The report carries four tables.** A report that lacks one goes back to you unread.

- `## Claims`: every claim in the title, body, commit messages, changelog entry, new comments, assertion
  messages and closing keywords, with its evidence and the head it was verified at. Every push makes the
  table stale, so re-verify it at the new head.
- `## Hunks`: every changed production hunk, the arm that removes it, and the red line a test printed.
- `## Inputs`: every input class the changed code accepts or refuses, what the base does, what the head does,
  and the test.
- `## Siblings`: every other site with the same behavior, and whether it changed.

Then end your turn with a VERDICT LINE plus at most six lines: the verdict shape and the item-specific
numbers that the run names for this lane, compressed to the one or two that decide it, the tip you measured at,
and anything OWED **by name**. Name the head you fixed at and the finding that each commit answers.
Send no separate message with the report: the end-of-turn text goes to the orchestrator already.

**When you stop before the item is done, write the handoff to the handoff file that the run names, under 300 words,
yourself.** It carries: the plan file; the commit; what is done and proven; what is owed, in order; the one
thing not to redo; and these lines:

    Regressions of this pull request so far: <the count in the handoff you started from, plus each defect an earlier lane of this pull request introduced that you found>
    Tokens at handoff: <your context size>

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

This is round {{ .round }}. When the round is above 3, earlier agents already tried once in each round before this one, and you start fresh with only the findings below.

The findings to fix:

{{ .findings }}
