**On a remote build host, a job runs through the host's job queue.** After the script is on the box, submit it:
`obee host submit <project> "<command>" --log <log> --marker <MARKER>`. Run it on your side, not on the
build host. It prints the `launched:` line. The host starts the job when the queue reaches it. Do not start the job yourself, do not
submit it twice and do not poll the load in a job script. The log is empty until the job starts: a log
with no marker is a job that has not ended, and that is not a hang. Only a single command under one
minute runs outside the queue. **Only your job waits for the host. You do not.** Start the steps that need
no host (the reading, the diff, the job script) while the host is full.

**Leave a host tree in place, name it in the handoff, and put no delete in any command you run.** The laptop's safety check stops for approval on `rm` inside an `ssh` string or a `printf` or heredoc, and on a relative path. Write scripts with the Write tool, never `sed -i.bak`. `obee check transcript` reads your transcript, and a hit sends your report back.

**Before EVERY build, check `df` on the build host.** If the free space is under the profile's floor,
wait. A build that starts on a nearly full disk fails partway with void results.

