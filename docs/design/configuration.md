# Configuration

Part of the [beehave architecture](../architecture.md).

The config folder holds Starlark files that declare the agent limits, the runners and the projects. The loader validates them, and `BEEHAVE_*` variables override the file values.

## Configuration

### Files of the config folder

| File | Calls |
| --- | --- |
| `runners.star` | `agents(...)`, `pool(...)` and one `runner(...)` for each login |
| `projects/<name>.star` | `project(...)` once, and a `host(...)` value for a project with a build host |

Each file is Starlark with the step limit of the workflow files. The limits and the runner of a step are keys of its block in the workflow file.

### `agents(...)`

| Key | Meaning | Default and limits | Override |
| --- | --- | --- | --- |
| `max_concurrent` | The global limit on agent processes. | | |
| `max_review_concurrent` | The same limit for the agent steps with edits = False. | | |
| `per_login` | The most agent steps that one login runs at once, in each of the two agent queues. The gate and merge queues keep one run for each repository. | Default 1. The least is 1. The most is the lower of `max_concurrent` and `max_review_concurrent`. | `BEEHAVE_AGENTS_PER_LOGIN` |
| `concurrency` | The mode of the cap of the agents queue: `"static"` or `"adaptive"`. | Default `"static"`: the queue holds `max_concurrent`. Any other value is refused. | `BEEHAVE_AGENTS_CONCURRENCY` |
| `max_turns` | The cap of turns for one agent run. | 0 means no cap. | |
| `max_budget_usd` | The cap of dollars for one agent run. | 0 means no cap. | |
| `context_window_tokens` | The context window of the agent model in tokens. The `max_context_percent` of an `agent` call counts against it. | Default 200000. 0 means the window is unknown, and no context limit fires. A negative value is refused. A model with a larger window needs the key set. | |
| `max_run_budget_usd` | The cap of dollars for the agent steps of one run in all. | 0 means no cap. | `BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD` |
| `max_tree_budget_usd` | The cap of dollars for the agent steps of an item tree in all: the root run and every run it spawns. A spawned run uses the cap of its root. | 0 means no cap. | `BEEHAVE_AGENTS_MAX_TREE_BUDGET_USD` |
| `max_week_budget_usd` | The cap of dollars for the agent steps of all items in any seven days. The sum counts the agent steps that ended in the last seven days, and a step that ended exactly seven days ago counts. | 0 means no cap. A `retention_days` from 1 to 6 is refused with a week cap, because it deletes finished runs before they leave the window. Two steps that start at once can both start under the cap. | `BEEHAVE_AGENTS_MAX_WEEK_BUDGET_USD` |
| `trace_parent` | When true, each agent process gets one W3C `TRACEPARENT` that names the run and the step. A `command` step gets none. | Default false. A value that is not true or false is refused. | `BEEHAVE_AGENTS_TRACE_PARENT` |
| `review_poll_minutes` | The minutes between two review polls. | 0 means no poll. | `BEEHAVE_REVIEW_POLL_MINUTES` |
| `review_lookback_days` | The days that the review poll looks back for replies. | Default 3. The least is 1. The most is 365. | `BEEHAVE_REVIEW_LOOKBACK_DAYS` |
| `retention_days` | The age in days at which `beehave run` deletes a finished run. | Default 7. 0 keeps every finished run. The most is 36500. | `BEEHAVE_AGENTS_RETENTION_DAYS` |

### `pool(...)`

`pool(...)` sets the usage caps of the runners, in percent. Call it once. Each key is the name of a usage window that an agent reports, and each key is optional. `beehave run` picks the runner that is under every cap of its agent.

Each agent declares its windows and the default cap of each. A cap applies to every agent that declares its window. A cap on a window that the agent of a runner does not report does not apply to that runner. Two agents that declare one window name declare one default.

A key that no agent declares is refused, and the message lists the names. A key that ends in `_max` after the name of a declared window, such as `five_hour_max`, is refused too, and the message names the form `five_hour = N`.

| Key | Meaning | Default and limits | Override |
| --- | --- | --- | --- |
| `five_hour` | The cap for the five-hour usage of a Claude runner. | Default 90. Above 0 and up to 100. | `BEEHAVE_POOL_FIVE_HOUR_MAX` |
| `seven_day` | The cap for the seven-day usage of a Claude runner. | Default 95. Above 0 and up to 100. | `BEEHAVE_POOL_SEVEN_DAY_MAX` |

The override of a window is `BEEHAVE_POOL_<NAME>_MAX`, with the name of the window in upper case.

### `runner(...)`

`runner(...)` declares one agent login. Call it once for each login. Each name is used once.

| Key | Meaning |
| --- | --- |
| `name` | Required. The label of the runner. |
| `agent` | Required. The agent CLI: `claude` or `opencode`. |
| `config_dir` | Optional. The config folder of the login. beehave sets `CLAUDE_CONFIG_DIR` to it for a Claude run. `~/` is the home folder. An empty `config_dir` means the default folder of the CLI under the home folder (`~/.claude` for Claude). The transcript of a session and the skills of a step come from that folder. A runner with no `config_dir` and no default folder, or whose folder does not exist, does not start. |
| `keychain` | Optional. The name of the macOS Keychain entry that holds the login token, for a machine with no logged-in profile. |

### `project(...)`

The project arguments are `repo`, `source`, `tracker`, `claims`, `forge`, `base_branch`, `gate`, `gate_steps`, `test_report`, `test_command`, `test_paths`, `notify`, `pin_file`, `gate_program`, `visibility`, `sandbox`, `sandbox_read`, `sandbox_deny`, `sandbox_enforcer`, `gate_network`, `gate_allow_write`, `pr_label`, `max_spawn_depth` and `build_host`.

`max_spawn_depth` is the deepest level below the run a user started at which `spawn` may start a run. It defaults to 3, so a research desk can spawn sources and each source can spawn claims. A project that must not fan out further than one level sets 1. A value below 1 is refused. `beehave check workflow --project <name> --dry-run` uses the cap of the named project.

`pr_label` is the label of every item pull request that the `pr_open` step opens. It defaults to `made-by-beehave`, and an empty value is refused. The forge creates the label first when the repository has none. The sync and integration pull requests of the remediation example carry the same label, because the steps `round_sync` and `integration_pull` read the `pr_label` of the project.

`test_paths` is a list of globs for the test files, for example `["**/*_test.go", "**/testdata/**"]` or `["tests/**"]`. The revert run copies the added and changed test files of a candidate onto the base code, so a new test that needs a changed helper in a test file still builds there. A project with new tests and no `test_paths` gets a refusal from the `before_review` function of `review_fix.star`.

`gate_program` is a list of the repository paths that hold the program of the gate, for example `["Makefile", "GNUmakefile", "makefile", "scripts/"]`. A directory ends in `/`. The default is an empty list, and the gate then runs as before. An entry uses only the characters A-Z, a-z, 0-9, `.`, `_`, `-` and `/`. The config refuses an entry that is empty, absolute, holds `..`, holds a character outside that set (a space, a control character, a non-ASCII character, a glob character, a leading `:`), is `.`, has a component equal to `.git` in any case, or is not a plain path. It refuses a duplicate, an entry whose path components start with the components of another entry (`scripts` and `scripts/x`, or `a` and `a/b`), and an entry that a `test_paths` glob can match. A project with no `source` and no `forge` refuses the key, because the program is read from the base branch. A path that the base branch lacks is legal: it means that the path must stay absent. `beehave check config`, `beehave check workflow` and `beehave run` load the key the same way. A gate on a build host and a local gate use the list (see `gate.md`). The key has no environment variable and no flag.

`notify` is a list of strings: a program and its arguments, for example `["ntfy", "publish", "beehave"]`. A wait for the owner is a `wait`, the wait for the owner after a gate job is lost, or the wait of a `merge` that asks the owner to decide a scope, or the wait of a `merge` with `hand_off` for the owner to merge. When one opens, the engine runs the list once and adds two arguments, the item id and the question. The engine runs the program with no shell, so each argument stays one word, and it stops the program after 30 seconds. A program that fails, does not start or hangs gets a line in the log and never fails the wait. A reminder, a timeout and a dry run send no notice. A lost-job wait that opens again sends one notice for each opening. A `merge` with `hand_off` that is canceled and cannot return a ready pull request to draft sends a notice that names the item. A project with no `notify` sends no notice. A run that waits for the owner resumes cleanly if you add or remove `notify` meanwhile: it sends no late notice and it does not repeat one. The config refuses a `notify` that is not a list of strings, an empty list and a list whose first item is empty.

The project also takes the config block of the adapter that it chooses. The adapter declares the keys of its block, and the config passes the block to the adapter unread. The beads adapter declares the `beads` block with one key: `beads = beads(prefix = "proj")`. A project that chooses the beads tracker or the beads claim store must give the block. The config refuses a block of an adapter that the project does not choose, and the refusal names the adapters that the project does choose.

The `host` call takes the block of the host adapter in the same way. The `skypilot`, `ssh` and `slurm` adapters each declare their own block, for example `host = host(provider = "ssh", disk_floor_gb = 50, ssh = ssh(user = "ci"))`. The `host` call also takes `lost_job_wait`, a duration from `"1m"` to `"24h"` with the default `"60m"`; see `docs/design/hosts.md`. It takes `lost_after` and `stop_limit` too, two durations above zero with the default `"5m"`; see `docs/design/hosts.md`. It takes `gate_slots` too, the number of gates that the host builds at once, from 1 with the default 1; see `docs/design/hosts.md`. It takes `credentials` too, whose only value is `"none"`: the host gets no SSH agent, key, X display or secret variable of the owner, and `beehave check config` checks the ssh config of the host for it; see `docs/design/hosts.md`. The config refuses the block of a host adapter that `provider` does not name, and the refusal names the block and the provider. The adapter reads and checks its keys when it builds the host.

The project also takes `credentials`, a dict from a role name to a `credential(file = ...)` or `credential(command = [...])` block. The only role is `"writer"`. A `file` lies in `~/.config/beehave/credentials/`, has mode 0600 and the user as owner, in a folder of mode 0700. A project with `credentials` starts only when its `source` is empty or an `https://` address: the token serves https only, so ssh, `host:path`, `git://`, `http://`, `file://` and a local path are refused at start. The loader writes the scheme and the host of a `source` in lower case. A `command` runs once at start with no shell and sees only `PATH`, `HOME` and `USER`. The token is the trimmed output, on one line. The key is optional. See [trust](trust.md#credential-config).

The project also takes `vars`, a dict of the settings of one workflow. It is optional and is empty when left out. The engine reads no key and no value. The remediation example reads its people, epics, rules, limits, traps, changelog and board from `vars`. The example checks each of these keys once, at the start of a run and before any step: a list or dict of the wrong type or with a blank entry, a `changelog` that is not a relative path to a file (no leading `/` or `./`, no `..`, no trailing `/`), and a `board` with a missing or wrong key. Each refusal names the key.

If a project file writes `people`, `epics`, `gate_files`, `conventions`, `brief_rules`, `stacked_rules`, `limits`, `traps`, `token_rules`, `changelog` or `board` at the top of `project()`, the loader refuses the file. The message names the key and says to move it into `vars`.

The `vars` keys must be strings. The values must be `None`, a bool, a string, a number, a list, a tuple or a dict, nested to any depth. The loader refuses a function, a non-finite number and a key that is not a string, because a run records the facts as JSON. A workflow reads `vars` with `facts().vars`.

#### The board

The remediation example reads the GitHub Project board of the project from the key `board` of `vars`: `vars = {"board": {"number": 3, "started": "In Progress", "merged": "Done"}}`. With no `board` key, the example makes no board call.

| Key | Meaning |
| --- | --- |
| `number` | A whole number above 0. |
| `started`, `merged` | The Status columns that the example sets. |
| `owner` | Optional. The login that owns the board. It defaults to the owner of the repository. |

#### The round settings

The engine has no round settings. The remediation example keeps its own under the key `round` of `vars`: `vars = {"round": {...}}`. With no `round` key, the example queues every open item. The function `settings` in `examples/remediation/workflow.star` reads `facts().vars` and refuses a missing or wrong setting with a message that names the key and the fix. The loader checks none of them.

| Key | Rule |
| --- | --- |
| `base` | Required. The branch of the round, such as `integration/r2`. The round id is `round/<base>`. |
| `owner` | Required. The login of the owner. |
| `lane_cap`, `fix_rounds` | Required. A whole number of 1 or more. |
| `label`, `items` | Required: set exactly one. `label` is the tracker label that marks the items of the round. `items` is a list of item ids. The example refuses neither, both, a blank label and an id that is not an item. |
| `next_base`, `owner_email`, `source_checkout`, `base_result` | Optional text. Empty when left out. |
| `acceptance` | Optional text. The path of a script. After each batch that lands an item, the round runs `sh <acceptance>` on the build host with a limit of 30 minutes. A script that fails stops the round. Empty means none. |
| `union_gate` | Optional list of words. The program that gates the union of a batch, in place of the gate of the project. Empty means the gate of the project. |
| `integration_pr` | Optional. A whole number of 0 or more. 0 means the round finds the pull request. |
| `wait_hours` | Optional. A whole number of 1 or more. The default is 24. |
| `merge` | Optional bool. The default is false. |
| `hand_off` | Optional bool. The default is false. Set it when the owner merges the approved pull requests: the round calls `merge` with `hand_off`, which marks them ready and waits for the owner, and lands none itself. An item whose pull request is closed, whose head moves after the ready mark, or that gets a change request, a conflict or an unresolved thread holds with the reason. It takes effect only when `merge` is true. |
| `base_reds`, `allow_paths` | Optional lists. The globs of `allow_paths` follow [the setting `allow_paths`](agent-runs.md#the-setting-allow_paths) of an `agent` step, which lists the patterns that it refuses. |
| `base_reds_file` | Optional. A path from the folder of the round. The default is `../base-reds-<base>.md`, with each `/` of `base` written as `_2F`. It lies in the folder of the project and outside every item folder, and each round has its own file. A file in the folder of the round is a change that is not committed, and the gate refuses the folder. A relative path that leaves the folder of the project, such as `../../reds.md`, is refused. The file is the record for the owner: the lanes get the same list in the data file of their step. |
| `lane_kind` | Optional: `impl` (the default), `papers` or `design`. A `papers` or `design` round needs a non-empty `allow_paths`. |
| `steps` | Optional dict from a step name to `{"model": text, "runner": text}`. The steps are `plan`, `implement`, `review`, `fix`, `fix_late`, `design`, `take_review`, `sync_review`, `sync_resolve`, `queue_audit` and `settle`. A step that the dict leaves out keeps its default model and the pool pick. A review on the model of the implementer is refused when two or more models are in use. See "The steps and the limits" in `durable-loop.md`. |
| `design_after` | Optional. A whole number from 1 to 100. The default is 2. |
| `void_reruns`, `void_asks`, `escalate_after` | Optional. A whole number from 0 to 100. The defaults are 2, 3 and 3. |

The example refuses any other key under `round`.

### What the loader refuses

- A top-level variable other than a `def` function or a name that starts with `_`. The loader names its position, because a config value belongs inside a constructor call.
- A `host(...)` call, or the call of a host adapter block such as `skypilot(...)`, that no argument holds.
- `print`.
- An old `.hcl` file. The refusal names the file.

### `beehave check config`

`beehave check config` loads the folder and prints one line for each problem, with the file path. It also runs the workflow check on each workflow file, and reports it together with any adapter problem. It exits 2 when it finds any problem.

### Environment

- `config` is the only package that reads the environment.
- The `BEEHAVE_*` variables override the file values.
- `BEEHAVE_CONFIG` names the config folder. `BEEHAVE_STATE_DIR` names the state folder.
- The state folder must be absolute, owned by the user and mode 0700.
- `cmd/beehave` sets `umask 077`.
- `cli.Execute` refuses to start when a `DBOS__*` variable is set.
