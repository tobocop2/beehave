# The trust boundary

Part of the [beehave architecture](../architecture.md).

The rule for every decision here: a rule that the forge or the operating system can enforce is not a rule of beehave. beehave verifies what the forge enforces. beehave holds no credential where an agent can reach it.

This page is the design. The pull requests of the trust work build it in steps. The pages [Sandbox](sandbox.md) and [Merge](merge.md) describe the code as it is today.

## The threat model

There are three trust levels. Each decision below names the level that it protects against.

| Level | Who | Holds | May do |
|---|---|---|---|
| Owner | The owner and the engine process that the owner starts | The app private keys, the `gh` login of the owner, the SSH agent | Everything. The engine mints app tokens and holds them in memory only. |
| Agent | Every agent step, ours or a model's | A worktree, a network allow-list, the model token of its CLI | Edit files and commit in its worktree. Nothing on the forge. |
| Outside | Issue text, a foreign pull request, the code of its head | Nothing | Reach an agent prompt as data, and a sandboxed review step on a host with no credential. |

The boundary between Owner and Agent is the operating-system sandbox, and the absence of any forge credential in the reach of the agent. The boundary on the forge is the ruleset. No identity that an agent can reach can merge. No identity can merge without an approval at the head.

An agent step that runs without the sandbox runs as the user id of the owner. It can read any file and the login keychain of that user. No scrub of the environment changes that. So an unsandboxed agent step is never a trust boundary, and no part of this design relies on one.

## Two GitHub Apps

| App | Repository permissions | Used for |
|---|---|---|
| `beehave-writer-<owner>` | Contents: write. Pull requests: write. Issues: write. Commit statuses: write. Workflows: write. Metadata: read. | Push lane branches, open and edit pull requests, comment, label and assign items, post `beehave/gate`, merge. |
| `beehave-reviewer-<owner>` | Pull requests: write. Contents: read. Metadata: read. | Post reviews: `APPROVE`, `REQUEST_CHANGES`, `COMMENT`. |

Each app has no webhook and no account permission. Each app installs on the account of the owner only, on selected repositories only.

The writer has `Workflows: write` because a lane can change `.github/workflows`. `CODEOWNERS` gives such a change to the owner.

If an approval needs write access (owner step 9 decides), the reviewer gets `Contents: write`. A second ruleset, `lanes`, on `refs/heads/beehave/**` then adds the rule `update` with the writer app as the only bypass actor. The reviewer cannot then push to a lane branch. Only the engine holds the reviewer token.

### Minting the tokens

The library `github.com/bradleyfalzon/ghinstallation/v2` signs the App JWT and exchanges it for an installation token. `Transport.Token` returns the cached token and renews it before it expires. beehave writes no JWT code.

The file `internal/forge/github/apps.go` is the one place that makes this exchange over `net/http`. The `no-http-client` rule of `depguard` has one named exception for that file. Every other forge call stays a `gh` call.

Each forge call carries an identity, writer or reviewer. The `gh` process of a call gets `GH_TOKEN` for that identity and an empty `GH_CONFIG_DIR`, so `gh` reads no file of the owner. Reads use the writer. No step uses the `gh` login of the owner.

A token never enters a workflow input, a step output, an event, an error text or a record. The redactor knows the `ghs_` prefix.

### The private keys

A key lives at `~/.config/beehave/apps/<app slug>.pem`, mode 0600, in a folder of mode 0700. The loader refuses any other mode and any path outside that folder.

`~/.config/beehave` is on the credential list of the engine, so a sandboxed step cannot read the keys. An unsandboxed step can read them. So the sandbox is on by default (see below), and foreign input never meets an unsandboxed step.

### An agent step holds no forge credential

1. The token lives in the forge adapter. No agent spec has a field for it.
2. The environment allow-list of an agent drops `SSH_AUTH_SOCK` for every step.
3. Every agent step gets `HOME` and `GH_CONFIG_DIR` set to empty folders of the step. It also gets `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, `GIT_TERMINAL_PROMPT=0`, and the writer bot as git author and committer. The login folder of the CLI stays on its own variable.
4. The sandbox denies `~/` and the credential list. The network list of a step drops `github.com`, so `git push` over HTTPS has no host. `api.github.com` stays, for reads of public data with no token.
5. The engine pushes, not the agent. A `push` step runs as the writer with `--force-with-lease=refs/heads/<branch>:<expect>`. It is a recorded step, so a replay does not push again. The prompts contain no push line.
6. A `command` step gets no forge token. A workflow cannot ask for one.

## The forge enforces the merge

### The ruleset

The owner posts the ruleset in [`trust/integration-ruleset.json`](trust/integration-ruleset.json):

```
gh api -X POST repos/tobocop2/beehave/rulesets --input docs/design/trust/integration-ruleset.json
```

- `bypass_actors` is empty, so the admin role of the owner does not bypass the ruleset. The owner can still edit it. No agent can, because no agent holds a credential.
- `15368` is the GitHub Actions app. `beehave/gate` must come from the writer app, so a status with the same name from another source does not count. The `0` for `beehave/gate` in the file is a placeholder for the writer app id. Replace it before the owner posts the ruleset (owner step 10).
- `strict_required_status_checks_policy` is false. `batch_merge` merges members one after another into a moving base. A strict policy would make each later member out of date, push a new head and dismiss its approval. The union gate of the batch holds the up-to-date property instead.
- `require_last_push_approval` holds by construction: the writer pushes and the reviewer approves. A push by anyone else after the approval needs a new approval.
- A ruleset of a user-owned repository cannot name a reviewer identity. The rule asks for one approval from an identity with write access. Only the owner and the reviewer app are such identities. Guard 0 adds the check that the approval is from one of them.

### Guard 0 reads the forge

Today the request to `merge` carries an `Approved` flag that a workflow sets. After the trust work, the flag does not exist. Guard 0 reads the reviews of the pull request once. The pull request passes when the latest review of a reviewer login is `APPROVED` and its `commit_id` equals the gated head. A later `CHANGES_REQUESTED` or `DISMISSED` review of that login cancels the approval.

The reviewer logins are the `reviewer_login` fact and the login of the owner. `batch_merge` gets the same lift with no new keyword, because the guard reads the forge.

The own logins gain the writer bot. A commit of the writer bot is our work and needs the reviewer approval.

The workflow-scope guard reads the permissions of the writer installation token. It passes when they hold `workflows: write`.

### Does the approval of the reviewer app count?

No document proves it. GitHub says that required approvals come from "reviewers with write permissions". Owner step 9 decides on a probe repository, before the ruleset is relied on. The result goes in this page.

## Merge queue

`tobocop2/beehave` is a public repository of a user account on the free plan. GitHub offers a merge queue for public repositories of an organization, or for private repositories on Enterprise Cloud. So this repository cannot use one.

`batch_merge` stays as the landing path. It gates the union of the members once and merges each member pinned with `--match-head-commit`. Under the ruleset the forge also checks each member merge: approval at the head, the required checks and resolved threads. `batch_merge` merges as the writer app.

A project of an organization can use a merge queue instead. That variant is not built. To get one, the owner moves the repository to an organization. That changes the URLs and the app installations, so it is a decision of the owner.

## The sandbox is on by default

An unset `sandbox` in a project gives `on`. `sandbox = "off"` stays, as an explicit choice of the owner for the whole project. `beehave run` prints one line that names it at the start of every run of such a project.

A step with `sandbox = None` in a project with the sandbox on is refused by `beehave check workflow` and does not start. A step that needs `docker` runs as a `command` step.

The step fails closed as today: no sandbox capability, Windows, a CLI below the minimum version or an unreadable version ends the step `not-started`. No adapter falls back to an unsandboxed run.

A project with `sandbox = "off"` still gets the environment of an agent step described above, and no `SSH_AUTH_SOCK`, no `gh` config, no global or system git config and no token name. It still runs as the user id of the owner, so beehave treats such a project as trusted input only.

A foreign pull request refuses to start when the project has the sandbox off, or when its `build_host` is not declared `host(..., credentials = "none")`. That key is the statement of the owner that the host holds no forge credential, no SSH agent forwarding and no app key. The host check reads `ssh -G <host>` and refuses `forwardagent yes`.

### Platform gaps

| Gap | macOS | Linux |
|---|---|---|
| Agent sandbox | Seatbelt through Claude Code. `make smoke-sandbox` proves it. | bubblewrap through Claude Code. Not proven (#199). Until it is, a step ends `not-started` if the CLI reports no sandbox. |
| Gate sandbox | `sandbox-exec` (deprecated by Apple, present) | Landlock ABI 4 (Linux 6.7 and later). ABI 9 socket scope is not verified (#252). |
| Unsandboxed step | Can read the login keychain. | Can read the Secret Service keyring if a session bus is reachable. |
| Same-user process reads | `ps eww` shows the environment of processes of the owner, so a token in a child `gh` of the engine is visible to an unsandboxed agent. | `/proc/<pid>/environ` is readable by the same user. |
| Windows | Refused. | Not applicable. |

## The gate result comes from the forge

After the candidate gate passes on head `H`, the engine posts `POST repos/<repo>/statuses/H` with `state = success`, `context = beehave/gate` and a description that names the gate key. It posts as the writer app. A red gate posts `failure`. A void gate posts nothing.

The ruleset requires `beehave/gate` from the writer app, and `check` and `test` from GitHub Actions.

`.github/CODEOWNERS` gives these paths to the owner: `/Makefile`, `/.github/`, `/.ai-rulez/`, `/.golangci.yml`, `/go.mod`, `/go.sum` and `/scripts/`. With `require_code_owner_review`, a change to the program behind the gate needs the approval of the owner. A GitHub App cannot be a code owner, so the reviewer app cannot give it.

The CI workflow file of a `pull_request` run comes from the head. `CODEOWNERS` on `/.github/` stops a head from rewriting the `check` job to pass. Required workflows from another repository are an organization feature, and not available here.

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

- `rounds`: one row per round with its status, last pass, integration pull request and steps version.
- `round_items`: one row per item with its attempt, run id, branch, pull request, the head that the engine last pushed (the fence) and its state.
- `spend`: one row per agent execution with its tree, run, step, session, item, pull request, call name and cost. A `NULL` cost means unknown.

An agent step writes its `spend` row with `INSERT OR IGNORE` after the agent exits and before the step returns. A replay of a finished step does not run the body. The fix cap, `fix_count(pr = n)`, counts the `fix` rows of a pull request. The tree budget sums `cost_usd` by tree, and any unknown cost fails the check.

### The round is a chain of short workflows

The round entry is `round_pass`. One pass reads the round record, audits, queues, spawns, gathers, lands and writes the record. Then it calls `next_pass(name = "round-next-<n>")`. That builtin enqueues the next pass and returns. DBOS dedupes the id, so a replay enqueues nothing twice.

The next pass starts under the binary that runs it, so it has the current steps version. A bump of the steps version strands at most the one open pass. `beehave round restart <base>` starts that pass again. It spawns no item twice, because it reads its children from `round_items` by run id.

No pass waits for the owner. When no item can move, the pass sets `status = waiting`, updates the integration pull request and returns. `beehave round continue <base>` starts the next pass.

## The claim lease

A fencing token works only when the resource checks it. Neither `bd` nor GitHub checks a token of ours. GitHub does check the expected old value of a ref, atomically. So the fence is the branch head.

- `push` runs with `--force-with-lease=refs/heads/<branch>:<expect>`. `expect` is the head in `round_items`, or empty for a new branch, which the forge reads as "the branch must not exist".
- `merge` and `batch_merge` pin with `--match-head-commit`.
- A stale worker that lost its claim fails at its next push or merge, at the forge.
- Inside one state folder, the DBOS workflow id `item-<n>-r<k>` dedupes a second start.
- Across engines, preflight reads the assignee and the linked pull requests. An own open pull request that `round_items` does not hold stops the item as `HELD`.

The `bd` lease stays for hand-run lanes as the `claims = "beads"` adapter. It is unfenced and is not called on the round path.

## The block list is hygiene

The shell scanner has no kind for `gh pr merge`, `gh pr review --approve`, `gh api` merge calls or `gh auth token`. The trust work adds none. A kind would be a second, weaker copy of the identity split. After the trust work, an agent that runs `gh pr merge` has no credential, and the forge refuses a merge without the reviewer approval.

## Owner steps

Run these as the owner, in order. Steps 1 to 8 need no change in beehave.

1. On github.com, open Settings, Developer settings, GitHub Apps, New GitHub App. Name it `beehave-writer-tobocop2`. Set the homepage URL to `https://github.com/tobocop2/beehave`. Clear "Active" under Webhook.
2. Set the repository permissions: Contents read and write, Pull requests read and write, Issues read and write, Commit statuses read and write, Workflows read and write, Metadata read. Set no account permission. Under "Where can this GitHub App be installed?", select "Only on this account". Create the app. Write down the App ID.
3. On the app page, select "Generate a private key". Move the file to `~/.config/beehave/apps/beehave-writer-tobocop2.pem`. Run `chmod 700 ~/.config/beehave/apps`, then `chmod 600` on the file.
4. Select "Install App", then your account, then "Only select repositories", then `tobocop2/beehave`. Write down the installation id from the URL (`settings/installations/<id>`).
5. Do steps 1 to 4 again for `beehave-reviewer-tobocop2` with these permissions only: Pull requests read and write, Contents read, Metadata read.
6. Run `gh api users/beehave-writer-tobocop2%5Bbot%5D --jq .id`, and the same for the reviewer. Write down each id. The commit address of the writer is `<id>+beehave-writer-tobocop2[bot]@users.noreply.github.com`.
7. In the repository settings, under Actions, General, keep "Allow GitHub Actions to create and approve pull requests" cleared.
8. Make a probe repository `tobocop2/beehave-trust-probe` with one commit on `integration/probe`. Install both apps on it. Add a ruleset on `integration/**` with one required approval, `require_last_push_approval` and no bypass.
9. Open a pull request on the probe repository as the writer and approve it as the reviewer. Run `gh pr view <n> -R tobocop2/beehave-trust-probe --json reviewDecision,mergeStateStatus`. If `reviewDecision` is `APPROVED` and `mergeStateStatus` is not `BLOCKED`, the approval counts: record that in this page. If not, give the reviewer app `Contents: write`, add the `lanes` ruleset, and run this step again.
10. After the pull request that posts `beehave/gate` merges, replace the `0` after `beehave/gate` in `trust/integration-ruleset.json` with the writer App ID. Then post the ruleset with the command in "The ruleset".
11. Add the `forge_apps(...)` block to `projects/beehave.star` with both App IDs, installation ids, key paths and bot logins.
12. Optional, for a merge queue: make an organization, transfer the repository to it, install both apps on the organization, and post the ruleset again with a `merge_queue` rule.

## Open decisions

- The ruleset `main` keeps the role bypass `always` and requires signed commits. The writer does not sign its commits. So `integration/*` lands in `main` by a squash on github.com, where GitHub signs the commit. The trust work does not change `main`.
- The ruleset accepts the approval of the owner as well as the approval of the reviewer app, and guard 0 accepts both. The approval of the owner also lifts the own-work guard.
- The agent sandbox on Linux waits on #199. Until it passes, an agent step on Linux can refuse to start. That is fail-closed.
