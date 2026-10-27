
## Report

**Write the full report to the report file named under "Names for this lane" at the end of this brief FIRST**: every measurement, every control, every arm you
discarded. That file is the record and it is read.

**Write it with Bash, not the Write tool** (the harness refuses it for report files): `cat > <report file> <<'EOF'` ... `EOF`.
If Bash cannot write it either, put the whole report in your final message and say so.

**The report carries four tables, and `obee check report <report> --head <sha>` must pass on it.** Its
header states the format. A report that fails goes back to you unread.

- `## Claims`: every claim in the title, body, commit messages, changelog entry, new comments, assertion
  messages and closing keywords, with its evidence and the head it was verified at. Every push makes the
  table stale, so re-verify it at the new head.
- `## Hunks`: every changed production hunk, the arm that removes it, and the red line a test printed.
- `## Inputs`: every input class the changed code accepts or refuses, what the base does, what the head does,
  and the test.
- `## Siblings`: every other site with the same behavior, and whether it changed.

Then end your turn with a VERDICT LINE plus at most six lines: the verdict shape and the item-specific
numbers named under "Names for this lane", compressed to the one or two that decide it, the tip you measured at, and anything OWED **by name**.
**Send no `SendMessage` report at all**: the harness delivers your end-of-turn text to the orchestrator
already, and a message truncates near 3000 characters. Keep to the budget named there.

**When you stop before the item is done, write the handoff to the handoff file named there, under 300 words,
yourself.** `obee check handoff` must pass on it, or no successor starts. It carries: the plan file;
the commit; what is done and proven; what is owed, in order; the one thing not to redo; and these lines:

    Regressions of this pull request so far: <the count in the handoff you started from, plus each defect an earlier lane of this pull request introduced that you found>
    Tokens at handoff: <your context size>
    Job not read because: <reason>     (only when you launched a job you did not read)
