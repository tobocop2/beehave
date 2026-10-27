**The gate steps come from the round's gate list. You do not choose them.** `[SCRATCHPAD]/gate-steps.md`
holds every step as a name and a command, derived from CI at round start. Your job script runs each step and prints `STEP_RESULT name=<name> rc=<n>`.
Before the launch, `obee check job --script <script> --list [SCRATCHPAD]/gate-steps.md` must pass.
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

