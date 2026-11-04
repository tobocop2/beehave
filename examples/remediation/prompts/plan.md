## This is a DESIGN lane: the rule and the acceptance script, no product code

Write the rule of the item as an acceptance script, run it at the base, and commit the script with its
recorded result. Change no product code. The script covers:

- **every code path the item has** (each converter, each backend, the fast path and the slow path);
- **every option mode**: each mode equals the base unless the item lists it as changed;
- **every context that must NOT change**: each row again inside each such context, head equals base;
- **the CI's own checks with the CI's base**.

Use a generator for the inputs, and an oracle that is not the code under test. Report the script path,
the result path, the number of inputs and the rows that are red at the base.

**When several outputs, formats, routes or settings render the changed value by different rules, write
the expected-behavior table first.** One row for each input, output and setting: the value at the
base, and beside it the value expected at the head. Take the expected column from the issue and from
what the base does. A cell that the issue does not name keeps its base value. Put the table under
`## Expected behavior` in the rulings file of the item, and make the script print one row for each cell.
Report each cell where the issue leaves the answer open, and do not choose: that is an owner question.
Write the paths of the harness and of the build of the base into the same file: every later lane of
the item uses them.

## A proof-and-papers lane writes no source path

**When the item asks for proof and papers only, run the proof, and write the report and the pull request body.**
Change no source path. When the proof finds a defect, do not fix it here: report it with the red row, and the
orchestrator dispatches a fix lane.

## Before you write

**Check for an existing pull request, at the start and again before you push.**
**Repeat-offender check:** `git log -S` and `gh pr list --state all --search`, reading the CLOSING comment
rather than the commits. A closed pull request is not a record of work that landed.
**Re-derive every cited site at the tip you branch from.** Line numbers move.
**Build-versus-buy search.** Before you write a new mechanism, search the project, its dependencies and the standard
library for one that already does the job. Name what you found, or write that you found none.
**Name the tempting wrong fix and why it is wrong.** Prefer enabling an existing judgement over writing a rival one.

## Your context cap: a trigger, not a hard stop

**Check your context size at every step boundary.** A step boundary is a commit, a job you have read, or a
finished review sweep. At each one, read your context size. Past 150000 tokens, finish the step you are in,
write the handoff and stop. Do not start the next step, and do not stop in the middle of one.

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

## Brief rules

**Read an issue or pull request thread from the thread file that the brief names.** Dump a thread into a file and name the file; never paste a thread into a prompt.
**A reproduction is self-contained.** A stranger must run it from the issue alone, with no round path, no lane name and no scratch file.

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
and anything OWED **by name**.
Send no separate message with the report: the end-of-turn text goes to the orchestrator already.

**Write the output `regressions` as a whole number.** Count only the regressions that are open at the head you plan from. It is the base of the regression count: the run compares the count of the last review with this one, and a rise holds the item. A count that is not a whole number holds the item too.
Write it to {{ .output_file }} as JSON, as a text, for example `{"regressions":"0"}`.

**When you stop before the item is done, write the handoff to the handoff file that the run names, under 300 words,
yourself.** It carries: the plan file; the commit; what is done and proven; what is owed, in order; the one
thing not to redo; and these lines:

    Regressions of this pull request so far: <the number of regressions open at your head: defects that a lane of this pull request introduced and that no later lane has fixed; 0 when there are none>
    Tokens at handoff: <your context size>

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}
