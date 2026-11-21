# Workflow files

Part of the [beehave architecture](../architecture.md).

A workflow is one Starlark file. The engine calls its `main` function with the work item, and each builtin call except `struct`, `json`, `paths_outside` and `closing_references` runs as a recorded DBOS step, so a restart replays the file and never runs a finished step again. This part lists the builtins, their arguments and results, the pinning of files, the static check and the examples.

## Workflow files

### The file

- One Starlark file (`workflow.star`) is one workflow.
- The file defines `def main(item):`. The engine calls it with the work item.
- Control flow is plain Starlark: `if`, `for` and `def`. The dialect has no `while`, no recursion and no top-level control flow, so a run always ends.
- A run stops after one million instructions.
- A method value, such as a prompt or a model name, is a value in the file and never a Go constant.

### `finish`

The file can also define `def finish(item):`.

- The engine calls `finish` after `main` on every end of the run except a cancel: success, error and a command stop.
- `finish` runs for an item that a user started, not for an item that `spawn` started.
- If `main` fails and `finish` fails too, the run reports both errors, with the error of `main` first.
- `beehave check workflow` refuses a `finish` that does not take one parameter.

### Builtins

The engine predeclares `agent`, `cancel`, `command`, `earlier_calls`, `env`, `facts`, `gate`, `gather`, `host_teardown`, `host_up`, `item_comment`, `item_create`, `item_read`, `item_status`, `merge`, `now`, `pr_comment`, `pr_history`, `pr_open`, `pr_state`, `preflight`, `progress`, `publish`, `push`, `queue`, `recv`, `sandbox`, `send`, `sleep`, `spawn`, `wait`, `write`, `struct` and `json`. `workflow.Builtins` is the one list of these names.

`json` is the `json` module of the Starlark library (`json.decode`, `json.encode`, `json.indent`). It is pure, so it records no step.

#### Recorded steps and replay

- Each builtin except `struct` and `json` takes keyword arguments, runs as a recorded DBOS step, and returns a struct of its outputs.
- The step is recorded under the name `<builtin>:<name>#<n>`. `name` is the argument of the call. `n` counts the calls of that builtin, whatever their names, so a call in a loop gets a new record in each round.
- Every `wait` call also records a step under this name, after the work of the call.
- A restart replays `main` from the top. Each call finds its record and returns the recorded outputs, so a finished step never runs again. The same code makes the same calls in the same order.
- A step that was running when the engine stopped has no record, so it runs again on resume. The next subsection says what that means for each builtin.

#### A step that ran when the engine stopped

DBOS records a step only when the step ends. If the engine stops after the effect of a step and before its record, the resume runs the step again. A step is safe to run twice when the tracker, the forge or the host answers the repeat. Four bases make a step safe, and each row names the one it uses.

1. Check first. The step reads the state and sends no second write.
2. A cited rule. The call is documented to treat a repeat as no change, and a test pins the exact call.
3. The same value. A repeat sets the value that is set already, and no page states that this is a no-op. The status and comment writes of the `local` tracker use this basis.
4. The library. DBOS replays the step and not beehave: `spawn`, `wait`, `sleep`, `send` and `recv`.

Four cases are not safe today, and a later change fixes each of them.

| Builtin | A repeat after a crash | Why |
| --- | --- | --- |
| `item_create` | Safe. It files no second item, and `created` is false. | The tracker finds the item by the key, which is the run id plus the `key` of the call. With `shared = True` the key is the `key` of the call alone. |
| `item_comment` | Safe. | GitHub and beads read the comments first and find the key. The `local` tracker inserts the comment under its key and ignores a second insert of that key. |
| `item_status` | Safe. | GitHub and beads read the state and the labels first and run only the commands that change them. The `local` tracker sets the state, and the same state again changes nothing. |
| `pr_comment` | Safe. | The forge reads the comments of the pull request first and finds the key. |
| `pr_open` | Safe. It opens no second pull request, and `created` is false. | The forge looks for an open pull request on the branch first. |
| `push` | Safe. | Git exits 0 when the branch is already at the head, and checks no lease then. A replay of a push that landed passes. |
| `merge`: each member | Safe. | The step `batch:<name>.intent#<item>` is recorded before the merge of the member. The forge shows the pull request as merged at the gated head, so the step counts it as done. |
| `spawn` | Safe. It rests on the library. | The child run has a fixed workflow id, and DBOS returns the recorded child for it. A test stops the parent while it waits in `gather`, after `spawn` returned, and shows one child run with the same id after the resume. No test reaches the window inside the start call, between the child row and the record of it in the parent. |
| `publish` | Safe. | The event writes and the list of keys are one recorded step, and a repeat sets the same value again. |
| `send` | Safe. It rests on the library. | The send carries an idempotency key made of the run and the step name, and the store inserts one message for a key. |
| `recv` | Safe. It rests on the library. | The deadline is a recorded step, and the receive is a durable DBOS step. A resume receives the same message. |
| `wait` | Safe. It rests on the library, and no test in this table stops a wait. | The deadline is the output of a recorded DBOS sleep, and the answer is a durable receive. A resume waits for the same answer. |
| `sleep` | Safe. It rests on the library. | The wake time is the record of a DBOS sleep. A test stops the engine in the middle of a sleep, and the resumed run sleeps only the time left. |
| `write` with `read_back` | Safe. | The read-back runs first and finds a write that is done. |
| `write` with no `read_back` | Not safe today. The program runs again with the same key. | Neither beehave nor the forge can tell if the outside system dropped the repeat. A later change asks the owner before the repeat. |
| `write` undo | The undo is a recorded step named `undo:<write step>`. A crash during the undos runs the one that has no record again with the same key. | The undo must be safe when nothing happened (see `undo`). A repeat after the effect depends on the program. |
| `command` with no `host`, and `gate` with a program on this machine | Runs again. | By design. Put an outside effect in `write`. |
| `sandbox` | Safe. | The call builds a setting value and changes nothing outside the run. |
| `gate` with a host job, `command` with `host` or `slots`, and the gate of `merge` | Not safe today. A crash after the host accepts the job and before the record submits a second job. | The job has no key that the host honors. A later change adds one. |
| `host_up` | Not safe today. If the crash came after the launch, the repeat finds the host up and reports `started` false, so `host_teardown` does not stop it. | The step cannot tell its own launch from a host that was up already. A later change adds a start mark. |
| `host_teardown` on SkyPilot | Not safe today. If the crash came after the delete, the repeat cannot read the work root of the deleted cluster and reports `down` false with a reason. | The step reads the host before it checks that the host exists. A later change makes a missing host count as down. |
| `host_teardown` on ssh and Slurm | Safe. | The step banks the work and leaves the machine up. The bank reads first and pushes only what is not banked, so a repeat pushes nothing. A test runs the bank once, and none runs it twice. |
| `agent` | The step is a lost step. It resumes the session or runs again with a note. | `durable-loop.md` describes the start mark of a phase. |

The reads (`item_read`, `facts`, `now`, `env`, `queue`, `preflight`, `earlier_calls`, `gather`, `progress`) change nothing outside the run, so a repeat is safe.

#### The folder of a program

- A `command` call, and a `gate` call that names a program, run in the worktree of the item.
- A round item (`round/<base>`) always has a worktree. The engine makes it after the claim and before `main` runs. The worktree is detached at the tip of `origin/<base>`, and every step of the item uses this one folder. If the remote has no branch `<base>`, the item stops and the error names the base. The worktree is made in a recorded step, which stores the commit that the worktree starts at. A resumed run does not make it again and keeps that commit, even when the base moved. A fresh run that finds the worktree that an earlier run kept moves it to the tip of `origin/<base>` when it is clean. If the kept worktree has tracked or untracked changes, or commits that no remote branch holds, the fresh run stops, and the error names the worktree, those commits and the `git worktree remove --force` command that removes it. The commit that the engine placed the worktree at, and its history, are not work of the item: a force-push of the base alone does not stop the fresh run. Nothing else moves the worktree. If someone removed the worktree, the next step of the item that uses the folder makes it again, detached at the stored commit, never at the newer tip of the base. The commits of earlier steps went with the removed worktree. If the clone no longer holds the stored commit, the step stops with an error that says to start the item again. Two steps that find it removed at the same time make it once. A worktree made again holds no start mark of a lost agent step, so the note to the agent says that the worktree is not the one that the step started in, and claims no change: the work of the step went with the removed worktree. A run of a round item that an older build started does not move to this build with `resume --on-this-build`. A step that needs the newest tip of the base runs `git fetch` itself. A round item of a project with no repository gets no worktree: its programs run in a new empty folder, as below.
- An item with no worktree, such as the parent of the swarm example, gets a new empty folder in the temporary folder of the system. Beehave removes it when the program ends, so the program never reads or writes the folder where `beehave` started. Such a program reads a repository by its address (`git ls-remote <url>`) or through `gh`.
- A `gate` call with no program, and an `agent` call, need a worktree and refuse an item that has none.

### Item fields

`item.<field>` reads a field of the work item, such as `item.id`, `item.title`, `item.body` or `item.severity`. The severity is `P0` to `P3`, or `P?` when the tracker keeps none.

### `env`

`env("<NAME>")` reads a variable of the engine environment. The value is recorded, so a replay sees the value of the first run.

- A name that is not set fails the step and names the variable, unless the call gives `default`, which is then the value.
- `env` refuses a name that starts with `DBOS__` or `BEEHAVE_`, a name that ends in `_KEY`, and a name that holds `TOKEN`, `SECRET`, `PASSWORD` or `CREDENTIAL`.

### `now`

`now()` takes no argument and returns the wall clock as RFC 3339 text in UTC, with whole seconds and no fraction, such as `2030-01-02T03:04:05Z`.

- In a dry run, `now()` always returns `2030-01-02T03:04:05Z`, so two dry runs of one workflow print the same text.
- It runs as a recorded step named `now#<n>`, so a replay after a crash returns the first reading and never reads the clock again.
- A workflow has no other clock. A script that needs the time uses `now()`, and passes the text to a command if it must compute with it.
- `beehave check workflow` refuses a keyword argument to `now`, and the run refuses any argument.

### `sleep`

`sleep(duration, name="")` pauses the run for the duration and returns a struct with one field, `slept`: the seconds that this execution slept.

- `duration` is required text, such as `"90s"` or `"10m"`. It must be above zero. `beehave check workflow` and the run both refuse zero, a negative value, text that is no duration, and a missing duration.
- The sleep is the durable sleep of the workflow library. The library records the wake time. A run that stops during the sleep and continues later sleeps only the time left, and a wake time in the past ends the sleep at once.
- The call records the steps `sleep:<name>#<n>/start` (a marker with no value) and `sleep:<name>#<n>`, with the library record `DBOS.sleep` between them. The marker tells the call from the other sleeps of the engine. `beehave status` shows `sleeps until <time>` for a run that is in the sleep.
- A cancel of the run ends the sleep, as it ends a wait.
- A dry run sleeps the real duration, as a dry run of `wait` waits its real timeout. Use a short duration in a file that you check with `--dry-run`.
- `sleep` records `slept` once, so a replay returns the first value. After a stop in the middle of the sleep, `slept` is the time that the second execution slept.

### `facts`

`facts()` takes no argument and returns the facts of the project of the run as a struct.

| Field | Kind |
| --- | --- |
| `repo`, `source`, `forge`, `tracker`, `base_branch`, `gate`, `gate_steps`, `build_host` | The project value of that name. |
| `vars` | Dict. The `vars` of the project file, as written, and frozen. Every setting of a workflow is a key of it. |
| `runners` | Dict. One entry for each runner of the config, by its name, with the field `loads_skills`: true when the agent CLI has a line that loads a skill before the first turn, false for OpenCode. The project file does not set it. A workflow reads it to refuse a step before any agent starts, for example a step with the `skill` keyword. |

- A fact that the project file leaves out is empty text, an empty list or an empty dict, never a missing field. A key that `vars` leaves out is missing: read it with `facts().vars.get("key", default)`.
- The call is a recorded step named `facts#<n>`, so a replay returns the facts the run started with and never reads the config again.
- A workflow passes the struct in the `inputs` of an agent call, and the prompt reads a field as `{{ .facts.<field> }}` and a setting of `vars` as `{{ index .facts.vars "<key>" }}`. The `index` form gives nothing, and no error, for a key that `vars` leaves out. The agent reads no file for it.
- A prompt declares a skill with the template function `skill`, as `{{ skill "name" }}` or `{{ skill .facts.vars.review_skill }}`. The function renders the plain name. The step gets the skills that its prompt declares, with a sandbox or without one. A sandboxed step sees only those skills. An unsandboxed step also sees the skills of the settings of its runner (see [the skills of a step](sandbox.md#the-skills-of-a-step)). If the runner lacks a declared skill, the step stops before the agent starts. The dry run renders the prompt, so it refuses a name that is no folder name. `beehave check workflow` without `--dry-run` reads no prompt.
- In a dry run the facts are empty, except `vars`. `beehave check workflow --project <name> --dry-run` gives `vars` the value of that project. Without `--project`, `vars` is an empty dict.
- `beehave check workflow` refuses a field that `facts()` does not return.

### Loading files and pinning

A file loads another file with `load("//file.star", "name")` for a file in its own folder, or `load("@beehave//review_fix.star", "review_fix")` for a library in the binary. The library `review_fix.star` holds the review and fix loop of the remediation method as a function. A project that wants another policy copies it into its folder. The function names no model: the argument `steps` gives the model and the runner of each step, and the arguments `design_after`, `void_reruns`, `void_asks` and `escalate_after` give its limits. The remediation example reads them from `vars.round` (see "The steps and the limits" in `durable-loop.md`).

At the start of a run, the engine pins the SHA-256 of the workflow file and of every file it loads.

- A run continues with its pinned path. `beehave resume` takes no file, and `beehave run` ignores `--workflow` for an open run.
- Each file must still have its pinned hash. A changed file refuses the run. To go on, restore the file, or cancel the run and start a new one.
- A different workflow file never applies to a run that is open.

#### Pinned scripts

A `command` call pins its script like a loaded file when all of these hold: the `run` list is a literal list, the call has no `host`, and the first word is a path to a file in the repository of the workflow file.

- The repository is the nearest folder with a `.git` entry at or above the workflow folder.
- A relative path resolves against the root of that repository, not against the work tree or the folder where the command runs.
- A word with no `/`, such as `git`, names a program on the path and is not pinned.
- A script outside the repository is not pinned, and a script on a build host is not pinned.
- A `command` call whose `run` list is not a literal list of texts is not pinned either, and the refusal counts such calls.
- A changed script refuses the run in the same way as a changed workflow file.

#### Resume and pinned files

A run that `beehave resume` reopens restarts under its pinned bytes only. `resume` reads no workflow file, and it reopens the run even when the file changed. The replay then compares the hash of each pinned file, and ends the run as failed with the name of the file that changed. Restore the file and run `beehave resume` again.

### Tracker builtins

The `item_` builtins change or read the tracker of the project.

| Call | Returns |
| --- | --- |
| `item_create(title, key, body, parent, assignee, severity, operator, forbid_text, shared, name)` | `id`, `created` |
| `item_status(id, status, name)` | `id`, `status` |
| `item_comment(id, text, key, forbid_text, name)` | `id` |
| `item_read(of, name)` | `found`, `id`, `fields`, `comments`, `parent` |

The `key` is required, and the engine joins it to the run id. `item_create` with `shared = True` is the one exception.

#### `item_create`

- When a resume follows a lost step record, `item_create` returns `created` false and the id of the first item.
- `assignee` is optional. The default is no assignee. The `local` tracker keeps no assignee and refuses the keyword. GitHub files the issue with that assignee. beads files the item with that assignee.
- `severity` is optional, one of `P0` to `P3`. The call refuses any other text before it writes.
- `operator` is an optional bool.
- `shared` is an optional bool. The default is false: the engine joins the `key` to the run id, so two runs never share an item. With `shared = True` the engine uses the `key` as it is, and it holds a file lock of the key in the state folder around the lookup and the create, so two runs of the project on this host that give the same key at the same time get one item. The lock does not reach another host: two hosts that share one tracker can still file two items for one key. Put in the key what makes the item unique in the project, such as the repository and a pull request number.

| Tracker | `severity` | `operator` |
| --- | --- | --- |
| GitHub | Filed as the label of that name. The adapter creates the label when the repository has none. | Files the label `operator`. The adapter creates the label when the repository has none. |
| beads | Filed as the priority. | Files the label `operator`. |
| `local` | Refused, because it keeps no severity. | Refused, because it keeps no labels. |

#### `item_status`

A status is `open`, `closed` or `hold`. On GitHub, `hold` is the label `hold`. The adapter creates the label in the repository when it does not exist.

#### `item_read`

`item_read(of, name)` reads an item.

- `of` is `"self"` (the default, the item of the run) or `"parent"`.
- `fields` is a dict of the fields that the tracker declares for the item: `id`, `title`, `body` and `severity`.
- `comments` is the list of the comment texts, oldest first, with no `beehave-key:` line.
- `parent` is the id of the parent of the item read, or an empty text.
- With `of = "parent"` and an item that has no parent, `found` is false and the other values are empty.
- A round item has no tracker issue, so `found` is false and the other values are empty, with no tracker read.
- The read is a recorded step, so a replay returns what the first start read and does not read the tracker again.
- The comments and the parent come from the tracker through the optional reader contract. The `local` tracker and beads serve both. GitHub serves both through the REST endpoints for comments and for the parent issue; an issue with no parent gives an empty `parent`.
- A tracker with no reader fails the call with an error that names the tracker.
- In a dry run an item has no comments and no parent.

### Pull request builtins

`pr_comment` and `pr_open` refuse a project with no repository. `pr_comment` fails for an item with no pull request. In a dry run they act on the dry-run pull request and write nothing.

#### `pr_comment`

`pr_comment(text, key, forbid_text, integration)` adds a comment to the pull request of the item and returns `number`. The forge reads the comments first, and posts only when none holds the line `beehave-key: <run id>:<key>`. A replay after a crash therefore posts once.

#### `pr_open`

`pr_open(title, base, body, issue, parent, forbid_text, integration, name)` opens the draft pull request of the item and returns `number`, `created` and `stack`.

- The branch of the item must be on the remote already.
- The forge looks for an open pull request on that branch first, and opens one only when there is none. A resume after a lost step record therefore opens no second pull request, and `created` is then false.
- The step needs a forge that implements `forge.Opener` (`CreatePR`, `LinkStack`). With a forge that does not, it fails with an error that says so.
- In a dry run it shows the dry-run pull request and writes nothing.

The body is `body` plus one closing line that the engine picks from the base:

| Case | Closing line |
| --- | --- |
| The base is the default branch. | `Closes #N` |
| Any other base. | `Addresses #N` |
| `issue` is 0 or left out. | No line. |

`body` is used only when the call opens the pull request. A pull request that exists already keeps its body, and the call owns only the closing line. It corrects a line for the issue that has the wrong keyword in place, adds the line at the end when the body has none, keeps every other line, and writes only when the body changes.

The `parent` argument:

| Case | Result |
| --- | --- |
| The pull request of `parent` is open. | The new pull request is based on the branch of the parent and linked into its native stack with `gh stack link` (the pull requests go by address, on github.com). The first link of a stack passes `--base` with the base of the call. The call reads the base of the parent pull request before and after every link. If the base moved on a new stack, the call unstacks, checks that the stack is gone, sets the base back and links again with that base. A second move, a stack that stays after the unstack, or a move on a stack that already held other pull requests fails the step. The call then reads the stack id of both pull requests, and fails unless they show one non-empty id. `stack` holds the id. |
| The pull request of `parent` merged. | The base stays as given, and the call links nothing. |
| `parent` has no pull request. | An error. |

#### `pr_state`

`pr_state(pr, name)` reads the state of the pull request with the number `pr` and returns `state`: `OPEN`, `CLOSED` or `MERGED`.

- The call reads that number. It looks no pull request up by the branch of the item, so a pull request of another author on any branch and any base reads the same.
- A `pr` below 1 is refused before any read. So is a project with no repository.
- The read is a recorded step, so a resumed run replays the first state.
- A read that fails ends the step with an error. A project with no forge cannot show a state, which is an error too.
- A build with no queue reader fails the call. A dry run does not fake the read: it reports the step as not run and ends there.

#### `push`

`push(branch, head, expect, name)` pushes the commit `head` of the clone of the project to `branch` on the remote and returns `head`. The engine process runs the push, so an agent step in a sandbox needs no credential: the workflow calls `push` after each agent step that commits.

- The push runs no setting that an agent can write. An agent whose worktree shares the clone can write the git config of the clone, so the push never runs in the clone. It runs in the push repository of the project, a bare repository that the engine makes on the first push and keeps, at `push/<project>.git` in an owner-only folder of the state folder. That repository has no template and no hooks.
- The push carries only objects of the clone folder. The engine copies `head` into the push repository with `git fetch --upload-pack`, and git runs `beehave` as upload-pack. That program runs `git upload-pack` under the sandbox enforcer of the project (`sandbox_enforcer`). It reads the clone folder and what git needs to start, and nothing else: the git program, its exec path (`git --exec-path`) and the libraries that it loads, each with its links resolved. On macOS the libraries come from the load commands of the program, and the frameworks and the shared cache of the system are read too. On Linux the dynamic linker that git names lists them (`--list`, what `ldd` runs), by its path, so no search path plays a part; the file in which the linker finds its library folders (`/etc/ld.so.cache` for glibc, `/etc/ld-musl-<arch>.path` for musl) is read too. A linker named by a link is read as its file, and one named by a relative path is refused. A static git reads no library. No system folder such as `/usr`, `/etc` or `/opt` is read whole. The copy writes the null device only, reads no system git config, and opens no network. A head that needs an object from outside the clone folder, through a list of other object stores, a link, or a git folder that the agent replaced, fails the copy, and nothing is pushed. The copy runs under the lock of the clone, and a later push fetches only the new objects.
- When no sandbox enforcer can run on the machine, or the read set of git cannot be found, every push is refused, and the error names the reason. On macOS, the git that the push finds must not be the Apple shim in `/usr/bin`, which the enforcer refuses, and it must load no library by a name relative to a program (`@rpath`).
- The remote is the address that the engine read: the `source` of the project, or else the clone address of its forge. It is never a remote name, so no remote, push address, address rewrite, ssh command or credential helper in the clone config applies.
- Every other git call that reaches the remote runs the same way, in the push repository and to the same address. The refresh of a worktree fetches the base there. The tip of a batch fetches every branch there, and the tags that point into them. Each then copies what it fetched into the clone. The release reads the branches of the remote there before it removes a worktree.
- Every other git call of the engine in the clone or a worktree is local: it allows the file transport only (`GIT_ALLOW_PROTOCOL=file`) and fetches no missing object from a promisor remote (`GIT_NO_LAZY_FETCH=1`). Git 2.45 and later and the 2024-05 security releases from 2.39.4 on honor the second variable. No git config overrides either, so no remote, transport or promisor in the clone config can make a local call fetch or push. Only the first clone and the calls in the push repository reach the remote.
- Every git call of the engine in the clone or a worktree names its repository from the engine layout, never from the `.git` file of the worktree or the `commondir` file of its record, and runs only when every key of the clone config is on the allow list. The release removes a worktree from the clone whose records hold it. The rules are in docs/design/sandbox.md, The git of the engine.
- On an unpatched git older than 2.45, a file promisor in the clone config still fetches lazily, so an upload-pack program that the clone config names can run as the owner (the class of #727); no promisor reaches the network.
- The push runs `git push --no-verify --force-with-lease=refs/heads/<branch>:<expect> <remote> <head>:refs/heads/<branch>` with the hooks off and no terminal prompt. With an empty `expect`, the push has no lease and git takes only a new branch or a fast forward.
- The run must hold a trusted claim on the item. A run that lost its claim pushes nothing and stops with the loss, and a loss during the push stops the push.
- A branch already at `head` is no error. A replay after a crash between the push and its step record therefore passes, whatever its lease. A resume after the step record reads the record and pushes nothing.
- A branch that moved since the lease is refused with an error that names the branch, and the item stops.
- An empty `branch` or `head` is refused before the push. So is a project with no clone, and a project with no forge, which pushes nothing.
- In a dry run the push writes nothing and returns `head`.

#### `pr_history`

`pr_history(name)` reads the pull requests of the item of the run in every state and returns `pulls`. Each has `number`, `title`, `state`, `branch`, `base`, `merged_at` and `closing_comment`.

- The forge lists the pull requests on the branch of the item. For an item that is an issue number, it also lists the pull requests whose title or body names `#N`, and keeps those that name it as a whole word. The two lists join by number.
- `closing_comment` is the body of the last comment made at or before the close of a closed or merged pull request. A comment after the close does not count. It is empty for an open one and for one with no comment until its close.
- A closed or merged pull request with no valid close time, or a comment with no valid time, fails the read: the call does not guess which comment explains the close.
- A list that reaches its limit is refused, so the call never returns a cut list. A project with no forge has none. A project with no repository is refused.
- The read is a recorded step, so a resumed run replays the first list.
- A read that fails ends the run with an error: no later step works from a guess.
- A dry run shows an item with no pull request.

A sandboxed agent holds no forge tool and no credential. The workflow passes `pulls` to the agent through the `data` keyword, and the prompt reads the data file.

#### Closing keywords in posted text

`pr_comment` and `pr_open` refuse text with a closing keyword (`Closes`, `Fixes`, `Resolves` and their forms, with an issue). The check reads the text of the comment, and the title and the body of the pull request. It uses the same keywords as the guard of `merge`. The refusal happens before the post and names the argument and the issue.

Only the integration pull request may close an issue. A call for that pull request sets `integration = True`. The line that `pr_open` adds from `issue` is not part of the text and is not checked.

### `forbid_text` on public text

The four steps that post public text (`item_create`, `item_comment`, `pr_comment` and `pr_open`) take an optional `forbid_text`, a list of RE2 patterns. It has the same meaning as the `forbid_text` of `merge`.

- The call tests each line of the text it posts with the matcher of the merge: the title and the body of `item_create` and `pr_open`, and the text of a comment. A line that matches refuses the step before the post.
- The match is per line, as in `merge`, so a pattern such as `^$` matches any blank line.
- The refusal names the pattern and the argument, never the text. The pattern lands in the run error and in local logs, so a pattern must not be a secret.
- Each call sets its own patterns, so core holds no word list. A call that leaves `forbid_text` out posts without a scan.
- `beehave check workflow` refuses a `forbid_text` literal that is not a list of RE2 strings, on these four steps and on `merge`.
- The setting changes no recorded output, so the version of the step records stays the same.

### Build host builtins

#### `host_up`

`host_up(name, host)` launches the build host of the project when it is absent. It returns `started`, true only when this call launched it, and `host`, the cluster that the call used.

- The optional `host` names one of the hosts that `build_host` of the project declares. Any other name is refused before a host is built. An empty `host` gives the first declared host.
- The step records the cluster, so a replay returns the first choice.
- A gate, its key and a `command` call with a `host` run on that recorded cluster. A run that did not call `host_up` uses the first declared host.
- A project with no host, and a build with no host keeper, start nothing and give `started` false.
- The start is a recorded step, so a replay of a recorded start does not launch again. A crash after the launch and before the record is not covered today: the repeat finds the host up and gives `started` false, so `host_teardown` does not stop it. A crash during the launch fails the repeat, because the host is in a launching state. A later change fixes both cases.
- The example calls `host_up` first in the round, with the `build_host` of the project file.

`host_up` reads the host first:

| Host state | Result |
| --- | --- |
| Up | Left alone. |
| Absent | Launched with `host.idle_minutes` and `host.max_run`. |
| A failed read, a failed launch, or any other state (draining, stopped, unknown) | The step ends with an error that names the state. |

#### `host_teardown`

`host_teardown(name)` stops the build host of the project after it banks the work on it, and returns `down` and `reason`.

- The call stops the cluster that `host_up` recorded, unless its own optional `host` names another.
- It stops only a host that a `host_up` of the same run launched. Otherwise it gives `down` false and a reason, and stops nothing.
- A host that cannot bank its work stays up: `down` is false and `reason` says why, so the workflow can report it.
- A project with no host block, or a provider with no teardown, also gives `down` false with the reason.
- A build with no host stopper fails the call.
- A stop deletes the host, so use `host_teardown` only for one item on the host.
- Two runs that start together can both report `started`, and then both stop the host. Teardown for a shared host is not done.
- On SkyPilot, a crash after the delete and before the record is not covered today: the repeat cannot read the work root of the deleted cluster, and it gives `down` false with a reason. A later change makes a missing host count as down. On ssh and Slurm the repeat is safe.
- The example calls it in `finish`.

### `preflight`

`preflight(head, base, hold, own, id, name)` reads the pull request of the item `id`, or of the item of the run when `id` is left out. It returns `ok`, `stop`, `code` and `reason`.

It checks, in this order:

1. Merged or closed.
2. The head branch.
3. The assignees. A login that is not in `own["logins"]` and holds the pull request, or an issue that the pull request body links, gives ASSIGNED, exit code 11. An empty `own` checks no assignee.
4. The author. An open pull request by a login that is not in `own["logins"]` gives CLAIMED, exit code 10. An empty `own` checks no author.
5. The `hold` label.
6. A draft.
7. The expected head.
8. The expected base.

The assignee check reads the pull request and each linked issue once. A link is a `Closes`, `Fixes`, `Resolves`, `Part of`, `Refs` or `Related` keyword with `#N`, `owner/repo#N` or the URL of an issue. The call reads a link to another repository on that repository.

- A read that fails ends the step with an error. A failed read is not a clean item.
- The read of the assignees of an issue is optional for a forge. A forge without it is refused when the body has a link.
- The reason of ASSIGNED names the holder and ends with `knows=yes` or `knows=no`. The value is `yes` only when the holder opened the pull request or reviews it.
- The reason also gives the recovery: leave the item to the holder, or ask the holder first.

- A keyword that the call leaves out checks nothing of its kind. An item with no pull request passes.
- A stop is a result and not an error, so the workflow decides what to do with it. `code` is the exit code of the stop.
- A stop also goes into the `stopped` field of the result of the run (step, exit code and reason).
- A read that fails ends the step with an error. A build with no preflight reader fails the call.
- The verdict is a recorded step, so a resumed run replays the first verdict and does not read the forge again.
- When a branch has many closed pull requests, the call reads the newest one.

#### Mentions in other pull requests

If no open pull request has the item as its head branch, the call searches the open pull requests for one that names the item in its title or body.

- Any `#N` mention in an open pull request counts, as in "Closes #7", "see #7" or "follow-up to #7". A passing mention makes the item not free, which is the safe direction.
- Such a pull request gives CLAIMED, and it outranks a merged or closed pull request on the branch.
- A search that fails ends the step with an error.

#### Other orchestrators

At the start of a run, the engine lists the other orchestrators that hold items in the claim store of the project. `beehave status` shows them for an open run. A store that cannot be read ends the run, because a failed read is not "nobody else runs".

### `before_review`

`before_review` is not a builtin. It is a function of the library: `load("@beehave//review_fix.star", "before_review")`. `gate` judges the code of a head. `before_review` judges the head against the base, with the result of `gate`.

`before_review(check, base, allow = None, name = "before-review")` returns `struct(ok, findings, unrepairable)`. `unrepairable` lists the findings that no new commit repairs: the closing keywords. `check` is the result of a `gate(base = ...)` call. The function reads `check.new_tests`, `check.checks` (the new tests and the secret scan) and `check.revert` from it. A gate without `base` holds no checks, so the function refuses it as a cut log. `base` is a git ref.

The function reads `facts()` first. If the project has no repository (no `source` and no `forge` in the project file), it stops the item before any step, with a message that names the keys to set. It is a refusal of the call, not a finding.

`allow` is a list of globs in the dialect of `allow_paths` of `agent`. With no `allow`, every path is allowed. An empty list, an empty entry, an absolute path, a path with a `..` part and a pattern that does not parse stop the item before any step, with a message that names the pattern.

The function runs git in the item folder with three `command` steps: `<name>-ancestor`, `<name>-paths` (only when `allow` is set) and `<name>-keywords`. The step `<name>-paths` lists the changed paths, and the builtin `paths_outside` matches them. The step `<name>-keywords` lists the commit messages, and the builtin `closing_references` reads them. A git command that fails with an exit code other than the ones that it documents stops the item with an error. It is never a finding.

A finding is a result, not an error. `ok` is true when `findings` is empty. The function gives a finding when:

- the log of the gate job has no end line (a cut log),
- the head adds no new test,
- a new test failed or was skipped on the head,
- a new test passed or was skipped on the base code, or the revert run proves nothing (see the revert run in `gate.md`),
- the secret scan did not exit with 0, or left no single exit code,
- the head is not a descendant of `base`, by `git merge-base --is-ancestor`,
- the head changed a path that `allow` does not allow,
- a commit message between `base` and `head` has a closing keyword. The grammar is the one of the merge guard, so a number on the next line counts. The finding names the commit and the issue, and it is unrepairable: `review_fix` stops the item at once on it, and spends no fix round.

The steps are `command` steps, so a replay reads their records and runs no git again. A run recorded with the builtin step `before_review` does not move to this build with `resume --on-this-build`: the gate module is at version 6, and the refusal names it.

#### `paths_outside` and `closing_references`

`paths_outside(allow, paths)` returns the sorted paths in `paths` that no glob of `allow` matches. It reads `allow` with the same code as `allow_paths` of `agent`, so the two judge a path alike. Unlike `allow_paths`, `allow` must be a list: `None` is refused, because a check with no scope has nothing to compare.

`closing_references(text)` returns the issues that `text` closes with a closing keyword, each as `#N` or `owner/repo#N`, in the order of the text. It reads the grammar of the merge guard.

Both builtins match text. They run no command, record no step and need no repository.

### `queue`

`queue(name, hold, label)` reads the open items of the tracker and the open pull requests of the forge in one recorded step. It returns `rows`, `free` and `pulls`.

`label` keeps the items that carry that tracker label. A call with no `label` keeps every open item. The engine reads no project setting for the choice: the workflow passes it. A workflow that needs a list of item ids keeps the rows whose `item` is in the list.

Each row has `item`, `state`, `severity`, `pr`, `parent`, `author`, `changes_requested` and `labels`, the tracker labels of the item. The state:

| State | When |
| --- | --- |
| `running` | The item has an open run. |
| `failed` | Its last attempt ended without success. |
| `done` | Its last run succeeded, and no other row applies. |
| `hold` | The tracker holds the item, or its pull request has the label that `hold` names. |
| `ready` | Otherwise. A held item is never `ready`. |

- A run that succeeded is offered again, with the state `ready` or `hold`, when its value holds `reason = "foreign"`, a `pr` number and a non-empty `findings` list, and the forge shows that pull request as merged. The call reads the state of that number and does not look the pull request up again. A read that fails ends the step with an error.
- The rows sort by severity, then by item id as byte strings.
- `pr` is the number of the lowest open pull request whose head branch is the item id, or 0.
- `free` is the cap of the `items` queue, the queue of depth 1, less the runs that the library lists on it as pending or enqueued, so the engine keeps no count.
- `pulls` lists every open pull request of the forge, each with `number`, `branch`, `base`, `author`, `draft`, `title` and `item`. `item` is the open item that the head branch names, or empty. `label` does not filter `pulls`, and an item that `label` leaves out of the rows still joins its pull request.
- A read of the tracker or the forge that fails ends the step with an error.
- A project with no forge has no pull requests.
- A build with no queue reader fails the call.

A run that `spawn` started reads the hold of its item in the tracker again before its first step. If the item is on hold, the run stops with the stop `queue` and the code of a held pull request, and `gather` reports it as `stopped`.

### `merge`

`merge(prs, base, repo, hold, own, forbid_text, gate_run, integration, hand_off, name)` lands a list of `struct(item, head, approved)` with one gate run on the union tree of the base tip and all heads. It splits the list when the gate is red. A forge with a native merge queue needs no `merge`.

`approved` is an optional bool of each member, and a member that leaves it out is not approved. It applies to that member only: a pull request of an `own` identity merges only when its own member sets `approved = True`. The call has no `approved` of its own, so the approval of one member never reaches another. A value that is not a bool fails the call and names the field.

`beehave check workflow` reads each `struct(...)` member of a `prs` list with the parser that the run uses, so a wrong field is a problem before the run. The error names the member and the field, for example ``prs[1]: `approved` of a member of `prs` must be a bool, not string``, and is the same text that the run gives. The check reads the first wrong field of a member. A member that is a literal but not a struct, such as `1`, a dict or a list, is refused. A list, a dict or a struct that a field or a member holds counts as that type, whatever it contains. A value that only the run knows, such as a variable or a call, is left to the run: the check skips that field or that member and still reads the other fields. A file that defines its own `struct` is not read as the builtin.

It returns `merged`, `rejected`, `deferred`, `tip`, `tree`, `gates`, `findings`, `note`, `unproven`, `moved`, `host_loss` and `host_losses`.

- Each entry of `rejected` and `deferred` has `item` and `reason`. For a member that a merge guard refused, the reason is every line of that refusal, one per line. No line of it matches a pattern of `forbid_text`, with each line tested as the posting steps test it, so a step can post it with the same `forbid_text`. This holds only for patterns that do not match the words of beehave itself, such as "left out".
- Each entry of `unproven` has `item` and `reason`. An item of `merged` that `unproven` names has not landed.
- `findings` holds one line for each finding. `note` says why no read ran (see below).
- `host_loss` lists one record for each lost gate job of the call, in the shape of the `gate` record, with `kind` `batch_gate` and the union commit in `head_or_commit`. It is an empty list when the call lost nothing.
- `host_losses` is the count of the host losses in the item tree that the last record of the call read. It is 0 when the call lost nothing.
- `name` is required, and each call of a run needs its own.
- The union is gated in the worktree of the item that runs the workflow.
- Each member merges through the guards of the merge package, pinned to its head and to the tree of its prefix.
- Git 2.38 or newer is the minimum on the gate host.
- For each member, the step `batch:<name>.intent#<item>` records the pull request as OPEN before the step `batch:<name>.merge#<item>` merges it. A resume after a crash therefore counts a merge that the forge already made as done.

#### `hand_off`

`hand_off = True` hands the landing to the owner. The call lands nothing, runs no gate and takes no `hold`, `own`, `forbid_text`, `gate_run` or `integration`: a call with one of them fails and names it. `beehave check workflow` refuses the same call when `hand_off` is the literal `True`.

- For each member, the step `batch:<name>.ready#<item>` reads the pull request. A draft at the head of the member is marked ready. A resume replays the step and marks nothing ready again.
- The call does not mark a draft ready while a review asks for changes, the pull request conflicts with its base branch, or a review thread is unresolved. The member goes into `rejected` with the reasons, and the draft stays a draft. A read of the reviews or the threads that fails fails the call.
- If a member waits, the run shows the merge state `owner_merge` and runs the `notify` command of the project once for the whole call. See `notify` in [configuration.md](configuration.md).
- Then the step `batch:<name>.watch#<item>@<round>` reads each member that still waits, and a durable sleep of one hour separates two rounds. A restart resumes the watch.
- A MERGED pull request goes into `merged`. A pull request that is not open goes into `rejected` with its reason, and nothing is drafted.
- If the head of an open pull request is not the head of the member, the call marks it a draft again, unless it is a draft already. It goes into `rejected` with a reason that names the new head, and into `moved` as `struct(item, head)` with that head. The workflow can review the new head and call `merge` again. A head that is another commit with the same tree is a rewrite and not a move.
- If a review asks for changes, or the pull request conflicts with its base branch, the call marks a ready pull request a draft again, at the first read or later. It goes into `rejected` with the reasons. An unresolved thread does not draft a ready pull request.
- If a pull request is a draft during the watch, whatever made it a draft, the member goes into `rejected` and the reason says why: the reasons to wait when a change request or a conflict stands, else "it is a draft". The call marks nothing ready again.
- The call returns when no member waits.
- A call that ends by a cancel of the run, or by an error, marks each member that has not ended a draft again, when it is open and ready for review. Each member has a limit of 30 seconds of its own. A resume of a canceled run goes on with the watch, finds the draft and ends the member as above. If a read or a draft call fails, the owner gets a notice that tells them to convert the pull request by hand, and the notice has its own limit of 30 seconds. A stop of the engine does not draft a pull request, because the run resumes.
- The call reads each pull request through the standing review of `internal/review`, so the reasons use the same words as the ready read.

#### A lost batch gate job

The project gate of the union is two steps in its gate run: `batch_gate:job` checks out the union commit and records the tree and the host job, then `batch_gate` waits for that job. A restart during the wait waits on the recorded job and submits none. A `gate_run` program has no host job and stays one step, `batch_gate`.

- When the wait ends as lost, the gate run waits for a proof as the `gate` call does, and a cancel of the run during that wait reads the proof once more. See "The wait for a proof" in [hosts.md](hosts.md). With no proof at `host.lost_job_wait`, the item run asks the owner.
- After a proof, the item run records the loss as the step `batch:<name>.gate#<n>/host-loss`, then gates the same commit once more. The rerun records its own loss as `batch:<name>.gate#<n>@2/host-loss`. The batch counts one gate in `gates`.
- A second proven loss in one call fails the call with "the gate gave no result". The count is over every gate of the call: the rerun of the same commit, or the gate of a part after a split. A restart keeps it, because each loss is a recorded step. The error names the host and both proofs. No member is rejected or deferred. The call returns no result, so `beehave status <run id> --json` holds the count.

#### Checks after the batch lands

- One recorded step `batch:<name>.prove` checks each merged member.
- One recorded step `batch:<name>.issues` reads the issue of each landed item, for a project of the GitHub tracker whose item ids are issue numbers. An issue that is not closed is a finding.
- If `integration` names a pull request that is MERGED, the step also reads its closing references. An issue that it closes and no landed item names is a finding.
- A read that fails is a finding, never a pass.
- `note` says why no read ran: a base that is not the default branch with no `integration`, an integration pull request that is not merged, or a tracker with no issue numbers.
- The read never closes or reopens an issue. A finding does not fail the call, because the batch has landed and the merge cannot be undone. The workflow reads `findings` and decides what to tell the owner.

#### The host loss fields

A `gate` result has two fields for a host job that was lost. See "The wait for a proof" in [hosts.md](hosts.md).

- `host_loss` holds `kind`, `job`, `host`, `head_or_commit`, `proof`, `note` and `at` when the gate lost its job and a proof came. It is an empty dict when the gate lost nothing.
- `host_losses` is the number of host losses in the item tree of the run, this one included. It is 0 when the gate lost nothing, and the engine then reads no other run.
- The item run records each loss as the step `<call>/host-loss`, with the loss and the count. The step counts the `/host-loss` steps of every item run of the tree from its root, so a replay reads the recorded count. If two runs of the tree record a loss at the same time, each count can leave out the other loss. The status reads the count again and holds both.
- `beehave status <run id> --json` shows `host_losses` for the tree of the run, and leaves it out at 0.
- The engine sets no limit on the count. A workflow reads `host_losses` and decides when to ask the owner.
- The library `review_fix.star` reruns a void gate with a host cause as `candidate-gate-host-<k>` at no cost to the item, and stops the loop at `HOST_LOSS_LIMIT` (5). Its result carries `host_losses` and `host_lost`. See "The host loss limit" in [durable-loop.md](durable-loop.md).


#### `own` and `approved`

Nobody merges their own work, and our pull requests merge only on a verdict that another step gave.

- A pull request of an `own` identity merges only when `approved` is true.
- If `approved` is not set, the step refuses our own pull requests.
- A pull request of any other author does not depend on `approved`.
- A standing changes-requested review of another person blocks the merge in every case.
- The review step must not be the step that wrote the code. Nothing checks this, because the file does not say which step wrote the head.



### `spawn` and `gather`

#### `spawn`

`spawn(items, entry="main", base="", retries=0, name="")` starts each item as its own run of the same workflow file, and returns `runs`, a list of structs with the fields `item` and `run`.

- With `retries`, a whole number from 0 to 10, `gather` starts a child that failed again as the next attempt of its item, with the same input and a new login, until the retries are spent. A child that was canceled by hand is not started again.
- The retries belong to the spawn call that recorded them, not to the value that it returned. A later `gather` of the same runs waits on the last attempt of each, with the retries it has left, so the retries of a call are spent once over all its `gather` calls.

- Each child run executes the function `entry(item)` on the bytes that the parent pinned, as a child workflow of the parent on the items queue of its depth. The queue holds at most `agents.max_concurrent` runs.
- A run that `spawn` started may call `spawn`. A run that a user started is at depth 0, and each child is one level deeper than its parent. The project key `max_spawn_depth` caps the depth, 3 by default.
- Each depth has its own queue: `roots` for the runs that `beehave run` starts, `items` for depth 1, then `items.2`, `items.3` and so on. A parent that waits in `gather` holds a slot of its own level only, so its children always find slots.
- Each queue has the cap `agents.max_concurrent`, and the caps add up: with `max_spawn_depth` 3, at most 4 times `max_concurrent` item runs are open at once. The agent queues still cap the agent steps.
- At start, the engine registers one queue for each depth up to the highest `max_spawn_depth` of the projects. If a run that can run again (open, canceled or failed) sits deeper, the engine also registers the queues down to the children of that run, which its replay starts again. A project file that lowers the cap does not strand a run on a queue that no worker reads.
- The tree cost cap `agents.max_tree_budget_usd` sums every level of the tree. `beehave cancel` of the root cancels every level. A cancel of a run in the middle cancels the runs below it, and its parent sees a child that did not finish.
- A child of a round item is cut at the base of the round item, `round/<base>`. A workflow that has a round base setting passes it as `base`, and `spawn` checks that it equals the base of the round item.
- A child run that finished with success is reused and does not run again.
- A cancelled run, or no run, starts the next attempt.

`spawn` refuses:

- An item that has an open run.
- An item whose last attempt failed.
- The parent itself.
- An item that is in `items` twice.
- A child that would run deeper than `max_spawn_depth`. The error names the item, the depth and the cap: `item 24 would run at depth 4, and the project has a cap of 3`.
- A `base` that differs from the base of the round item that calls `spawn`. The error names both bases. An item that is no round item has no base to compare, so `spawn` cuts each child at the `base` of the call. The gate of the child reads the pin file at the same branch.

#### `gather`

`gather(runs, fail="return", until="all", timeout="", on_timeout="", name="")` waits for the runs and returns `results`, a list in the order of the call.

- With `until="any"`, the wait ends when at least one run is no longer open, and `results` holds only the runs that ended, in the order of the call. Any other `until` value is refused.
- A second `until="any"` call on the same list returns an ended run again, so a loop removes the ended runs from its list before the next call.
- If `fail="raise"` and a run failed, the call waits for every run, as `until="all"` does.
- A run that this run did not spawn is refused with `run <id> is not a child of this run`. `gather` accepts only the runs that a `spawn` call of this run returned, including a run that `spawn` reused because it had finished. The check reads no store: the plan of each `spawn` is a recorded step, so a replay or a fork of this run lists the same runs. The dry run and `beehave check workflow` cannot see this, because the runs exist only at run time.
- Each result has the fields `item`, `ok`, `error`, `merge`, `sent_back`, `closed`, `stopped`, `cost_usd`, `value`, `attempts` and `open`. `attempts` counts the runs of the item that this call waited for.
- `cost_usd` is the spend of the child run and of every run below it, in dollars, as the status view counts it. The step reads it once, when the wait ends, and records it with the outcome, so a replay returns the same number.
- A run of `spawn` with retries left that failed does not end the wait and does not trigger `fail="raise"`. The call records the plan of the next attempts as one step, starts them, and waits again. A replay starts nothing twice.
- `beehave resume` of a parent reopens only the last attempt of each item. An earlier attempt that a retry replaced stays failed, and the parent goes back to the start of the last attempt.
- The wait is one recorded step that reads the rows of all the runs, so the record does not depend on the order in which the children end.
- With `fail="raise"`, the step cancels the runs that are still open when it sees a run that failed, and records all the outcomes. The call then fails with the error of that run.
- A restart replays `spawn` and `gather` from the records, and each child keeps its own output.

#### `publish` and `progress`

`publish(key, value, name="")` sets the value of a key of this run and returns None. A second call with the same key replaces the value. `progress(runs, key, name="")` reads the value of that key from each run and returns a list in the order of the call. Each element has the fields `item`, `run`, `set` and `value`.

- A key has 1 to 64 characters from `a-z`, `0-9`, `_`, `.` and `-`, and starts with a letter or a digit. A key that starts with `beehave.` is reserved. The check of a workflow file refuses a literal key that breaks the rule, and the run refuses a computed one.
- A value is a dict with string keys, a list, a tuple, a string, a number, a bool or None, at most 64 KiB as JSON, as the value of a child run is. The engine hides secrets in the value before it stores it.
- `publish` sets a DBOS event of the run, named `u.<key>`, and the event `beehave.keys`, which lists the keys of the run in the order of their first use. DBOS has no call that lists the events of a run.
- `progress` does not wait. A run that published nothing under the key reads `set = False` and `value = None`. A published None reads `set = True` and `value = None`.
- `progress` accepts only the runs that a `spawn` call of this run returned, as `gather` does, and refuses any other run with `run <id> is not a child of this run`.
- For a child that `gather` started again, `progress` reads the latest attempt, and `run` is the id of that attempt.
- `progress` reads all the runs in one recorded step, so a replay of the parent returns the values of the first read, even if a child published again since.
- `beehave status <run id> --events` prints the keys of a run and their last values, one line each, while the run goes on and after it ended. Add `--json` for one JSON list.

#### The deadline of a group

`timeout` is a duration above zero, such as `"10m"`. It sets a deadline on the wait. At the deadline, each run that is still open gets `ok = False` and `error = "deadline"`, and `on_timeout` says what happens to it:

- `"cancel"` (the default when `timeout` is set) cancels the run and its children, as `beehave cancel` does. Its `open` is False.
- `"return"` leaves the run running. Its `open` is True, and the workflow can wait for it again or cancel it.

A run that ended before the deadline keeps its own outcome. `on_timeout` with no `timeout` is refused, and so is any other value of `on_timeout`.

The deadline is a recorded step, `gather:<name>#<n>.deadline`, before the step of the wait. A parent that restarts during the wait reads the first deadline and does not start a new one. With `"cancel"`, the step of the wait records the outcomes and cancels nothing, and the step `gather:<name>#<n>.cancel` after it cancels the runs that were open. So a crash between a cancel and a record cannot change a `"deadline"` outcome. A deadline is not a failure: `fail="raise"` does not raise for it.

#### `cancel`

`cancel(run, name="")` cancels one run that a `spawn` call of this run returned, with its children, and returns `cancelled`.

- `cancelled` is True when the call ended a run that was open. It is False when the run had already ended, with success or not. The store leaves an ended run as it is.
- A run that `spawn` reused because it had finished is ended, so `cancel` returns False for it and stops nothing.
- For a child that `gather` started again, `cancel` ends the latest attempt and its children, and `cancelled` is True only when that attempt was open. A deadline cancels the latest attempts too.
- A run that this run did not spawn is refused with `run <id> is not a child of this run`.
- The call is two recorded steps. `cancel:<name>#<n>.open` records whether the run is open. `cancel:<name>#<n>` then cancels it, writing the cancel marker first as `beehave cancel` does, so an engine that stops gives the claim of the run back at its next start. The read is recorded before any cancel, so a cancel step that runs again after a crash still returns True for a run that it ended.
- `beehave resume` of the parent does not reopen a run that `cancel` or a deadline canceled.

#### The value of a child run

`value` is what the entry function of the child returned with a plain `return`. It is None when the child returned nothing or did not succeed.

- The entry function of a run that `spawn` started may return a dict with string keys, a list, a tuple, a string, a number, a bool or None. The value is nested at most 32 levels deep and is at most 64 KiB as JSON.
- A tuple reads back as a list. A number reads back as JSON text reads: a float with no fraction, such as `2.0`, reads back as the int `2`.
- Any other value (a struct, a function, a dict with a key that is not a string, a float that is not finite), or a larger value, fails the child. The error names the problem and tells the author to return a value that is safe or to remove the return.
- The engine counts the bytes while it converts the value, and stops at the limit. A value that shares one list many times is therefore refused before it is built.
- `beehave status --json` does not show the value, because a value can be 64 KiB. Read it with `gather`.
- The value is part of the recorded result of the child run, so a parent that restarts reads the same value. The child writes nothing outside the worktree to report what it found.
- A run that a user started keeps no value: its `main` return is ignored.

Before this rule, the engine dropped what a spawn entry returned. A spawn entry that still ends with `return command(...)` or `return struct(...)` now fails its child. Check each entry that `spawn` names: return a safe value or None, or remove the return.

#### Retry after a failed `gather`

The retry route for a parent that failed in a `gather` is `beehave resume <parent>`. The resume:

1. Finds the step that started the failed child.
2. Takes a fresh claim for that child, and reopens it on the items queue of its depth. A child at depth 2 or 3 reopens in the same way, from the failed run at its own level.
3. Forks the parent from that step.

The resume also reopens each sibling that the raise canceled while it was open, so that no child stays canceled and fails the next `gather`.

`spawn` and `gather` then run again. The children that finished are reused, and the new `gather` reads the new outcome of the reopened child.

- The `gather` step records the ids of the runs that its raise canceled, and the resume reads that record before it rewinds the parent.
- A child that a user canceled by hand is not in the record. The resume leaves it canceled, and the next `gather` reports it as a failed outcome.
- The raise cancels the children of each canceled child, so no phase of a canceled child stays in the queue.
- A canceled child gives its claim back on the next tick of the engine, not when the `gather` returns. If a resume comes right after the raise, it can ask you to run `beehave resume` again.
- A failed parent cannot be cancelled, and a new attempt of it is refused until it resumes.

### `send` and `recv`

Each run has one inbox. A run sends a message to its parent or to a child, and a run reads its own inbox by topic.

#### `send`

`send(to, topic, value, name="")` puts a message on the inbox of another run, and returns `id`, the id of the message.

- `to` is `"parent"`, or a run that a `spawn` call of this run returned. `"parent"` names the run that spawned this run.
- `topic` is lowercase letters, digits, `.`, `_` and `-`. A topic that starts with `beehave.` is reserved.
- `value` is a value that encodes as JSON: text, numbers, booleans, lists and dicts. The encoded value is at most 64 KiB, and secrets are removed from it.
- The id is the id of the sending run, a colon and the step name, such as `item-21-r1:send:tell#1`. It is the idempotency key of the send, so a resumed run inserts the message once.

`send` refuses `"parent"` in a run that a user started, any other text, and a run that is not a child of this run.

A send to a child goes to its latest attempt: the run that `gather` started again last, or the run that `spawn` returned. If that attempt ended, the send is refused with `item <id> has no open run`, as no attempt reads the message. The check is part of the recorded send step, so a replay gives the same answer.

#### `recv`

`recv(topic, timeout, name="")` waits for the next message of the topic on the inbox of the run. It returns `value`, `sender`, `id` and `timed_out`.

- `timeout` is required, such as `"10m"`. When it passes, `timed_out` is true and `value` is None.
- The messages of one topic come in the order that they arrived. A message of another topic waits for a later `recv` of its topic.
- The deadline is a recorded step, so a resumed run waits only the time that is left.

#### `beehave send`

`beehave send <item> --topic <topic> <text>` puts a message on the inbox of the open run of the item. `--json <file>` gives the value as a file of JSON in place of the text. The command prints the id of the message. A second send with the same `--key` to the same run inserts nothing, so a repeated send delivers once. The sender of the message is `cli:<user>`.

### `command`

A `command` step has five settings besides `run`, `timeout`, `host` and `name`: `max_new_processes`, `slots`, `stdin`, `env` and `stop_on_rc`.

A `command` can run twice after a crash that cuts the step before its record. Put a program that changes a system outside the run in `write`. A `command` with `host` or `slots` can also submit its host job twice today. A later change fixes this.

#### `slots`

`slots` is the name of a slot class, such as `slots = "mutation"`. It limits the host jobs of one kind.

- A step with `slots` needs `host`. A step with no `host` is refused.
- The `host` block of the project lists the class and its count in `slots`. The engine runs at most that many jobs of the class on one host at once. A job that finds no free slot waits in a durable queue and starts when a job of its class ends.
- A class that the `host` block does not list is refused when the step runs. The refusal names the class and the host.
- A job of another class, and a job with no `slots`, never waits for the class.
- `beehave check workflow` refuses a literal `slots` that is blank, and a literal `slots` with no `host`.

#### `max_new_processes`

`max_new_processes` is a whole number from 1 to 100000. It caps the tasks of the program and of everything it starts at `max_new_processes` besides the program itself. A thread counts as a task, so a program with many threads reaches a small cap early.

- A step with no `max_new_processes` has no cap. A step with `host` cannot set it.
- An agent run and a gate command in the sandbox take no cap.
- A program that starts a process past the cap fails. When it fails and its output shows a failed start (the text "Resource temporarily unavailable", "Cannot fork" or "fork: retry:"), the output ends with a line that names `max_new_processes`.
- `beehave check workflow` refuses a literal value that is not a whole number in range.

| | Linux | macOS |
| --- | --- | --- |
| Mechanism | The program runs in a transient systemd user scope with `TasksMax` set to the cap plus one. | The per-user limit: the process count of the user at the start plus `max_new_processes`. A helper that the engine starts from its own binary sets it with the `setrlimit` system call. |
| What counts | The kernel counts the tasks of that control group alone. Other programs of the user do not use the cap, and the cap is exact. | There is no counter for one tree. The cap is shared with the other programs of the user. A user with root rights has no cap. |
| Cancel or timeout | Stops the scope, which kills every process in it, also a child that called `setsid`. | Kills the process group only, so a child that called `setsid` can survive. |
| Failure to set the cap | The step fails with "max_new_processes needs a systemd user manager", and the program does not start. | The step fails with "could not set max_new_processes" or "could not count the user's processes", and the cause. |

On Linux, the scope needs a systemd user manager with the pids controller. The error says which cause it saw:

- If `XDG_RUNTIME_DIR` is not set (cron, a system service with `User=`, `sudo -u`), it says to set `XDG_RUNTIME_DIR=/run/user/<uid>`.
- If it is set, it says the user manager is not running, and names `loginctl enable-linger`.

There is no fallback to the per-user limit on Linux, because it would look the same and count the wrong thing.

On macOS, the helper reports a failure on a pipe that the program never holds. A program that exits with code 125, or prints similar text, therefore keeps its own result.

#### `stdin`, `env` and `stop_on_rc`

| Setting | Meaning |
| --- | --- |
| `stdin` | A string that the program reads. |
| `env` | A dict of strings that the program gets as variables. It cannot set a name that starts with `DBOS__` or `BEEHAVE_`. |
| `stop_on_rc` | A list of exit codes from 1 to 255. |

- A step with `host` takes neither `stdin` nor `env`, because a host job takes a command line only.
- The engine evaluates `stdin`, `env` and `stop_on_rc` before the durable step, and the record keeps the stop. A replay builds the same request, and a stopped step stays stopped.

When the exit code of the program is in `stop_on_rc`, the item ends with the name, the code and the output of the step, and no later step runs.

- `beehave run` prints them and exits with code 1.
- `beehave status` shows the state `stopped` and `stopped at <step> rc <n>: <output>`. `<step>` is the record name, such as `command:probe#1`. `<output>` is the output of the step on one line, cut at 120 characters. A step with no output has no `: <output>`.
- Other codes stay readable as `rc` and `stdout` of the returned struct. A workflow can send code 1 to a fix round and stop on code 2.

#### `stderr`

The returned struct also holds `stderr`, the second output stream of the program, apart from `stdout`.

- A step with `host` has an empty `stderr`, and so has a dry run.
- A host job that failed gives the end of its log as `stdout` (the last 16 KiB). A job that succeeded gives an empty `stdout`. A log that cannot be read leaves `stdout` empty, and `rc` still says the job failed. The tail can start in the middle of a line.
- The engine records `stderr` in the run database like `stdout`, so a program must not print a secret to either stream.
- A run recorded before the field existed has no `stderr`. A workflow that reads it fails on a replay of that run.

#### `retries`, `backoff` and `failure`

| Setting | Meaning |
| --- | --- |
| `retries` | A whole number from 0 to 10. The default is 0. |
| `backoff` | The wait before the first retry, a duration above zero such as `"2s"`. The wait doubles before each later retry, and stops at 10 minutes. A `backoff` above 10 minutes is refused. It needs `retries` of 1 or more. With no `backoff`, the first wait is 100 ms and no wait is longer than 5 s. |
| `failure` | `"raise"` (the default) or `"return"`. |

- A try that exits with a code that is not zero, and not in `stop_on_rc`, starts again until the retries are spent. A code in `stop_on_rc` is never retried.
- An error of the step, such as a program that cannot start, also starts again. A cancel and a lost host are never retried.
- The result has the field `attempts`, the number of tries. After the last try, `rc` is the code of that try.
- The step is one record. A replay reads it and does not retry again. A crash during the retries starts the count again: the resume runs the step from the first try.
- With `failure = "return"`, an error of the step that is left after the retries becomes the field `error` of the result, with `rc` -1 and empty `stdout` and `stderr`, and the run goes on. The default ends the run with the error. A cancel of the run always ends it.
- A step with `slots` takes no `retries`, because it runs as a queued host job.
- `agent` takes no `retries`.
- `beehave check workflow` refuses a literal `retries` out of range, a `backoff` that is not above zero or is above 10 minutes, a `backoff` with no `retries`, `retries` with `slots`, and a `failure` that is not `"raise"` or `"return"`.

#### The environment of the program

The program of a `command` step gets a small environment: the names that an agent run also takes from the caller (the path, home, locale, proxy and certificate variables, and `SSH_AUTH_SOCK`), then the variables of `env`. No other variable of the caller reaches it. The `env` builtin refuses the name of a secret, so a workflow cannot read a token from the environment of the engine.

A `gate` step keeps the environment of the process.

### `write`

`write(run, read_back, undo, stdin, env, timeout, name)` is a `command` step for a program that changes a system outside the run.

- It takes `run`, `stdin`, `env` and `timeout` with the rules of `command`. It takes no `host`, `stop_on_rc` or `max_new_processes`.
- The result has the fields `stdout`, `stderr`, `rc`, `attempts`, `key` and `skipped`.
- It takes `retries` and `backoff` with the rules of `command`, and no `failure`. Each try runs the read-back first, so a try after a write that landed is skipped. All the tries have the same key.
- A write runs on this machine only.
- The program of `run`, `read_back` and `undo` is pinned like the program of a `command`. A script that changes between the first run and a resume is refused. A `read_back` or `undo` list that is not a literal is counted as a dynamic command.

#### The key

The engine puts a key in the variable `BEEHAVE_WRITE_KEY` of the program, the read-back and the undo. The key is the SHA-256 hash of the run id and the step name, cut to 32 hexadecimal characters. A replay has the same run id and the same step name, so it has the same key, and the outside system can drop a repeat. If the engine stops after the write and before the step is recorded, the replay runs the step again with the same key.

A rerun is safe when the call has a `read_back`: the read-back finds the finished write. A rerun with no `read_back` is not safe today. The program runs again with the same key, and neither beehave nor the forge can tell if the outside system dropped the repeat. A later change asks the owner before the program runs again.

#### `read_back`

`read_back` is an optional program. It runs first in the same step, on a first run and on a replay.

- If it exits with zero, the write is done. The step does not run `run`, and records the output of the read-back with `skipped` true.
- Any other code means not done, and `run` starts.
- The read-back must be a pure check that changes nothing, because it also runs on a first run.
- A read-back that cannot start fails the step.

#### `undo`

`undo` is an optional program. A write with an `undo` joins the undo list of the run, unless the read-back skipped it.

- The engine cannot tell a write that another party made from its own write that a crash cut off before the record, so the replay of such a write registers no undo either.
- A write that exits with a code other than zero joins the list too, because the engine cannot tell how much of it happened. The undo must be safe when nothing happened.
- The undo gets the same key as its write.
- If `main` fails, the engine runs the undos in reverse order of the writes, before `finish`.
- Each undo is a recorded step named `undo:<write step>`, so a replay after a crash during the undos reads the finished ones and runs the rest.
- An undo that fails does not stop the other undos. The error of the run joins the errors and names each undone step.

No undo runs:

- On a success.
- On a cancel (a resume continues the run).
- On a stop by `stop_on_rc`.
- When only `finish` fails.
- When the failure comes from the timeout of the context of the run. The result does not say so.

### `agent`

- An `agent` call must name a `model`. A call without it fails at its line.
- `resume` of an `agent` call is the session of an earlier agent, such as `impl.session`.
- `skill` of an `agent` call is the name of a skill folder, such as `"lint-pass"`. The agent CLI loads the skill before the first turn of the model, so the step does not rest on a choice of the model. The adapter puts its own skill-load line first in the prompt: for Claude Code the line is `/beehave-skills:<name>`. The step gets the skill like a skill that its prompt declares. An empty text is no skill. `beehave check workflow` refuses a name that is no folder name. A step with `skill` on a runner whose agent has no skill-load line (OpenCode) does not start: the message names the runner. The dry run gives every runner a line.
- The call takes a `pin`, a commit id such as `check.head`, and starts only when the head of the worktree equals it.
- The call takes one free `label`, a text that shows in `beehave status`. A label never changes what the step does. `edits = False` makes the step read-only and puts it on the review queue.
- The call takes no `kind`, `lane`, `rounds`, `follow_ups` or `require_skills`. `beehave check workflow` refuses each one by name, for example "agent takes no argument kind".
- The result of the call has the field `skills`: the list of the skills that the agent called, in the order of the calls. The list is a record of what the agent did. To make a step use a skill, use the `skill` keyword, which loads the skill before the first turn. A test of the list does not make the agent call the skill.
- In a dry run, the fake agent reports each skill that the workflow requires. The dry run reads the syntax tree of the workflow file and of the files it loads, and runs no code. It parses these files again for this: it does not reuse the parse of `beehave check workflow`.
- A skill is required when an `if` tests a text literal with `in` or `not in` against `<expression>.skills`, and the branch that runs when the skill is missing has a call of `fail(...)` as one of its own statements. Either quote style, `not`, `not not` and parentheses around the test count. The two usual forms are `"x" not in r.skills` with `fail` in the `if` branch, and `"x" in r.skills` with `fail` in the `else` branch. A name that the same function binds only from `<expression>.skills` (`called = r.skills`) counts in place of `<expression>.skills`. Each function is its own scope: a name from another function, a parameter, a loop variable and a name that the function binds again to something else do not count. A test inside a helper function counts.
- The tested name can also be `<expression>.vars["key"]` with a text literal as the key. The dry run then reports the text that the key holds in the vars of the dry run, and no skill when the key is missing or holds no text.
- A forbid check does not count: `if "x" in r.skills: fail(...)` requires nothing, so the fake agent does not report `x`.
- The dry run does not follow these forms, and a workflow that needs the skill through one of them stops the dry run with its own refusal: a bool bound to the membership (`ok = "x" in r.skills`), `r.skills.count("x")`, a comprehension over the skills, an index such as `r["skills"]`, a skill name held in a constant (`n = "x"`), two tests joined with `and` or `or`, a `fail` that is not a statement of the branch itself (for example inside a deeper `if`), and a helper that takes the name as an argument (`def need(r, name)`). Write the literal in the test of the `if`: `"lint-pass" not in r.skills`.
- A check under `if False` still counts. A check in a loaded function that the step never calls still counts. The dry run reads the text of the functions and does not follow calls.

### `beehave check workflow`

`beehave check workflow [file]` reads `workflow.star` by default. It prints one line for each problem as `file:line:col: text`, then the count. It runs no workflow code.

| Exit code | Case |
| --- | --- |
| 0 | A valid file. |
| 1 | Problems. |
| 2 | A file cannot be read or parsed. |

It checks that:

- the file and every file it loads parse, and each name in them is defined;
- no two files load each other (the error shows the path as `a -> b -> a`);
- each `load` names a file in the folder of the workflow or a library in the binary;
- the file defines `main`.

#### Keywords and result fields

- Each builtin call is checked against one table of keywords, which the run also reads (`internal/workflow/signature.go`). The check reports an unknown keyword, a missing required keyword, and a literal value that is not one of the listed values.
- For a name bound once in a function by `x = <builtin>(...)`, the check reports `x.<field>` when the result of that builtin has no such field. The fields come from a second table (`internal/workflow/results.go`). A test fails when a field in the table and a field that the run returns differ.
- For an `agent` call, its literal `outputs` are fields too. An `agent` call whose outputs are not a list of text literals is left to the run.
- A name bound more than once, or bound again as a parameter of a nested def or lambda, is left to the run.

#### Item fields

- The first parameter of `main`, of `finish` and of each function that a literal spawn entry names counts as the item when the function binds it once.
- The check reports `<param>.<name>` when the name is not an item field: `id`, `title` or `body`, the same for every tracker.
- A helper function that receives the item is not tracked, and neither is a function in a loaded file.
- A missing prompt file, or a field that the item lacks, fails the step when it runs, with the file, the line and the column of the call.

### Examples

#### `examples/remediation/`

One workflow written in this form, with five prompt files. The method lives in that folder and never in Go. The steps:

1. A plan.
2. An implement step.
3. A review and fix loop of up to `fix_rounds + 1` candidates. Before each candidate, up to three scoped fixes and scoped reviews run with no gate. Each candidate gets the gate on the base, the checks before review, the mutation step and one full review, in that order.
4. A wait for the owner.
5. A settle step.
6. A merge step.

#### The review skill of the sweep step of `examples/remediation/`

The workflow names no skill. The project names it.

- The project file sets `vars.review_skill`: the name of a skill, such as `"your-review-skill"`. A run with no such key, or a value that is not a text that is not blank, is refused before the first agent starts. The message names the key.
- The prompts name the skill with `{{ skill .facts.vars.review_skill }}`, so the agent of the step can load it from the config folder of the runner (see [the skills of a step](sandbox.md#the-skills-of-a-step)).
- The engine starts the review sweep as the step `implement-sweep`, right after the implement step. The step has `skill = project.vars["review_skill"]`, so the agent CLI loads the skill first. No check rests on an agent that chooses to call a skill.
- The sweep has the runner and the sandbox of the implement step, and it resumes the session of that step. It may edit, and its prompt `prompts/sweep.md` orders it to commit each fix and to write the report again.
- The engine pushes the head of the sweep, not the head of the implement step. The review loop starts from the sweep.
- A sweep on a runner with no skill-load line does not start. The message names the runner.
- The example refuses at the start of the lane, before the plan agent, when the runner that vars.round.steps.implement names has no skill-load line. It reads the fact `runners.<name>.loads_skills`. A step on the pool, with no runner name, is refused at the sweep step, because the pool picks the runner at run time. The dry run with `--project` reads the runners of the config and refuses the same way. `beehave check workflow` without `--dry-run` runs no facts step, so it cannot check a runner. Neither can resolve a runner that the pool picks at run time.

#### The sweep step of `examples/remediation/`

The engine has no cleanup builtin. The project owns the clean command and the workflow runs it as a host command.

- The project file can set `vars.clean_command`: a list of words, such as `["sh", "scripts/clean-lane.sh"]`. A value that is not a list of words that are not blank is refused before the first agent starts. The message names the key.
- When a lane ends, the child run calls `sweep`. It runs `vars.clean_command` as the step `sweep-clean`, then `docker volume prune -f` as the step `sweep-prune`. Both run with `host`, so they run on the build host.
- The sweep runs for each end of the lane that returns a value: an approval, a rejection, a hold and a ruling of the owner. It also runs before the `fail` call of the lane for a missing changelog entry.
- Both sweep steps use `failure = "return"`. A sweep step that cannot run, such as a step on a host that is gone, puts its error in the `sweep` field as `clean: rc=-1 error: <text>` and does not fail the lane. The prune still runs after such a clean.
- A workflow file cannot catch an error. A lane that the error of an earlier builtin ends, such as a host that is gone at the mutation run, gets no sweep. Issue #753 tracks it.
- With no `vars.clean_command`, the sweep runs the prune only.
- The sweep does not fail the lane. A failed clean does not skip the prune.
- The value that the lane returns holds the field `sweep`: one line for each step, `clean: rc=<n>` or `clean: not run, vars.clean_command is not set`, then `prune: rc=<n>`.

#### The mutation step of `examples/remediation/`

The engine has no mutation builtin. The project owns the tool and the workflow runs it as a host command.

- The project file sets `vars.mutation_command`: a list of words, such as `["sh", "scripts/mutation-pr.sh"]`. A run with no such key, or a value that is not a list of words, is refused before the first agent starts. The message names the key.
- After the gate and the checks before review of each candidate pass, `check_mutation` runs the command with `host` and `slots = "mutation"`. It adds one word at the end: the base ref, `origin/` and the base of the round (`vars.round.base`). The command mutates only the lines that differ from that ref.
- The `host` block of the project lists the class: `slots = {"mutation": 1}`.
- The command exits with code 0 when the head is clean, and with another code when the run fails. A run that applied no mutant is a failed run, and the command says so.
- A failed run prints one finding per line on its standard output, as `finding: path:line: text`, for example `finding: internal/cli/check.go:112: a mutant lived: CONDITIONALS_NEGATION`. The output of a failed host job is the end of its log, so it can hold other lines of the tool, such as the `KILLED <TYPE> at file:line:col` lines of gremlins. Only a line that starts with the literal prefix `finding: ` is a finding, and the finding is the text after the prefix. A failed run with no such line gives each non-blank line, the last 20 at most.
- A failed run that prints no line, or a job whose log cannot be read, gives one finding with its exit code.
- The findings go to the fix lane in the place of the report of a red gate. The loop starts no review on that head. The loop gates the fixed head and runs the mutation step again.
- With exit code 0 the output is ignored.

A project with another mutation tool sets another command. This repository uses `scripts/mutation-pr.sh`, which `make mutation-pr BASE=<ref>` also runs. Its results:

- A diff with no Go source change prints `mutation-pr: not applicable: <reason>` and exits with code 0. This path needs git only.
- A diff whose changed non-test lines hold no operator that gremlins mutates prints `mutation-pr: no candidate: <reason>` and exits with code 0. This run prints no counts line, because a counts line with `applied=0` is a failed run. The merged report of gremlins lists every mutant of the changed folders and marks each one that is not on a changed line as skipped. A path mismatch also marks every mutant skipped. So the script reads "no candidate" from an all-skipped report only after three checks: the script runs in the top folder of the repository, which is also the module root; each file that the report names exists under that folder; and the mutants that the report does not skip are exactly the mutants on the changed lines of the diff. The script builds those lines the way gremlins does, from the same `git diff --merge-base` with context lines and renames: for each hunk, the lines start after the leading context lines, one line for each added line.
- A report that fails one of these checks fails the run: its paths or its changed lines do not match the diff.
- A run that applied no mutant fails in every other case: an empty report, or mutants on changed lines that are all not covered.
- Gremlins reports LIVED for a test run that exits 0, for a run that a signal ends, and for every exit code other than 1 and 2. So the script sets `GOFLAGS` to run each mutant test binary through `scripts/mutation-exec.sh`, which records the exit code and the first failing line of the run. The `-count=1` flag in `GOFLAGS` makes each mutant run its tests, so that no cached pass leaves a mutant with no record. The script keeps the `GOFLAGS` value that `go env GOFLAGS` gives and adds the two flags.
- Gremlins writes each mutant with `go/printer`, which reformats the file. So `scripts/mutation-exec.sh` formats the original file and the mutated copy with `gofmt`, and records the line and the column of the first byte that differs. It reads the mutant before the test binary starts, so a record of a run that gremlins stopped on a timeout still names its own mutant. The step pairs a lived mutant with the records at its file, line and column. A boundary mutant changes the byte after its token, so its column is one more. A record of a timed out mutant is not read. The step needs `gofmt` and `go` on the path.
- A run with lived mutants fails. For each lived mutant whose test run exited 0, at most 50, it prints one `finding: ` line and one marker line, `mutation-pr: lived <file>:<line> <mutator>`. A summary line follows, with the full count and the number of finding lines printed.
- A lived mutant whose test run did not exit 0, or left no record, is an error of the run and not a weak test. It prints `mutation-pr: run error <file>:<line> <mutator>: <reason>`, at most 50, and no finding. The counts line still counts the mutant as lived. The run fails.
- A run that ends with no report, because it exceeds `MUTATION_TIMEOUT` or because gremlins fails, prints each recorded test run as `mutation-pr: test run of <file>:<line>:<column> exited with <code>: <first failing line>`.
- The end marker `mutation-pr: end count=N printed=P` is the last `mutation-pr:` line of each run that prints a result line: "not applicable", "no candidate" or the counts line. N is the number of lived mutants whose test run exited 0, and P is the number of marker lines. Both are 0 when no mutant lived. A run that stops before its result line prints no end marker.
- The script gives the git of gremlins the settings `diff.noprefix=false` and `diff.mnemonicPrefix=false`, so a repository that sets `diff.noprefix` gets the same result.
- Gremlins has no package filter, so the script starts it once for each folder that holds a changed non-test Go file. A folder below another changed folder is dropped, a folder that the diff deleted is dropped, and a changed file in the module root gives one start on the module root. The coverage run and the mutants of a start cover that folder only. A start on a folder runs there with `diff.relative=true`, and the script prefixes the file names of its report with the folder, so the checks read paths from the module root. A diff with no folder left prints `no candidate`.
- `MUTATION_TIMEOUT` is one deadline for the whole run. Each start gets the time that is left, and a start with no time left fails the run with the name of its folder. The script also adds `-timeout=<limit>s` to `GOFLAGS`, because the coverage run of gremlins sets no `-timeout` and `go test` stops it after 10 minutes. The test run of each mutant sets its own `-timeout`.
- A run that exceeds `MUTATION_TIMEOUT` fails with its own line and no end marker.
- `MUTATION_TIMEOUT` accepts only a positive whole number of seconds. Any other value fails before the run starts.
- With no `MUTATION_TIMEOUT`, the limit is 3600 seconds when the diff changes a non-test Go file in `internal/flow` or below it, and 1200 seconds otherwise. A flow run with 4 workers took 2478 seconds for 21 mutants.
- The file `.gremlins.yaml` at the module root sets 4 gremlins workers for `make mutation`, `make mutation-pr` and a direct run. Gremlins starts one worker for each core by default, and each worker runs a test binary that runs tests in parallel. On a 16-core host that load made the mutants of `internal/flow` time out. To change the count, edit that file.

`examples/remediation/CONFORMANCE.md` has one row for each rule of the method, with its home (`core`, `file`, `skill` or `drop`) and its status (`gap`, `expressed`, `proven` or `n/a`). A row moves to `proven` when a behavior test runs it. A test in the `workflow` package fails when:

- an ID repeats;
- a home or status is not one of the allowed words;
- an `expressed` row names a builtin call that the example and its library do not hold;
- a `proven` row names no test;
- the file cites a test name, in any column or in the prose, that no `_test.go` file of the repository declares as a top-level function. A citation `TestX/sub` checks `TestX` only.

#### `examples/research/`

A workflow for work that is not code, with no forge and no gate. The environment variables `RESEARCH_TOOLS` and `RESEARCH_OUT` name the tools folder and the report folder. The scripts are POSIX shell, and `check.sh` uses `curl`, because the engine code may not import `net/http`.

The steps:

1. A `scope` agent writes up to five sub-questions as the output `questions`, one on each line.
2. One `research` agent for each sub-question writes `notes/<n>.md` in the work folder. Every line of a notes file is a claim, except a blank line and a heading. Each claim ends with one or more `[source: <URL>]` tags.
3. The `command` step `check` runs `tools/check.sh`.
4. A `summary` agent writes a short summary of the notes.
5. A fresh `review` agent, with the outputs `verdict` and `open` and `output_tries = 2`, tries to refute the notes, and checks that the summary states only what the notes state. Any text in `open` keeps the loop going.
6. The `report` step runs `tools/report.sh`.

`tools/check.sh` exit codes:

| Code | Case |
| --- | --- |
| 1 | A claim with no source, or a URL that does not answer. The engine sends only the files named in its output back to `research`. Also when the total time passes after a source failed: it prints the failures found so far. |
| 2 | It cannot run: no network (no source answers, and a known address does not answer either; `CHECK_PROBE` sets that address), no `curl`, a limit that is not a whole number, a notes file with no sub-question (the workflow passes the number of sub-questions in `CHECK_QUESTIONS`, so `notes/6.md` with two sub-questions stops the item at once), or the total time passed before any source failed. |

- Each fetch has a connect timeout and a time limit, and a run reads at most 40 URLs.
- The check has three passes. The step has `stop_on_rc = [2]` on the first two passes and `stop_on_rc = [1, 2]` on the third. The third failing pass, or a check that cannot run, ends the item with the output of the check.

The review loop:

- Any open finding gives the verdict `changes`, and a `revise` agent and a new check follow.
- After each `revise` step and its check, the `summary` agent runs again with the findings, so each review reads a summary of the notes it reviews.
- The loop ends at the first review with no finding, or after the third review.
- The reviewer also writes the text of its findings as the output `open` of the verdict file, and the report uses that text.

The report:

- `tools/report.sh` writes `<RESEARCH_OUT>/<item id>.md`, which starts with the summary.
- When the third review still finds a problem, it writes `<RESEARCH_OUT>/<item id>-unreviewed.md` with the open findings on top, and exits with code 3. That code is in `stop_on_rc` of the step, so `beehave status` shows `stopped at command:report#<n> rc 3` and not a plain success.

The library `review_fix.star` does not fit this workflow:

- Its fix step needs an implementer agent and resumes that session.
- It needs a model for each of its steps, and it sets its own forbidden commands for code.
- Its loop goes back to one fix step, while the notes come from one agent for each sub-question, and a failed check goes back to the research step of each file that failed.

#### `examples/desk/`

A refund desk for casework with no code change. Two workflow files share a policy table (`policy.star`) and a clerk step (`clerk.star`).

`workflow.star` runs a shift:

- The intake scripts read the email and chat channels and mask personal data. Each new message becomes a case item that runs in its own run.
- The clerk agent has no edits and no write, and it proposes an intent.
- The workflow checks the intent against the policy row, asks the system of record for eligibility, and approves by the tier. A contain tier approves under its limit, and otherwise holds the case with `wait`.
- The saga is a list of `write` calls with a read-back and an undo, so a failed reply undoes the credit.
- A refusal goes to the exception queue by its reason code.

`eval.star` scores an intent against a gold set with a cost-weighted confusion matrix, and compares the judge agent with the people who graded the same replies. Its result is a proposal for a tier change, never the change.

The example runs its cases one after the other, because two runs that start together can fail on the git config lock when each makes its work folder.

`examples/desk/tests/run.sh` tests the scripts against stand-ins for `curl` and `sendmail`. The scripts `desk*.txt` in `cmd/beehave/testdata/e2e` run both workflows with a fake agent.

#### `examples/incident/`

An incident desk. `workflow.star` has two entries:

- `main` is a watch. It reads the alert spool, opens one child item for each alert, and starts one run of `incident` for each.
- `incident` pages the first role, reads the diagnostics with GET calls, and asks the `analyst` agent (no edits, no write, forbidden command kinds) for one mitigation.

`policy.star` holds the runbook table. Each row has a risk (`safe` or `risky`) and a verify time.

- The desk checks the proposal against the table, then pages the roles of the chain for the severity one after the other.
- Each role has an answer deadline, set as the `timeout` of a `wait`, so a restart keeps the question and the deadline.
- A safe row runs when at least one role was paged and nobody answered in time.
- A risky row never runs without an approval.
- If every page fails, nobody was reached. The desk holds the incident for a person and runs nothing.

The saga saves the earlier state, runs the mitigation under an idempotency key, and checks the health of the service. A failed write or a failed health check calls the rollback script, and the text of the handoff states whether the restore worked. The `postmortem` agent drafts a postmortem from the timeline file.

The ops API (`OPS_API`) is the one part that the example does not ship. `examples/incident/tests/run.sh` tests the scripts against a stand-in for `curl`. The scripts `incident*.txt` in `cmd/beehave/testdata/e2e` run the workflow with a fake agent.

#### `examples/research-desk/`

A research desk. The item is a question, and the result is one report page on a wiki.

1. A planner agent splits the question into sections. The plan is kept in a store of plain files (`tools/store.sh`), so a run on an item that has a plan keeps it.
2. One child run for each section searches and fetches by script, and a reader agent extracts claims with exact quotes. `tools/claims.sh` tests each quote against the page snapshot. It refuses a quote that holds a newline or a tab, because the snapshot is searched line by line.
3. A fresh judge agent grades the claims. It also gets one planted false claim: a judge that accepts it is void, and no verdict is recorded.
4. A writer agent writes the section from the supported claims.
5. The program gate `tools/cites.sh` needs a cite of a supported claim in every paragraph, and a whole number of a cited quote for every number.
6. An abstract agent follows.
7. A person reviews the draft with `wait`: `approve`, or `redo N: a note`, which runs section N again as a new generation.
8. After the approval, the program gate `tools/staleness.sh` reads every cited page again. It fails when a quote left its page, or when it tested no quote.
9. `tools/publish.sh` writes the page with a key from the run and the hash of the report, then reads it back and compares the hash.

- The review and the budget pause have a `timeout`. On a timeout (`timed_out` in the result of `wait`), the run fails with the reason, and the plan and the finished sections stay in the store. `beehave resume <item>` and a new `beehave run` continue at the same wait (`research-desk-recover.txt`).
- A call counter in the store caps the agent calls of one item.
- The scripts `research-desk*.txt` in `cmd/beehave/testdata/e2e` run the workflow with a fake agent and a stand-in for `curl`. `research-desk-tools.txt` runs the scripts of `tools/` alone.

#### `examples/approvals/`

An approval desk. One item is one request for sign-offs on a document.

`policy.star` holds the quorum as data. For each kind of document it holds:

- the roles with their members;
- the count each role needs;
- a required flag (a reject from a required role is a veto);
- `min_roles`;
- the window, the extension and the reminder points;
- the people who get the question at the deadline.

The flow:

- `scripts/submit.sh` stores the document in a git repository, and `scripts/open.sh` copies the rule into the ledger.
- `workflow.star` waits for a change in the ledger. The deadline is the `timeout` of a `wait`, so a restart keeps it.
- A sign-off is a message signed with `ssh-keygen -Y sign` (`scripts/sign.sh`). `scripts/intake.sh` verifies it against `allowed_signers` and refuses the rest. A delegation is a signed message too.
- `scripts/tally.sh` and `scripts/tally.jq` read the ledger and give the state: satisfied roles, veto, stale sign-offs and the deadline. A sign-off on an old hash is stale, and the request asks again.
- The extension question is a wait with `to` (the `escalate_to` people) and `choices`, so the ledger row of the extension names the person. The engine does not prove the name, so the sign-offs stay signed files.

The ledger:

- It is a SQLite table with triggers that refuse update and delete.
- Every append takes `flock`, and each row carries the hash of the row before it.
- `scripts/audit.sh` and `audit.star` check the chain and every signature. A cut tail shows only when the verifier passes the head that `scripts/finalize.sh` printed.

The notifier (`NOTIFY_API`) is the one part that the example does not ship. `examples/approvals/tests/run.sh` tests the scripts against a stand-in for `curl`. The scripts `approvals*.txt` in `cmd/beehave/testdata/e2e` run the workflow with a fake agent, including a restart while the run waits and a deadline that passes.

#### `examples/maintenance/`

A weekly job over a list of repositories. A scheduler outside beehave starts `scripts/tick.sh`, because beehave has no schedule.

- The tick syncs a clone, runs `plan.sh`, and files one item for a repository that has news.
- `plan.sh` skips a repository that ran this week, has nothing outdated, has the same fingerprint of updates as the last bump, or has an open bump pull request. A skip costs no run.
- `bump.star` runs one `update` agent step, which runs `apply.sh`. Only an agent step makes the worktree of an item, so a deterministic update needs one.
- Then the project gate runs, and up to two `fix` agent rounds follow. A red result waits with `wait(choices = ["retry", "hold"], timeout = RED_TIMEOUT)`. DBOS keeps the question and the deadline across a restart.
- `pr.sh` pushes the branch and opens a draft pull request.
- The state of the last bump and the run log are files outside the worktrees, because a step cannot read an earlier run and beehave deletes finished runs after seven days.
- `report.star` runs `report.sh` and `post.sh` for the week.
