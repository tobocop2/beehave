**On a remote build host, a job runs through Slurm.** After the script is on the box, submit it:
`obee host submit <lane> --script <script> --log <log> --marker <MARKER>`, with `--browser` when the
job runs browser tests. Run it on your side, not on the build host. It prints `SUBMITTED` and the
`launched:` line. Slurm starts the job when a gate license is
free, in the order of the submits, with one browser job at a time. Do not start the job yourself, do not
submit it twice and do not poll the load in a job script. The log is empty until the job starts: a log
with no marker is a job that has not ended, and that is not a hang. Only a single command under one
minute runs outside Slurm. **Only your job waits for the host. You do not.** Start the steps that need
no host (the reading, the diff, the job script) while the host is full.

**Delete a host tree with `obee host clean <lane> <path>...` (or `<lane> --all`), and put no delete in any command you run.** The laptop's safety check stops for approval on `rm` inside an `ssh` string or a `printf` or heredoc, and on a relative path. Write scripts with the Write tool, never `sed -i.bak`. `obee check transcript` reads your transcript, and a hit sends your report back.

**A mutation run is not a gate step.** A gate script with one is refused.
Read the gate as soon as its marker appears, and report. Then submit with `--mutation`: the run starts when no gate runs, alone, in its own small
build tree, with debug information off (for cargo `CARGO_PROFILE_DEV_DEBUG=0 CARGO_PROFILE_TEST_DEBUG=0`;
else the profile's setting), on the diff only, against the named tests when the suite is red on the base.
Its result is a later addition to the report.

**Before EVERY build, check `df` on the build host.** If the free space is under the profile's floor,
wait. A build that starts on a nearly full disk fails partway with void results.

