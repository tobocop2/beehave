# The sandbox of an agent step

Part of the [beehave architecture](../architecture.md).

The `forbid_commands` hook reads the text of a command. It is not a sandbox (see [Agent runs](agent-runs.md#the-setting-forbid_commands)). The setting `sandbox` of an `agent` step runs the Claude Code sandbox on the shell of the step, and the operating system enforces it (Seatbelt on macOS, bubblewrap on Linux). With the sandbox on, a gate on this machine runs under a sandbox enforcer too.

The default is off. A project opts in with `sandbox = "on"` in its project file.

[The trust boundary](trust.md#the-sandbox-is-on-by-default) describes the planned change: the default becomes on, and a step with `sandbox = None` in such a project is refused.

## The setting

`sandbox = sandbox(network = ["api.example.org"])` turns the sandbox on for the step.

| Key | Default | Meaning |
|---|---|---|
| `network` | empty list (no network) | The domains that the shell can reach. |

### The skills of a step

The sandbox loads no settings file, so the agent sees no skill of the runner. The prompt of a step declares each skill that the step needs with the template function `skill`, such as `{{ skill "review" }}` or `{{ skill .facts.vars.review_skill }}`. The function renders the plain name, and the engine gives the step each skill that the prompt declares, with a sandbox or without one. A name is a folder name: no path, no `..`; the render refuses any other name.

- With a sandbox, the step sees exactly the declared skills.
- Without a sandbox, the step sees exactly the declared skills too. The CLI has no flag that limits skills alone, and each setting source carries both its skills and its settings. So the launch empties the setting sources, and the engine passes what the step needs in its own `--settings` JSON: the attribution, the hook of `forbid_commands` and the allow rules of the output files. The model, the permission mode and the allowed tools are launch flags, and the login is the config folder and token variables of the runner. The step does not read the `model`, `env`, `apiKeyHelper`, permission rules, hooks or `CLAUDE.md` files of the runner or of the project.
- The remediation example declares skills in its prompts, so it needs an adapter that supports skills. The OpenCode adapter does not, and the engine stops a step of an OpenCode runner that declares a skill.

- The engine finds each skill at `<config folder of the runner>/skills/<name>`. A runner with no config folder gets the default folder of its CLI under the home folder, `~/.claude` for Claude Code. If that folder holds no `SKILL.md`, or the CLI has no default folder, the step does not start, and the message names the skill and the folder.
- If a skill folder holds a symbolic link, the step does not start, and the message names the skill and the link. A copy keeps a link as a link, and its target can be a file of the config folder. The skill folder itself can be a link: the copy reads through it.
- The Claude adapter copies each declared skill into a plugin folder in the run folder, `skills-plugin`, beside a manifest with the name `beehave-skills`. It passes the folder with `--plugin-dir`, which loads the skills. The setting sources stay empty in every launch, sandboxed or not, so no other user skill, plugin or hook loads, and a step that declares no skill gets none.
- The adapter also passes the plugin folder with `--add-dir`, as it does the folder of the data file. The flag loads no skill from the folder; it makes the folder a working directory, so `blockReadsOutsideWorkingDirectories` lets the Read tool and the shell read the files of a skill. The sandbox and the permission rules deny the write of the folder. The config folder of the runner stays denied.
- The CLI names a plugin skill `beehave-skills:<name>`. The adapter records it as `<name>`. A skill of any other plugin keeps its prefix, so it never counts as the named skill.
- A skill counts as called only when the CLI answers its Skill call with no error. A call that the CLI answers with `Unknown skill` does not count.

The work folder, the temporary folder and the shared git directory of a linked work tree are writable. No other path is. A step has no keyword that opens one.

- `sandbox = None` turns the sandbox off for the step. `beehave check workflow` prints a note for each such step.
- A step that leaves out `sandbox` takes the default of the project.
- A step that needs `docker`, or another tool that the sandbox blocks, sets `sandbox = None`.

The project file also takes these keys. Each path is absolute or starts with `~/`.

| Key | Meaning |
|---|---|
| `sandbox_read` | Extra paths that the shell can read. |
| `sandbox_deny` | Extra credential paths. |

### The record

The record of a step stores the sandbox that the step runs with, in the `/render` step beside the prompt. A replay reads the record and never the current default of the project. A record without the value replays as a step with no sandbox.

### The data file

A step with the `data` keyword gets one file in the state folder (see [Agent runs](agent-runs.md#the-setting-data)). The file is the only file of a folder of its own, `<state>/inputs/<item>.<step>/data.json`. The plan adds that one path to the read paths, after the check of the `sandbox_read` paths. It also passes that folder to the CLI with `--add-dir`, which makes it a working directory, and denies the write of the folder in the sandbox and in the permission rules. The phase derives the path from the state folder of the engine, the item and the step before each start of the agent, also for a queued or recovered phase. No record carries the path to the phase or the plan. A phase with data and no step name, from an older build, starts no agent. The recorded prompt names the path in its text, so a replay whose recorded prompt names another path stops with an error that says to start a new run. Such paths are the file of a run recorded at an older build, a `data.json` in another folder, and the file of this item and step under another state folder. It never adds the inputs folder or the state folder, which holds `dbos.db`. The sandbox denies the home folder, so the default state folder under `~/.local/state` stays closed except for this file. A data file inside a credential path stops the step. The permission rules keep `blockReadsOutsideWorkingDirectories`. A `Read` allow rule does not lift that block: the Claude Code settings reference says "No allow rule lets Claude's file tools through either block", and that a working directory does. So the folder of the data file is a working directory, and the agent reads the file with the Read tool or the shell. The file holds what the workflow passed and no credential.

### The output file

A step that declares `outputs` writes them to one file in the state folder. The file is the only file of a folder of its own, `<state>/outputs/<item>.<step>/output.json`. The file belongs to the run, not to the sandbox plan: every launch opens it, with a sandbox, with a guard only, and with neither. For Claude Code, the launch passes its folder with `--add-dir`, which makes it a working directory, and one function writes the allow rules in every mode. In a sandbox the file is also in the read paths. The Write tool of Claude Code refuses to overwrite a file that the session has not read, and `blockReadsOutsideWorkingDirectories` refuses the Read of a file outside the working directories, so the output file must be in one. The folder holds no other file, so the CLI still blocks the read of the output of every other call, and the plan never adds the outputs folder. A phase that declares outputs and names an output file other than the one the engine derives for the item and the step, such as the file of a run recorded at an older build, starts no agent, makes no file at the old path, and says to run `beehave cancel` and then start a new run. A call with no outputs gets no folder. The launch opens that one file for read and write, by its exact path, and nothing else outside the work folder. The adapter writes the path as one `Read` and one `Edit` allow rule with no `/**` tail. The Write tool obeys the rule, and Claude Code merges its path into the write paths of the sandbox, so the shell can write the file too. The adapter sets no `allowWrite` list. A step with `edits = False` runs in the default permission mode, which refuses every other write of the file tools, so the agent still edits no file of the work folder. The outputs folder and the state folder stay closed, so a step cannot write the output of another step or the state of the engine. An output file inside a credential path stops the step.

Claude Code does not escape the paths of its rules. So a data file or an output file whose path holds a glob character (`*`, `?`, `[` or `\`) stops the step, because its rule would open more than the one file.

If the agent ends with no output file, the error names each call on that file that the permission check of the CLI refused, by a rule or by the permission mode: a file tool that writes it, a file tool that reads it, and a shell call whose command names it as a whole word. The adapter reads these calls from the `permission_denials` list of the result line.

The file stays in the state folder. The temporary folders are shared by every run, so one step could write the output of another there. The work folder is tracked by git, so the file would dirty the tree of the item. The Write tool refuses a path in either folder without an allow rule.

The engine resolves the state folder to its real path once, when it opens the folder, so the output file, the data file, the prompt and the grant all hold one spelling. The sandbox compares real paths, so a grant that holds a link opens nothing.

On Linux, the sandbox ignores a write path that does not exist when the run starts. Before each try of a call that declares outputs, the engine removes the old output file and makes a new empty one, so the exact grant names a file that exists. An empty output file counts as no output file. Because the file exists, the Write tool asks for a Read of it first, and the folder of the output file, a working directory, lets that Read through.

### Forge data for a sandboxed step

In the sandbox, gh cannot read its config, and no forge credential may reach the agent. So no sandboxed step reads the forge itself. The engine reads the forge outside the sandbox, and the workflow passes the answer through the `data` keyword. In the remediation example, `queue` gives the queue audit the rows and the open pull requests of the pass, and `pr_history` gives the plan and the implementer the earlier pull requests of the item with their closing comments. The plan and the queue audit then need no network.

## What the Claude adapter sets

### Isolation flags

The adapter passes three flags, and `--add-dir <data folder>` for a step with a data file:

| Flag | Effect |
|---|---|
| `--setting-sources ""` | Loads no user, project or local settings file, so a planted `excludedCommands`, allow rule or hook has no effect. |
| `--strict-mcp-config` | Loads no MCP server, because a local server runs with the access of the owner. |
| `--disallowedTools WebFetch WebSearch` | Removes the two tools that ignore the domain list. |
| `--add-dir <folder>` | Makes the folder of the data file a working directory, so the file tools and the shell read it. The CLI reference says the flag adds "additional working directories for Claude to read and edit files". |

It passes one `--settings` JSON that holds the sandbox object, the permission rules and, for a step with `forbid_commands`, the hook.

### The sandbox object and the permission rules

- The sandbox object sets `enabled`, `failIfUnavailable` and `allowUnsandboxedCommands: false`.
- The base denies the home folder. `denyRead` holds `~/`. `allowRead` holds only the work folder, the shared git directory, the `sandbox_read` paths, the env file, the data file and the output file of the step. Outside the home folder, the sandbox reads every path that is not a credential path.
- The network list sets `allowedDomains` with `strictAllowlist`.
- The permission rules set `blockReadsOutsideWorkingDirectories`, deny the write of the data folder, and deny `WebFetch` and `WebSearch`. For a step with outputs, they allow `Read` and `Edit` of the output file alone.

The adapter does not set `excludedCommands`, `allowLocalBinding`, `allowAllUnixSockets` or `allowAppleEvents`. It sets no `allowWrite` list, so it never puts the shared git directory in it. Claude Code makes it writable for a linked work tree and keeps `hooks` and `config` read-only, and an explicit entry could open them.

### The credential list

The engine owns the credential list:

`~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.config/gcloud`, `~/.azure`, `~/.gnupg`, `~/.netrc`, `~/.npmrc`, `~/.pypirc`, `~/.git-credentials`, `~/.docker`, `~/.kube`, `~/.cargo/credentials*`, `~/.terraform.d`, `~/.config/op`, `~/.config/beehave`, `~/.claude*`, `~/Library/Keychains`, `~/.local/share/keyrings`, and the login folder of the runner.

A step cannot remove an entry. `sandbox_deny` adds to the list.

Every file whose name starts with `.env` is denied, at any depth of the work folder and whether or not it exists when the step starts. The patterns are `<work>/.env*` and `<work>/**/.env*`. The sandbox and the file-tool rules both take the patterns. The CLI matches them when a file is used, so the plan does not walk the tree.

The adapter writes each path in two places:

- a denied read and a denied write in the sandbox;
- a denied `Read` and `Edit` rule for the file tools. A rule path is written as `//absolute`.

A path in the sandbox alone leaves the Read tool open. A path in the rules alone leaves `cat` open.

### Refused paths

A project whose `gate_allow_write` overlaps a credential path does not start its gate. The same holds for a `gate_allow_write` path that is, is inside or holds one of these, because a write there runs later outside the sandbox:

- `.git`, `.claude`, `.husky`, `.githooks`;
- the folder that `core.hooksPath` names.

`beehave check workflow` refuses a literal path with one of these names at any depth. A `gate_allow_write` path that is a link to a place that does not exist yet is checked at the place it points to.

`.claude` is refused in `gate_allow_write` and is not denied as a whole, because Claude Code writes its own state there. Only its settings files and hooks are closed.

A `sandbox_read` path is refused, when the project loads and when the step starts, if it:

- is `/`, the home folder or a folder above it;
- equals, holds or lies inside a credential path or a `.env` file.

### Write denies for hooks and git

The sandbox denies writes to these folders. The path deny names them at the top of the work folder only:

- `<work>/.husky`, `<work>/.githooks`;
- the `core.hooksPath` folder;
- `.git`, in a plain checkout where `.git` is a folder. A step in a plain checkout cannot commit. A linked work tree, where `.git` is a file, can.

A `.git` that does not exist when the step starts stays in the list, so a `git init` at the top fails.

Write-deny patterns name these places at the top of the work folder and at every depth, each with its contents, so that a folder that a step makes later is covered:

- `.git/hooks`
- `.husky`
- `.githooks`
- `.claude/hooks`
- `.claude/settings*.json`

The top level has its own pattern, because the documentation of Claude Code gives no rule for a `**` that matches no folder. The patterns stay readable.

Not proven: the patterns at depth are expressed in the settings only. The documentation of Claude Code shows `**` in a `denyRead` path (`~/**/.env`), names a wildcard `denyWrite` path, and says that on Linux and WSL2 a write deny on a file that does not exist yet holds. It gives no depth rule, and no rule for a folder that does not exist yet. A real `claude` run, the Linux smoke, must prove them.

These gaps stay open:

- the rest of a nested `.git`, made before or during the step (its `config` can name a hook command);
- `.claude/skills`, `.claude/agents` and `.claude/commands`.

### Environment

The CLI keeps `CLAUDE_CODE_OAUTH_TOKEN` for its own calls. A sandboxed run sets `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1`. The documentation of Claude Code says this strips credentials, by name or by value, from the environment of the shell and of other subprocesses.

### The edit rule of the work folder

From Claude Code 2.1.329, the scrub variable also forces the permission mode `default`. The CLI ignores `--permission-mode acceptEdits` and writes on stderr: "Permission mode forced to default", then "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB is set (allowed_non_write_users hardening). Declare allowedTools explicitly, or set CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=0 to opt out." In print mode, the default mode refuses each Edit and Write call.

The run keeps the scrub. For a step that may edit, the settings add one allow rule, `Edit(//<work folder>/**)`. An `Edit` rule covers every file tool that writes. A deny rule wins over an allow rule, so the closed paths and the credential paths stay refused. A step with `edits = False` gets no such rule. The data folder stays read only, and the output file keeps its own rule by its exact path.

A probe of CLI 2.1.330 with the full launch arguments measured this. With the rule, Write created a file at the top of the work folder and one in a subfolder. It refused `.git/hooks/pre-commit`, `.claude/settings.json` and a file outside the work folder. The bare tool names in `--allowedTools` let the write outside the work folder through, so the rule names the folder. The mode in the init line stays `default` and the stderr line stays, so the engine does not check the mode.

The rule is a pattern, so a glob character in the path of the work folder would change what it opens: a probe with the work folder `w[ab]` refused the folder itself and opened its sibling `wa`. A step that may edit, in a work folder whose path holds a glob character, does not start, as for the state files. A space in the path works.

The engine checks the result instead of the mode. A step that may edit fails when the CLI reports a refused Edit, Write or NotebookEdit call on a file of the work folder that no deny rule of the plan covers. That refusal means the rule of the work folder did not apply. The failure names each refused file and quotes the stderr of the CLI. The step gets no second try, because the same launch refuses the same edit again. A refused edit that a deny rule covers, such as a hook, an agent settings file or a `.env` file, is the rule at work: the step goes on, and its output decides it. The engine decides what a deny rule covers by the one test that the other sandbox checks use: a rule covers its own path, each path that its pattern matches and each path below either, without case and again with every link resolved. So `.ENV` and `.envs/dev.yaml` count as covered by the `.env*` rule.

Not proven: a real `claude` run that prints `env` in the shell must show that the token is gone. Until then, the shell may read the token and, with a non-empty `network` list, send it out.

The child environment holds no proxy variable and no `SSH_AUTH_SOCK`. It sets `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1`, because the base denies `~/.gitconfig`. A step that needs a proxy of the owner sets `sandbox = None`.

The CLI process keeps the `HOME` of the owner, so that its login works. The shell gets its own `HOME`: a new, empty folder `beehave-home-<run folder name>` in the temporary folder of the sandbox, made by the env file before the first command. Each step has its own folder, so no file of one step reaches the next. The Go command keeps its telemetry token and its build cache under the home folder, and the base denies the home folder of the owner, so `go test` fails there. The Go command has no documented setting that turns telemetry off. The build cache of the Go command, and its module cache, start empty in each step. The env file sets `HOME` with the git variables.

The engine makes a temporary folder for each sandboxed step, `/tmp/bh-<random>` (mode 0700), and names it in `CLAUDE_CODE_TMPDIR`. The CLI keeps its temporary files in `<that folder>/claude-<uid>/` and gives the shell that path as `TMPDIR`, so the home folder of the shell is `/tmp/bh-<random>/claude-<uid>/beehave-home-<run folder name>`. The CLI gives the shell a fallback `TMPDIR` of its own when the override is a long path, so the folder is short. When the step ends, whatever its exit, the engine removes the folder with the run folder. When a plain removal fails, it makes each folder in it writable and tries again, because the Go command leaves its module cache read-only. A step with no sandbox gets no such folder and no `CLAUDE_CODE_TMPDIR`.

The scrub drops `GIT_CONFIG_GLOBAL` by name. The engine writes the git variables to a file `sandbox-env.sh` in the run folder and names it in `CLAUDE_ENV_FILE`; the CLI sources the file in its shell after the scrub. The run folder holds only that file. The run folder is in the temporary folder of the engine, which the sandbox reads by default; the `allowRead` entry for the file matters only when the run folder is under the home folder.

With no global config, the clone has no `user.name` or `user.email`. The engine reads both with its own git at the worktree and sets `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL` in the child environment and in the env file. If either value is empty, the step does not start, and the note names `git config user.email`. The shell gets no credential: a push from the shell still fails.

### The git of the engine

An agent can write the `.git` file of its worktree, the `commondir` file of its record and the git config of the clone. Every git call of the engine in a clone or a worktree therefore sets `GIT_DIR`, `GIT_COMMON_DIR` and `GIT_WORK_TREE` from the engine layout: `<state>/repos/<project>/.git` for a clone, and the record under `.git/worktrees` whose `gitdir` file names the worktree for a worktree. In a project with a source or a forge, and in a project that the config does not hold, a folder that no clone holds a record of runs no git. A project that the config holds with neither has no clone: the engine runs git in its folder as the owner's git does, with the hooks off and local transports only, and the gate copy of such a folder takes its git folders from git in the copy before the gate command starts. A sandboxed step cannot write the `.git` of that folder, because `.git` is a closed name. A rewritten `.git` or `commondir` file does not move a call to another repository. The plans of a step and of a gate take the shared git folder of a worktree from the same records, so a rewritten `.git` file does not add a folder to the read list of the next step. A record that `worktree.useRelativePaths` writes names the worktree with a relative path, and the engine reads it from the record folder.

Before each call, the engine lists the keys of the clone config with `git config --file <clone>/.git/config --list --name-only --null --no-includes`, which starts no program. It refuses the clone when a key is outside the allow list, and the call does not run. The allow list holds the keys that `git clone`, `git worktree add` and a local identity write: `core.repositoryformatversion`, `core.filemode`, `core.bare`, `core.logallrefupdates`, `core.ignorecase`, `core.precomposeunicode`, `core.symlinks`, `extensions.objectformat`, `extensions.refstorage`, `extensions.relativeworktrees`, `user.name`, `user.email`, `remote.<name>.url`, `remote.<name>.fetch`, `branch.<name>.remote` and `branch.<name>.merge`. An include, a hooks folder, a filter, an fsmonitor and every key that a later git adds are refused. `core.hooksPath=/dev/null` stays on every call.

The global and the system config of the owner still apply, so a filter that the owner set there runs as before. The listing and the call are two processes, so a write to the clone config between them is not seen; the sandbox keeps the clone config read-only to the step, which closes that window for a sandboxed step only.

## When the step does not start

The step ends as `not-started`, with a note, when:

- the adapter of the runner has no sandbox;
- the system is Windows (Claude Code runs commands there with no sandbox);
- the Claude Code version is below the minimum;
- the version cannot be read;
- git has no `user.name` or no `user.email` at the worktree.
- the git config of the clone holds a setting outside the allow list (see [The git of the engine](#the-git-of-the-engine)). The note names each setting and the `git config --unset` command that removes it. A plan without the deny of the hooks folder never starts.

A Claude Code that does not know a sandbox key ignores it and exits with code 0, so the minimum version is the only guard.

No adapter falls back to a run with no sandbox. The adapter profile has a `Sandbox` capability and a `MinVersion`. An adapter without the capability refuses a sandboxed step, and the owner sets `sandbox = None` on purpose.

## The gate under the sandbox

When a project turns the sandbox on and builds on this machine, the gate command runs under a sandbox enforcer.

### The plan

The enforcer lets the command:

- read the system folders, the work folder, the shared git directories of a linked work tree and the `sandbox_read` paths, and nothing else under the home folder;
- write the work folder, its own temporary folder (the command gets it as `TMPDIR`) and the `gate_allow_write` paths;
- connect to the `gate_network` domains through one local allow-list proxy.

The kernel limits a TCP connection by port only. The enforcer lets the command connect to the port of the proxy, on any address, and to no other port. The command can connect to no unix socket outside its plan. The enforcer does not limit UDP.

The first line of the output of the gate is the plan. The report of the gate step records it, and a replay reads the recorded result.

### The gate lists

The project file keeps two lists for the gate, and `beehave` records them. The enforcer applies them to a gate that runs on this machine.

| Key | Default | Meaning |
|---|---|---|
| `gate_network` | empty list | The domains that the gate command can reach. |
| `gate_allow_write` | none | Extra paths that the gate command can write. A relative path starts at the work folder. |

### Choice of the enforcer

The project key `sandbox_enforcer` names the enforcer. It holds for the enforcer of the gate and of the copy of the head that the engine push makes (see [push](workflow-files.md#push)), not for the sandbox of Claude Code in an agent step.

| Value | Meaning |
|---|---|
| `auto` | The default. Picks the best enforcer that can run here: `landlock` on Linux, `seatbelt` on macOS. A key that is set and empty is refused. |
| `landlock` | The Linux Landlock enforcer. |
| `seatbelt` | The macOS enforcer. |

Platform support:

- Linux: `landlock` needs Linux 6.7 or later (Landlock ABI 4), because older kernels cannot limit TCP connections.
- WSL2 runs a real Linux kernel, so it can use `landlock` when the kernel is 6.7 or later. beehave does not detect WSL. A kernel below 6.7 makes the check fail.
- WSL1 has no Landlock, so its check fails and the gate is refused.
- Windows without WSL is refused.

### The seatbelt enforcer

The `seatbelt` enforcer runs the command under `sandbox-exec` with a Seatbelt profile that beehave generates from the plan. The profile:

- denies everything by default;
- allows reads and writes by the paths of the plan, and reads of the folders of macOS that every program loads from (`/System`, `/Library/Apple` and the shared cache of the libraries). It adds no other folder: a plan that needs `/usr`, `/etc` or `/opt` names them, as the plan of a gate does;
- allows TCP connect to the port of the proxy on localhost only;
- grants no unix socket;
- allows a Mach service lookup by name only, for the few services that the system libraries need (user and group names, logging, preferences, notifications). A command cannot reach launchd or any other service to start a job.

The profile text holds no path. beehave passes each path to `sandbox-exec` as a `-D` parameter, so a path cannot add a rule. A command can stat any path, but it cannot read a file that the plan does not list.

Apple deprecated `sandbox-exec`, and every macOS still has it. The check of the enforcer fails, `auto` finds no enforcer, and the gate is refused, if:

- `/usr/bin/sandbox-exec` is missing;
- the system refuses the profile;
- a probe write outside the plan succeeds.

The gate never runs unconfined.

The Apple tools in `/usr/bin` (`git`, `make`, `clang` and the others of Xcode) are shims that ask `xcrun` for the real tool. `xcrun` reads the Xcode or Command Line Tools folder and writes a cache file in the per-user system temporary folder, a path that no plan opens. So the enforcer refuses a shim with a message before it starts. A gate command on macOS uses the same tool from another folder, such as the one that Homebrew installs. If it needs the libraries of that tool, they go in `sandbox_read`.

A change under `internal/sandbox/seatbelt` must pass `make check-darwin` on a Mac before it merges, because `make check` runs on Linux and does not compile the macOS code.

### The copy of the work tree

Landlock rights inherit down a folder, so a rule cannot close `.git` below a writable work folder. For this reason the gate command never works in the real work folder.

When the work folder is a git work tree, `beehave` makes a detached worktree of its head in the temporary folder of the gate. It grants that copy whole, and removes it when the gate ends. The command can make, move and remove any file or folder at the top of the copy, such as `coverage.out`, `bin` or `node_modules`.

The real work folder is not in the plan. The real git folder is read-only in it; the copy needs it to read the history, so the command can read its config and hooks. The command cannot write:

- the hooks of the repository;
- the folder that `core.hooksPath` names;
- the `.husky`, `.githooks` and `.claude` folders of the real work folder.

So it cannot plant a hook or an agent setting that runs later as the owner. A `gate_allow_write` path that is, holds or lies in one of them is refused.

The copy removes these things from the gate:

- The gate cannot write the git index or the git objects. `git add`, `git commit`, `git stash` and a tool that installs hooks (`pre-commit install`, a `go generate` that runs `git add`) fail with a permission error. Read-only git works: `git status`, `git diff`, `git ls-files`, `git rev-parse` and the version stamp of `go build`.
- The copy holds the files of the head commit only. The gate tests the commit, not the working tree: a change that is not committed is not tested. Untracked and ignored files (`node_modules`, build output, `.env.local`, fixtures that are not tracked) are not in the copy, and submodules are not checked out.

The `.env` files of the head are in the copy.

### A work folder with no .git

A work folder with no `.git`, such as the folder of a project with no repository, has no copy, so the gate would write the real folder, where a planted `.husky` folder or `.claude` setting runs later as the owner, and Landlock cannot close a name below a writable folder. For this reason the plan gives such a folder to the gate for reading only. The gate of such a folder works in the work folder itself.

- The gate writes its temporary folder and the `gate_allow_write` paths.
- A `gate_allow_write` path that overlaps a closed name of that folder, or the folder itself, is refused, so the gate does not start.
- Exception: the folder that `beehave` makes for an item with no work folder. `beehave` made it for the gate alone, and the gate may write it.

This rule covers the project gate (the `gate` key of the project) and the program gate alike. If a gate in a folder with no `.git` writes a report or build output into that folder, the write fails with "Permission denied". List the folder for that output in `gate_allow_write`, for example `gate_allow_write = ["reports"]`.

Known limit: the enforcer has no right for `chmod`, so a gate can still change the mode of a file in the read-only folder, for example mark it executable. The content of the file stays unchanged. A gate with a copy is not affected, because it changes the copy.

### The program gate

A program gate (a `gate` call with `run`, or the `gate_run` of `merge`) runs under the same enforcer, the same copy and the same plan as the project gate. `gate_network` and `gate_allow_write` apply to it. `beehave check` prints no note for it, because it is not outside the sandbox.

The sandbox does not limit stdin or `max_new_processes` of a program gate.

### The folder of the gate copies

Every copy lives in one folder that beehave owns in the temporary folder, `beehave-gate-<uid>`. The name of a copy holds the process id of beehave. Each copy is a unique folder (mode 0700) inside it.

The folder `beehave-gate-<uid>` must be a real folder, not a link, owned by the user, with no write right for the group or others. Otherwise the gate does not run, because another user could have made the name first on a shared temporary folder.

Removal:

- The copy is removed when the gate ends, also after a signal of `SIGINT`, `SIGTERM` or `SIGHUP`.
- The removal first gives the owner write rights on the folders that the gate left read-only.
- If the removal fails, the output of the gate names the copy.
- After `SIGKILL` or a crash the copy stays. The next gate removes every copy whose process is gone. It also removes, by path, the records of gate copies whose folder is gone from the repository of that gate.
- It never runs `git worktree prune`, so the record of a worktree that the owner made, whose folder is away (an unmounted disk), stays.

### Unix sockets on Linux

- On a kernel with Landlock ABI 9 or later, the kernel refuses a connection to a unix socket outside the plan, and the abstract socket scope refuses an abstract socket. ABI 9 is unverified on a real kernel: no machine of the project runs Linux 6.18 or later.
- On an older kernel, the helper first loads a seccomp filter that refuses `socket(AF_UNIX)`, `io_uring_setup`, every system call of another interface than the native one, and `socket` for every address family with bit 0 set (`AF_UNIX`, `AF_KEY`, `AF_PACKET` and `AF_BLUETOOTH` among them). So the command makes no unix socket at all.
- `socketpair` stays open: its sockets have no name and reach nothing outside the command.

`Check` runs a child that applies the same filter and the same Landlock configuration to a command that does nothing. A kernel or a container that refuses either makes the enforcer unavailable. The gate is then refused. At run time the helper gives these exit codes before the command runs: 127 for a program that is not found, and 125 for a plan that it cannot read, a plan with no command, a confinement that fails, and an exec that fails (for example EAGAIN or ENOMEM under a process or memory limit). After the exec the exit code is the one of the command, or 128 plus the signal number when a signal ended it.

### When no enforcer can run

When no enforcer can run here, `beehave` runs no gate command outside a sandbox that the project asked for, and the gate does not run. The refusal text ends with the reason of the enforcer. The engine push is refused too, in every project, because its copy of the head always runs under an enforcer.

The same holds, even in a project that builds on a host, for:

- a `gate` call that names `run` and has no `host`;
- the gate of `merge`.

A program gate runs on this machine.

Each of these refuses in the step that starts the gate, with a note that says so. The refusal comes before the gate key, before any earlier pass is reused and before any gate program runs. The refusal is neither a pass nor a fail.

A run that recorded a gate pass while the sandbox was off keeps that pass when it resumes with the sandbox on. The replay reads the record and is not refused.

To run the gate on this machine, set `sandbox = "off"` in the project file. `beehave check config` prints the same note for each such project, and `beehave check workflow --project` lists each such call with its position.

### A gate on a build host

A gate on a build host (a `build_host` other than `local`) runs outside the sandbox. The sandbox covers only a command that `beehave` starts on this machine. `beehave check config` prints a note for each such project that has the sandbox on.

### The key of a gate verdict

The key of a recorded gate verdict does not depend on the sandbox. A gate on this machine with the sandbox on refuses before it reads the key, so it never reuses a verdict from an earlier run with the sandbox off. A gate on a build host still reuses a verdict for the same tree and the same command.

## What stays open

### Code left for the gate

A sandboxed agent step protects the owner from the shell of the agent. It does not protect the owner from code that the agent leaves for the gate. The agent can write a Makefile target, a package script or a git hook path, and a later `git commit` then runs that code outside the sandbox, as the owner.

With the sandbox on, the gate on this machine runs under the enforcer (see [The gate under the sandbox](#the-gate-under-the-sandbox)), which limits what that code can read, write and reach. A gate on a build host runs outside the sandbox, and it runs that code as the user of the host.

### Branches of a linked work tree

A linked work tree shares objects and refs with the main repository, and a commit needs them. The shell of the agent can move another branch of that repository, for example with `git update-ref`. That is a risk to the integrity of the branches of the owner. It is not a way to run code. `forbid_commands` with `git-write` does not see it in a sandboxed step.

### Listed domains

A listed domain is a channel out. `github.com` in `network` means the agent can push to any fork if a token is in its environment. A step lists the domains it needs and no token variable it does not need.

### What the sandbox does not cover

- The tools Read, Edit and Write are covered by permission rules, not by the sandbox.
- Command hooks, local MCP servers and LSP servers of the owner files or of the checkout are not covered by the sandbox. The isolation flags above remove them.
- The hook of `forbid_commands` is ours and runs outside the sandbox on purpose. Its input is text from the agent, so its parser is an attack surface.

### Not yet proved against the real CLI

- the Linux sandbox (the Unix-socket block needs the optional seccomp filter there);
- the denial of `WebFetch` and `WebSearch` in print mode;
- the `sandbox.credentials` page in place of the hand list;
- whether `--setting-sources ""` also drops managed settings;
- the first version that knows `failIfUnavailable`.

### make smoke-sandbox

`make smoke-sandbox` proves the rest on macOS with the real CLI. It runs one sandboxed step over a fake home folder. The step:

- reads the fake credential files and the `.env` files with `cat` and with the Read tool;
- writes outside the work folder;
- writes `.githooks/pre-commit`;
- fetches one domain that is off the `network` list and one that is on it.

Each denied action passes only with the tool result of its call and a denial text in that result. The agent writes its transcript into the Claude profile of the run, because a fresh profile holds no login.

The target passes only when:

- each denied action is denied;
- a control file reads;
- the listed domain answers;
- no marker of a denied file is in the transcript or the state folder.

It costs a real run (capped at 0.30 USD) and is not part of `make check`. On macOS the fake home links to the real keychain folder so that the login of the profile works.

The WebFetch and WebSearch denial stays unproved there, because the step removes the two tools with `--disallowedTools` and the smoke fetches with `curl`.
