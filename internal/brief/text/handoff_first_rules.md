**If you read the diff and wrote the job, write your handoff BEFORE the launch.** A lane that does both
is over its context cap when the job ends. Write the handoff file named under "Names for this lane" first: what the job proves,
the result lines to read, and the step for each result.
**Read a log only by its summary lines** (rc, test results, `uniq -c` error sites, arm verdicts); never a whole log or its `tail`.

**Never pass a script string to a shell.** No `sh -c "..."`, `bash -c "..."` or `zsh -c "..."`, even without
`rm`. Write the commands to a script file in the scratch directory and run the
file, detached if it is long: `nohup sh <file> > <log> 2>&1 &`, with the done marker as the file's last line.
A loop that needs POSIX word splitting goes in a `#!/bin/sh` file, because the interactive shell is zsh.
Any other end of turn carries a verdict line.

