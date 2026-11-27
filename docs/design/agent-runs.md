# Agent runs, shell rules and lane papers

Part of the [beehest architecture](../architecture.md).

An `agent` step starts one AI coding CLI through an adapter, feeds it the prompt and reads the calls it makes. The setting `forbid_commands` refuses shell commands of listed kinds through a hook and records hits after the run. The setting `allow_paths` limits which files a step may change. `check/shell` holds the shell rules; the lane paper checks live in the remediation tools.

## Agent runs

### Adapters

- `agent` holds the runner and the `Adapter` contract.
- An adapter supplies the profile of one CLI (binary, fixed arguments, login variables) and reads its output stream.
- `agent/claude` and `agent/opencode` are the adapters. Each other agent is its own package. OpenCode has no sandbox, so a sandboxed step with it is refused. Its guard is a plugin file that beehest writes into the run folder and that OpenCode loads. beehest runs no JavaScript.
- `cmd/beehest` holds the table that maps an agent name to the constructor of its adapter. It puts the adapter in the `Spec` of each run. A run with no adapter does not start.
- `agent/agenttest` is the conformance suite. It starts a fake CLI and checks the profile, the arguments and the call reader of an adapter. Every new adapter calls it from its tests.

### Time limits of a step

The keys `timeout` and `idle_timeout` of the `agent` call take a duration text, such as `"90s"` or `"10m"`. `beehest check workflow` refuses any other text.

- `timeout` limits the whole run. The Go module download before a sandboxed step does not count against it (see [sandbox.md](sandbox.md#the-go-module-cache)).
- `idle_timeout` limits the time with no output. Each write of the CLI to its standard output or its standard error starts the time again.
- A key that is left out, an empty text, or a duration of 0 or less sets no limit of that kind. OpenCode is the one exception: with no `timeout`, its total limit is one hour.
- A run that a limit ends has the outcome `timed-out` or `idle`. The step fails with an error that names the outcome and the item, and the item stops.

### The OpenCode adapter and its limits

The adapter starts `opencode run --format json` and reads its event stream. The prompt goes on stdin; no message argument follows `run`. The adapter never passes `--auto`, so a file tool that reaches outside the work folder stays refused, except the folder of the output file of the step: the launch allows it in the config of the run (`OPENCODE_CONFIG_CONTENT`, `external_directory`).

A step author must know these limits:

- **Supported versions.** The adapter supports OpenCode from 1.19.5 up to but not including 3.0.0. The tests hold a recording of 1.19.5 and one of 2.2.5. The runner reads `opencode --version` before every run. A version outside the range, or one that it cannot read, stops the step before it starts, and the note names the range.
- **OpenCode 2.x runs with a private server.** From 2.0.0 the runner adds `--standalone`. Without it, OpenCode 2.x sends the run to a shared background service, and a service that another process holds on its port makes the run fail. The 2.x line names the shell tool `shell`.
- **A resumed OpenCode 2.x step names its earlier session.** A step with `resume =` runs with `--standalone --session <id>`. On 2.2.5, `opencode session list --standalone` and `opencode session export <id> --standalone` returned a session that an earlier run had stored, so the private server reads the stored sessions. No resumed model run was made, so it is not proven that a resumed step sees the earlier messages. The help text of 2.2.5 says that `--session` creates the session when it does not exist, so a wrong id starts a new session with no error.
- **`forbid_commands` works on OpenCode 1.x and 2.x.** The two lines load a plugin through different functions and name the shell tool differently, so the guard plugin has one function for each line. A run of 2.2.5 refuses a forbidden command, and the call ends with the refusal text.
- **No sandbox.** A step with a sandbox does not start. Set `sandbox = "off"` in the project to run it with no sandbox.
- **No skill proof.** OpenCode reports no skill calls, so the result field `skills` of its steps is an empty list. The fact `runners.<name>.loads_skills` of `facts()` is false for such a runner, because OpenCode has no skill-load line. A step with the `skill` keyword does not start on it.
- **A lost run starts again.** OpenCode keeps all sessions in one database and has no file for one session. The transcript path in the step output names a path that no file holds. The check before a resume finds no file, so a lost OpenCode step starts again from its note and never resumes.
- **Subagent sessions are not counted.** The calls and the cost of a subagent session of OpenCode do not reach the output stream of the parent run, so the turn cap and the cost cap do not count them. This is from a read of the source of OpenCode 1.19.5.
- **The turn cap is one call late.** OpenCode shows a tool call on its output when the call ends, so the runner counts a shell call after it ran.
- **The cost cap is checked on finished steps.** A run can pass the cap by the cost of one step.
- **The guard needs a POSIX shell.** A step with `forbid_commands` does not start on Windows. If the plugin does not load, OpenCode runs every shell command with no check. The plugin writes a marker file when it loads, and a run that ends with no marker gets a violation line.
- **A run with no timeout ends after one hour.** A model over its usage quota prints nothing and retries. The note of the timed-out run names that cause.
- **The pool never picks OpenCode.** OpenCode has no usage report, so the pool reads an unknown usage for it and skips it, and the doctor makes no login check for it. A step runs on OpenCode only when it names the runner with `runner = "<name>"`.
- **The record names the runner and the model.** The render record of an agent step holds `label` (the free label of the call, left out when empty), `login` (the named runner, or the login that the pool picked) and `model` (the model that the step sets). The engine writes it before the agent starts, so `beehest status <run id>` and `beehest status <run id> --json` show all three on the step line while the step runs and after it fails, and `beehest status --item <item>` lists a running or failed lane with them. The dry run prints them in the render record. The engine reads both fields for display only, so a record without them replays as before.

### The process of the CLI

The launch of an adapter says how the prompt reaches the CLI. The default is stdin, so the prompt never shows in a process list. Both adapters use it.

- `argument` puts the prompt in one argument, the last one, after every other argument. It shows in the process list of the machine. The argument limit of the operating system bounds it: a prompt over the limit fails to start, so use `file` for a long prompt. A flag that names the prompt goes last in the arguments of the launch.
- `file` writes the prompt to `beehest-prompt.txt` in the run folder and puts the path in the last argument. The file has mode 0600, the folder has mode 0700, and the runner removes the folder when the run ends.

The launch can also set a prefix: a list of words that the runner puts before the CLI. The first word is a program found on PATH, and each other word is one argument. No shell reads the command line. With a prefix, the runner does not look up the CLI on PATH: the program of the prefix finds it by name. The probe of the version of the CLI runs the prefix, then the CLI, then `--version`. A run with no prefix looks up the CLI on PATH as before.

The CLI gets an allowlisted environment:

- the path, home, locale, proxy and certificate variables;
- the login folder variables of its runner;
- the token, if one is passed;
- one `TRACEPARENT`, only when `agents.trace_parent` is true.

The `TRACEPARENT` names the run and the step. The trace id is the first 16 bytes of the SHA-256 of `beehest-trace:` and the run id. The parent span id is the first 8 bytes of the SHA-256 of `beehest-span:`, the run id, `/` and the call name. The flags are `01`. A replay builds the same value. A sandboxed run gets it too. The engine value replaces any `TRACEPARENT` of the caller. `TRACESTATE`, `OTEL_*` and the telemetry switches of the caller never reach the CLI, and beehest exports no span: the CLI exports its own. A child run has its own run id and so its own trace id. The `command` step gets no trace variable.

No other variable of the caller reaches it. `SSH_AUTH_SOCK` is not on the list, for any step, sandboxed or not, and the usage reader and the `command` step use the same list. If the login folder is missing, the run does not start, because the CLI would create an empty profile there.

The CLI runs in its own process group through `proc`. A total timeout, an idle timeout or a cancel of the context kills the group. Each write on stdout or stderr resets the idle timeout. When the CLI exits, the group is killed again, so no child outlives the step.

Stdout is the transcript. A line writer redacts each line before it reaches the log: the token value, the redactor of the engine and anything shaped like an `sk-ant-` key. The redactor of the engine holds the values of the environment variables whose name marks a secret, as the [durable loop](durable-loop.md) describes. A value shorter than 8 bytes is not redacted. A line longer than 16 MiB is dropped, so a secret is never split across two writes. Stderr keeps a redacted tail of 4 KiB for the result.

### Permission flags of the Claude adapter

The Claude adapter starts each step with `--allowedTools Bash`. It adds `--permission-mode acceptEdits` unless the step sets `edits = False`. A sandboxed run forces the mode `default` from CLI 2.1.329, so its settings open the work folder by an allow rule instead (see [The edit rule of the work folder](sandbox.md#the-edit-rule-of-the-work-folder)).

Print mode cannot ask for permission. Without these flags, Claude Code refuses a file edit and a shell command ("Claude requested permissions to write to ..., but you haven't granted it yet"), and an implement step can do no work. Sources:

- cli-reference, of the permission flag: "Begin in a specified permission mode ... Overrides `defaultMode` from settings files."
- permission-modes: "`acceptEdits` mode lets Claude create and edit files in your working directory without prompting."
- cli-reference, of `--allowedTools`: "Tools that execute without prompting for permission".

A file tool that writes outside the work folder still prompts, so the print run refuses it.

The `edits` key of the `agent` call is `True` by default. A step with `edits = False` starts without the permission flag, so Claude Code refuses each file edit of the step. Shell commands stay allowed. The plan, review and design steps of the examples set `edits = False`.

`--allowedTools` stands in for `permissions.allow` in the settings JSON. It works for a step with no `forbid_commands` too, and it adds no second `--settings` flag next to the one of the hook. The prompt is on stdin, so the list of the flag takes no prompt.

### The limit of acceptEdits

The fence of `acceptEdits` covers the file tools only. No rule limits the paths of a Bash command. A shell command can write outside the work folder, because Claude Code checks redirects and `tee` against the work folders and not "arbitrary subprocesses that read or write files indirectly".

A step that runs untrusted work sets `forbid_commands`, so the hook refuses the commands that change what it must not change. The hook of `forbid_commands` stays the check for the forbidden kinds. The permissions page says of a hook that blocks: "A blocking hook also takes precedence over allow rules."

Not verified here: that a real Claude Code still runs the hook for a call that `--allowedTools` allows. `make smoke-hook` shows it.

### How the adapter reads the calls

The adapter reads the calls from the stream-json lines on stdout, which the runner already reads. It does not read the session file on disk. The session path stays in the result as a reference. The only check on the file is whether it exists, before a lost step resumes its session (see "The saved session" in durable-loop.md).

The tap gives each new session id to the request at the line that holds it, so the engine saves the id while the run goes on.

- A Skill call counts in a `tool_use` block of an assistant line.
- A Bash call counts at any depth of a line.
- A call of a sub-agent counts too. Claude Code forwards the `tool_use` blocks of sub-agents in the stream; they carry `parent_tool_use_id`.

The entries that the adapter reads go to the durable stream `agent` of the phase, redacted. The workflow builtin `stream` reads that stream for a child run, with the reader of `beehest status --follow` (see [`stream`](workflow-files.md#stream)).

The shape is the one of the Claude Code headless page (https://code.claude.com/docs/en/headless). The shell rules live in `check/shell` (see [Shell rules](#shell-rules)).

### How the adapter reads a provider limit

A rate limit or a spend limit of the provider ends a run with the outcome `limited`, not as a failed run. The adapter reads the limit from the stream, as the CLI reports it:

- A `rate_limit_event` line with `rate_limit_info.status` set to `rejected` is a limit. The status `allowed` or `allowed_warning` is no limit.
- A rejected status with `isUsingOverage` set to true is no limit. Paid overage serves the request, and the CLI keeps working.
- The limit is a spend limit only when paid overage was available and is now refused: `overageStatus` is `rejected` and `overageDisabledReason` is `out_of_credits` or `org_level_disabled_until` (a spend cap). Any other reason, such as `overage_not_provisioned`, or no reason, means the account had no paid overage to refuse, so the limit is a rate limit with the plan reset time.
- `rateLimitType` names the window. `resetsAt` is the reset time in Unix seconds. A spend limit resets at the earlier of `resetsAt` and `overageResetsAt`, because either reset lets work start again.
- An assistant line with `error` set to `rate_limit` is read like a `rate_limit_event`: its kind, window and reset time come from the `rate_limit_info` of its `api_error_params`, by the rules above. A line with no usable `rate_limit_info` is a rate limit with no window and no reset time, unless it says that the provider is overloaded: its `api_error` is `overloaded`, or its text says that the model "is experiencing high load". That line is an overload, a limit of the kind `overload` with no reset time, and the step waits a backoff for it (see [Provider limits](durable-loop.md#provider-limits)).

The runner keeps the first limit of the run. The run is limited only if the run did not go on to a result line with no error: a success or a cap after the limit means the CLI took more work. A limited run keeps its session, its message and its violations, whatever the exit code. Its note names the limit and the reset time. A run that timed out or was canceled keeps that outcome.

#### A refusal that no wait cures

Some refusals do not end at a reset time. The adapter reports such a line as a denial with a reason, not as a limit. The adapter tests the pause before the credits, as the CLI does.

- A pause of the organization: `dailyCapPaused` is true on a rejected `rate_limit_event`, or in `api_error_params.rate_limit_info` of an assistant `rate_limit` line (the CLI writes that line with `api_error` set to `usage_limit_reached`). A pause carries the `overageDisabledReason` of a spend cap, so the flag is what tells the two apart. The CLI tells the user to contact support.
- Usage credits are required: a rejected `rate_limit_event` has `errorCode` set to `credits_required` and `rateLimitType` missing or set to `overage`. This is the test of the CLI. The same holds for an assistant `rate_limit` line whose `api_error` is `long_context_credits_required` or `model_requires_usage_credits`. A wait buys no credits.

The runner keeps the first denial of the run. A denied run fails, even when an earlier line reported a limit, and it waits for no reset. The reason is a note of the result, after the note of the runner. A run that exited with code 0 fails with an error that names the reason. As for a limit, a denial counts only if the run did not go on to a result line with no error, and a run that timed out or was canceled keeps that outcome.

The engine still fails a limited agent step. The wait for the reset is a later change.

### The setting forbid_commands

The setting `forbid_commands` of an `agent` step is a list of finding kinds. A step that sets it installs a hook before the call, and scans the calls after the run. A step with no `forbid_commands` installs no hook and records no violation.

#### The kinds

| Kind | Refuses |
|---|---|
| `bulk-staging` | Bulk staging. See [Shell rules](#shell-rules). |
| `config-read` | The print of a file that holds a key. |
| `delete-text` | Delete text in `echo`, `printf` or a heredoc. See [Shell rules](#shell-rules). |
| `env-override` | A change of the search path or of a shell start file. |
| `git-author` | A commit author. |
| `git-bypass` | A skipped git safeguard: no-verify, stash, a config write, a force-push, a delete of a remote branch. |
| `git-write` | Each git command that changes a repository. See below. Opt-in. |
| `laptop-copy` | A copy from this machine to another machine for a repository that is not private. See below. |
| `local-delete` | A delete whose path is not a literal absolute path. See [Shell rules](#shell-rules). |
| `opaque-command` | A command word the scan cannot read. See [Shell rules](#shell-rules). |
| `protected-path` | A write inside a protected directory. See below. Needs `protect_paths`. |
| `remote-delete` | Any delete inside an `ssh`, `gcloud compute ssh` or wrapper command. See [Shell rules](#shell-rules). |
| `unparsed` | A line of a command that does not parse as shell. |
| `zsh-expansion` | The zsh-only forms `${(flags)x}`, `${=x}`, `${~x}` and `${^x}` outside single quotes. |

#### zsh-expansion

A shell that is zsh splits and globs by the zsh-only forms, and POSIX sh refuses them. Single-quoted text and heredoc bodies pass. The bash forms `${#x}`, `${x:-y}` and `${x/a/b}` pass.

#### laptop-copy

The kind refuses, for a repository that is not private:

- `rsync`, `scp`, `gcloud compute scp` or `kubectl cp` with a remote target (`host:path`, `user@host:path`, or an rsync, ssh or scp address) when a source is local;
- a pipe or a redirect from a file into a remote shell: `ssh`, `gcloud compute ssh`, `az ssh`, `aws ssm start-session`, `kubectl exec -i`.

The refusal line says to clone the repository from the forge on the host.

These pass: a heredoc of commands into `ssh`; a copy to a private bucket (on purpose); a copy from a remote; a local copy; a copy that runs on a host; a `git clone`.

The hook learns the visibility from `--visibility`. The step passes `--visibility private` only for a project whose file says `visibility = "private"`. A hook with no `--visibility` counts the repository as public, so it refuses. A value other than `public` or `private` is refused.

#### protected-path

The kind keeps an agent off a directory, such as the checkout of the owner.

Setup rules:

- The setting `protect_paths` of the step lists the directories. No built-in path exists.
- The kind needs at least one path. If a step lists the kind with no `protect_paths`, or sets `protect_paths` without the kind, the step is refused when it runs, before any agent starts.
- `beehest check workflow` refuses the same two cases, and an empty path, when both settings are list literals of text literals. A setting that is not a literal is left to the run.
- A relative path starts at the folder of the step.
- The hook gets the directories as `--protect` (absolute, comma-separated or repeated). `--protect` is required when `--forbid` lists the kind. The default `--forbid` list leaves the kind out, as it leaves out `git-write`.
- The hook gets the directory of the command from the event: the `cwd` of Claude Code, or the `directory` that the OpenCode plugin forwards.
- The step passes `protect_paths` in its recorded phase only when the list is not empty, so a replay of a step without it reads the same record as before.

Path compare:

- The scan resolves a symbolic link before it compares paths.
- When the check starts, it tests once for each protected directory whether its file system ignores case. The compare ignores case for that directory only.
- A glob that can reach the directory counts.

Refused writes inside a protected directory:

- a redirect, `tee`, `cp`, `mv`, `rm`, `touch`, `mkdir`, `chmod`, `sed -i`, `dd of=`, `find -delete` or `tar -C`;
- `curl -o`, `wget -O`, `unzip -d`, `tar -x -C` or `env -C`;
- a git command that is not a read when it works there, by `-C`, `--git-dir`, `--work-tree`, `GIT_DIR`, `GIT_WORK_TREE` or an earlier `cd`.

Git commands that pass when they work there: `log`, `show`, `diff`, `status` and the like; a listing `branch`, `tag`, `remote`, `config` or `stash`; `worktree list`.

A read of the directory passes. A git command that only reads it passes: `git clone`, `git fetch`, `git pull` and `git remote add` with the checkout as the source. A git command writes in the directory only when the directory is:

- its repository (`-C`, `--git-dir`, `--work-tree`, `GIT_DIR`, `GIT_WORK_TREE` or the current directory), or
- its write target (the clone destination, the path of `worktree add`, `--output`, `apply --directory`).

Paths the scan cannot resolve:

- A write target or a `cd` target that the scan cannot resolve (a substitution, or a variable that the command did not set) is refused. The line says that a path cannot be resolved.
- A read with such a path passes.
- A `cd` whose target is unknown leaves later relative paths unplaced, so a write by them is refused.

#### git-write

The kind refuses each git command that changes a repository: add, commit, push, apply, merge, rebase, reset, restore, checkout, switch, update-ref, commit-tree, init, clone, `stash` (except `list` and `show`), `branch` and `tag` (when they create, delete or move), `config` (a write), `remote` (add, remove, set-url and the like), `notes` (add, remove and the like) and `submodule update`.

`worktree add` and `worktree list` pass. A reviewer builds its own copy of the tree with `worktree add`, not with `apply`.

`git-write` is for a read-only step. `review_fix.star` lists it for each review call (the scoped reviews, the full review `candidate-review` and the design step), so a reviewer cannot fix, commit or push. It is opt-in, like the other kinds that a step lists. The default `--forbid` list of `beehest check command --agent <agent>` holds every kind except `git-write`, so a bare hook does not refuse every commit.

#### Check of the workflow

`beehest check workflow` refuses a kind that is not in the list, and shows its position. It reads each string literal of a list literal. A list that is not a literal (a variable, or `A + B`) and an element that is not a literal are left to the run, which refuses them when the step runs.

### The hook

The Claude adapter passes `--settings` with inline JSON. The JSON adds a `PreToolUse` hook with matcher `Bash` for that run (https://code.claude.com/docs/en/hooks). The adapter never edits a settings file of the user.

The hook runs `beehest check command --agent <agent> --forbid <kinds>`. It reads the event on stdin, in the hook format of the agent that `--agent` names. When the command breaks a listed rule, it exits 2 with the reason on stderr, and the agent CLI blocks the call. The command exits 0 only for a shell call that has a command and breaks no rule. It exits 2 for every other input: a missing or unknown agent (the message lists the known ones), a bad flag, a bad kind, an event it cannot read, an event longer than 1 MiB, an event of another tool, an event in the format of another agent, and a shell call with no command. The hook of each agent runs for the shell tool only, so a call of another tool never reaches the command, and an event that is not a shell call is a fault, not a call to allow.

An agent session that keeps running across an upgrade of beehest carries the old hook, which has no `--agent`. The command refuses every call of that session until the session restarts. Each new run builds the hook again, so only a session that started before the upgrade is affected.

The shell rules are the same for each agent. Each adapter supplies only the reader of its hook format: `agent/claude` reads the `PreToolUse` event of Claude Code, and `agent/opencode` reads the call that its plugin forwards (`directory`, `tool` and `args`). A new agent CLI needs a reader and a name in the table of `--agent`, and no copy of the rules.

The hook also sets `"onFailure": "block"`. With it, a hook that cannot start (for example, the program of beehest moved during the run), exits with another code or times out blocks the call too. Without the key, those failures let the call go ahead. The hooks page says of the key: "Requires Claude Code v2.1.295 or later." An older Claude Code ignores the key and fails open on those failures.

A step that sets `forbid_commands` does not start (outcome `not-started`, with a note) when:

- beehest cannot find the path of its own program, or
- the agent CLI of the runner has no hook.

Hooks run in the shell of Claude Code. The quoting of the command is for a POSIX shell. A Windows shell (PowerShell or cmd) is not supported.

#### Merge with the hooks of the user

- cli-reference, of `--settings`: "Values you set here override the same keys in your `settings.json` files for this session. Keys you omit keep their file-based values." (https://code.claude.com/docs/en/cli-reference)
- hooks page: "Hook entries merge across settings levels rather than replacing each other: user, project, and local settings add their own hooks without removing managed ones." That sentence does not name `--settings`.

Not verified against a real Claude Code: whether the hook of `--settings` adds to the hooks of the user, or replaces the `hooks` key. `make smoke-hook` shows that the real Claude Code runs the hook of `--settings` and blocks the call on exit 2.

### The scan after the run

The hook does not see a command that does not go through the Bash tool, so the check after the run stays as the second line.

After the run, `cmd/beehest` scans each Bash call of the stream with `check/shell`. It records one line for each hit of a listed kind, as `<kind>: <command>`, in the output `violations`.

- The output holds at most 20 lines, which include the lines for an incomplete check. Then one line gives the number left out.
- The output is empty when the run is clean.
- A hit does not fail the step. The workflow reads `agent.<name>.violations`, for example in the `until` of a loop.
- The phase of the agent is a recorded step, so a replay reads the recorded lines and scans no output again.

How calls are matched:

- A line of the output that is not a JSON object holds no call.
- Each Bash call has the id of its `tool_use` block.
- A call whose `tool_result` is an error that starts with `PreToolUse:` was refused by the hook and never ran. It is not a violation.
- A call whose error result does not start with `PreToolUse:` still counts as a violation, so a permission denial is counted too. This errs toward strict.

When the check did not see the whole output, the violations hold one of these lines:

| Line | Cause |
|---|---|
| "a line of the output was too long to check" | The runner dropped a line over 16 MiB. |
| "the output of the run was not complete: it has no result line" | No result line. |
| "the output of the run was not complete: it holds a line that is not valid JSON" | A line is not valid JSON. |

The check reads only the output of the run itself. A resumed session (`resume`) starts a new run, so the calls of earlier runs of the session are not read.

### What the scan does not see

The scan reads the text of the command. It is not a sandbox (see [The sandbox of an agent step](sandbox.md)). For `protected-path`, it does not see a write:

- by an interpreter (`perl -e`, `awk`), by `xargs` or by `find -exec`;
- by `sed -n 'w file'`, `sort -o` or `mktemp -p`;
- by `git -c core.worktree=`, `zip` or `git submodule add`;
- by a link that the same line makes and uses;
- by a `..` after a symbolic link, which the kernel resolves through the link;
- by `builtin cd`;
- by a program that writes on its own after a `cd` (`make`, `npm`, a script);
- by a tool that is not Bash.

A `cd` in an earlier Bash call is not seen either. The hook starts from the `cwd` of the event, which covers most of it.

For `laptop-copy`, the scan does not see a copy that a script file, a variable operand or a program hides.

### The setting allow_paths

The setting `allow_paths` of an `agent` step is a list of globs, for a lane that may change only some files, such as papers or design files. An empty list, an empty entry, an absolute path, a path with a `..` part and a pattern that does not parse are refused, and the message names the pattern. The `..` check reads each brace choice, so `{docs,..}/x` is refused like `../x`. The function `before_review` of the library reads its `allow` list with the same code.

At the start, the step records in the recorded `/start` step:

- the commit at its start;
- a snapshot of the untracked and ignored files already in the folder. Each file carries its size and modification time. A folder of a known build cache (the names are in `isBuildCache` in `internal/flow/pathscope.go`) is one entry, so a step may write in it.

A resume keeps the snapshot of the first start.

After the run, the step names each path that the item folder changed since then:

- committed changes;
- uncommitted changes of tracked files;
- every new untracked or ignored file;
- every untracked or ignored file that is gone;
- every untracked or ignored file whose size or time changed.

A file that was already there and is unchanged does not fail the step. The diff runs with no rename detection, so a rename lists the old path as well as the new path. Every path is relative to the root of the repository, also when the item folder is a subdirectory.

A path that no glob matches fails the step, and the message names each such path.

The globs use the `github.com/bmatcuk/doublestar/v4` dialect: `*` and `?` stop at a slash, `**` crosses it and also matches zero folders, and a leading `./` is dropped. A glob that does not parse is refused. An empty list is refused: leave the keyword out to turn the check off.

A step with no `allow_paths` records no extra step, so no module version moves.

Not seen:

- a write inside a known build cache folder that was already there at the start;
- a rewrite of a file that restores its size and its modification time;
- a write outside the repository, such as a sibling checkout; the sandbox is the layer for that (see the sandbox design);
- an untracked file inside a submodule;
- a write outside the tree through a symbolic link inside the globs; git cannot see that write.

### The setting data

The setting `data` of an `agent` step is a dict of values that the agent reads from a file, not from the prompt. A large record, such as a list of reds or of pull requests, then costs no prompt text.

- The call encodes the dict once to JSON, with the encoder of the Starlark JSON library. The `/render` step records the text and the path `<state>/inputs/<item>.<step>/data.json`, a folder of its own in the inputs folder.
- The phase writes the file, mode 0600, from the recorded text before each start of the agent. The phase derives the path from the state folder of the engine, the item and the step, and the phase input carries no path. A replay writes the same bytes to the same path. The value is as fresh as the step that read it, never a read at the start of the agent.
- The prompt names the file as `{{ .data_file }}`. A step with no `data` gets no file and an empty `data_file`.
- A value that is not a dict is refused, by the run and by `beehest check workflow`.

The prompt tells the agent to read the file with the shell and to refuse when the file is missing. The sandbox reads this one file: see [the sandbox](sandbox.md#the-data-file).

## Shell rules

### check/shell

`check/shell` reads shell text with the `mvdan.cc/sh/v3` parser and walks the tree, so quotes, heredocs, pipes, subshells and functions need no hand-written tokenizer.

`Scan` is the scan of the `forbid_commands` setting. It finds:

- bulk staging. A command that sets `GIT_INDEX_FILE` to a value stages into a private index and passes.
- a delete that needs approval: a path that is not a literal absolute path, or any delete inside an `ssh`, `gcloud compute ssh` or wrapper command;
- delete text in `echo`, `printf` or a heredoc.

### Parser and expansion

- The package imports the parser and its `expand` package, never `interp`. `make deps` checks every package of the module, and `depguard` denies `interp`.
- `expand` resolves a variable that holds a host wrapper. A name it does not know stays as `$name` text, so the literal path rule still sees it.
- Command substitutions, backticks and process substitutions are read as commands.
- `busybox` is a wrapper.
- The check reads behind the wrappers it lists, and it is stricter than the real command. It scans every suffix of their words and every word that holds a command line, glued option values included.
- The sandbox (#199) is the guard for a wrapper the check does not know.

### Caps on the variable lookup

Two caps bound the work of the variable lookup. A value longer than 4096 bytes is unreadable. A lookup that must work out more than 1000 values is unreadable.

An unreadable variable stays `$name` text. If it is the command word, or the first word after the wrapper words (`sudo`, `env`, `nice`, `timeout`, `xargs`), the scan reports `opaque-command`. So a cap never lets a staging or a delete pass.

A value that depends on a variable being read is kept for the current lookup only. The number of values worked out for one use does not grow with the number of uses. The time for a script still grows with its size.

## Lane papers

### Where the checks live

The engine has one check package: `check/shell`, for the shell scan. The other checks are not part of the engine:

| Check | Run by |
|---|---|
| The gate list derive | `beehest check gate-list` |
| The report check | `remedy report` |
| The handoff check | `remedy handoff` |
| The author check | `remedy authors` |
| The spend tables | `remedy spend` |

They live in `examples/remediation/tools/gate`, `examples/remediation/tools/papers` and `examples/remediation/tools/transcript`. See [Remediation tools](merge.md#remediation-tools).

- Each keeps its own copy of the small text helpers it uses. They share only the `Refusal` type of `examples/remediation/tools/check`.
- The package `papers` reads the context of a lane through `transcript.LaneContext` of the tools.
- Each check returns its lines and a verdict, or an error that is a `Refusal` when it cannot run. A refusal carries the lines found before it stopped.
- `Report` and `Authors` cannot refuse. `Authors` returns the exit code, because its two refusals (2 and 3) differ.

### The lane report

The lane report has four sections: Claims, Hunks, Inputs, Siblings. Each is a table with a fixed column count. A section with nothing to list has one `none: <reason>` row. A claim names the head commit it was verified at. The report says that no background command is left.

### The handoff file

The handoff file of a lane that stops early states the regressions so far and the lane context. A handoff written between a launch and its read is refused.

### The burn record

The burn record reads the transcript of the orchestrator. It counts the main thread calls for each hour and the token count of each lane notice.

- A token count prints as the transcript wrote it, whole or fractional.
- The lane cap comes from the `--cap` flag of `remedy spend`, else 150000. The tool reads no environment variable.
- An empty cap is refused. A cap above the integer range is the largest cap.
- A text that is not UTF-8 gets one replacement mark for each cut multi-byte sequence and one for each other bad byte.
- A transcript line whose `message` or `usage` is not an object is skipped, and so is a line with `NaN` or `Infinity`.
- A transcript key matches by exact case.

### The glob dialect

The globs of [`allow_paths`](#the-setting-allow_paths) use the `github.com/bmatcuk/doublestar/v4` dialect. A pattern has three forms that `fnmatch` does not have:

- `[!x]` and `[^x]` negate a set.
- `{a,b}` chooses between patterns, so a literal brace is `\{` or `\}`.
- `\` escapes the next character, so a literal backslash is `\\` and `\*` is a literal star.

A pattern that does not parse is a refusal that names the pattern. Examples are an unclosed `[` or `{`, a lone `[!` and a trailing `\`.

### The author check

The author check compares the address of a commit with the address of the account. A login with no linked account and a foreign address is refused as well.

### The gate list

The gate list is a Markdown file. Its `## Gate steps` section holds one row for each step.

`beehest check gate-list [--steps <name>]... [--map <json>] <ci-file>...` derives the steps from the CI workflow files: one step for each run step of each job, named `<job>:<step name>`, in file order. The round runs it from the project var `gate_files` before its first pass, with each name of `gate_steps` as `--steps` and the project var `gate_map` as `--map`.

#### The guard is the gate, not the label

The gate of a round runs only the commands of `gate_steps`, which the owner writes. No derived CI command runs. A gate step runs the code of the candidate head, so no read of the CI text can stop a network write by the gate. The build host of the gate holds no credential of the repository, so a publish that a CI step names cannot sign in there. The label below decides only which CI commands the round must account for.

The host still holds the cloud identity of the machine, which any gate step can reach. Issue #717 tracks it.

#### The label

The command labels each run step `step` or `refused`, and names a refused one in the output as `refused <name>: <reason>`. When the label cannot tell, it refuses. It never reads the step name, and it holds no list of tools. The command parses each file once and puts the node of each YAML alias in its place, so every check below reads a value given through an alias or a merge key where it is used. An alias inside the node it names makes the file not a workflow. A file whose tree, with each alias expanded, holds more than 100000 nodes is not a workflow either, and the error names the file and the bound. The command leaves out:

- A job that calls a reusable workflow.
- A job whose token of the run can write. The job `permissions` key wins over the file key. With no key at the job or the file, the token takes the default of the repository, which can write. Read only is `read-all`, `{}`, or a mapping of `read` and `none` values only. Any other value, an expression or a merge key included, can write.
- A job that names a secret or the token of the run anywhere in the job, any of its steps included, or in the file `env` or `defaults`. A later step can read what an earlier step writes to `GITHUB_ENV`, `GITHUB_OUTPUT` or a file, so a secret in one step leaves out the whole job. A secret is any text that names `secrets`. The token is an expression that reads `github.token`, or the `github` context as a whole: an index such as `github['token']`, the filter `github.*`, or a function of it such as `toJSON(github)` or `join(github.*, ',')`. A field such as `github.ref` is not the token. CI reads each `if` of a job or a step as an expression, with or without `${{ }}`, so the command reads the whole text of each `if` for a secret and for the token. A checkout action takes the token of the job, which the permissions rule above governs.
- A step that uses an action other than a checkout or a setup action (so a lint action is left out).
- A step whose shell is not exactly `bash` or `sh`. The shell comes from the step, then the job `defaults.run.shell`, then the file `defaults.run.shell`. A custom shell string such as `bash -e {0}` is refused.
- A step whose run text the shell reader of `check/shell` finds one of its kinds `git-write`, `opaque-command` or `unparsed` in. No other kind of the reader is judged.

Files that give no step are exit code 2.

#### The command of a step

The command of a derived step is its whole record: the run text, then each part of the context that CI runs it in, when set: `[shell <shell>]`, `[working-directory <folder>]`, `[env <names>]`, `[container <container>]`, `[continue-on-error <value>]` and `[if <condition>]`, the `if` of the step, which decides whether the step runs. The shell and the folder come from the step, then the job defaults, then the file defaults. The env names are the sorted names of the file, job and step `env`, with the names that a merge key merges, without values. The run text keeps each blank inside quotes as CI has it; outside quotes, a run of blanks is one space. The record holds no other part: a change to `runs-on`, `timeout-minutes`, the matrix, `services`, the `if` of the job or an env value leaves the key as it is.

#### The hold

`gate_map` maps each exact derived command, the whole record as the step line prints it, to a list of `gate_steps` names. A change in CI to the run text or to any part of the record stops the round. An empty list says that the command is not a check, such as an install. `--map` is one JSON object, given once. A command given twice, a null list, a blank command or name, and any other form are exit code 2. With `--steps` or `--map`, the command is exit code 2 with one line for each defect, and the round stops before its first pass:

- `unmapped <step name>: <command>`: a derived step whose command is no key. The match is exact: no trim, no substring.
- `stale <command>`: a key that no derived step has.
- `unknown-step <name>`: a name in the map that is not a gate step.

A `--map` with no `--steps` is exit code 2, and the message names `gate_steps`. The round refuses a project with `gate_files` and no `gate_steps` before it runs the command. A refused step needs no key, because it never runs. Add a refused step, such as the lint action, to `gate_steps` by hand.

