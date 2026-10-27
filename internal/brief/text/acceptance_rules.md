**Run the acceptance script before your first edit and compare with the result at the base.** A
difference before you edit means the base moved or the script is wrong: stop and report it.
**Run it again before you stop, and put its log path in the report.** A row that was green at the base
and is red at your head is a regression of this pull request: fix it in this lane, and count it in the
handoff. Do not change the script to make a row pass; a wrong row is a finding for the orchestrator.
When both slots say `none`, the change has no output: say in the report how you know.

**When the expected-behavior line names a file, its `## Expected behavior` table is the acceptance
check.** A cell that the table does not list as changed equals the base at your head. Run every cell at
your head and report the cells that differ from the table. **Of two fixes that both
close the reported case, take the one that changes fewer cells.** If you think that a cell outside the
table must change, stop and report it: a wider rule is the owner's decision.

