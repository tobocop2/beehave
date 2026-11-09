# Workflow files

Part of the [beehave architecture](../architecture.md).

A workflow is one Starlark file. The engine calls its `main` function with the work item, and each builtin call except `struct` and `json` runs as a recorded DBOS step, so a restart replays the file and never runs a finished step again. This part lists the builtins, their arguments and results, the pinning of files, the static check and the examples.

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

The engine predeclares `agent`, `batch_merge`, `command`, `earlier_calls`, `env`, `facts`, `gate`, `gather`, `host_teardown`, `host_up`, `item_assign`, `item_comment`, `item_create`, `item_read`, `item_status`, `merge`, `now`, `pr_assign`, `pr_comment`, `pr_open`, `preflight`, `queue`, `sandbox`, `spawn`, `wait`, `write`, `struct` and `json`. `workflow.Builtins` is the one list of these names.

`json` is the `json` module of the Starlark library (`json.decode`, `json.encode`, `json.indent`). It is pure, so it records no step.

#### Recorded steps and replay

- Each builtin except `struct` and `json` takes keyword arguments, runs as a recorded DBOS step, and returns a struct of its outputs.
- The step is recorded under the name `<builtin>:<name>#<n>`. `name` is the argument of the call. `n` counts the calls of that builtin, whatever their names, so a call in a loop gets a new record in each round.
- Every `merge` and `wait` call also records a step under this name, after the work of the call.
- A restart replays `main` from the top. Each call finds its record and returns the recorded outputs, so a finished step never runs again. The same code makes the same calls in the same order.

#### The folder of a program

- A `command` call, and a `gate` call that names a program, run in the worktree of the item.
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

### `facts`

`facts()` takes no argument and returns the facts of the project of the run as a struct.

| Field | Kind |
| --- | --- |
| `repo`, `source`, `forge`, `tracker`, `base_branch`, `gate`, `build_host` | The project value of that name. |
| `people`, `epics` | Dict. |
| `gate_files`, `conventions`, `brief_rules`, `stacked_rules`, `limits`, `traps`, `token_rules` | List. |
| `changelog` | Text. |
| `board_number` | The board number, 0 when the project has none. |
| `board_owner`, `board_started`, `board_merged` | The other values of the board. |

- A fact that the project file leaves out is empty text, an empty dict or an empty list, never a missing field.
- The call is a recorded step named `facts#<n>`, so a replay returns the facts the run started with and never reads the config again.
- A workflow passes the struct in the `inputs` of an agent call, and the prompt reads it as `{{ .facts.<field> }}`. The agent reads no file for it.
- In a dry run the facts are empty.
- `beehave check workflow` refuses a field that `facts()` does not return.

### Loading files and pinning

A file loads another file with `load("//file.star", "name")` for a file in its own folder, or `load("@beehave//review_fix.star", "review_fix")` for a library in the binary. The library `review_fix.star` holds the review and fix loop of the remediation method as a function. A project that wants another policy copies it into its folder.

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
| `item_create(title, key, body, parent, assignee, severity, operator, forbid_text, name)` | `id`, `created` |
| `item_status(id, status, name)` | `id`, `status` |
| `item_comment(id, text, key, forbid_text, name)` | `id` |
| `item_assign(id, assignee)` | `id` |
| `item_read(of, name)` | `found`, `id`, `fields`, `comments`, `parent` |

The `key` is required, and the engine joins it to the run id.

#### `item_create`

- When a resume follows a lost step record, `item_create` returns `created` false and the id of the first item.
- `assignee` is optional. The default is no assignee.
- `severity` is optional, one of `P0` to `P3`. The call refuses any other text before it writes.
- `operator` is an optional bool.

| Tracker | `severity` | `operator` |
| --- | --- | --- |
| GitHub | Filed as the label of that name. | Files the label `operator`. |
| beads | Filed as the priority. | Files the label `operator`. |
| `local` | Refused, because it keeps no severity. | Refused, because it keeps no labels. |

#### `item_status`

A status is `open`, `closed` or `hold`. On GitHub, `hold` needs a label named `hold` in the repository.

#### `item_assign`

`item_assign(id, assignee)` sets the assignee of a tracker item and returns `id`. `item_create` also takes an assignee.

| Tracker | Result |
| --- | --- |
| `local` | Keeps no assignee, and refuses both `item_assign` and the `assignee` of `item_create`. |
| GitHub | Adds the login and removes no one. |
| `beads` | Keeps one assignee. Sets it when the item has none or the same login. Refuses an item that another login holds, and writes nothing. |

#### `item_read`

`item_read(of, name)` reads an item.

- `of` is `"self"` (the default, the item of the run) or `"parent"`.
- `fields` is a dict of the fields that the tracker declares for the item: `id`, `title`, `body` and `severity`.
- `comments` is the list of the comment texts, oldest first, with no `beehave-key:` line.
- `parent` is the id of the parent of the item read, or an empty text.
- With `of = "parent"` and an item that has no parent, `found` is false and the other values are empty.
- The read is a recorded step, so a replay returns what the first start read and does not read the tracker again.
- The comments and the parent come from the tracker through the optional reader contract. The `local` tracker and beads serve both. GitHub serves both through the REST endpoints for comments and for the parent issue; an issue with no parent gives an empty `parent`.
- A tracker with no reader fails the call with an error that names the tracker.
- In a dry run an item has no comments and no parent.

### Pull request builtins

`pr_comment` and `pr_assign` refuse a project with no repository, and fail for an item with no pull request. In a dry run they act on the dry-run pull request and write nothing.

#### `pr_comment`

`pr_comment(text, key, forbid_text)` adds a comment to the pull request of the item and returns `number`. The forge reads the comments first, and posts only when none holds the line `beehave-key: <run id>:<key>`. A replay after a crash therefore posts once.

#### `pr_assign`

`pr_assign(assignee, linked)` assigns the pull request of the item and returns `number` and `issues`.

- When `linked` is true, it also assigns each issue that merging the pull request closes, and `issues` lists them.
- The forge keeps one entry for a login, so the same call twice changes nothing.

#### `pr_open`

`pr_open(title, base, body, issue, parent, forbid_text, name)` opens the draft pull request of the item and returns `number`, `created` and `stack`.

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

### `forbid_text` on public text

The four steps that post public text (`item_create`, `item_comment`, `pr_comment` and `pr_open`) take an optional `forbid_text`, a list of RE2 patterns. It has the same meaning as the `forbid_text` of `merge`.

- The call tests each line of the text it posts with the matcher of the merge: the title and the body of `item_create` and `pr_open`, and the text of a comment. A line that matches refuses the step before the post.
- The match is per line, as in `merge`, so a pattern such as `^$` matches any blank line.
- The refusal names the pattern and the argument, never the text. The pattern lands in the run error and in local logs, so a pattern must not be a secret.
- Each call sets its own patterns, so core holds no word list. A call that leaves `forbid_text` out posts without a scan.
- `beehave check workflow` refuses a `forbid_text` literal that is not a list of RE2 strings, on these four steps and on `merge` and `batch_merge`.
- The setting changes no recorded output, so the version of the step records stays the same.

### Build host builtins

#### `host_up`

`host_up(name, host)` launches the build host of the project when it is absent. It returns `started`, true only when this call launched it, and `host`, the cluster that the call used.

- The optional `host` names one of the hosts that `build_host` of the project declares. Any other name is refused before a host is built. An empty `host` gives the first declared host.
- The step records the cluster, so a replay returns the first choice.
- A gate, its key and a `command` call with a `host` run on that recorded cluster. A run that did not call `host_up` uses the first declared host.
- A project with no host, and a build with no host keeper, start nothing and give `started` false.
- The start is a recorded step, so a replay does not launch again.
- The example calls `host_up` first in `main` only when `HOST_UP` is "true".

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
- The example calls it in `finish`.

### `preflight`

`preflight(head, base, hold, own, id, name)` reads the pull request of the item `id`, or of the item of the run when `id` is left out. It returns `ok`, `stop`, `code` and `reason`.

It checks, in this order:

1. Merged or closed.
2. The head branch.
3. The author. An open pull request by a login that is not in `own["logins"]` gives CLAIMED, exit code 10. An empty `own` checks no author.
4. The `hold` label.
5. A draft.
6. The expected head.
7. The expected base.

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

### `queue`

`queue(name, hold)` reads the open items of the tracker and the open pull requests of the forge in one recorded step. It returns `rows` and `free`.

Each row has `item`, `state`, `severity` and `pr`. The state:

| State | When |
| --- | --- |
| `running` | The item has an open run. |
| `failed` | Its last attempt ended without success. |
| `done` | Its last run succeeded. |
| `hold` | The tracker holds the item, or its pull request has the label that `hold` names. |
| `ready` | Otherwise. A held item is never `ready`. |

- The rows sort by severity, then by item id as byte strings.
- `pr` is the number of the lowest open pull request whose head branch is the item id, or 0.
- `free` is the cap of the `items` queue less the runs that the library lists on it as pending or enqueued, so the engine keeps no count.
- A read of the tracker or the forge that fails ends the step with an error.
- A project with no forge has no pull requests.
- A build with no queue reader fails the call.

A run that `spawn` started reads the hold of its item in the tracker again before its first step. If the item is on hold, the run stops with the stop `queue` and the code of a held pull request, and `gather` reports it as `stopped`.

### `merge`

A `merge` step has these settings besides `head`, `merge` and `name`: `repo`, `base`, `hold`, `own`, `approved`, `tree`, `forbid_text` and `children`. A file keeps no login or address: it reads them with `env("<NAME>")`.

| Setting | Meaning |
| --- | --- |
| `repo` | The repository. It must be the repository of the project, or the step refuses. |
| `base` | The branch: the integration branch, never the default branch. |
| `hold` | A label. A pull request with that label is never merged. Case does not count. |
| `own` | `{"logins": [...], "emails": [...]}`, the identities of the owner and of the runners. |
| `approved` | A boolean that the file computes, such as `review.verdict == "approve"`. |
| `tree` | The tree id that the gate recorded as its output `tree`. |
| `forbid_text` | A list of RE2 patterns for the lines of the pull request body and of each commit message. |
| `children` | `refuse` (the default) or `retarget`. |

#### The `tree` proof

- After the merge, the step compares the tree of the base tip with `tree`, and stops on a difference.
- A call without `tree` asks for no proof.
- A call that merges and sets `tree` to the empty text is refused, because a gate that runs a program, or a resumed host job whose head is not the current head, records no tree.
- A call that does not merge never reads `tree`. A workflow that merges passes `tree`.
- The proof runs after the merge. If the base moved between the gate and the merge, the merged tree was never gated, and the run stops as refused. The merge is not undone, and the item ends failed with the pull request merged.
- The remediation example does not merge, so it passes no `tree`.

#### The `steps` proof

`steps` is the output `steps` of the gate: a list of `{"name", "status"}`, one for each step, where the status is `pass`, `fail` or `unknown`.

- A call that merges is refused, before it marks anything ready, when it leaves out `steps`, when the list is empty, or when any step is not `pass`.
- The workflow check refuses such a call before the run when `steps` is missing or an empty list literal and `merge` is not `False`.
- A call that does not merge (`merge = False`, or no `merge`) needs no `steps`.

A gate gives these steps:

| Step | Status |
| --- | --- |
| `command` | Its exit code, or the state of its host job. |
| `tests` | `unknown` when the report is missing, stale, not valid or empty, or when the command failed, because the report is read only after an exit code 0. On a build host it is `pass` only when the job succeeded. |

A gate that runs a program gives the step `command` only.

#### The pinned head

The step pins the merge to the head of the pull request, not to `head`, when a rewrite left that head with the same tree as the reviewed `head`: the review stands for it. A head with another tree is refused.

#### `own` and `approved`

Nobody merges their own work, and our pull requests merge only on a verdict that another step gave.

- A pull request of an `own` identity merges only when `approved` is true.
- If `approved` is not set, the step refuses our own pull requests.
- A pull request of any other author does not depend on `approved`.
- A standing changes-requested review of another person blocks the merge in every case.
- The review step must not be the step that wrote the code. Nothing checks this, because the file does not say which step wrote the head.

#### What the merge step refuses

When it runs, the merge step refuses:

- `merge = True` with no `repo`, `base` or `own`.
- An `own` that is not a dict with `logins` and `emails`, each a list of strings, or that has an unknown key or two empty lists.
- A `hold` with a comma.
- A `forbid_text` item that is not a string, is empty or is not RE2.
- An unknown `children` value.

A value of another type fails the step and names the value. An environment name that is unset fails the step and names the variable.

### `batch_merge`

`batch_merge(prs, base, repo, hold, own, forbid_text, gate_run, integration, name)` lands a list of `struct(item, head)` with one gate run on the union tree of the base tip and all heads. It splits the list when the gate is red. A forge with a native merge queue needs no `batch_merge`.

It returns `merged`, `rejected`, `deferred`, `tip`, `tree`, `gates`, `findings`, `note` and `unproven`.

- Each entry of `rejected` and `deferred` has `item` and `reason`.
- Each entry of `unproven` has `item` and `reason`. An item of `merged` that `unproven` names has not landed.
- `findings` holds one line for each finding. `note` says why no read ran (see below).

- `name` is required, and each call of a run needs its own.
- The union is gated in the worktree of the item that runs the workflow.
- Each member merges through the same guards as `merge`, pinned to its head and to the tree of its prefix.
- Git 2.38 or newer is the minimum on the gate host.
- For each member, the step `batch:<name>.intent#<item>` records the pull request as OPEN before the step `batch:<name>.merge#<item>` merges it. A resume after a crash therefore counts a merge that the forge already made as done.

#### Checks after the batch lands

- One recorded step `batch:<name>.prove` checks each merged member.
- One recorded step `batch:<name>.issues` reads the issue of each landed item, for a project of the GitHub tracker whose item ids are issue numbers. An issue that is not closed is a finding.
- If `integration` names a pull request that is MERGED, the step also reads its closing references. An issue that it closes and no landed item names is a finding.
- A read that fails is a finding, never a pass.
- `note` says why no read ran: a base that is not the default branch with no `integration`, an integration pull request that is not merged, or a tracker with no issue numbers.
- The read never closes or reopens an issue. A finding does not fail the call, because the batch has landed and the merge cannot be undone. The workflow reads `findings` and decides what to tell the owner.

### `spawn` and `gather`

#### `spawn`

`spawn(items, entry="main", name="")` starts each item as its own run of the same workflow file, and returns `runs`, a list of structs with the fields `item` and `run`.

- Each child run executes the function `entry(item)` on the bytes that the parent pinned, as a child workflow of the parent on the `items` queue. The queue holds at most `agents.max_concurrent` runs.
- A child run that finished with success is reused and does not run again.
- A cancelled run, or no run, starts the next attempt.

`spawn` refuses:

- An item that has an open run.
- An item whose last attempt failed.
- The parent itself.
- An item that is in `items` twice.
- A call from a run that `spawn` started.

#### `gather`

`gather(runs, fail="return", until="all", name="")` waits for the runs and returns `results`, a list in the order of the call.

- With `until="any"`, the wait ends when at least one run is no longer open, and `results` holds only the runs that ended, in the order of the call. Any other `until` value is refused.
- A second `until="any"` call on the same list returns an ended run again, so a loop removes the ended runs from its list before the next call.
- If `fail="raise"` and a run failed, the call waits for every run, as `until="all"` does.
- Each result has the fields `item`, `ok`, `error`, `merge`, `sent_back`, `closed`, `stopped` and `value`.
- The wait is one recorded step that reads the rows of all the runs, so the record does not depend on the order in which the children end.
- With `fail="raise"`, the step cancels the runs that are still open when it sees a run that failed, and records all the outcomes. The call then fails with the error of that run.
- A restart replays `spawn` and `gather` from the records, and each child keeps its own output.

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
2. Takes a fresh claim for that child, and reopens it on the `items` queue.
3. Forks the parent from that step.

The resume also reopens each sibling that the raise canceled while it was open, so that no child stays canceled and fails the next `gather`.

`spawn` and `gather` then run again. The children that finished are reused, and the new `gather` reads the new outcome of the reopened child.

- The `gather` step records the ids of the runs that its raise canceled, and the resume reads that record before it rewinds the parent.
- A child that a user canceled by hand is not in the record. The resume leaves it canceled, and the next `gather` reports it as a failed outcome.
- The raise cancels the children of each canceled child, so no phase of a canceled child stays in the queue.
- A canceled child gives its claim back on the next tick of the engine, not when the `gather` returns. If a resume comes right after the raise, it can ask you to run `beehave resume` again.
- A failed parent cannot be cancelled, and a new attempt of it is refused until it resumes.

### `command`

A `command` step has four settings besides `run`, `timeout`, `host` and `name`: `max_new_processes`, `stdin`, `env` and `stop_on_rc`.

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
- The engine records `stderr` in the run database like `stdout`, so a program must not print a secret to either stream.
- A run recorded before the field existed has no `stderr`. A workflow that reads it fails on a replay of that run.

#### The environment of the program

The program of a `command` step gets a small environment: the names that an agent run also takes from the caller (the path, home, locale, proxy and certificate variables, and `SSH_AUTH_SOCK`), then the variables of `env`. No other variable of the caller reaches it. The `env` builtin refuses the name of a secret, so a workflow cannot read a token from the environment of the engine.

A `gate` step keeps the environment of the process.

### `write`

`write(run, read_back, undo, stdin, env, timeout, name)` is a `command` step for a program that changes a system outside the run.

- It takes `run`, `stdin`, `env` and `timeout` with the rules of `command`. It takes no `host`, `stop_on_rc` or `max_new_processes`.
- The result has the fields `stdout`, `stderr`, `rc`, `key` and `skipped`.
- A write runs on this machine only.
- The program of `run`, `read_back` and `undo` is pinned like the program of a `command`. A script that changes between the first run and a resume is refused. A `read_back` or `undo` list that is not a literal is counted as a dynamic command.

#### The key

The engine puts a key in the variable `BEEHAVE_WRITE_KEY` of the program, the read-back and the undo. The key is the SHA-256 hash of the run id and the step name, cut to 32 hexadecimal characters. A replay has the same run id and the same step name, so it has the same key, and the outside system can drop a repeat. If the engine stops after the write and before the step is recorded, the replay runs the step again with the same key.

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
- The call takes a `pin`, a commit id such as `check.head`, and starts only when the head of the worktree equals it.

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
3. A review and fix loop of up to five fix rounds (six passes, so the sixth reviews the fifth fix).
4. A wait for the owner.
5. A settle step.
6. A merge step.

`examples/remediation/CONFORMANCE.md` has one row for each rule of the method, with its home (`core`, `file`, `skill` or `drop`) and its status (`gap`, `expressed`, `proven` or `n/a`). A row moves to `proven` when a behavior test runs it. A test in the `workflow` package fails when:

- an ID repeats;
- a home or status is not one of the allowed words;
- an `expressed` row names a builtin call that the example and its library do not hold.

#### `examples/research/`

A workflow for work that is not code, with no forge and no gate. The environment variables `RESEARCH_TOOLS` and `RESEARCH_OUT` name the tools folder and the report folder. The scripts are POSIX shell, and `check.sh` uses `curl`, because the engine code may not import `net/http`.

The steps:

1. A `scope` agent writes up to five sub-questions as the output `questions`, one on each line.
2. One `research` agent for each sub-question writes `notes/<n>.md` in the work folder. Every line of a notes file is a claim, except a blank line and a heading. Each claim ends with one or more `[source: <URL>]` tags.
3. The `command` step `check` runs `tools/check.sh`.
4. A `summary` agent writes a short summary of the notes.
5. A fresh `review` agent, with `follow_ups = "forbid"` and `output_tries = 2`, tries to refute the notes, and checks that the summary states only what the notes state.
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
- It sets its own model tiers and its own forbidden commands for code.
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
