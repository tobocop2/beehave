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
- A gate that a newer head cancels cancels its job. A cancel of the item cancels the build job too.
- The cap is 1. A cap above 1 is not built.

The host of a gate is the cluster that `host_up` recorded, else the default `build_host` of the project. The engine reads it once, when the gate is queued, and keeps it in the input of the gate. So a replay uses the same host after the project file changes.

A gate on a build host checks for a pass to reuse in the step `gate:reuse` before it takes its place on the host queue. So a gate that needs no build never waits for a host.

### Recorded steps

A project gate of a `gate` step is two recorded steps in the exclusive run:

- `gate:job` reuses the pass of an earlier gate with the same key, or submits the host job and records its id.
- `gate` waits for that job.

The workflow library cannot record from inside a step, so the id needs a step of its own. After a crash between the two steps, the replay finds the id on record and the wait reads the same job: the host gets one job. A crash inside `gate:job`, after the host accepted the job and before the step is recorded, can still submit a second job.

The `batch_merge` gate still runs in one step. A gate call that names a program is one step, `gate`.

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

### Pinned tools

A gate that passes with the wrong tool proves nothing about the pinned tool. The optional project key `pin_file` names a file in the format of `.tool-versions`: one tool and its exact version on each line. A `#` starts a comment. The path is relative and has no `..`. The config refuses any other path.

beehave reads the file at the base commit, `origin/<base_branch>` (`git show`), not in the work tree of the lane. So a lane cannot edit its own pins.

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
