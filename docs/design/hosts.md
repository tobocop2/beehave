# Build hosts

Part of the [beehave architecture](../architecture.md).

This part describes the build host contract, the three providers, the guards that protect work on a host, and the project keys that select a host.

## Build host

### The Host contract

`host` holds the `Host` contract: `Up`, `Status`, `Down`, `Submit`, `Wait`, `Cancel`, `WorkRoot`, `LostProof` and `Log`. An adapter is a child package.

Every provider also has `Drain` and `Resume`. They are not part of the contract, because no run calls them. The conformance suite asserts them on each adapter, and a host without them fails the suite. The disk-floor wrapper does not keep them.

The config names three providers, and each one runs:

| Provider | Machine | Creates and deletes it |
|---|---|---|
| `skypilot` | A cluster that SkyPilot makes | Yes |
| `ssh` | A machine of the owner | No |
| `slurm` | A Slurm cluster of the owner | No |

### The job key

A `Submit` with a key returns the job that the host already holds for that key, and starts no second job. The engine builds the key from the run id and the step id. A `command` call with retries adds the number of the try, so each try has its own key and a retry of a failed job starts a new job.

A step that runs again after a crash counts its tries from 1 again. Slurm keeps ended jobs in accounting, so the re-run finds the job of the try that the crash cut short. The ssh provider releases the unit of a try in `Wait`. A crash during try 2 therefore runs try 1 again, because its unit is gone. The base had the same duplicate on any crash. The follow-up issue tracks it.

### Shared packages and imports

- The ssh command line and the text of a failed command are in `host/link`.
- The wait states are in `host/jobwait`.
- No adapter copies them.

The adapters never import each other, and `depguard` denies every pair. `skypilot` takes its job queue as a parameter, and `cmd/beehave` passes the Slurm queue. The tests of that pair live in `host/skyslurm`.

The job id, job state, disk reading and host status types live in `model`, so layer 2 declares its own interfaces without importing `host`. Nothing outside a provider names SkyPilot or Slurm.

`host/hosttest` is the conformance suite, in the style of `forgetest`.

### The Slurm adapter

`host/slurm` is the Slurm adapter. It holds the job half of the contract (`Submit`, `Wait`, `Cancel`, `LostProof`, `Log`), the `Drain` and `Resume` of a provider, the job states and the parsers. It runs over a `proc.Runner` that reaches the head node, which is ssh. It works for any Slurm cluster that beehave can reach, and it creates and deletes no machine.

| Method | Runs over ssh |
|---|---|
| `Submit` | `sbatch` |
| `Wait` | `squeue`, then `sacct` after the controller forgets the job |
| `Cancel` | `scancel` |
| `Drain`, `Resume` | `scontrol` |

### The slurm provider

`slurm.Host` is the Slurm provider for a cluster that the owner provides. The `slurm` block names the head node, and `build_host` is its ssh alias.

| Method | Behavior |
|---|---|
| `Up` | Checks that the head node answers and that `sinfo` lists its nodes. It changes nothing. |
| `Status` | Absent when ssh cannot connect. Draining when Slurm drains a node. Otherwise up, with the free disk. |
| `Down` | Sends nothing: beehave did not create the cluster. |
| `Drain`, `Resume` | Run `scontrol update` on every node, which changes node state on a cluster that beehave does not own. They are not part of the `Host` contract. Only the conformance suite calls them; the run never does. |
| `Teardown` | Banks the work and leaves the cluster up. |

- No slot job holds the cluster, because nothing deletes an idle cluster.
- `host.max_run` goes to `sbatch` as `--time`. Slurm ends a job at the limit as `timed_out`.
- Jobs run in the login folder of the head node, so the key `host.slurm.work_root` is refused for this provider.

### The ssh provider

`host/ssh` is the adapter for a machine that the owner provides and that runs systemd. It uses `host/link` for the ssh command line. It creates and deletes no machine: `Down` sends nothing, and `Teardown` banks the work and leaves the machine up.

`Up` refuses a machine:

- without a user manager,
- with a systemd older than 240,
- with linger off for the login user. The error tells the owner to run `loginctl enable-linger`.

`Submit` runs the command as a transient user unit (`systemd-run --user --remain-after-exit`, with `RuntimeMaxSec` from `host.max_run`). The user that `host.ssh.user` names runs the unit. On a cloud host that user is the job user that `scripts/host-setup.sh` makes (see below). The command text travels as base64, so the shell on the host cannot change it.

`Wait` and `Cancel` use the same engine as Slurm (`host/jobwait`). The poll reads the cancel marker, the unit properties and the size of the log in one ssh call.

| Unit state or `Result` | Read as |
|---|---|
| `deactivating`, `reloading` (systemd is stopping or reloading the unit) | Still runs |
| `exit-code`, `signal`, `core-dump`, `oom-kill`, `watchdog`, `resources`, `start-limit-hit`, `protocol`, `exec-condition` (every documented `Result` of a fault) | A failed job |
| `timeout` | The run limit |
| A value that beehave does not know | An error, never a guess |

The unit is stopped and reset after the bank. So the poll that first reads a final state writes it as an empty file beside the log. A second `Wait` for the same job reads that file, as a second read of a Slurm job reads `sacct`.

`host/sshtest` is the fake machine, and the adapter runs `host/hosttest` against it.

#### Make a build host

`scripts/host-setup.sh` makes a linux amd64 machine ready for the ssh provider, then proves it. It has two stages. The root stage runs once as root. The job stage runs as the job user, from a clone of the repository at the commit under test, and uses no `sudo`.

```sh
sudo sh scripts/host-setup.sh --stage root --watchdog idle-selfdestruct.service --job-key /path/to/controller.pub
sh scripts/host-setup.sh --stage job --work-root /home/beehave-jobs/work --watchdog idle-selfdestruct.service
```

Set `host.ssh.user` to `beehave-jobs`. The adapter then logs in as the job user, and every gate step, agent run and `make setup` of a candidate commit runs as that user.

- **The rule.** Code of a pull request runs as a user that has no sudoers entry, is in no group that gives root, and can write no file that root or the admin user runs. The metadata guard refuses every user but root on the metadata server. So the token is out of reach by construction: the firewall stops every non-root user, and the user of the jobs can never become root. A guard on one process cannot give this. `NoNewPrivileges=` holds for one unit only, and the same user can start a new unit with `systemd-run --user` or log in again over ssh.
- **The job user.** The root stage makes `beehave-jobs` with `useradd --create-home`, with no extra group and no sudoers entry. The Google guest agent puts the users that it makes into `google-sudoers`. It does not touch a user that `useradd` made. The key of the controller goes in the `authorized_keys` file of the job user, and never in project or instance metadata. Root copies the key to `/etc/beehave/job-key.pub`, and the job user writes it into its own home with `sudo -u`. Root writes nothing in that home, so a link that the job user puts there does not lead root to another file. The stage refuses a key file that holds a private key or more than one line. It turns linger on for the job user, so that user has a user manager of its own. A user unit of the job user, or an ssh login to itself, is still the job user.
- **Root stage refusals.** Before any change, it refuses a user that is not root, a machine that is not linux amd64, a folder that is not a clone of the repository, and a clone that a user other than root and the admin user (`SUDO_UID`) can write, or whose parent folders such a user can write. It judges `/usr/local/go` and `/usr/local/bin` and their parent folders the same way. Root runs the scripts of the clone, the Go in `/usr/local/go`, and `/usr/local/bin` comes first on its PATH, so a file there that the job user can write is a way to root. Write for the group counts unless the group is the primary group of the admin (`SUDO_GID`) and `getent` shows no other user in it: with a umask of 002 every clone of the admin is writable by that group. A sticky parent folder does not count. A folder whose owner `stat` cannot read counts as a folder of another user. If one of these paths is a link, the stage judges the target, the folders above the target, and the folders that hold the link, because a user who can write such a folder can replace the link. A link whose target does not exist yet is judged the same way. For a chain of links, where the target of a link is itself a link, the folder of each link in the chain is judged. A chain of more than 40 links, or a loop, is refused. Not held: ACLs are not judged, and links inside the clone are not judged. Only root or the admin can make a link inside the clone, and the root stage already runs the tracked files of the clone as root. A link in a middle part of the target of a link is also not judged: if `/usr/local/go` links to `/srv/a/go` and `/srv/a` is a link, the folder `/srv` is not checked. Only root or the admin can build that layout, and the admin already has sudo. It also refuses a job user that is in `root`, `sudo`, `admin`, `wheel`, `google-sudoers`, `docker`, `lxd`, `disk`, `shadow` or `staff`. On Debian, `staff` can write `/usr/local`, and the PATH of root starts with `/usr/local/sbin:/usr/local/bin`.
- **Job stage refusals.** Before any change and before any code of the clone runs, it refuses the root user, a user that can run `sudo` without a password, also for some commands only (`sudo -n -l` succeeds), a user in one of those groups, and a work root that the adapter refuses. It also refuses a work root that holds a single quote, a work root that is not its canonical path (a `.` or `..` part, a doubled or trailing slash, or a link), a system folder such as `/` or `/home`, and an existing work root that the job user does not own or cannot write. A missing work root needs a parent folder that exists and that the job user owns. The stage makes only that last folder, with `mkdir` and no `-p`, and it never changes the owner or the mode of a folder. The work root belongs under the home of the job user, such as `/home/beehave-jobs/work`, and not under a folder of the admin user.
- **The watchdog.** The machine needs a self-delete watchdog unit. Each stage refuses to start, and refuses to finish, unless that unit is active, enabled and `Restart=always`. The host maker installs the watchdog. The script installs none. The watchdog runs as root, so it keeps the token and the compute scope that deleting the instance needs.
- **The metadata guard.** The root stage installs the unit `metadata-guard.service` and the rules file `/etc/beehave/metadata-guard.nft`. The unit loads an `nftables` table that rejects every TCP port but 53 to `169.254.169.254` and to `[fd20:ce::254]` from any user except root. That covers port 80, the HTTPS endpoint on port 443 and any later port. The IPv6 address is the one that Google Cloud documents for IPv6-only machines. DNS on port 53 is not touched. The unit runs after `nftables.service`, so the default `flush ruleset` of that service cannot drop the table. `PartOf=nftables.service` stops or restarts the unit when `nftables` stops or restarts. `PartOf=` does not carry a reload, and `systemctl reload nftables` also runs `flush ruleset`. So `ReloadPropagatedFrom=nftables.service` reloads the unit when `nftables` reloads, and its `ExecReload` loads the same file as its `ExecStart`. systemd runs that reload after the reload of `nftables`, because the unit is `After=nftables.service`. Between the two `nft` runs the table is absent for a moment. The unit runs before `network-pre.target` and before `systemd-user-sessions.service`, and `WantedBy=multi-user.target` loads it at every boot. The file makes the table, deletes it and defines it again, and `nft` applies the file as one change, so a restart leaves no moment without the rule. The unit has no `ExecStop` for the same reason. A second run writes a file only when it differs, and restarts the unit only when a file differs or the unit is not active.
- **Checks.** At the end of the root stage, a check fails the script unless the unit is active and enabled, the table is loaded, and root gets HTTP 200 for the token path. At the end of the job stage, a check runs in a user unit and fails the script unless the job user gets no answer on port 80 and port 443, over IPv4 and IPv6. A refused connection and a timeout are no answer. Any other `curl` failure fails the check, because it proves nothing. The job stage also checks again that `sudo -n -l` fails and that `id -nG` shows no group that gives root. The record holds `metadata user=refused sudo=none`.
- **Not held.** The guard holds for the code that runs as the job user. It does not hold for the skill scripts of a round that run as the login user of the admin, for a user who logs in as the admin, for the moment between the flush and the reload of the guard when root reloads `nftables`, for a start of `nftables.service` by root after boot when it did not run (a start runs `flush ruleset`, and the guard unit is already active), for `nft flush ruleset` typed by root, or for a host that uses OS Login: OS Login reads the metadata server as a non-root user, so the guard blocks it. The round hosts use ssh keys from the metadata and do not use OS Login. A service account without the compute scope would also keep the token away. It is not built, because the watchdog then cannot delete the instance.
- **Tools.** `scripts/host-tools.sh system` runs in the root stage. It installs the apt packages, the Go toolchain that `go.mod` names (checked against the sha256 that go.dev lists) and uv. For a new Go, the old Go moves to `/usr/local/go.old`. The script deletes that folder only when the Go in `/usr/local/go` prints the pinned version. If both folders hold a Go and the one in place is not the pinned one, the script stops, names both folders and deletes neither. `scripts/host-tools.sh user` runs in the job stage and installs actionlint into the Go folder of the job user. A host maker with its own idle delete can run `system` alone. `make setup` and `make contract-tools-install` install the other tools at their pins.
- **The PATH of a job.** A job runs with the PATH of the user manager, not the login PATH. The job stage writes `~/.config/environment.d/zz-beehave.conf`, which puts the Go folders on that PATH and sets `BEEHAVE_BD_132`. systemd reads the environment.d files in file name order and the last `PATH` line wins. The name `zz-` sorts after every file with a number prefix, such as the `99-environment.conf` that Ubuntu ships. The stage then reads `systemctl --user show-environment` and fails with the cause if the Go folders are not first. So a gate is plain `make check`.
- **The proof.** The job stage runs each tool once as a transient user unit, the way the adapter runs a job. A tool that does not run, or that is not at the pin in `go.mod`, the `Makefile` or `host-tools.sh`, fails the stage. A tool that `go install` builds shows no version in its own output, so the stage reads the `mod` line of `go version -m` and compares its version with the pin exactly. Then it runs the reads of `Up`: linger, the user manager, the systemd version and the work root.
- **The record.** The job stage writes `<work_root>/.beehave/host-setup.record` and prints it: the time, the commit, the kernel, systemd, the Landlock result, the watchdog state, the metadata check and the version of each tool. A kernel older than 6.7, or one without the Landlock module, is a warning only, because only an agent run on the host needs the sandbox.
- **A second run.** Each stage reads the user, the key, Go, uv, actionlint, linger, the environment file and the work root first, and changes them only when they differ. `apt-get` runs each time and changes no package that is installed. A second run of the job stage gives the same record except the time.

The last line of a pass of the root stage is `HOST-SETUP-ROOT-DONE`. The last line of a pass of the job stage is `HOST-SETUP-DONE`. No other path prints either.

### The SkyPilot provider

`host/skypilot` is the SkyPilot adapter. It runs the `sky` and `ssh` command lines through `proc.Runner`, links no cloud library and opens no listener. The `sky` command starts its own local API server; only a project that selects this provider gets it. A project with no `host` block builds no adapter and needs neither `sky` nor Slurm.

It composes the Slurm adapter: SkyPilot creates and deletes the cluster, and the Slurm queue runs the jobs.

- `Up` is the first call and `Down` the last.
- `Up` runs `sky launch --down` with `host.idle_minutes`. It passes `host.max_run` as the SkyPilot setting for the lifetime of an instance, which SkyPilot documents for GPU instances only.
- `Status` reads `sky status -o json`, then `sinfo` and `df` through the Slurm adapter.
- The Slurm adapter calls the SkyPilot slot: `Hold` after the job is queued, `Release` once the work is banked.
- The `skypilot` provider keeps its own slot and its own run limit (the `timeout` in the slot command).
- The Slurm queue of the `skypilot` provider sends `--time` with `host.max_run`, rounded up to whole minutes, as the `slurm` provider does. This ends the whole gate job at the run limit.
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

#### Proof of a lost job

`LostProof` stops a job whose wait ended as `lost`, and then reads whether the job can no longer run. It never waits for the job, never banks, and is safe to call again.

- `Kind` is empty (no proof), `stopped`, `ended` or `gone`.
- No proof is not an error. A link that is down gives an empty `Kind`, a nil error, and a `Note` that says why.
- An error is a fault that no later call fixes, such as a job id that `Submit` did not return.
- The proof comes from the owner of the fact: `systemctl stop`, `scancel` with `sacct` and `squeue`. A list of machines in a cloud console is never a proof.

| Provider | Proof |
|---|---|
| `ssh` | `stopped`, when `systemctl --user stop` answers: exit 0, or exit 5 for a unit that is not loaded. The call writes the cancel marker first, and it makes the state folder if the machine has none. After the stop it resets the unit, so no failed unit stays. |
| `slurm` | `ended`, when `squeue` or `sacct` names a final state after `scancel`: `succeeded`, `failed`, `canceled` or `timed_out`. A state that is not final is no proof. An empty record (`died`) is no proof, because the accounting record can come late. The exit status of `scancel` is not read. |
| `skypilot` | No proof. The read of the cluster state is issue #401. |

The key `host.lost_job_wait` is the time that the engine waits for a proof. See the host block below.

#### The wait for a proof

When the wait of a project gate ends as `lost`, the gate keeps its host slot and waits for a proof. It submits no second job.

- The step `gate:lost-start` records the time that the wait starts.
- Each look is one step, `gate:lost-look.<k>` from 0. A look calls `LostProof` once, then reads the clock.
- A durable sleep of one minute comes between two looks, so a restart replays the earlier looks.
- Each look reads `host.lost_job_wait` from the config of the engine that runs it. A project with no host block waits for the default.
- A proof ends the wait as a void gate. `passed` is false, every step of the list is `unknown`, and the report says "the gate job was lost on <host> and the proof is <kind>". The field `host_loss` holds the job, the host, the head, the proof and the time of the look. The item run records the loss as the step `<call>/host-loss`, and the field `host_losses` counts the losses of the item tree. See "The host loss fields" in [workflow-files.md](workflow-files.md).
- A look with no proof at or after the limit ends the wait, and the host slot and the exclusive slot are free. See "The owner wait for a lost job" below.
- A look whose read of the proof fails fails the gate with that error.
- If the run is canceled during the wait, the engine calls `LostProof` once more under a context that the cancel does not reach. A call with no proof is logged, because the job can still run. The wait never cancels the job: a cancel waits for the job and banks its work.
- A build with no host reader has no proof, and the wait runs to the limit.

#### The owner wait for a lost job

When the wait for a proof reaches the limit, the item run asks the owner. Only the item run receives an answer, so this wait is never in a child run.

- The run reads `running`, and no step fails. A parent that gathers the run keeps it.
- `beehave status <run id> --json` shows `lost_job` with `job`, `host`, `since` and `limit`. The question names the job, the host and the cancel command of the provider.
- After each answer, the step `<call>/lost-ask.<n>` from 0 calls `LostProof` once. The documented answer is `CONTINUE`, and any answer reads the proof.
- A proof gives the void gate of the wait above, and clears `lost_job`.
- No proof asks the owner again.
- The owner wait starts no gate job. No rerun of the head comes before a proof.
- A read of the proof that fails fails the gate with that error.

### Host config

A project selects a host with `build_host` and the `host` block.

#### The host block

- The `host` block is required when `build_host` is a cluster name, and refused when it is `"local"`.
- `provider` is `skypilot`, `ssh` or `slurm`.
- Each provider declares its own block, and the config reads no key of it. The `skypilot` provider takes the `skypilot` block, `ssh` takes the `ssh` block, and `slurm` takes the `slurm` block.
- A block of another provider is refused, and the refusal names the block and the provider.
- `idle_minutes` is refused for `ssh` and `slurm`, because beehave does not delete a machine it does not own.
- An unknown key or block is refused.
- `lost_job_wait` is a duration with the default `"60m"`, from `"1m"` to `"24h"`. It is the time that the engine waits for a proof that a lost job no longer runs. It is allowed for all three providers. A value outside the range, or text that is not a duration, is refused, and the refusal names the key and the range.
- `lost_after` is a duration above zero with the default `"5m"`. It is the time without an answered read of a job before the wait for the job ends as lost. It is not `lost_job_wait`: that key is the time the engine waits for a proof after the loss. It is allowed for all three providers.
- `stop_limit` is a duration above zero with the default `"5m"`. It is the time that a job may take to end after a cancel. When the job is not final by then, the cancel ends with an error. It is allowed for all three providers.
- A value of `lost_after` or `stop_limit` that is zero, negative or not a duration is refused, and the refusal names the key.
- `slots` is an optional dict from a slot class name to a count, such as `{"mutation": 1, "bench": 2}`. See the next section. The class name `gate` is refused: use `gate_slots`.
- `gate_slots` is the number of gates that the host builds at once. It is a whole number from 1 to 1048576, with the default 1. A value outside the range, or one that is not a whole number, is refused, and the refusal names the key. Two projects that declare one host must give the same value.

#### Slot classes

A slot class limits the host jobs of one kind. The engine runs at most the stated count of jobs of one class on one host at once. It knows a class by its name only. The limit holds for one host. The engine queue limits the jobs of one state folder, which has one `dbos.db`. The job script on the host limits the jobs of every engine, so two engines with separate state folders share the count on the host.

- The job script takes one of `count` lock files with `flock` before the command runs. The files are `slots/<class>/<i>.lock` in the work folder of the host. The kernel frees a lock when the last process that holds it ends, so no cleanup and no lease table exist.
- A job that waits for a lock writes a marker file. The poll reads the marker and reports the job as `pending`, so a waiting job is not stale. The wait counts against `host.max_run`. A stop signal while the job waits removes the marker. A kill that no shell can catch leaves it, and no poll reads it for a job that is not running.
- Every engine that uses a host gives the same count for one class. Equal counts are needed. With different counts the lower count does not hold, because each job takes any free lock of its own count.
- `Up` refuses a host with no `flock` command on `ssh`, `slurm` and `skypilot`, and the refusal says to install `flock`.
- On `slurm` the lock files are in the login folder. A cluster with many nodes and no shared file system that supports `flock` runs the jobs of different nodes with no limit.

- A class name holds letters, digits, dot, underscore and hyphen, and starts with a letter or a digit.
- A count is a whole number from 1 to 1048576. A count below 1, or a value that is not a whole number, is refused, and the refusal names the class.
- A `command` call joins a class with `slots = "<class>"`. See `command` in the workflow file reference.
- The jobs of a class with one count share one durable queue, named `slots:<class>:<count>`, with one partition for each host. A job of another class, and a job of no class, never waits for it. Two hosts with different counts for one class use two queues, so each host keeps its own count.
- The engine records the count in the input of the job when it queues the job. A rewind of the job reads the queue from that input.
- A change of a count in the `host` block takes effect for the next job that queues. A job that is already queued stays on the queue with its old name and still runs, because the queues live in the database. For a short time a host can run the old count plus the new count at once.
- A job that was queued by a build before the count was recorded has no count in its input. A resume cannot put it back in a queue, and it fails with an error that names the run, the step and the job. Run the item again from its start. Before an upgrade, let the slot queues drain.
- A call names a class that the `host` block of its project lists. Any other class is refused when the workflow calls it, and the refusal names the class and the host.
- Projects share a host by name. If two projects declare the same host and give different counts for one class, the config load is refused, and the refusal names the host, the class, both projects and both counts. Equal counts are fine.
- A call with no `slots` has no limit from the engine. A gate uses the reserved class `gate` with the count `gate_slots`, on the queue `slots:gate:<count>`.
- A gate that was queued before an upgrade waits on the old queue `hosts`. That queue is gone. Let the gates drain before an upgrade, or run the item again from its start.

`cmd/beehave/adapters.go` picks the provider from its host block by `provider`, like the tracker and the forge. `beehave check config` builds the host of each project.

| Block | Keys |
|---|---|
| `skypilot` | `cloud`, `region` (needs `cloud`), `instance_type`. The cluster name is `build_host`. |
| `slurm` | The keys of the `ssh` block. `work_root` is refused, because jobs run in the login folder. `build_host` is the ssh alias of the head node. |
| `ssh` | `address`, `user`, `port`, `identity`, `work_root`, `known_hosts` (`strict`, the default, or `accept-new`; a host key that changes is always refused). `build_host` is the ssh alias, so the ssh config of the user applies. The alias holds a letter, then letters, digits, dots and hyphens, with no trailing dot, no `..` and no `.-`, so a gcloud alias such as `rig.us-central1-a.bench` is allowed. |

#### The build_host key

The key `build_host` is the one declaration of the hosts of a project: a name, or a list of names that share the `host` block. The first name is the default. The gate key holds the whole list, so a pass on one host is reused on the others. Every host in one list must therefore match in architecture and toolchain.

- A run chooses another with the `host` argument of `host_up`, and the step records the cluster. A call of `host_up` with no name records the default host.
- A run that never calls `host_up` has its gates placed: each gate goes to the declared host that is up and has the most free gate slots, and the first declared host wins a tie (see the gate design, `gate:place`). To keep the old behaviour, declare one host or call `host_up`.
- The gate, its key, each `command` call with a `host`, and `host_teardown` read that record, so a resume uses the same cluster.
- A resume reads the cluster from the replayed record of `host_up` for every later step.
- `"local"` is the only name of a project that builds here. `host_up(host = "local")` starts nothing there and records `local`, and a cluster name is refused.
- If the project file no longer declares the recorded cluster, the gate, the command and `host_teardown` refuse it with an error that lists the declared hosts.
- A host that `host_up` recorded never changes for the run. Only a run that recorded none has its gates placed.
- The placement lock covers only the gates that are placed. A gate on a host that `host_up` recorded and a `command` job queue into the same host partition without the lock, so a free slot that a placed gate read can be taken before its job queues.
