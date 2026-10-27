
## Long jobs: nothing wakes you once your turn ends

**Never end your turn waiting on a job you started yourself** (`run_in_background`, `Monitor`, a trailing
`&`). An idle lane is not woken when its own job finishes; the orchestrator reads it as a stall.
**You read the job you queue.**

- **A job shorter than 20 minutes: wait for it in your turn.** Submit it with `obee host submit`,
  poll its log for the marker with one foreground `sleep` of 4 to 9 minutes for each poll, and read the
  result yourself. The same holds for every job on a profile whose build host is the local machine.
- **Before a longer job, read your context size.** Near the cap, submit nothing: write the handoff with the
  job script's path and `Job not read because: <reason>`; the orchestrator submits it. Under the cap, hand
  the wait over: write the job script on the build host in one call, its last line `echo "<MARKER> rc=$?"`;
  submit it in a second call with `obee host submit`; end your turn with exactly
  `launched: <log>, marker <MARKER>`, the line that `submit` prints. The orchestrator resumes you with the
  log tail.

**After the launch, do nothing more. No `sleep`. No monitor. No background wait. No look at the log.**
The `launched:` line is the last line of your turn.
**The marker is a whole line of the log, and no other line holds it**: not in a header line, and no stage
marker starts with it (final `DONE-x`, stage `STAGE-x-rep-1`).
**A job that carries edits no compiler has read starts with a compile check, and stops there on a
failure.** Format the edited files before the launch. The first step of the script is the cheapest command that
compiles every edited target, with its output in a stage log. On a failure the script prints the tail of the
stage log and `<MARKER> rc=<non-zero>`, and exits, so its slot is free.
**Stop your job when a later edit supersedes it.** A job proves the tree it started from. Read its group id
with `ps -o pgid= -p <pid>`, stop the group with `kill -TERM -- -<that number>` typed as a literal number, and
launch again on the new tree.
**Say in the report and the handoff whether a compiler read your edits**: `Compiled: yes, <log>` or
`Compiled: no`.
**Before you end, stop each background command you started, by task id**: a live one wakes you later.
Wait loops have a timeout. Test commands read `</dev/null`. Report no background commands left.

