## What every review here must do

One complete sweep with the review skill your rules name, every finding reported, **not iterated to zero**: a reviewer
cannot converge a loop it may not author commits for.
Every added test gets a mutation arm **shown red**, with blob hashes either side.
**Cite the reference the ruling names.** When the ruling of the item names a reference (a rule, a design note or the
behavior of an upstream tool), check the change against that reference and quote it in the finding. Do not
review against your own idea of correct.
**Check the state the change stores.** Name each place where the change keeps state (a file, a ledger row, a cache
or a field). For each one, say who writes it, who reads it and what a reader sees when the state is stale or missing.
**Check for dead code.** Run the dead-code check of the project at the head. List each function, field or branch that
the change adds and that no call site reaches.
**Build-versus-buy search.** When the change builds a mechanism, search the project, its dependencies and the
standard library for one that already does the job. Name what you found, or write that you found none.
**A SOURCE-CONTRACT TEST IS NOT PROVEN UNTIL A WRONG IMPLEMENTATION FAILS IT.** A test that reads a file and
matches substrings asserts that two tokens appear, not that they are on an executing path. Write the
adversary before you accept the test (comment the guard out; invert one condition), and require
STRUCTURAL containment rather than co-occurrence: the `bail!` inside the block the predicate names.
Strip comments first; the profile names a test in this repository that already does.
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
**List the gate steps that ran at the head you review, with the job that ran each, and name every step of the round's gate list (`[SCRATCHPAD]/gate-steps.md`) that did not run at that head.**

## Condition 4, every item, unasked

Worktree at the head, `detect-secrets` per file, **MERGE-BASE content against head content**, the difference
reported. Assert the file count is nonzero **and matches the pull request's own file list**. Confirm the
merge-base is the one against the CURRENT base branch. **Plant an AWS key pair as a positive control and show it SCORING**,
then remove it and show `git status --porcelain` empty. If the plant scores ZERO, your instrument is not
reading what you think: the repository's own script diffs a three-dot range, which cannot see the index, so
a STAGED plant is invisible to it. Commit the plant, re-run, and only then believe the number. The
repository's own `secrets:check` is NOT this: it scans the tree, not the difference the change makes to it.
