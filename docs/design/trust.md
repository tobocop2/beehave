# The trust boundary

Part of the [beehave architecture](../architecture.md).

The rule for every decision here: a rule that the forge or the operating system can enforce is not a rule of beehave. beehave asks the forge adapter for a capability and verifies what the forge enforces. beehave holds no credential where an agent can reach it. If a forge lacks a capability, the engine refuses with a clear error. It never falls back to custom code.

## Status

This page is the design. The pages [Sandbox](sandbox.md) and [Merge](merge.md) describe the code as it is today.

Built: the `credentials` key with the `writer` role, the `file` and `command` loaders, the empty `GH_CONFIG_DIR` and `GH_PROMPT_DISABLED=1` of each `gh` call, the credential helper of each git call to the remote, the redactor prefixes and the start probe. The `git` calls that reach a remote (clone, fetch, ls-remote and push) run as the `writer` of a project that has `credentials`. A project with `credentials` starts only when its `source` is empty or an `https://` address, because the token serves https only. The credential helper of a git call answers for the https protocol and the host of the address of that call, and gives nothing for any other host. The client commands that reach the remote (`cancel`, `resume`, `answer` and `send`) refuse a git call to the remote of a project with `credentials`, because they do not load the token. A project with no `credentials` key keeps the login of the owner, and its git calls to the remote start with no git config. They keep only the `credential.*` settings of the system and global git config, which the engine reads once when it starts and passes with `-c` on each call. A `core.sshCommand`, a `url.insteadOf` or a `core.hooksPath` in those files does not run and does not redirect a clone, a fetch or a push. The client commands read the same settings when they start. An engine that starts again to resume a run reads the files again, so a `credential.helper` that an agent wrote before the resume applies to the next owner call. The helper of the owner needs the variables of the engine process, and the call keeps `PATH`, `HOME`, `TMPDIR`, `XDG_CONFIG_HOME`, `GH_TOKEN`, `GITHUB_TOKEN` and `GH_CONFIG_DIR`. An ssh remote needs no git config: `ssh` reads `~/.ssh`, and the call keeps `SSH_AUTH_SOCK` for the SSH agent. An `ssh` setting in `~/.ssh/config` is not a git setting, and this rule does not cover it. The engine commits in an item worktree are unsigned. The engine's own git calls start with no global config, so the `commit.gpgsign`, `gpg.program` and `user.signingkey` of the owner do not apply. A signing program named in a config that an agent can write is the same hole as a `core.fsmonitor` command. If a project requires signed commits on its branches, the engine commits fail that rule. The start probe checks the environment of an agent step and a command step. A command step that does not set `sandbox` still runs as the owner.

This list is not complete. Not built yet: the deleted `Approved` flag and guard 0 of `merge`, the `CanWriteCI` and `Protection` start checks (the tree still has `TokenScopes`), the `Login` forge method, the `reviewer` role and the `login` and `ci_locked` fields of `credential`, the `As(role)` forge calls, the engine push step, the failure status of a red gate and the status posts of a gate outside the batch `merge` (the batch `merge` posts the two success statuses; see [merge.md](merge.md#commit-statuses)), the base program of the local gate, the rest of the new environment of an agent step (the `SSH_AUTH_SOCK` drop is built) and the network lists without `github.com`. The sandbox default, the ledger lift and the claim fence are also design only. The ruleset in this page is not posted.

## The threat model

There are three trust levels. Each decision below names the level that it protects against.

| Level | Who | Holds | May do |
|---|---|---|---|
| Owner | The owner and the engine process that the owner starts | The `writer` credential (by default a token of the owner's own account), the optional `reviewer` credential, the `gh` login of the owner, the SSH agent | Everything. The engine reads each credential once at start and holds it in memory only. |
| Agent | Every agent step, ours or a model's | A worktree, a network allow-list, the model token of its CLI | Edit files and commit in its worktree. Nothing on the forge. |
| Outside | Issue text, a foreign pull request, the code of its head | Nothing | Reach an agent prompt as data, and a sandboxed review step on a host with no credential. |

The boundary between Owner and Agent is the operating-system sandbox, and the absence of any forge credential in the reach of the agent. The boundary on the forge is the ruleset. No identity that an agent can reach can push to `integration/**` or merge. No merge happens without the required statuses at the head.

An agent step that runs without the sandbox runs as the user id of the owner. It can read any file and the login keychain of that user. No scrub of the environment changes that. So an unsandboxed agent step is never a trust boundary, and no part of this design relies on one.

## Identities

beehave needs no second account. The engine holds one credential, `writer`, and uses it for every push, status, comment and merge.

| Mode | Credentials | What the forge requires |
|---|---|---|
| Default | `writer` only: a fine-grained token of the owner's own account | `beehave/review` and `beehave/gate` at the head. A pull request into `main` also needs its CI run to pass. A pull request into an integration branch has no Actions check. No approval. |
| Optional | `writer` and `reviewer`: one bot account | The same, plus one native approval from a non-author. |

In the default mode the independent review verdict is the commit status `beehave/review`. The engine posts it at the reviewed head. The owner is the author of the pull request, so the owner cannot approve it. The owner posts `beehave/review` by hand when needed.

In the optional mode the engine also submits a native approval as the `reviewer` bot. The bot is a collaborator with write access, because only an approval from write access counts. Only the engine holds its token.

No mode needs a GitHub App. An App token source for the `reviewer` role can come later as an option in the GitHub adapter. It is not the default.

### Credential config

The project file gets one key:

```
project(
    ...,
    credentials = {
        "writer": credential(command = ["security", "find-generic-password", "-s", "beehave-writer", "-w"], ci_locked = True),
        "reviewer": credential(file = "~/.config/beehave/credentials/reviewer.token", login = "<bot login>"),  # optional
    },
)
```

- `credential(file?, command?, login?, ci_locked?)` takes exactly one of `file` and `command`. There is no `env` source: the start block of the environment of the engine stays readable by `ps eww` and `/proc/<pid>/environ` (see "Platform gaps"), even after the engine removes the variable.
- `file`: a path inside `~/.config/beehave/credentials/`, mode 0600, in a folder of mode 0700. The loader refuses any other mode and path.
- `command`: an argument list, run once at start with no shell. Its trimmed output is the token. Examples: the macOS keychain, the Linux Secret Service, `gh auth token`.
- `login`: optional. The engine reads the login of the token at start with the forge method `Login` and refuses when it differs.
- A workflow that pushes, posts or merges needs `writer`. Without it, `beehave check workflow` refuses.

The recommended `writer` token on GitHub is a fine-grained token of the owner's account. It covers the project repository only. It has Contents, Pull requests, Issues and Commit statuses at read and write, Metadata at read, and no Workflows permission.

### The engine proves that agents cannot read the credentials

1. Every source is read in the engine process. No agent spec, workflow input, step output, event, error text or record holds a token.
2. A `file` source must lie under `~/.config/beehave`, which is on the sandbox credential list.
3. A `command` source keeps the token in the keychain or keyring. Both are on the credential list.
4. The engine builds the environment of a probe agent step. It refuses to start when that environment holds a token value or any of `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GITLAB_TOKEN`, `CI_JOB_TOKEN` or `SSH_AUTH_SOCK`.
5. A project with the sandbox on refuses an unsandboxed agent step.
6. The redactor knows the prefixes `ghp_`, `gho_`, `ghs_`, `github_pat_` and `glpat-`.

### An agent step holds no forge credential

1. The tokens live in the forge adapter. No agent spec has a field for one. Each forge call runs as one role, with that token and an empty `GH_CONFIG_DIR` and `GH_PROMPT_DISABLED=1`. A call with no credential for its role fails closed. No step uses the `gh` login of the owner.
2. The environment allow-list of an agent drops `SSH_AUTH_SOCK`.
3. The CLI process of an agent step keeps the `HOME` of the owner, so that its login works. The shell of a sandboxed step gets a `HOME` that is a new, empty folder of that step, in a temporary folder that the engine makes for the step and removes when the step ends. The shell of a sandboxed step also gets `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1`. It sets `GIT_AUTHOR_*` and `GIT_COMMITTER_*` to the git identity of the engine for the work folder, so its commits carry that identity. No agent code sets `GH_CONFIG_DIR`: an agent step holds no forge credential in its environment, and the sandbox denies the `gh` config of the owner.
4. The sandbox denies `~/` and the credential list. No network list names `github.com`, so `git push` over HTTPS has no host.
5. The engine pushes, not the agent. A `push` step runs with `--force-with-lease=refs/heads/<branch>:<expect>`. It is a recorded step, so a replay does not push again. The prompts contain no push line.
6. A `command` step gets no forge token. A workflow cannot ask for one. It gets the same environment as an agent step, so it has no `SSH_AUTH_SOCK`.

### Command steps

A `command` step runs code that an agent wrote (`make`, tests, the gate program), and it runs as the user id of the owner. So an unsandboxed `command` step is not a trust boundary. It can read the `gh` config, the keychain and any file by its absolute path. A new empty `HOME` does not close that: a keychain login and an absolute path stay open, and an empty home breaks `go`, the git identity and `bd`.

Two rules close it.

- A forge write is not a `command` step. The round writes (`pr_take`, `round_sync`, `round_land`, `integration_pull`, `round_cut`, `board_card`) are engine builtins that run as `writer`. The `remedy` step that stays (`report`) and `beehave check gate-list` only read.
- A `command` step that runs agent-written code sets `sandbox = sandbox()` (see [`sandbox` of `command`](workflow-files.md#sandbox)). It reuses the enforcer of the gate (`cmd/beehave/gatesandbox.go`), and its plan is the plan of the gate: the system folders, the work folder and the `sandbox_read` and `gate_allow_write` paths of the project. The Go folders come only from those two keys, and the home folder of the owner is never granted. A step that leaves `sandbox` out keeps today's behavior.

An unsandboxed `command` step that is left is a limit. Until the sandbox covers it, the ruleset stays unposted while such a step can run agent code.

### Limits

| Channel | Mechanism or limit |
|---|---|
| `SSH_AUTH_SOCK` variable | No allow-list (agent, `command`, usage reader) holds it, `env` refuses the name in a `command` or `write` call, and a sandboxed gate drops it. Keys that the agent holds are out of reach. A step keeps `HOME`, so `ssh` still finds a key file in `~/.ssh`. That is a limit for an unsandboxed step. A sandboxed step denies `~/.ssh`. |
| The agent socket, a file | A sandboxed step: the engine adds the path of its own `SSH_AUTH_SOCK` to the deny list of the plan, with the target when the socket is a link, and `~/.ssh` is denied. Whether the deny of a file also stops a connect to a Unix socket is not measured (`make smoke-sandbox` is owed). An unsandboxed step can find the socket. This is a limit. The sandbox default closes it, because a project with the sandbox on refuses an unsandboxed step. |
| The gate step | A gate under the sandbox of the project gets no `SSH_AUTH_SOCK`, and the plan refuses a read or write path that holds the socket. A gate with no sandbox keeps the variable and runs as the owner. That is a limit. |
| The OpenCode launch | It has the same `HOME` as the Claude launch (`internal/agent/opencode/guard.go`) and the same allow-list, so it has the same channels. They are not measured. |
| The token file of the `file` source | An unsandboxed step can read it. This is a limit, closed the same way. The sandbox denies `~/.config/beehave`. |
| The git credential helper of the owner | An unsandboxed step reads `~/.gitconfig`. The engine sets `GIT_CONFIG_GLOBAL=/dev/null` for its own git calls in a clone or a worktree and for its forge calls of a project with credentials. The token of a call is readable by the same user while the call runs (`ps eww`). This is a limit. |
| The start check | It sees the environment that the engine builds. It cannot see files, so it checks the environment channel only. |

### The CI lock

The CI of a head runs with the workflow files of that head. A workflow that an agent wrote could ask for `statuses: write` and post `beehave/review` itself. Two rules stop that.

- The `writer` token has no Workflows permission. GitHub refuses a push that creates or updates a workflow file with such a token. The forge enforces this, not beehave.
- `ci.yaml` has `permissions: contents: read`, so the code that `check` and `test` run cannot post a status.

A lane that must change a workflow file is held for the owner. The owner lands that change by hand.

The start check asks the forge adapter whether the `writer` can change CI files. It has three answers.

- `no`: the engine starts.
- `yes`: the engine refuses to start, whatever `ci_locked` says. On GitHub, a classic token whose scopes hold `workflow` gives `yes`.
- `unknown`: the engine starts only when the `writer` entry sets `ci_locked = True`. A fine-grained token gives `unknown`, because the engine cannot read its permissions. `ci_locked` is the statement of the owner that the token has no Workflows permission.

## The forge enforces the merge

### The ruleset

The owner posts the ruleset in [`trust/integration-ruleset.json`](trust/integration-ruleset.json):

```
gh api -X POST repos/tobocop2/beehave/rulesets --input docs/design/trust/integration-ruleset.json
```

It protects `integration/**`. It requires a pull request, resolved review threads, and the statuses `beehave/review` and `beehave/gate`. GitHub CI runs on pull requests into `main` only, so the ruleset names no GitHub Actions check. It requires 0 approvals and has no bypass actor.

- `beehave/review` and `beehave/gate` name no source. A status that a user token posts has no App to name. GitHub shows the author of each status in the merge box. Without an App, the ruleset cannot restrict the source of these two statuses. The CI lock and the empty credential list of agents take its place.
- Who can post a status: users with push access, and an Actions token with `statuses: write`. Here that is the owner, the engine with the token of the owner, and an installed App with Commit statuses write (owner step 4). The CI lock rules out the Actions token.
- A status belongs to one commit. A new push makes a head with no `beehave/review`, so a stale verdict cannot pass.
- `required_approving_review_count` is 0 because the owner is the author and cannot approve.
- `bypass_actors` is empty, so the admin role of the owner does not bypass the ruleset. The owner can still edit it. No agent can.
- `strict_required_status_checks_policy` is false. `merge` merges members one after another into a moving base. A strict policy would make each later member out of date. The union gate of the batch holds the up-to-date property instead.

### The optional mode

With a `reviewer` credential, the owner posts [`trust/integration-ruleset.reviewer.json`](trust/integration-ruleset.reviewer.json). It sets one required approval and `require_last_push_approval`. The approval of the bot must come from write access. No document proves that the approval of a bot counts, so the owner checks it on a probe repository first (owner step O4).

### What each mode stops

| Threat | Default | Optional |
|---|---|---|
| An agent merges, approves, pushes to integration or posts a status | Stopped: the agent holds no credential | Stopped |
| The CI of a head posts `beehave/review` | Stopped by the CI lock | Stopped by the CI lock |
| A stale verdict on a new head | Stopped: statuses are per commit | Stopped: per commit, plus `require_last_push_approval` |
| An App that the owner installed with Commit statuses write posts `beehave/review` | Not stopped. Owner step 4 lists the installed Apps. | Not stopped for the status. Stopped for the approval. |
| The owner posts a status or merges by hand | Allowed: it is the authority of the owner | Allowed |
| A bug in the engine posts `success` for a red review | Not stopped: the engine is trusted code | Not stopped |
| A stolen `writer` token | The full power of that token on the repository | The same. The attacker still lacks the approval. |
| Telling the work of the engine from the work of the owner in the history of the forge | Not possible: both are the owner. The status description names the run and the gate key. | The reviewer bot is visible. |

### The merge checks no flag

The merge request carries no `Approved` flag. The forge refuses a merge without the required statuses at the head. The engine merges pinned with `--match-head-commit`. The engine still reads the protection of the branch at the start of a round, and reports which required context is missing when the forge refuses.

### The forge capability methods

Each adapter maps these methods to its forge. A forge that cannot do one returns `forge.ErrNoCapability` with the reason, and the engine refuses.

| Method | GitHub | GitLab | Forgejo |
|---|---|---|---|
| Call as a role | `GH_TOKEN` per call | `GITLAB_TOKEN` per call | token per call |
| Push at an expected head | `git push --force-with-lease` | the same | the same |
| Post a status | `POST repos/{repo}/statuses/{sha}` | external status check response | `POST repos/{repo}/statuses/{sha}` |
| Post a review | `POST pulls/{n}/reviews` with `commit_id` | approve at the head SHA | `POST pulls/{n}/reviews` with `commit_id` |
| Read the protection | rules for the branch | approval rules and status checks | branch protection |
| Login | `gh api user` | `GET /user` | `GET /user` |
| Can the `writer` change CI files | `yes` for a classic token with the `workflow` scope, `unknown` for a fine-grained token | `no` when the CI file lives in another project | `unknown` |

The default mode on GitLab needs external status checks, which is an Ultimate feature. On a lower tier the adapter reports the capability missing and the engine refuses. On Forgejo, branch protection takes a status context pattern and refuses a merge until it succeeds.

## Merge queue

`tobocop2/beehave` is a public repository of a user account on the free plan. GitHub offers a merge queue for public repositories of an organization, or for private repositories on Enterprise Cloud. So this repository cannot use one.

`merge` is the landing path. It gates the union of the members once and merges each member pinned with `--match-head-commit`. Under the ruleset the forge also checks each member merge: the required statuses and the resolved threads.

A project of an organization can use a merge queue instead. That variant is not built. To get one, the owner moves the repository to an organization. That changes the URLs and the repository access of the token, so it is a decision of the owner.

## The sandbox is on by default

An unset `sandbox` in a project gives `on`. `sandbox = "off"` stays, as an explicit choice of the owner for the whole project. `beehave run` prints one line that names it at the start of every run of such a project.

A step with `sandbox = None` in a project with the sandbox on is refused by `beehave check workflow` and does not start. A step that needs `docker` runs as a `command` step.

The engine also refuses such a step at the agent call, before it picks a login. The refusal fails the run, and `beehave resume` refuses it again. To go on, set `sandbox = "off"` in the project, run `beehave cancel <item>`, then run `beehave run <item>`. Every run stores its sandbox mode, on or off, when it starts. Only a run recorded before the mode was stored has none, and it reads the live sandbox mode of its project.

The step fails closed: no sandbox capability, Windows, a CLI below the minimum version or an unreadable version ends the step `not-started`. No adapter falls back to an unsandboxed run.

A project with `sandbox = "off"` still gets the environment of an agent step described above. It still runs as the user id of the owner, so beehave treats such a project as trusted input only.

A foreign pull request refuses to start when the project has the sandbox off, or when its `build_host` is not declared `host(..., credentials = "none")`. That key is the statement of the owner that the host holds no forge credential, no `credentials` source and no SSH agent forwarding. The host check reads `ssh -G <host>` and refuses `forwardagent yes`.

### Platform gaps

| Gap | macOS | Linux |
|---|---|---|
| Agent sandbox | Seatbelt through Claude Code. `make smoke-sandbox` proves it. | bubblewrap through Claude Code. Not proven (#199). Until it is, a step ends `not-started` if the CLI reports no sandbox. |
| Gate sandbox | `sandbox-exec` (deprecated by Apple, present) | Landlock ABI 4 (Linux 6.7 and later). ABI 9 socket scope is not verified (#252). |
| Unsandboxed step | Can read the login keychain. | Can read the Secret Service keyring if a session bus is reachable. |
| Same-user process reads | `ps eww` shows the environment of processes of the owner, so a token in the child `gh` of the engine is visible to an unsandboxed agent. | `/proc/<pid>/environ` is readable by the same user. |
| Windows | Refused. | Not applicable. |

## The gate result comes from the forge

After the candidate gate passes on head `H`, the engine posts `POST repos/<repo>/statuses/H` with `state = success`, `context = beehave/gate` and a description that names the gate key and the run id. A red gate posts `failure`. A void gate posts nothing.

The review step posts `beehave/review` the same way, with the run id in the description.

### The gate program comes from the base

A head can weaken the program behind the gate: `make check` and its scripts. The owner is the author of the pull requests and cannot approve them. So a code-owner rule would block every change to those files, and beehave adds no `CODEOWNERS` file.

Instead, the project key `gate_program` lists repository paths. For beehave they are `Makefile`, `scripts/` and `.golangci.yml`. The host gate clones the head, refuses a head that changed a listed path, removes the listed paths and restores them from the base commit, and compares them with the base after the last step. The gate runs the program of the base, so a head that weakens `make check` is gated by the `make check` of the base. See `gate.md`.

A head whose code needs a new gate program fails the base gate. The owner then lands it by hand. The CI lock stops a head from rewriting the workflow.

With a `writer` bot, `CODEOWNERS` works again as an extra rule, because the owner is then not the author. That is not part of the default design.

Required workflows from another repository are an organization feature, and not available here.

### Self-hosted runners against the host gate

| Part | Host gate (beehave) | Self-hosted Actions runner |
|---|---|---|
| Start and stop the build host | `internal/host` | Not owned. An ephemeral runner needs an autoscaler. |
| Run the gate on a pinned head | `internal/gate`: clone, `checkout --detach`, pins, JUnit, gate key | Owned: `actions/checkout` at the SHA of the event. |
| Known base reds | `internal/gate` and the base result | Not owned. |
| Replay of a verdict | The gate key and the recorded step | Not owned. A re-run is a new run. |
| Report to the forge | One status post | Owned. |
| Public repository safety | The host is the host of the owner and runs only what the engine sends. | GitHub advises against self-hosted runners for public repositories, because any user can open a pull request against them. |

beehave keeps the host gate for the candidate and posts its verdict as `beehave/gate`. The repository uses GitHub-hosted runners for its own CI. beehave adds no self-hosted runner.

## Durable round state

Retention deletes successful runs after 7 days, so a count that reads run records resets. The ledger holds the counts that must last.

The package `internal/ledger` owns `<state folder>/ledger.db`, a SQLite file with mode 0600. Retention never touches it. Only `beehave round close <base>` deletes rows. It has three tables:

- `rounds`: one row per round with its status, last pass, integration pull request and the version of the build with the version of each step module.
- `round_items`: one row per item with its attempt, run id, branch, pull request, the head that the engine last pushed (the fence) and its state.
- `spend`: one row per agent execution with its tree, run, step, session, item, pull request, call name and cost. A `NULL` cost means unknown.

An agent step writes its `spend` row with `INSERT OR IGNORE` after the agent exits and before the step returns. A replay of a finished step does not run the body. The fix cap counts the `fix` rows of a pull request. The tree budget sums the cost by tree, and any unknown cost fails the check.

### The round is a chain of short workflows

The round entry is `round_pass`. One pass reads the round record, audits, queues, spawns, gathers, lands and writes the record. Then it calls `next_pass(name = "round-next-<n>")`. That builtin enqueues the next pass and returns. DBOS dedupes the id, so a replay enqueues nothing twice.

The next pass starts under the binary that runs it, so it has the current module versions. A bump of the version of a step module strands at most the one open pass, and only when the pass recorded a step of that module. `beehave round restart <base>` starts that pass again. It spawns no item twice, because it reads its children from `round_items` by run id.

No pass waits for the owner. When no item can move, the pass sets `status = waiting`, updates the integration pull request and returns. `beehave round continue <base>` starts the next pass.

## The claim lease

A fencing token works only when the resource checks it. Neither `bd` nor GitHub checks a token of ours. GitHub does check the expected old value of a ref, atomically. So the fence is the branch head.

- `push` runs with `--force-with-lease=refs/heads/<branch>:<expect>`. `expect` is the head in `round_items`, or empty for a new branch, which the forge reads as "the branch must not exist".
- `merge` pins with `--match-head-commit`.
- A stale worker that lost its claim fails at its next push or merge, at the forge.
- Inside one state folder, the DBOS workflow id `item-<n>-r<k>` dedupes a second start.
- Across engines, preflight reads the assignee and the linked pull requests. An own open pull request that `round_items` does not hold stops the item as `HELD`.

The `bd` lease stays for hand-run lanes as the `claims = "beads"` adapter. It is unfenced and is not called on the round path.

## The block list is hygiene

The shell scanner has no kind for `gh pr merge`, `gh pr review --approve`, `gh api` merge calls or `gh auth token`. A kind would be a second, weaker copy of the identity split. An agent that runs `gh pr merge` has no credential, and the forge refuses a merge without the required statuses.

## Owner steps

Run these as the owner, in order. No step creates an account.

### Default mode

1. On github.com, open Settings, Developer settings, Personal access tokens, Fine-grained tokens, Generate new token. Set the resource owner to your account and the expiry to one year or less. Under Repository access, select "Only select repositories" and `tobocop2/beehave`. Set Contents, Pull requests, Issues and Commit statuses to "Read and write". Leave Workflows at "No access". Generate the token.
2. Run `security add-generic-password -a "$USER" -s beehave-writer -w`. Paste the token at the prompt. The token is not on the command line.
3. In the repository settings, under Actions, General, set "Workflow permissions" to "Read repository contents and packages permissions". Keep "Allow GitHub Actions to create and approve pull requests" cleared.
4. In Settings, Applications, check each integration installed on `tobocop2/beehave`. If one has Commit statuses write and does not need it, remove it from the repository.
5. When the push step exists, push a throwaway branch that changes `.github/workflows/ci.yaml` with the token. If GitHub refuses the push, record the result in this page. If GitHub accepts it, the token has Workflows access. Fix step 1 and run this step again.
6. When the credential config exists, add `credentials = {"writer": credential(command = ["security", "find-generic-password", "-s", "beehave-writer", "-w"], ci_locked = True)}` to `~/.config/beehave/projects/beehave.star`.
7. When the engine posts both statuses, run `gh api -X POST repos/tobocop2/beehave/rulesets --input docs/design/trust/integration-ruleset.json`.
8. Optional, for a merge queue: make an organization, transfer the repository to it, and post the ruleset again with a `merge_queue` rule.

### Optional mode

- O1. Make one machine account for the reviewer and turn on two-factor authentication for it.
- O2. Invite it to `tobocop2/beehave` as a collaborator, and accept the invitation as the bot. A collaborator on a personal repository has write access, which an approval needs.
- O3. As the bot, make a classic token with the `public_repo` scope only. A fine-grained token cannot reach a repository where its account is a collaborator of another user. Run `security add-generic-password -a "$USER" -s beehave-reviewer -w` and paste it.
- O4. Make a probe repository `tobocop2/beehave-trust-probe` with one commit on `integration/probe` and the bot as a collaborator. Add a ruleset on `integration/**` with one required approval and no bypass. Open a pull request as the writer and approve it as the bot. Run `gh pr view <n> -R tobocop2/beehave-trust-probe --json reviewDecision,mergeStateStatus`. If `reviewDecision` is `APPROVED` and `mergeStateStatus` is not `BLOCKED`, the approval counts. Record that in this page.
- O5. Add `"reviewer": credential(command = ["security", "find-generic-password", "-s", "beehave-reviewer", "-w"], login = "<bot login>")` to `credentials`. Post `docs/design/trust/integration-ruleset.reviewer.json`.

## Open decisions

- The ruleset `main` keeps the role bypass `always` and requires signed commits. The engine does not sign its commits. So `integration/*` lands in `main` as a merge commit that the owner makes on github.com with the role bypass, where GitHub signs the commit. The trust work does not change `main`.
- A lane cannot change `.github/workflows/`. The owner lands such a change by hand. To let lanes change CI, the forge must pin the source of a status, and that needs an App token source.
- The agent sandbox on Linux waits on #199. Until it passes, an agent step on Linux can refuse to start. That is fail-closed.
- On GitLab, whether a commit status from the API counts under "Pipelines must succeed" on lower tiers is not documented. No GitLab adapter exists yet.
