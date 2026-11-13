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
| `max_review_concurrent` | The same limit for the agent steps of the kind review. | | |
| `max_turns` | The cap of turns for one agent run. | 0 means no cap. | |
| `max_budget_usd` | The cap of dollars for one agent run. | 0 means no cap. | |
| `max_run_budget_usd` | The cap of dollars for the agent steps of one run in all. | 0 means no cap. | `BEEHAVE_AGENTS_MAX_RUN_BUDGET_USD` |
| `review_poll_minutes` | The minutes between two review polls. | 0 means no poll. | `BEEHAVE_REVIEW_POLL_MINUTES` |
| `retention_days` | The age in days at which `beehave run` deletes a finished run. | Default 7. 0 keeps every finished run. The most is 36500. | `BEEHAVE_AGENTS_RETENTION_DAYS` |

### `project(...)`

The project arguments are `repo`, `source`, `tracker`, `claims`, `forge`, `base_branch`, `gate`, `gate_steps`, `test_report`, `pin_file`, `visibility`, `sandbox`, `sandbox_read`, `sandbox_deny`, `sandbox_enforcer`, `gate_network`, `gate_allow_write` and `build_host`. It takes `beads = beads(prefix = ...)` when the project uses the beads adapter. The `host(...)` block takes the block of its provider: `skypilot`, `ssh` or `slurm`. The adapter that owns a block declares its keys, and the core passes the block to the adapter unread.

The project also takes facts. All facts are optional and are empty when left out.

| Fact | Type |
| --- | --- |
| `people`, `epics` | A dict of strings. |
| `gate_files`, `conventions`, `brief_rules`, `stacked_rules`, `limits`, `traps`, `token_rules` | A list of strings. |
| `changelog` | One relative path inside the work tree. The example workflow checks it before a merge. |

The loader refuses a blank item, and a blank dict key or value.

#### `board`

The optional `board = board(number, started, merged, owner)` value is the GitHub Project board of the project. With no `board` value, the project has no board.

| Argument | Meaning |
| --- | --- |
| `number` | A whole number above 0. |
| `started`, `merged` | The Status columns that a workflow sets. |
| `owner` | The login that owns the board. It defaults to the owner of the repository. |

#### `schedules`

The optional project key `schedules` is a list of `schedule(name, cron, workflow, item, timezone, catch_up)`. This key declares and validates the schedules only: no engine fires them yet.

| Argument | Rule |
| --- | --- |
| `name` | Lower case letters, digits and hyphens. Unique in the project. |
| `cron` | Read by the parser that DBOS uses (robfig cron with a leading seconds field). A 5-field text is refused, and `@weekly` is accepted. |
| `workflow` | A file. A relative path starts in the folder of the project file, and `~/` is the home folder. |
| `item` | The prefix of the item id. |
| `timezone` | An IANA zone. Defaults to `UTC`. |
| `catch_up` | A bool. Defaults to `False`. |

The loader parses `CRON_TZ=<timezone> <cron>`, as DBOS does. It refuses a `cron` text that starts with `TZ=` or `CRON_TZ=`, because the `timezone` key sets the zone. It refuses a bad schedule with the file and the line.

### What the loader refuses

- A top-level variable other than a `def` function or a name that starts with `_`. The loader names its position, because a config value belongs inside a constructor call.
- A `host(...)` or `skypilot(...)` call that no argument holds.
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
