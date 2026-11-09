# Build hosts

Part of the [beehave architecture](../architecture.md).

This part describes the build host contract, the three providers, the guards that protect work on a host, and the project keys that select a host.

## Build host

### The Host contract

`host` holds the `Host` contract: `Up`, `Status`, `Down`, `Submit`, `Wait`, `Cancel`, `Drain` and `Resume`. An adapter is a child package.

The config names three providers, and each one runs:

| Provider | Machine | Creates and deletes it |
|---|---|---|
| `skypilot` | A cluster that SkyPilot makes | Yes |
| `ssh` | A machine of the owner | No |
| `slurm` | A Slurm cluster of the owner | No |

### Shared packages and imports

- The ssh command line and the text of a failed command are in `host/link`.
- The wait states are in `host/jobwait`.
- No adapter copies them.

The adapters never import each other. Only `skypilot` imports `slurm`, for its queue, and `depguard` denies every other pair.

The job id, job state, disk reading and host status types live in `model`, so layer 2 declares its own interfaces without importing `host`. Nothing outside a provider names SkyPilot or Slurm.

`host/hosttest` is the conformance suite, in the style of `forgetest`.

### The Slurm adapter

`host/slurm` is the Slurm adapter. It holds the job half of the contract: `Submit`, `Wait`, `Cancel`, `Log`, `Drain`, `Resume`, the job states and the parsers. It runs over a `proc.Runner` that reaches the head node, which is ssh. It works for any Slurm cluster that beehave can reach, and it creates and deletes no machine.

| Method | Runs over ssh |
|---|---|
| `Submit` | `sbatch` |
| `Wait` | `squeue`, then `sacct` after the controller forgets the job |
| `Cancel` | `scancel` |
| `Drain`, `Resume` | `scontrol` |

### The slurm provider

`slurm.Host` is the Slurm provider for a cluster that the owner provides. The `ssh` block names the head node, and `build_host` is its ssh alias.

| Method | Behavior |
|---|---|
| `Up` | Checks that the head node answers and that `sinfo` lists its nodes. It changes nothing. |
| `Status` | Absent when ssh cannot connect. Draining when Slurm drains a node. Otherwise up, with the free disk. |
| `Down` | Sends nothing: beehave did not create the cluster. |
| `Drain`, `Resume` | Run `scontrol update` on every node, which changes node state on a cluster that beehave does not own. They belong to the `Host` contract. Only the conformance suite calls them; the run never does. |
| `Teardown` | Banks the work and leaves the cluster up. |

- No slot job holds the cluster, because nothing deletes an idle cluster.
- `host.max_run` goes to `sbatch` as `--time`. Slurm ends a job at the limit as `timed_out`.
- Jobs run in the login folder of the head node, so the key `host.ssh.work_root` is refused for this provider.

### The ssh provider

`host/ssh` is the adapter for a machine that the owner provides and that runs systemd. It uses `host/link` for the ssh command line. It creates and deletes no machine: `Down` sends nothing, and `Teardown` banks the work and leaves the machine up.

`Up` refuses a machine:

- without a user manager,
- with a systemd older than 240,
- with linger off for the login user. The error tells the owner to run `loginctl enable-linger`.

`Submit` runs the command as a transient user unit (`systemd-run --user --remain-after-exit`, with `RuntimeMaxSec` from `host.max_run`). The command text travels as base64, so the shell on the host cannot change it.

`Wait` and `Cancel` use the same engine as Slurm (`host/jobwait`). The poll reads the cancel marker, the unit properties and the size of the log in one ssh call.

| Unit state or `Result` | Read as |
|---|---|
| `deactivating`, `reloading` (systemd is stopping or reloading the unit) | Still runs |
| `exit-code`, `signal`, `core-dump`, `oom-kill`, `watchdog`, `resources`, `start-limit-hit`, `protocol`, `exec-condition` (every documented `Result` of a fault) | A failed job |
| `timeout` | The run limit |
| A value that beehave does not know | An error, never a guess |

The unit is stopped and reset after the bank. So the poll that first reads a final state writes it as an empty file beside the log. A second `Wait` for the same job reads that file, as a second read of a Slurm job reads `sacct`.

`host/sshtest` is the fake machine, and the adapter runs `host/hosttest` against it.

### The SkyPilot provider

`host/skypilot` is the SkyPilot adapter. It runs the `sky` and `ssh` command lines through `proc.Runner`, links no cloud library and opens no listener. The `sky` command starts its own local API server; only a project that selects this provider gets it. A project with no `host` block builds no adapter and needs neither `sky` nor Slurm.

It composes the Slurm adapter: SkyPilot creates and deletes the cluster, and the Slurm queue runs the jobs.

- `Up` is the first call and `Down` the last.
- `Up` runs `sky launch --down` with `host.idle_minutes`. It passes `host.max_run` as the SkyPilot setting for the lifetime of an instance, which SkyPilot documents for GPU instances only.
- `Status` reads `sky status -o json`, then `sinfo` and `df` through the Slurm adapter.
- The Slurm adapter calls the SkyPilot slot: `Hold` after the job is queued, `Release` once the work is banked.
- The `skypilot` provider keeps its own slot and its own run limit (the `timeout` in the slot command).
- Every parser refuses an answer it does not recognise.

### Teardown

`host_teardown` of the three providers banks first. For `ssh` and `slurm`, its output `down` means banked and released, and the machine stays up.

### The invariant

A job holds its slot until its work is banked, for at most `host.max_run`.

- The provider deletes an idle host with no beehave code alive.
- A provider that does not own its machine (`ssh`, `slurm`) has no idle delete. So `Down` is a no-op, `Teardown` is a bank, and the slot is a no-op.
- SkyPilot counts only its own job queue. So `Submit` also starts a `sky exec -d` job that waits for a bank marker. `Wait` banks the work of the host, writes the marker, and returns a final state.
- When the bank fails, `Wait` returns the state of the job together with the error.
- `Wait` and `Teardown` scan the same root: the login folder of the head node by its absolute path (`WorkRoot`).
- `Teardown` is the only way to stop a host with the guards. It scans, banks and stops the host. It takes no function, so a caller cannot pass a stub for the check.

### The guards

The guards live in `host`, so every provider gets them. `Banked`, `Bank`, `FindRepos` and `EnsureUnused` take a command runner and run git or find through it, so the same code scans the machine of beehave or a remote host. `DiskFloor` and `WaitJob` take a `Host`.

#### 1. Banked

`Banked` lists every branch tip, tag, stash entry and worktree HEAD that is not an ancestor of a head or tag that `git ls-remote` reports on a remote with a host part.

- A path or `file://` remote does not count.
- A remote that cannot be read holds nothing.
- A repository with no remote is still scanned, and its commits are unbanked.
- An annotated tag counts by the commit it names.

Each repository gets one time budget, shared by all its git calls and all its pushes: `host.bank_budget`, a duration with the default "20s". The budget ends the local wait only: a git process on the host may go on after it. A repository whose git call fails, or whose budget ends, counts as unbanked, and the scan goes on to the next one. `Bank` refuses and never pushes from it.

`Bank` pushes the missing commits to `host.private_remote`, checks again, and refuses when anything is still missing.

- Each commit goes to its own branch, `beehave-banked/<12 characters of the commit>`, on the private remote.
- The push is a plain push that never overwrites a branch.
- All repositories of a host share that namespace.
- The branches stay until someone deletes them, and CI on the remote can see them.
- A retry after a partial failure pushes the same names.
- Uncommitted and untracked files are not banked: they are lost when the host goes.

#### 2. Repository scan

`FindRepos` has no depth cap. It lists a directory that holds `.git`, and a bare repository.

- A symlink as the root is followed.
- A symlink inside the root is not followed, so a repository reached only through one is not listed.
- A scan that fails, including one that cannot read a directory, refuses.
- A scan that finds no repository refuses.

#### 3. Disk floor

`DiskFloor` refuses a new job when the free space is under `host.disk_floor_gb` or the reading is not readable.

When `host.disk_wait_minutes` is above zero, a job under the floor reads the disk again every 30 seconds for that many minutes. It starts when the space is back. A reading that cannot be read is still refused at once.

#### 4. In-use directories

`EnsureUnused` refuses when a process or one of its threads:

- has the directory as its working directory,
- runs an executable from it,
- holds a file open in it.

One call to `sh` on the host resolves the directory with `readlink -f` and matches the cleaned and the resolved path. Nothing resolves on the machine of beehave. A scan that fails counts as in use.

#### 5. Waits

`WaitJob` accepts only a final job state from the scheduler of the host. Any other state refuses.

A Slurm or ssh `Wait` can also end with one of these states:

| State | Condition | Then |
|---|---|---|
| `died` | The scheduler holds no record of the job for one minute. | |
| `stale` | The log did not grow for `host.stale_minutes` (default 20). | The wait sends one cancel and waits up to five minutes for its final state before it banks. If the job has not ended then, the error says the cancel did not take. |
| `lost` | No ssh read answered for five minutes. | The error is `ErrLost`. The job keeps running, and the error tells a person to cancel it before a new run. |

A dropped link never submits the job again.

### Host config

A project selects a host with `build_host` and the `host` block.

#### The host block

- The `host` block is required when `build_host` is a cluster name, and refused when it is `"local"`.
- `provider` is `skypilot`, `ssh` or `slurm`.
- The `skypilot` provider takes the `skypilot` block. `ssh` and `slurm` take the `ssh` block.
- A block of another provider is refused.
- `idle_minutes` is refused for `ssh` and `slurm`, because beehave does not delete a machine it does not own.
- An unknown key or block is refused.

`cmd/beehave/adapters.go` picks the provider from its host block by `provider`, like the tracker and the forge. `beehave check config` builds the host of each project.

| Block | Keys |
|---|---|
| `skypilot` | `cloud`, `region` (needs `cloud`), `instance_type`. The cluster name is `build_host`. |
| `ssh` | `address`, `user`, `port`, `identity`, `work_root`, `known_hosts` (`strict`, the default, or `accept-new`; a host key that changes is always refused). `build_host` is the ssh alias, so the ssh config of the user applies. The alias holds a letter, then letters, digits, dots and hyphens, so a gcloud alias such as `rig.us-central1-a.bench` is allowed. |

#### The build_host key

The key `build_host` is the one declaration of the hosts of a project: a name, or a list of names that share the `host` block. The first name is the default.

- A run chooses another with the `host` argument of `host_up`, and the step records the cluster.
- The gate, its key, each `command` call with a `host`, and `host_teardown` read that record, so a resume uses the same cluster.
- A resume reads the cluster from the replayed record of `host_up` for every later step.
- `"local"` is the only name of a project that builds here. `host_up(host = "local")` starts nothing there and records `local`, and a cluster name is refused.
- If the project file no longer declares the recorded cluster, the gate, the command and `host_teardown` refuse it with an error that lists the declared hosts.
- The run never moves to another host.
