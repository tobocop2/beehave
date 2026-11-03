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
**The engine has claimed this item.** Claim nothing.
**Name the tempting wrong fix and why it is wrong.** Prefer enabling an existing judgement over writing a rival one.
**Read the files that the item names before you start, and no others.**

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
**A mutation run is its own job after the gate**, never a gate step. Start it when the gate steps are done.
**Capture the exit code before any pipe**: `cmd > out 2>&1; rc=$?; grep ... out`. After a pipe, `$?` is the exit code of the last stage.
**A `while read` loop gives its stdin to every command inside it.** Read the list on fd 3 (`while read -r x <&3; do ...; done 3< list`) or put `</dev/null` on the inner command, and assert the loop count against the input count.
**A regeneration is evidence for its own tree only.** Compare the output with a fresh tree of the same commit. A regeneration that wrote nothing looks the same as one that wrote identical bytes.
**A gate that covers only the diff becomes a full run in two cases**: the diff touches a shared foundation (the workflow files, a lockfile, the schema, a widely used helper), or a red has no explanation. Name the trigger in the report. An unfinished full run never blocks a verdict: report it as a named gap.
**Before each push, search the diff for `TODO` and `FIXME`.** Each one that the change adds names its author, as `TODO(name)`.

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

## Shell commands, background jobs and the build host

**Never pass a script string to a shell.** No `sh -c "..."`, `bash -c "..."` or `zsh -c "..."`, even without
`rm`. Write the commands to a script file in the scratch directory and run the
file, detached if it is long: `nohup sh <file> > <log> 2>&1 &`, with the done marker as the file's last line.
A loop that needs POSIX word splitting goes in a `#!/bin/sh` file, because the interactive shell is zsh.
**Put no delete in any command you run.** Write scripts with the Write tool, never `sed -i.bak`.
**Never end your turn waiting on a job you started yourself** (`run_in_background`, `Monitor`, a trailing
`&`). An idle lane is not woken when its own job finishes.
**Before you end, stop each background command you started, by task id**: a live one wakes you later.
Wait loops have a timeout. Test commands read `</dev/null`. Report no background commands left.
**Before EVERY build, check `df` on the build host.** If the free space is under the floor that the rules of the
project name, wait. A build that starts on a nearly full disk fails partway with void results.
**Disk is per lane.** Share a compile cache between lanes, never a target directory.
**Job scripts, bundles and inputs live in a jobs folder of the host** (`jobs/<lane>/` under the work root that the brief names), never in `/tmp`.
**Name your target directory in the report as soon as you have no further build**, so that the orchestrator deletes it at once, by its literal path.
**Sweep dangling docker volumes as a backstop.** Report `docker volume ls -qf dangling=true | wc -l`. The sweep prunes volumes that no running container uses, and never images.

## Compiled code

**A BINARY FIXTURE MUST BE ASSERTED INVALID UTF-8 *AND* ASSERTED TO TAKE THE REFUSAL IT WAS WRITTEN FOR.**
Assert the bytes are what you think (`from_utf8` errors) and that the path you are testing actually rejects
them; either assertion alone lets a fixture drift into proving nothing.
**A restore is not green until something forces the rebuild.** `touch` after every restore,
unconditionally, whatever the hash says, and re-run clean: a plain `cp` can land inside the same mtime
second and cargo reuses the mutated test binary.
Name targets `-p <crate> --test <name>` and **assert a NONZERO count**; a run that selects nothing exits 0.
**Name the FEATURES beside the target, every time**: a module behind a conditional-compilation flag is invisible
to a target that does not name the feature. Read the package's feature list and default first, and if the count
is zero suspect the feature list before the code. The rules of the project list the packages that need one.
**Read the package name out of its own manifest, never from the repository or the directory name.** A wrong
package name answers rc=101 with no tests; piped through `tail`, the rc you read is `tail`'s zero.

## Merging two branches

**A union identity is not complete until you count what came from NEITHER side.** `result - development ==
branch - base` and `result - branch == development - base` both hold for a rewrite that balances. Add
the third: lines present in the result and absent from BOTH parents must be zero, with a control that
counts lines absent from one parent alone.
**THE LINE CHECKS ARE NOT THE GATE. The union gate is a BUILD at the merge tip.** Every line can come from one
parent while the tree does not compile. Be most suspicious when a branch ADDS an implementor of a shared
trait: the base cannot have updated a file it never had. When the branch CHANGES a trait's method
signatures, enumerate that trait's implementors AT THE TIP and confirm every one is in the branch's diff.
**The third check compares FILE CONTENT, never two added-line sets.** Intersecting the `+` lines of the two
diffs matches `+}` and `+#[test]` by coincidence of text. Per changed file, the result's lines must be a
subset of the union of the two parents' versions of that file; the control is the same count against one
parent alone, which must be nonzero.
Restore a mutated file from a snapshot and **assert the blob**; `git checkout -- <file>` resets to HEAD and
silently discards other uncommitted edits.

## Brief rules

**The closing keyword depends on the base.** On a round branch, the pull request body says `Addresses #N`. When the base is the default branch, it says `Closes #N`.
**Read an issue or pull request thread from the thread file that the brief names.** Dump a thread into a file and name the file; never paste a thread into a prompt.
**A reproduction is self-contained.** A stranger must run it from the issue alone, with no round path, no lane name and no scratch file.

## Merge rules

**Open a draft pull request at the first checkpoint.** Push the branch and open the pull request as a draft when the first commit is green. Push every later commit to it at once; a commit that stays local is lost with the lane.
**A pull request that depends on another open one goes into a native stack.** Never express the dependency with a hand-set base alone. Link the branches from the bottom to the top with `gh stack link`, then read the stack id of each pull request and check that all of them show the same non-empty id. Read each head before and after the link: a head that moved is a push you did not intend.
**Never use the merge queue of GitHub.** The orchestrator merges a pinned head with `gh pr merge`. Never run `gh pr merge --auto` and never add a pull request to a queue.
**Known base reds are in one file.** The run names the file. Read it, and re-derive each red at the tip you branch from. Never copy the list into a report or a brief.

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

**When you stop before the item is done, write the handoff to the handoff file that the run names, under 300 words,
yourself.** It carries: the plan file; the commit; what is done and proven; what is owed, in order; the one
thing not to redo; and these lines:

    Regressions of this pull request so far: <the number of regressions open at your head: defects that a lane of this pull request introduced and that no later lane has fixed; 0 when there are none>
    Tokens at handoff: <your context size>

## Lane layout

**A fix lane is not a proof lane and not a review lane.** The gate is the proof and a fresh review lane judges the change. You write the change; you do not review it as the verdict, and you do not approve it.
**A rise in the regression count blocks the lane.** End your report with the line `Regressions of this pull request so far: <n>`. If n is higher than in the handoff you started from, write REGRESSION RISE on the verdict line and do not push. The run holds the item.
**Patch rounds are bounded for each pull request.** After the last round, no fix lane starts for this pull request. A person rules on each open finding, and then one settle lane applies the ruling. The settle lane is the only edit after the last round: it runs once, no review follows it, and nothing merges. Do not start a second fix lane for a pull request yourself.

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

The plan:

{{ .plan }}
