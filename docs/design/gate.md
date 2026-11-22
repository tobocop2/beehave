# Gate

Part of the [beehave architecture](../architecture.md).

`gate` runs the gate command of a project, on this machine or on its build host, and says whether the tree passed. It gives each gate step one of three states: `pass`, `fail` or `unknown`. A gate is green only when every step is `pass`.

## Gate

### Inputs

`gate` takes three function values:

- the shell,
- the reader of the tree,
- the finder of the build host.

`proc.RunShell` is the shell. It returns both streams in order. For a process that a signal ends, it returns 128 plus the signal number. It keeps the last `model.ReportLimit` bytes of the output, so a command that prints without end does not grow memory.

### Refusals and results

The gate must judge the commit that beehave sends on, so it refuses a tree with uncommitted changes and a head or tree that changes during the run. The cases:

| Case | Result |
|---|---|
| The tree has uncommitted changes | Refused |
| The head or the tree changes during the run | Refused |
| A host job died, went stale or ran past `host.max_run` | Void: every step is `unknown`, and the report says why |
| A host job in any state other than succeeded or failed | `ErrNoVerdict`; it never counts as a pass |
| A clone or checkout on the host fails (for example, a head that is not pushed) | A failed gate, never a pass |
| A host request without a `CloneURL` | Refused |
| A `Resume` request without `ResumeHead` | Refused |

### The host command line

A gate on a build host sends one command line that makes the host judge the sent commit. The line does these steps:

1. Make a fresh folder under the login folder. An `EXIT` trap removes it without a change to the exit status.
2. Clone the repository from the `CloneURL` of the request.
3. Do a detached checkout of the head.
4. Check that the checkout is that head.
5. Run the gate command.

Every word is shell-quoted.

### Submit and resume

`Submit` starts the host job of a request and returns its `Job` (the id and the head) without a wait. A local gate has no host job and gets an empty `Job`.

A request with a `Resume` job id reads that job again and submits none. It must carry `ResumeHead`, the head that the job was submitted with. `Result.Head` is that head.

The engine calls `Submit`, records the `Job`, and then calls `Run` with that `Job` as `Resume` and `ResumeHead`.

### Queues

The exclusive queue runs one gate or merge for each project.

A gate on a build host also runs its build job as a child run on the queue `hosts`, under the key `host:` and the host name. Each key runs one job at a time. So the gates of all projects on one host take turns, and the gates of two hosts run together.

- The gate keeps its slot in the exclusive queue while the job waits, so a merge of the project still waits for the gate.
- The queues cannot deadlock. A job queues nothing, so no job on the host queue waits for a slot of the exclusive queue.
- A cancel of a gate, by a newer head or with its item, cancels the build job too. DBOS cancels each level of runs before it lists the children of that level. A child start checks in its own transaction that its parent still has an owner, and the cancel clears that owner. So a job that starts during the cancel is listed and canceled, or its start is refused. Until a DBOS release has this order, `go.mod` pins a fork commit that has it. The wait of the gate never cancels the job by its id, so a stop that ends late cannot cancel a job that a resume reopened.
- DBOS commits each level of a cancel in its own transaction. A cancel that fails after the gate level leaves the gate canceled and its job running. The gate of a later head lists a canceled old gate whose job is still queued or running, and cancels it again with its children. `beehave cancel` of the item does the same. A cancel of a canceled run lists the open runs under it first. If there are none, it says the run has nothing to cancel and exits non-zero. Otherwise it cancels them and names them on stderr.
- The cap is 1. A cap above 1 is not built.

The host of a gate is the cluster that `host_up` recorded, else the default `build_host` of the project. The engine reads it once, when the gate is queued, and keeps it in the input of the gate. So a replay uses the same host after the project file changes.

A gate on a build host checks for a pass to reuse in the step `gate:reuse` before it takes its place on the host queue. So a gate that needs no build never waits for a host.

### Recorded steps

A project gate of a `gate` step is two recorded steps in the exclusive run:

- `gate:job` reuses the pass of an earlier gate with the same key, or submits the host job and records its id.
- `gate` waits for that job.

When the wait of `gate` ends as `lost`, the steps `gate:lost-start` and `gate:lost-look.<k>`, with a durable sleep between two looks, wait for a proof that the job can no longer run. A proof gives a void gate with the fields `host_loss` and `host_losses`, and the item run records the loss as the step `<call>/host-loss`. See "The wait for a proof" in [hosts.md](hosts.md). When no proof comes within `host.lost_job_wait`, the item run asks the owner, and the step `<call>/lost-ask.<n>` reads the proof after each answer. See "The owner wait for a lost job" in [hosts.md](hosts.md).

The workflow library cannot record from inside a step, so the id needs a step of its own. After a crash between the two steps, the replay finds the id on record and the wait reads the same job: the host gets one job. A crash inside `gate:job`, after the host accepted the job and before the step is recorded, can still submit a second job.

The project gate of `merge` has the same two steps, `batch_gate:job` and `batch_gate`. After a lost job with a proof, the batch gates the same commit once more. See "A lost batch gate job" in [workflow-files.md](workflow-files.md). A gate call that names a program is one step, `gate`, and a `merge` with `gate_run` is one step, `batch_gate`.

### Gate steps

A project sets either `gate` (one command line) or `gate_steps` (a dict of step names to command lines). The config refuses both together. A name is letters, digits, dot, underscore and hyphen. The name `tests` is reserved for the report step.

With `gate_steps`, the gate runs each command in the dict order and gives one result for each name. So a recipe that exits 0 without its work cannot hide behind another step.

- A failed step does not stop the later steps.
- A signal or the time limit stops the later steps and makes every step `unknown`.

A job on a build host runs each command in the same way and writes the line `beehave-gate-step <name> rc=<code>` after it. The gate reads these lines from the log of a failed job:

- A step with no line is `unknown`.
- A step line that the log holds twice makes the step `unknown`.
- A host that cannot give the log gives every step `unknown`, never a pass.

The step commands also write to the log, so a step line is a reading and not proof.

The gate list is part of the gate key, so a changed list never reuses an earlier pass.

The facts of a project with `gate_steps` show the list in `gate`, as `name: command` joined by `; `, and the names in `gate_steps`.

### The recorded plan

The gate fixes its plan once, in the step that submits the host job (`gate:job`). The plan is the gate string, the list and `test_report` as they are then. That step records the plan with the job. The wait and the finish judge the job by the recorded plan, never by the config of the day. So a restart after a config edit cannot report a step that the job never ran.

A gate call records the names of that plan in the result field `required`: the names of `gate_steps`, then `tests` when the project sets `test_report`.

A merge call refuses a `steps` value that has no result for any name in the `required` field of the gate call that gave it. The merge reads the record, never the config of the day. So a config edit or a restart between the gate and the merge changes neither the check nor a replay.

A program gate (`gate(run = ...)`) gives only the step `command`. So it cannot merge in a project with `gate_steps`.

### Test reports

A gate that exits 0 has not proved that tests ran. The project key `test_report` is a glob, relative to the work tree, for JUnit XML files.

Every project that has a gate must set `test_report`, with or without a repository and on any build host. The config refuses a gate without it, because the gate would have no test count.

The beehave repository itself writes its JUnit files with `make test`, through gotestsum, into the folder `reports/` (git ignores it). A project config for beehave sets `test_report` to `reports/*.xml`.

#### On a build host

A gate on a build host cannot read the report files here. So its job command first refuses a report file that the repository tracks. It ends with a check that the glob matches at least one `testcase` element. The job writes a marker line before it fails, and the gate reads the end of the job log:

- A job that passed its command and found no test case gives `command` pass and `tests` fail.
- A tracked report file gives `tests` fail and names the file.
- Any other failed job gives `command` fail and `tests` `unknown`.

#### On a local gate

If a local gate exits 0, the gate reads every file that matches. It sums the `tests` count of every `testsuite` element at any depth, with or without a `testsuites` root. It never reads a `testsuites` test total, so a total and its suites are not counted twice.

The gate also counts failed tests:

- For a suite: the count of `testcase` elements with a `failure` or `error` element, or the `failures` and `errors` attributes of the suite, whichever is higher.
- For a `testsuites` root: the higher of its own `failures` and `errors` and the count of its suites.

| Report | Result |
|---|---|
| One failed test (even with 0 tests) | The step `tests` and the gate fail |
| No matching file, a file that does not parse, or a sum of 0 and no failed test | No result line: `tests` is `unknown`, and the report names the cause (`ErrNoTests`) |

#### Stale reports

Before the gate command starts, beehave records the modification time and the size of each matching file. After the gate, a file counts only if it was not in that record, or its modification time or size differs. An unchanged file is stale. beehave deletes no file.

beehave reads no clock. So a file with a future timestamp, a coarse timestamp or a clock that steps back does not change the result. If no file counts, the gate fails with a reason that says the gate wrote no new report.

### The candidate gate

A `gate` call with `base` submits the job of a candidate head. A `gate` call without `base` is a sync head or a landing gate: it runs the gate command only, with no secret scan and no new-tests check. A candidate gate never reuses an earlier pass, because a reused pass holds no checks.

The job of a candidate runs its steps apart, each in its own subshell, and a failed step never stops a later one. Each step writes the line `beehave-gate-step <name> rc=<code>`. The step names `head-gate`, `secret-scan`, `base-suite`, `revert-run` and `gate-program` are reserved, and `gate_steps` refuses them. The job exits with the exit code of `head-gate`, and with a failure when the final check of the gate program failed.

| Step | What it does |
|---|---|
| `head-gate` | Runs the gate command and the report checks. Its exit code is the verdict of the gate. |
| `secret-scan` | Runs `gitleaks git --log-opts=<merge base>..HEAD`, so it reads the diff of the candidate only. |
| `base-suite` | Runs `test_command` on the merge base when the host has no cached report for that commit. |
| `revert-run` | Runs the new tests on the base code, when the head has new tests. See "The revert run". |

The merge base is the merge base of the head and the base commit that the engine read for the gate. The host keeps one parsed base report for each merge base commit, in a folder outside the clone. The job writes each file under a temporary name and renames it, so no reader sees a partial file.

After the steps, the job compares the head report with the base report per test, by `classname` and `name`. A subtest is its own test. The reader is `awk` only. The job prints:

- one line `beehave-new-test <class> <name>` for each new test,
- one line `beehave-new-test-failed <kind> <class> <name>` for each new test that failed or was skipped on the head,
- the end line `beehave-new-tests-end new=<n> failed=<n>`.

Each kind of line stops at 50 lines or 4096 bytes, whichever comes first. The end line carries the full counts. The host reads the last 16384 bytes of the log, so with the revert lines below, the end lines and the step lines stay in that tail whatever the length of a test name.

The engine reads the log of a passing candidate job and records `checks` and `new_tests` in the result of the gate call. A log with no end line is a cut log: `checks.complete` is false. The step line of `beehave status <run id> --json` shows `new_tests`.

### The revert run

A new test must fail without the change. The revert run checks this on the host. It checks out the merge base in its own folder, copies onto it the test files that the head added or changed, and runs `test_command` there. The project key `test_paths` names the test files as globs (for a Go project, `["**/*_test.go", "**/testdata/**"]`). The run reads the reports of its own folder only, never the reports of the head. The job prints its lines after the new-tests lines:

- one line `beehave-revert-passed <class> <name>` for each new test that passed or was skipped on the base code, at most 50 lines or 4096 bytes,
- the end line `beehave-revert-end new=<n> passed=<n> cases=<n>`.

A new test that the base report does not name failed: a package that does not build on the base gives a report with no new test name. The exit code of `revert-run` is recorded in `checks.revert_rc` and never decides a verdict. A head with no new test, or a project with no `test_command` or no `test_paths`, gets the line `beehave-revert-error <reason>` instead of a run. The engine reads the last of the end and error lines. It sets `revert` to `pass` when that line is the end line, `cases` is above 0 and `passed` is 0, and to `fail` otherwise. So an end line that a test prints in the gate output cannot pass a run that did not run. The result of `gate` and the step line show `revert`. The `before_review` function of `review_fix.star` (see `workflow-files.md`) refuses a head with new tests whose `revert` is `fail`, and names each test that passed on the base.

The secret scan needs gitleaks 8.30.1, which `make setup` and the CI `test` job install. A test with a planted key proves that the step fails. The test builds the key from parts at run time, and it fails when `CI` is set and gitleaks is missing.

### Non-zero exits and kills

- A gate that exits non-zero fails, and the report is not read. The step `command` is `fail`, and every later step (`tests`) is `unknown`, in the step results and in the `steps=` field of `beehave status`.
- A local gate that the gate time limit (one hour) kills, or that a signal kills, has no result. Every step is `unknown`, and the report says what killed the command. The review loop reads it as a void gate.
- A gate command that exits 124 on its own is a failure.

### Void gates

A gate with no failed step and at least one `unknown` step is void, not red.

- A batch gate stops the batch and blames no member (`ErrGateVoid`).
- The review loop of one item starts no fix agent for it. The loop reruns a void gate twice in the same run. Each rerun is a new recorded gate step, because a resume replays a recorded result and never runs a recorded step again.
- After the reruns, the loop asks the owner, and an answer runs the gate again. After three answers it refuses and names the steps with no result.
- The loop of `examples/local` still reads a void gate as red.

Three states, one owner: `internal/gate` sets `pass`, `fail` and `unknown`.

### Gate program

The optional project key `gate_program` lists the repository paths that hold the program of the gate: `Makefile`, its shadow files, `scripts/`. The gate takes its program from the base commit and its code from the head. See `configuration.md` for the rules that the config checks.

A gate on a build host with `gate_program` set runs these words, in this order, in a new clone of the head. The clone holds no untracked file, and the job runs no `git submodule update`.

1. Snapshot. Before any head command, the job fetches the base commit into a private bare repository and copies the bytes and the mode of each listed path into a folder of the job. The job also keeps a digest, a mode and a link target for each file in shell variables. The final check reads only these variables, never the folder. Git reads the private repository with no replace refs and no config of the machine. The one exception is the fetch of the base commit from the clone URL when the clone itself lacks it: that fetch reads the config of the machine, which holds the credentials.
2. Refusal. If the head changed a listed path since the merge base with the base commit, the job fails with the line `beehave-gate-program` and the changed paths. The gate result then has the step `gate-program` in the state `fail`.
3. Overlay. The job removes each listed path from the clone and restores it from the snapshot. A path that the base lacks stays absent, so a head cannot add `GNUmakefile`.
4. Final check. After the last head command, the job compares each listed path with the values that the job kept in shell variables, by the digest of the bytes, the execute bit and the kind of file. Head code cannot change a shell variable of the job. The job resolves git, find, sort, readlink, cat and the digest tool to absolute paths before any head command, so a tool that the head puts first on the PATH is not used. The check uses no git word, because the head writes the git config. A changed, added or missing file, a link that points elsewhere and a file that is not regular, a link or a folder all fail the job.

The base commit is the one that the engine read once for the gate. A gate with `gate_program` and no base commit starts no job. The gate key holds the list.

A local gate with `gate_program` set runs the same words. The gate makes a new folder, and the job clones the worktree of the item into it with `git clone --no-local`. The copy holds its own object files, so it shares no file with the worktree, its git folder or the source. The job then removes the `origin` remote, so the git config of the copy holds no path of them. The job reads the base commit from the source of the project when the copy lacks it.

Every git call of a local job goes through one shell function, the clone, the checkout and the report check included. The function starts the git that the engine resolved through `env -i`, so the call gets no variable of the caller except `PATH`, `HOME` and `TMPDIR`: no `GIT_TEMPLATE_DIR`, `GIT_CONFIG_COUNT`, `GIT_CONFIG_PARAMETERS` or other `GIT_` variable. It sets `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `XDG_CONFIG_HOME` to an empty folder of the job, `GIT_NO_REPLACE_OBJECTS=1` and `GIT_TERMINAL_PROMPT=0`, and it passes `-c core.hooksPath=/dev/null`. Each repository that the job makes, the copy and the snapshot, takes the empty folder as its template, so it holds no hook. The template is the defense that holds on every git: Apple Git 2.39 still runs a hook of the repository in `git remote remove` with `core.hooksPath` set. So no config file or attributes file of the user or the machine reaches these calls, with the sandbox on or off, and no hook, template hook or `core.fsmonitor` command runs. The fetch of the base commit from the source has no exception here.

This has a cost for a private repository. A local project with no `source` reads the base from the forge. When the copy lacks the base commit, the job fetches it from the forge with no credential helper and no prompt, the fetch fails, and the gate fails closed. A local `source` folder, or a copy that holds the base commit, does not need the fetch. A host job runs its clone and that fetch with the config of the host, because that config holds the credentials of the forge. Its checkout and the other git calls of the gate program run as in a local job.

The engine finds git, env, find, sort, readlink, cat and the digest tool once, when it starts, and gives these absolute paths to the job. A tool that an agent step puts first on the PATH after the engine starts is not used by the words of the gate program. A tool that is first on the PATH before the engine starts is used. An agent step of an earlier run can put a fake tool there, and the gate does not see it. Only the sandbox of #438 closes this.

With the sandbox on, the job runs under the enforcer in that folder. The plan lets it read the worktree, its shared git folders and a source that is a local folder. With the sandbox off, head code runs as the user and can write any path that it can name. This includes any tool in a folder that the user can write, and the seven tools above when they live there, as in `/opt/homebrew/bin`. The gate keeps head code from the paths that it finds in the copy, and no more. A job that fails keeps its log in the report. A job that fails with a log line `beehave-gate-program` gives the step `gate-program` in the state `fail`. The limits of an unsandboxed gate stay: head code can still write the tools of the host, `go.work` and the git config of the copy, and the final check does not see them.

#### The owner command

A pull request that changes a listed path fails the refusal by design. The owner runs `beehave gate --own-program <pull request> [project]` on a terminal. The command refuses to start when its input or output is not a terminal. It prints the diff of the pull request and runs nothing until the owner types the number of the pull request. The printed diff shows each control character except the line break and the tab, each format character (such as a bidi override or a zero-width space) and each line or paragraph separator as an escape such as `<U+202E>`. The command runs the gate of the project with `gate_program` empty, so the head runs its own program with no overlay and no refusal. Then it posts `beehave/gate` on the head with the description "gate program changed, run by owner command" and the gate key. The status is `success` for a pass and `failure` for a fail. The status goes through the writer role of the project.

The command refuses a pull request on the branch of an item. The engine names the branch of an item for the item, with one rule for the worktree, the branch lookup, the pull request lookup and the pull request it opens. The push step pushes to the branch that the workflow names. The command reads the items from two places: the runs of the project, and the open items that the tracker of the project lists, through the same tracker read as the queue. When the tracker cannot list its items, the command stops before it reads any path. The command takes the head branch without the spaces around it and without a `refs/`, `heads/` or `refs/heads/` prefix, and compares it with each item id, ignoring case. It refuses the branch when the branch is the item id, or starts with the item id and then a character that is not a letter or a digit, such as `bh-12-gate` or `bh-12/x`. It admits a branch that holds the item id elsewhere, such as `fix/bh-12`, and a branch whose prefix only looks like the id, such as `bh-123`.

The command refuses a pull request whose base is not the base branch of the project. A status stands for the commit on every pull request, so a pull request into another branch would bless the commit for the base branch. It also refuses a head that is behind the base branch, so the gate never judges code that the base has since changed. It reads the tip of the base branch once, and the paths, the diff and the ancestor check all use that tip. After the gate, it reads the head of the pull request and the tip again. When either moved, it posts nothing. A move in the short time between that read and the post is not seen; a ruleset that requires an up-to-date branch closes it.

The command refuses a pull request that changes a path that `gate_program` does not list, and a pull request that changes a submodule. It reads the changed paths with git, against the tip of the base branch. A rename shows as a delete of the old path and an add of the new path, so a rename and a long file list cannot hide a path. A project with no `gate_program` lists no path, so the command refuses every change.

The command is not a barrier. The terminal and the diff stop a script by mistake. An agent step that is not in a sandbox can still push a branch under another name and run the command. Only the sandbox closes that.

### Pinned tools

A gate that passes with the wrong tool proves nothing about the pinned tool. The optional project key `pin_file` names a file in the format of `.tool-versions`: one tool and its exact version on each line. A `#` starts a comment. The path is relative and has no `..`. The config refuses any other path.

beehave reads the file at the base commit (`git show <commit>:<file>`), not in the work tree of the lane. So a lane cannot edit its own pins. The engine reads the base commit once for each gate: the tip of the base branch at the source of the project (`git ls-remote` in the push repository of the project, as the writer of a project with `credentials`, so no setting of the clone config reaches it). The key of the gate reads the tip, and the host job takes that same commit from the key, so a push to the base between the two changes neither. The commit goes into the plan of the gate and into the host job, and every later read of the base uses it, never the branch name. The tip must be a full commit id of 40 hexadecimal digits. If the clone lacks the commit, the engine fetches that commit by its id from the source into the push repository, then into the clone over the file transport. A source that speaks git protocol version 2 serves a commit by its id. A plan that an earlier build stored with a pin file and no base commit is void: run the gate again. A lane can move `origin/<base_branch>` in the clone, and it cannot move the source. A gate call with a `base`, the branch that the head merges into, reads the tip of that branch instead, so a child of a round gates with the pins of its round base. If the engine cannot read the base commit, the gate fails and the error names the base branch. The gate key holds the base commit and the pin text that it read.

A gate call with a `pin_base` reads the file at `origin/<pin_base>` and turns on no candidate check: `base` does both, and `pin_base` only the first. A call with both reads the file at `pin_base` and runs the checks of `base`. The sync gate of a round sets `pin_base` to the round base. The landing gate of a `merge` call reads the file at the `base` of the call, with no candidate check. So a round that changes a pinned tool on its round base gates its sync head and its landing union with the new tools, as its children do.

#### The probe

Before each gate command, in the same shell as the command, a probe asks each tool for its version with `<tool> version`, then `<tool> --version`. It never reads a version from a name on the path.

- A word of the output must equal the exact pinned version, with or without a leading `v` or the tool name. Pin `1.27` does not match `1.27.3`, and pin `1.2` does not match `go1.27.3`.
- The check runs the tool by name in the directory and the environment of the gate. A gate that sets its own PATH or runs the tool through a wrapper is not checked.

The probe is part of the command text, so a gate on a build host runs it on the host. A tool at another version writes the line `beehave-gate-void <tool> <pinned version> <text>` and exits with 97. The gate reads that line from the log of every failed job, whatever else the project sets. A host log that cannot be read leaves a failed gate, not a void one.

#### Void on a pin mismatch

The gate is void when:

- a tool is at another version,
- a tool gives no version, or
- the pin file is absent at the base commit, empty or malformed.

Then the gate command does not run, and every step is `unknown`. The report names the tool, the pinned version and the version found.

The gate key holds the versions that a local tool reports only for a local gate.
