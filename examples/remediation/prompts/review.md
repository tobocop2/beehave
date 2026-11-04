## What every review here must do

One complete sweep with the review skill your rules name, every finding reported, **not iterated to zero**: a reviewer
cannot converge a loop it may not author commits for. You do not fix, commit, push or comment.
**Read the existing review first.** Read every earlier review and comment on the item before you read the diff. **Label each finding NEW or ALREADY RAISED**, and for an already-raised one name who raised it and where. Report an already-raised finding that is still true in one line: confirm it at this head. Give its arm when it is now fixed, when it is wrong, or when it has no reproduction and you have one.
**The run names one of three modes.** In take-over mode, you review a pull request of another author, and you fix nothing. In audit mode, you re-review work that is already merged, and each finding becomes a fix item. In queue mode, you review the change of a lane that did not write it, and the verdict gates the merge.
**The verdict `reject` is for every method.** Use `reject` when no fix round can repair the change: the approach is wrong, or the change does not belong. A `reject` ends the loop with no fix round, and the item is held. Write the reason in your reply text.
Every added test gets a mutation arm **shown red**, with blob hashes either side.
**Cite the reference the ruling names.** When the ruling of the item names a reference (a rule, a design note or the
behavior of an upstream tool), check the change against that reference and quote it in the finding. Do not
review against your own idea of correct.
**Write the attack list for this diff before you read it closely.** List the ways this change can be wrong, each one
as a sentence about the product, and run an arm for each. A review without an attack list for its own diff is not complete.
**Frame a security review of our own validators as verification.** When the change touches a check that our code
runs on input (a validator, a filter or a guard), state the review as a verification of that check against inputs
that must pass and inputs that must fail. Show each failing input rejected and each passing input accepted.
**Check the state the change stores.** Name each place where the change keeps state (a file, a ledger row, a cache
or a field). For each one, say who writes it, who reads it and what a reader sees when the state is stale or missing.
**Check for dead code.** Run the dead-code check of the project at the head. List each function, field or branch that
the change adds and that no call site reaches.
**Build-versus-buy search.** When the change builds a mechanism, search the project, its dependencies and the
standard library for one that already does the job. Name what you found, or write that you found none.
**A SOURCE-CONTRACT TEST IS NOT PROVEN UNTIL A WRONG IMPLEMENTATION FAILS IT.** A test that reads a file and
matches substrings asserts that two tokens appear, not that they are on an executing path. Write the
adversary before you accept the test (comment the guard out; invert one condition), and require
STRUCTURAL containment rather than co-occurrence: the error return inside the block the predicate names.
Strip comments first; the rules of the project name a test in this repository that already does.
**When a mutation arm AGREES with the thing it was built to challenge, suspect the arm before the code.**
A green result from an arm designed to go red is a claim about your instrument first: check that the arm
reaches a path the test takes. The cheapest tell is a result that contradicts the test's own name.
**Apply at least one arm AT THE CALL SITE, not inside the helper the tests call.** Phrase it as a sentence
about the product, such as "the handler never applies the profile", and run it. If the call-site arm stays
green, the suite does not cover the change, however many tests it has.
**The call site comes from the ISSUE'S SENTENCE, not from the biggest file in the diff.** Write the issue's
symptom as one clause about the product and arm the code that clause names. **Predicting your arm will be
green means you picked the wrong site, not that a gap exists.** A named gap UNDER a red call-site arm is
honest; a named gap INSTEAD of one is the failure this rule exists to catch.
**Reuse the implementer's proven build. Do not build the base and the head again, and do not rerun the
suite.** Compare the tree hash of the build (`git rev-parse <sha>^{tree}`) with the tree hash of the
head you review. If the two are equal, copy the build directory and run your own arms and your own
inputs on the copy. If they differ, say so in your verdict line and build only the head.
**Read the implementer's report as a CLAIM.** Re-run the arms that carry the argument.
Prefer your OWN instrument over running theirs: running theirs checks arithmetic, not the claim.
**For a change of observable output, compare EVERY changed cell with the BASE, in the first review
round.** Run each input, each output and each setting on a build of the base and on a build of the head,
and list every cell that differs. Do not sample, and do not compare with the previous head only. A cell
that differs and that neither the issue nor the `## Expected behavior` table of the item's rulings file
asks for is a finding, also when the new value looks better. Report three counts: the cells that
differ, the cells asked for, the cells not asked for. **Before you trust a pass, show that the harness
sees a known difference**: two builds that must differ in a named cell, and that cell quoted.
**The matrix harness and the build of the base belong to the item.** Their paths are in the item's
rulings file when an earlier lane built them: check the tree hash of the build, and write only your own
CASES on the harness. When the file names none, build them one time, keep them, and report the paths.
Run a probe whose inputs are independent as one process for each input, up to the idle cores of the
host, inside the slot of your job.
**Read every review body and every inline comment**, timestamps first, including `Suppressed comments (N)`.
Pin the head at the start, at the gate and at the end.
**List the gate steps that ran at the head you review, with the job that ran each, and name every step of the gate list of the round that did not run at that head.**

## Condition 4, every item, unasked

Worktree at the head, `detect-secrets` per file, **MERGE-BASE content against head content**, the difference
reported. Assert the file count is nonzero **and matches the pull request's own file list**. Confirm the
merge-base is the one against the CURRENT base branch. **Plant an AWS key pair as a positive control and show it SCORING**,
then remove it and show `git status --porcelain` empty. If the plant scores ZERO, your instrument is not
reading what you think: a script that diffs a three-dot range cannot see the index, so
a STAGED plant is invisible to it. Commit the plant, re-run, and only then believe the number. The
secret scan of the project is NOT this: it scans the tree, not the difference the change makes to it.

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

**Read an issue or pull request thread from the thread file that the brief names.** Dump a thread into a file and name the file; never paste a thread into a prompt.
**A reproduction is self-contained.** A stranger must run it from the issue alone, with no round path, no lane name and no scratch file.

## Merge rules

**Never use the merge queue of GitHub.** A merge is `gh pr merge` on a pinned head. A pull request that waits in a queue is not reviewed at its head.
**A finding that survives the review is filed before the merge.** Name each surviving finding in the report so that an issue is filed for it. A finding that only lives in the review thread is lost when the pull request merges.
**Known base reds are in one file.** The run names the file. A red that the file lists is not a finding of this change: re-derive it at the tip, and cite the line of the file.

## Project rules

Follow the rules file of the project.

## Facts of the project

The run read these facts from the project file at its start. A fact that is empty holds no fact: ask the owner, and do not guess it.

- Repository identity and fork topology: repo `{{ .facts.repo }}`, source `{{ .facts.source }}`, forge `{{ .facts.forge }}`.
- Build host: `{{ .facts.build_host }}`.
- Base branch: `{{ .facts.base_branch }}`.
- The gate is `{{ .facts.gate }}`. The gate list comes from these workflow files:{{ range .facts.gate_files }} `{{ . }}`{{ end }}
- Tracker: `{{ .facts.tracker }}`. Queue to epic:{{ range $queue, $epic := .facts.epics }} `{{ $queue }}` is `{{ $epic }}`;{{ end }}
- People and the idle threshold for a third-party pull request:{{ range $role, $who := .facts.people }} `{{ $role }}` is `{{ $who }}`;{{ end }}
- Review against these convention files:{{ range .facts.conventions }} `{{ . }}`{{ end }}

Brief rules of the project:
{{ range .facts.brief_rules }}
- {{ . }}
{{ end }}
Rules for a pull request that stacks on another:
{{ range .facts.stacked_rules }}
- {{ . }}
{{ end }}
Token rules, cross-repository steps and release steps:
{{ range .facts.token_rules }}
- {{ . }}
{{ end }}
Hard limits, commands never run and real user data:
{{ range .facts.limits }}
- {{ . }}
{{ end }}
Measured traps and ownership rules:
{{ range .facts.traps }}
- {{ . }}
{{ end }}

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

**The report ends with three buckets, in this order**: blocking, should-fix and non-blocking. Put each finding in one bucket. Write `none` for an empty bucket.

**When you stop before the review is done, write the handoff to the handoff file that the run names, under 300 words,
yourself.** It carries: the plan file; the commit; what is done and proven; what is owed, in order; the one
thing not to redo; and these lines:

    Regressions of this pull request so far: <the number of regressions open at your head: defects that a lane of this pull request introduced and that no later lane has fixed; 0 when there are none>
    Tokens at handoff: <your context size>

## Lane layout

**One reviewer judges one pull request.** You review the pull request of this item and no other. Do not review a second pull request in this lane, and do not fix, prove or merge: the fix lane writes the change and the gate is the proof.
**A rise in the regression count blocks the lane.** Write the output `regressions` as a whole number. Count only the regressions that are open at the head you review: a defect that a later lane fixed is not open. The run compares your count with the count of the plan, and a higher count holds the item. A count that is not a whole number holds the item too.

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

The commands that the workflow forbids in the latest run of the implementer or the fixer, one for each line, or nothing when there were none:
{{ .violations }}
Report each one as a finding.

This method has no follow-ups: every open finding needs a commit. A finding that you leave open gives the verdict `changes`.
The verdict is `approve` when you have no open finding, and `changes` when you have one.
Write it to {{ .output_file }} as JSON with the list of your open findings, for example `{"verdict":"approve","findings":[]}`.
Give each open finding as an object with a `class` and a `text`: `{"class":"blocking","text":"what is wrong and where"}`.
Write the output `regressions` beside them, as a text that holds one whole number. For example: `{"verdict":"approve","regressions":"0","findings":[]}`.
Write the output `mechanism` beside them: the one file that your open findings are about, as the file path. You may add `:function` after the path. The loop counts the fix rounds on the part before the first colon. When you have no open finding, or the findings do not share one mechanism, write an empty text. For example: `{"verdict":"changes","mechanism":"internal/store/retry.go:Do","findings":[{"class":"blocking","text":"the retry never stops"}]}`.
The class is one of `blocking`, `deferred`, `pre-existing` or `minor`.
Name the head you reviewed. Put each finding under Blocking, Should-fix or Non-blocking.
