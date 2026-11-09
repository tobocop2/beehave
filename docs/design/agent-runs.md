# Agent runs, shell rules and lane papers

Part of the [beehave architecture](../architecture.md).

An `agent` step starts one AI coding CLI through an adapter, feeds it the prompt and reads the calls it makes. The setting `forbid_commands` refuses shell commands of listed kinds through a hook and records hits after the run. The setting `allow_paths` limits which files a step may change. `check/shell` holds the shell rules; the lane paper checks live in the remediation tools.

## Agent runs

### Adapters

- `agent` holds the runner and the `Adapter` contract.
- An adapter supplies the profile of one CLI (binary, fixed arguments, login variables) and reads its output stream.
- `agent/claude` is the only adapter. Other agents return later as their own packages.
- `cmd/beehave` holds the table that maps an agent name to the constructor of its adapter. It puts the adapter in the `Spec` of each run. A run with no adapter does not start.
- `agent/agenttest` is the conformance suite. It starts a fake CLI and checks the profile, the arguments and the call reader of an adapter. Every new adapter calls it from its tests.

### The process of the CLI

The prompt goes on stdin, so it never shows in a process list.

The CLI gets an allowlisted environment:

- the path, home, locale, proxy and certificate variables;
- `SSH_AUTH_SOCK`;
- the login folder variables of its runner;
- the token, if one is passed.

No other variable of the caller reaches it. If the login folder is missing, the run does not start, because the CLI would create an empty profile there.

The CLI runs in its own process group through `proc`. A total timeout, an idle timeout or a cancel of the context kills the group. Each write on stdout or stderr resets the idle timeout. When the CLI exits, the group is killed again, so no child outlives the step.

Stdout is the transcript. A line writer redacts each line before it reaches the log: the token value and anything shaped like an `sk-ant-` key. A line longer than 16 MiB is dropped, so a secret is never split across two writes. Stderr keeps a redacted tail of 4 KiB for the result.

### Permission flags of the Claude adapter

The Claude adapter starts each step with `--allowedTools Bash`. It adds `--permission-mode acceptEdits` unless the step sets `edits = False`.

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

The adapter reads the calls from the stream-json lines on stdout, which the runner already reads. It does not read the session file on disk. The session path stays in the result as a reference, and no code reads it.

- A Skill call counts in a `tool_use` block of an assistant line.
- A Bash call counts at any depth of a line.
- A call of a sub-agent counts too. Claude Code forwards the `tool_use` blocks of sub-agents in the stream; they carry `parent_tool_use_id`.

The shape is the one of the Claude Code headless page (https://code.claude.com/docs/en/headless). The shell rules live in `check/shell` (see [Shell rules and the job check](#shell-rules-and-the-job-check)).

### The setting forbid_commands

The setting `forbid_commands` of an `agent` step is a list of finding kinds. A step that sets it installs a hook before the call, and scans the calls after the run. A step with no `forbid_commands` installs no hook and records no violation.

#### The kinds

| Kind | Refuses |
|---|---|
| `bulk-staging` | Bulk staging. See [Shell rules](#shell-rules-and-the-job-check). |
| `config-read` | The print of a file that holds a key. |
| `delete-text` | Delete text in `echo`, `printf` or a heredoc. See [Shell rules](#shell-rules-and-the-job-check). |
| `env-override` | A change of the search path or of a shell start file. |
| `git-author` | A commit author. |
| `git-bypass` | A skipped git safeguard: no-verify, stash, a config write, a force-push, a delete of a remote branch. |
| `git-write` | Each git command that changes a repository. See below. Opt-in. |
| `laptop-copy` | A copy from this machine to another machine for a repository that is not private. See below. |
| `local-delete` | A delete whose path is not a literal absolute path. See [Shell rules](#shell-rules-and-the-job-check). |
| `opaque-command` | A command word the scan cannot read. See [Shell rules](#shell-rules-and-the-job-check). |
| `protected-path` | A write inside a protected directory. See below. Needs `protect_paths`. |
| `remote-delete` | Any delete inside an `ssh`, `gcloud compute ssh` or wrapper command. See [Shell rules](#shell-rules-and-the-job-check). |
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
- `beehave check workflow` refuses the same two cases, and an empty path, when both settings are list literals of text literals. A setting that is not a literal is left to the run.
- A relative path starts at the folder of the step.
- The hook gets the directories as `--protect` (absolute, comma-separated or repeated). `--protect` is required when `--forbid` lists the kind. The default `--forbid` list leaves the kind out, as it leaves out `git-write`.
- The hook gets the directory of the command from the `cwd` of the event.
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

`git-write` is for a read-only step. `review_fix.star` lists it for the `review` call, so a reviewer cannot fix, commit or push. It is opt-in, like the other kinds that a step lists. The default `--forbid` list of `beehave check command` holds every kind except `git-write`, so a bare hook does not refuse every commit.

#### Check of the workflow

`beehave check workflow` refuses a kind that is not in the list, and shows its position. It reads each string literal of a list literal. A list that is not a literal (a variable, or `A + B`) and an element that is not a literal are left to the run, which refuses them when the step runs.

### The hook

The Claude adapter passes `--settings` with inline JSON. The JSON adds a `PreToolUse` hook with matcher `Bash` for that run (https://code.claude.com/docs/en/hooks). The adapter never edits a settings file of the user.

The hook runs `beehave check command --forbid <kinds>`. It reads the event on stdin. When the command breaks a listed rule, it exits 2 with the reason on stderr, and Claude Code blocks the call. The command exits 2 for every failure: a bad flag, a bad kind, an event it cannot read.

The hook also sets `"onFailure": "block"`. With it, a hook that cannot start (for example, the program of beehave moved during the run), exits with another code or times out blocks the call too. Without the key, those failures let the call go ahead. The hooks page says of the key: "Requires Claude Code v2.1.295 or later." An older Claude Code ignores the key and fails open on those failures.

A step that sets `forbid_commands` does not start (outcome `not-started`, with a note) when:

- beehave cannot find the path of its own program, or
- the agent CLI of the runner has no hook.

Hooks run in the shell of Claude Code. The quoting of the command is for a POSIX shell. A Windows shell (PowerShell or cmd) is not supported.

#### Merge with the hooks of the user

- cli-reference, of `--settings`: "Values you set here override the same keys in your `settings.json` files for this session. Keys you omit keep their file-based values." (https://code.claude.com/docs/en/cli-reference)
- hooks page: "Hook entries merge across settings levels rather than replacing each other: user, project, and local settings add their own hooks without removing managed ones." That sentence does not name `--settings`.

Not verified against a real Claude Code: whether the hook of `--settings` adds to the hooks of the user, or replaces the `hooks` key. `make smoke-hook` shows that the real Claude Code runs the hook of `--settings` and blocks the call on exit 2.

### The scan after the run

The hook does not see a command that does not go through the Bash tool, so the check after the run stays as the second line.

After the run, `cmd/beehave` scans each Bash call of the stream with `check/shell`. It records one line for each hit of a listed kind, as `<kind>: <command>`, in the output `violations`.

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

The setting `allow_paths` of an `agent` step is a list of globs, for a lane that may change only some files, such as papers or design files.

At the start, the step records in the recorded `/start` step:

- the commit at its start;
- a snapshot of the untracked and ignored entries already in the folder. An untracked file carries its size and modification time. An ignored directory is one entry.

A resume keeps the snapshot of the first start.

After the run, the step names each path that the item folder changed since then:

- committed changes;
- uncommitted changes of tracked files;
- every new untracked or ignored entry;
- every untracked or ignored entry that is gone;
- every untracked file whose size or time changed.

A file that was already there and is unchanged does not fail the step. The diff runs with no rename detection, so a rename lists the old path as well as the new path. Every path is relative to the root of the repository, also when the item folder is a subdirectory.

A path that no glob matches fails the step, and the message names each such path.

The globs use the `github.com/bmatcuk/doublestar/v4` dialect: `*` and `?` stop at a slash, `**` crosses it and also matches zero folders, and a leading `./` is dropped. A glob that does not parse is refused. An empty list is refused: leave the keyword out to turn the check off.

A step with no `allow_paths` records no extra step, so `stepsVersion` does not move.

Not seen:

- a write inside a directory that was already ignored at the start;
- an untracked file inside a submodule;
- a write outside the tree through a symbolic link inside the globs; git cannot see that write.

## Shell rules and the job check

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

### The job check

`job.Script`, in `examples/remediation/tools/job`, reads a job through the scan of `check/shell`. It:

- applies the bulk staging rule;
- refuses a test command with no `timeout` in front of it. A test command is `cargo test`, `cargo nextest`, `pytest`, `py.test`, `bats` or `make test*`.

The check reads a string that a shell, `ssh`, `eval`, a container runner or `uv run` runs. It reads a heredoc that a shell reads or that a later command runs as a file.

`job.Script` also refuses two kinds of text:

- A line that does not parse as shell. The lines that do parse are still judged, and so is a line that only ends in an operator, such as `git add -A &&`.
- A command the check cannot read: `env -S`, `find -exec sh -c`, and a command word that holds a variable or a substitution when the words after it would be a staging or a delete if the word were `git` or `rm`, such as `$g add -A` and `$x -rf "$d"`. When the variable is the `rm` itself, a delete needs a recursive or force option to count. A command word that hides no rule, such as `$ROOT/bin/run.sh`, passes.

## Lane papers

### Where the checks live

The engine has one check package: `check/shell`, for the shell scan. The other checks are not part of the engine:

| Check | Run by |
|---|---|
| The gate list check | `remedy gatelist` |
| The job check | `remedy job` |
| The report check | `remedy report` |
| The papers check | `remedy papers` |
| The handoff check | `remedy handoff` |
| The author check | `remedy authors` |
| The spend tables | `remedy spend` |

They live in `examples/remediation/tools/gate`, `examples/remediation/tools/job`, `examples/remediation/tools/papers` and `examples/remediation/tools/transcript`. See [Remediation tools](merge.md#remediation-tools).

- Each keeps its own copy of the small text helpers it uses. They share only the `Refusal` type of `examples/remediation/tools/check`.
- The packages `gate` and `job` read shell text through `check/shell` (`ParseUnits`, `Scan` and a `Reader` of command words).
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

### The papers check

The papers check lists the source paths that a revision range changes. A lane of proof and papers changes none. A range that starts with `-` is refused, so git never reads it as an option.

The globs use the same `github.com/bmatcuk/doublestar/v4` dialect as [`allow_paths`](#the-setting-allow_paths). A pattern has three forms that `fnmatch` does not have:

- `[!x]` and `[^x]` negate a set.
- `{a,b}` chooses between patterns, so a literal brace is `\{` or `\}`.
- `\` escapes the next character, so a literal backslash is `\\` and `\*` is a literal star.

A pattern that does not parse is a refusal that names the pattern. Examples are an unclosed `[` or `{`, a lone `[!` and a trailing `\`.

The package `papers` declares its own `runner` interface for git, so it does not import `forge`.

### The author check

The author check compares the address of a commit with the address of the account. A login with no linked account and a foreign address is refused as well.

### The gate list

The gate list is a Markdown file. Its `## Gate steps` section holds one row for each step, with a `Derived from:` line that ends in a commit.

When a step passes:

- A row with the command `see <file>` is an unstated step. The job passes when it prints `name=<step>`.
- Any other step passes when one command of the job holds every literal part of the gate command, in order, around the `<slot>` parts.

A command here is one of:

- a simple command with its redirects;
- a pipeline or list (`&&`, `||`, `;`, `|`);
- a subshell;
- a compound command that sits on one line (`time cargo fmt`, `{ cmd; }`, `if`, `while`, `for`, `case`, `coproc`), with its keyword;
- one line of a here-document.

A compound command on several lines is not one command. The check keeps the text of the outermost command of a chain once, so its cost is linear in the length of the job.

Other rules:

- A line that runs a step and also declares a skip, such as `cargo fmt; echo "GATE-SKIP fmt: x"`, counts the run and records the skip.
- Words join on blank and tab only, so a no-break space does not join them.
- A comment at the end of a line is not a command.
- The skip reason and the fixed-status message print the text of the command, not of its whole line.
- The rows and sections are read as text with `splitRow`. The job is read with the parser of `shell.ParseUnits`. The `let` rule parses its arithmetic text with the same parser.

A `GATE-SKIP <step>: <reason>` line declares a skip. It needs:

- a reason;
- a step of the list;
- a `command -v` check when the reason names a missing tool;
- a final marker with `gate=partial`.

### The exit marker rule

The exit marker rule finds a print command that prints `rc=` followed by a literal number. A print command is `echo`, `printf`, a here-document or a here-string, also inside a shell string argument.

A variable counts as a literal when one assignment sets it, to a number, and nothing else can change it: no arithmetic, `read`, `for`, `printf -v`, `eval` or sourced file. The caller passes the function that reads sourced files. A file that cannot be read, or a path that only the shell can resolve, makes every variable unknown.
