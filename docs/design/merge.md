# Merge

Part of the [beehave architecture](../architecture.md).

`merge` merges one reviewed pull request into the base, pinned to the commit that a reviewer gated. `batch` lands a set of reviewed pull requests with one gate run for the whole set. The remediation tools are the `remedy` binary of the remediation example. They are not part of the engine.

## Merge

### Run, request and stops

`Run` takes a `Request` and returns a `Result` with a `Stop` and the lines to show. The stops are a closed set with a `Code`:

| Code | Stop |
|---|---|
| 0 | done |
| 2 | refused |
| 3 | no workflow scope |
| 4 | own work |

The request carries every policy value: the repository, the base, the own identities, `Approved`, the hold label, the `forbid_text` patterns and `Retarget`. The engine holds no pattern of its own.

### The refusal and the forbidden text

The `Result` keeps the lines of the guard that stopped in their own field, `Refusal`. `Lines` is the whole log of the run, and it ends with `Refusal`. A caller that shows why a pull request was refused uses `Refusal`, so it shows every line of the cause and no line of a guard that passed.

No line of a `Result` that `Run`, `Poll` or `Disarm` builds matches a pattern of the `forbid_text` of its request, because callers post these lines. The test is the one that the posting steps use: it splits a text at each newline and tests each part, so an anchored pattern such as `^Co-Authored-By` finds the second line of a comment or of a forge error. The forbidden text guard names each hit by its line number and by the position of the pattern in `forbid_text`, for example `line 3 matches pattern 4 of forbid_text`. It never quotes the line or the pattern, because a pattern such as `(?i)noreply@anthropic` matches its own source. The open threads guard leaves out a first comment when a pattern matches one of its lines, and names the pattern in the same way. The one place that builds the result then tests every line once: the lines of the guards that passed and the lines of the refusal. It replaces a line that a pattern still matches with `(a line is left out: it matches pattern N of forbid_text)`. A line that holds a newline is replaced whole. This holds only for patterns that do not match the words of beehave itself, such as "left out" in the replacement line or the fixed header of a refusal. No replacement can satisfy such a pattern.

### Tree proof

A request with `GatedTree` set gets a tree proof after the merge. The run reads the tree of the base tip and compares it with the gated tree. A different tree ends the run as refused, and the lines list the paths that differ.

A forge proves trees when it also implements the `Trees` interface of `merge`. The `github` backend does, through `CommitTree` and `TreeBlobs`.

### Logs of failed checks

When a poll first sees a check as failed or cancelled, the run writes the log of the failed steps to `check-logs/pr<number>-<job>-<check>.log` in the state folder. It uses the optional `Logs` interface of `merge`.

- A log is written to a temporary name and renamed.
- A log is written only when its text is not empty, so an empty log is read again at the next poll.
- A log that is already on disk is not read again.
- One poll reads at most five logs. The output names the checks that the cap leaves for the next poll.

The `github` backend reads a log with `gh run view --job <number> --log-failed`.

### Review verdicts

`review` also holds the verdict of a finding list and the rewrite rule of a standing.

- `NeedsCommit` is true for a blocking finding. It is also true for any open finding when the `Policy` sets `NoFollowUps`.
- `Standing.Compare` lets a review of an earlier commit stand for a head that holds the same tree.

The round cut and the merge audit are not part of the engine. They live in `examples/remediation/tools/` and run through the `remedy` binary. See [Remediation tools](#remediation-tools).

### Own work merges only after another step approved it

[The trust boundary](trust.md#the-merge-checks-no-flag) describes the planned change: the forge requires the statuses `beehave/review` and `beehave/gate` at the head, and the `Approved` flag is deleted.

Nobody merges their own work. Guard 0 runs before any other read or refusal. It reads the pull request once.

The stop is `OwnWork`, unless the request has `Approved`, when one of these has a login or address equal to an owner login, an owner address or the login of a runner in `runners.star`:

- the author of the pull request,
- any author of any commit,
- the committer of any commit.

Case does not count. A third party's pull request with one commit of ours is refused too, because the rule is about the work and not the opener. The text names the login and says that the owner merges it in the forge.

Only `Approved` lifts this rule, and it lifts only this rule. `AllowOrphans`, `AllowNoChecks` and `AllowReady` do not reach it. `auto_merge` covers only the pull request of a contributor.

The `merge` builtin of `flow` sets `Approved` for each member from the `approved` field of that member, and a member that leaves it out is not approved. The call has no `approved`, so one approved member never approves another.

A missing or empty owner login or address is a refusal, because the rule cannot tell whose work it is.

The scopes of the token and every later read run after guard 0, so a refused pull request has had no write.

#### Incomplete lists

The forge can cut the commit list of the first read. So guard 0 also reads the forge's own count of commits with `PullCounts`. A count that is larger than the list read, or that cannot be read, is an `OwnWork` refusal that says the list is incomplete.

The commit list names authors only. So guard 0 also reads the committer of each commit with `Committers`, from the REST list of the commits. A list shorter than the count, or a read that fails, is an `OwnWork` refusal too.

The workflow scope guard applies the same rule to changed files. A file list that is absent, or shorter than the forge's count, is a refusal.

### Order of the guards

After guard 0, the guards refuse in this order:

1. the repository and the base branch of the merge request,
2. a gated commit id that is not 40 characters,
3. the state,
4. the base,
5. the head,
6. the mergeable flag,
7. the hold label,
8. the closing references,
9. children,
10. checks,
11. the workflow scope,
12. change requests,
13. open threads,
14. the forbidden text of `forbid_text`,
15. commits with no linked account,
16. the draft rule.

#### Reads

The first read asks for every field in one call. The tip of the base is read at the start of the run, after the gated commit id. Then the merge reads the head once more, reads the tip of the base again, and reads the stack of the pull request.

- A head or a base that moved is refused.
- A base tip that cannot be read is refused.

#### Closing references

The closing-references guard runs only for an item of the GitHub tracker whose id is a plain positive number, the number of its issue. For any other item, the merge prints one line that says no audit ran. A project that the config lacks is an error before the request exists.

The guard reads the issues that GitHub lists as closed by the pull request. It refuses when they are not exactly the issue of the item, with a line that names both lists. A forge that cannot read them refuses with a transient refusal.

The guard skips in these cases:

- GitHub lists no issues for a pull request into a branch other than the default branch. The guard skips there and prints a line that names the base.
- A member of a batch always skips, because its base is the integration branch. The read after `merge` covers it.

The audit does not see a closing keyword that is only in a commit message, because the field may not list it. It does not report a close of an issue in another repository.

#### Children

A child is an open pull request whose base is the branch of the pull request. A merge orphans it, unless it is in the same native stack, which GitHub retargets. That child is a note.

- `AllowOrphans` lets the merge go on and says that this is a stated decision.
- A list of 1000 pull requests is refused as cut off.

If `Retarget` is set, the children of a direct merge move onto the base after all guards have passed, right before the merge call. The children of a queued merge move only after the forge has merged it, so a window that ends unmerged leaves them where they are. If a move fails, the children before it stay moved, the merge does not happen, and the refusal names both groups.

#### Checks

A check that skipped is not a pass. With no pass, the merge refuses unless `AllowNoChecks` is set.

#### Queued merge of a native stack

A pull request in a native stack merges through the queued merge of the forge. This needs the optional queue calls on the forge and a request that allows queuing. Without the call or the setting, the pull request is refused.

The run then:

1. writes the intent (the record of `merge`) as a step of its own,
2. queues the merge pinned to the gated head,
3. returns Queued.

The flow then waits with a durable sleep between recorded read steps: 30 seconds apart, 40 reads, about 20 minutes. These are constants, not settings. A crash in the wait resumes at its round and does not queue again.

GitHub checks the expected head only when it enables the queued merge. It keeps the merge armed after a push by a user with write access. So each read compares the head with the gated head.

| Read result | Outcome |
|---|---|
| A moved head, a closed pull request, a window that ends, or a failed last read | The run turns the queued merge off (`gh pr merge --disable-auto`) and refuses. A refusal that cannot turn it off says so. |
| MERGED at another head than the gated head | Refusal that says unreviewed commits landed |
| MERGED at the gated head | Goes to the ancestor and tree proofs |

#### The merge call and the landing proof

A forge that cannot compare commits (it lacks the optional ancestry read) is refused before any write. A merge cannot be undone, and the run could not prove that it landed.

The merge call pins the head on the forge side. A merge call that fails is a refusal with code 2, and the result keeps the text of the forge.

After the merge call, the run asks the forge whether the merged head is in the history of the base. If that read fails, the item fails after the merge. Only this read can fail late.

### Replay of a merged pull request

A caller that writes an intent before the merge can ask `Run` to count a `MERGED` pull request as done. A request with no intent still refuses `MERGED`.

The `Intent` holds:

- the pull request,
- the head,
- the base,
- the base tip tree,
- the time,
- the state of the read (it must be `OPEN`).

The run accepts a `MERGED` read only when all of these hold:

- the intent names this pull request, head and base,
- the merge time is not before the intent time minus the skew margin (a zero `Skew` means the default of two minutes),
- the tree proof passes.

#### Guards on a replay

| Kept | Skipped |
|---|---|
| own work, hold, checks, file list, workflow scope, review, forbidden text, author | the `OPEN` and mergeable checks, the draft rule, the orphans guard, the steps before the merge call |

The orphans guard does not run on a replay, because a merged pull request cannot be un-orphaned. A hand merge at the same head inside the window passes the same checks, and the design accepts that.

#### Meaning of a refused replay

A refusal on a replay means that a person must look. It does not mean that the pull request did not merge. These refuse the replay when they appear after the merge:

- a hold label,
- a review thread,
- a change request,
- a running check,
- a push to the base by someone else.

`BaseTipTree` is read by the batch merge, not by `Run`.

A replay with an integration pull request writes the row `- #N merged at SHA` once, because `Run` skips the append when that exact row is already in the body.

### Edit of the shared body

`EditBody` reads a body, applies an edit and writes the result. For the whole read and write, it holds an exclusive `flock` lock on `integration-<pr>.lock` in the state folder, so two writers never lose a row. The lock is released on every exit, a failed write included.

The author check is inside `SetBody`, the forge write that sets a body. It is the only such write, so no caller (the merge row, the closing line of `pr_open`) can write a body without the check.

- The author must be the login of the token that the forge command line uses.
- A body of another author is never edited.
- A body whose author login or token login cannot be read is never edited.

A merge with an integration pull request records its row through `EditBody`. For a refused body, the step says a REFUSE line that names the author, and the owner adds the row. A row that cannot be recorded does not undo the merge. The step says so, and the owner adds the row.

The step never edits a title. Ready, draft and base changes are not covered by this rule, because beehave merges third-party work that is reviewed.

## Batch

`batch` lands a set of reviewed pull requests with one gate run for the whole set. It is a layer 2 package. The `merge` builtin of `flow` wires it.

### Interfaces and result

The gate, the git calls, the forge and the step recorder are the interfaces `Gate`, `Git`, `Base` and `Recorder`.

`Run` takes the members, each an item and a head commit, and sorts them by item as byte strings. It returns a `Result` with:

- the merged items,
- the rejected and deferred members, each with a reason,
- the last base tip,
- the number of gate runs.

### Git version

`batch` needs git 2.38 or newer on the gate host, for `git merge-tree --write-tree`. `CheckGit` reads `git --version` once, before any step, and fails with "git 2.38 or newer is needed for merge".

`GitCLI` implements `Git` over any runner with the `Exec` method of `proc.Runner`. No maintained Go library does this fold: go-git has no three-way tree merge, and libgit2 bindings need cgo.

### Head check

Before the fold, one recorded step checks that the head of each member is in the repository (`git cat-file -e <head>^{commit}`). A member with a missing head is rejected with "head X of M is not in the repository; fetch it". The check exists because `git merge-tree` exits with the conflict code for an unknown commit.

### The union

The union folds the members onto the base tip. Each step is:

1. a `git merge-tree --write-tree` of the fold so far and the next head,
2. then a `git commit-tree` with both parents, so the next step has a commit to merge into.

The union records the tree after each member.

- A member that conflicts with the base tip alone is rejected with the reason `conflict`, before any gate runs.
- A member that conflicts only with an earlier member is held out.

### Scope check

Before the merge of each member, the recorded step `batch:<name>.scope#<item>` reads the issues that the pull request of the member closes. It compares them with the issues of the first review of that item. For the item of the run, the first review is the first `gate` call. For another item, it is the newest earlier run of that item.

A new issue with no owner line makes the run ask the owner. The question lists the new issues and, in the form `(then: #12 #14)`, the issues of the first review. The question and the refusal name the run, so `beehave answer` with the item of the run answers the wait, and the item of the member does not. The accepted answer is `keep #N[, #N ...]`. Any other answer rejects that member with the reason, as the batch rule rejects any member. The other members regate and land. A batch of one lands nothing. A member with no recorded first review has nothing to compare.

### Splitting a red union

A red union is split in two halves in item order. Each half is gated on the tip that the earlier half left.

- A single red member is rejected.
- A union with the tree id of a union that was gated red is split without a gate run.
- A member that fails only on top of merged members is rejected with the reason "red on top of the merged members".

A void gate (every step unknown, for example a pinned tool at another version) is not red. The batch stops with an error that names the reason. It does not split, and no member is blamed.

### Held-out members

After the first round, each held-out member is folded once on the new tip. The folded members are landed with the same split. A member that conflicts there is rejected with "conflicts with" and the name of the member or `base`. There is no second retry.

### Gate run bound

For N members and k bad members, the gate runs at most `min(2N - 1, 1 + 2k * ceil(log2 N))` times. `Bound` returns that number.

With H held-out members and R retried members, total gate runs are at most `Bound(N - H, k)` plus `Bound(R, k')`.

Each member that the merge refuses (own work, hold, checks, review) costs one more gate round on the members left after it. So with r refusals, the total can reach about r + 1 times that sum.

### Merge outcomes

`Base.Merge` answers with a `MergeOutcome`:

| Outcome | Effect |
|---|---|
| A refusal by a guard of the member (own work, hold label, checks, review, a head that moved while preparing) | Rejects that member with the reason: every line of the `Refusal` of `merge.Run`, one per line. The members after it are folded and gated again on the tip. |
| Unsettled (`mergeable` stayed unknown for three reads, 10 seconds apart) | Defers that member and all members not yet decided, with the reason |
| A transient refusal of `merge.Run` (`merge.Transient`: a failed read of the forge, checks that still run, a base that moved while preparing, an error of the forge merge call) | Defers the same way, with every line of the `Refusal` as the reason and no second read, because the merge step of a member never runs twice in one attempt |
| A pull request that is not OPEN at the intent read | Rejects that member, and the call goes on |
| An error | Stops the call |

The `Result` that comes with an error still lists the members that landed, and the error names the member where the call stopped.

### Contracts of the interfaces

- `Merge` is idempotent for one member, because a replay calls it again after a crash. The wiring uses the intent record of `merge.Run`.
- `Recorder.Step` does not record a failed run.
- `Gate.Run` gates exactly the commit it gets and reports the tree of that checkout.
- `GitCLI.NewCommit` needs an author identity in the environment of the runner.
- `Deps.Name` is not empty, and it is unique for each batch call in a workflow.

### Recorded steps

Every input that picks a path is a recorded step: `batch:<name>.heads#k`, `.tip#k`, `.union#k` and `.gate#k`, and after the merges `.prove` and `.issues`.

- The steps that read retry twice. The gate step runs once.
- A later read of one member adds `@<read>` to the step name. An item id has no `@`.
- A step records its input with its output, and a replay with other input fails.

The batch needs the worktree of the item that runs the workflow. `merge` makes it before the first git call, and the batch host refuses an item with no worktree.

### Green gate and tree checks

A project gate counts as green only when it reports `passed` and every one of its steps is `pass`. An unknown step, or a gate with no step, is red, and no member of that union lands.

The gate output carries the tree id the gate ran on. A tree id that differs from the tree of the union is the error "the recorded gate ran on tree X, and the union is tree Y".

Before each member merge, the run reads the tip and requires the tree of the members merged so far. Another tree means that someone else pushed, and the rest of the members are deferred.

### Landing proof

After the last member merge, the recorded step `batch:<name>.prove` checks each merged member against the base (`batch.Prove`).

A member has landed when the forge compare shows its head in the history of the base: `behind_by` is 0 and the status is ahead or identical. If the head is not there, as after a squash, the member has landed on the vehicle commit only when each file it changed against the tip where the batch started has the same content on the base.

Every other member, and every member whose read failed, is a line of the result field `unproven`. The check fails closed, so a member that shares a path with another member is unproven. The call does not fail, because the merge cannot be undone.

## Remediation tools

`examples/remediation/tools/` holds the tools of the remediation example, in the same Go module as the engine.

### Binary and packages

- The one binary is `examples/remediation/tools/cmd/remedy`. `make remedy` makes `bin/remedy`, and `make build` makes it too. The release archive and the cask hold only `beehave`. A `command` step finds `remedy` on the `PATH` of the beehave process, so a round starts with `bin` on its `PATH`. See `examples/remediation/README.md`.
- The package `remedy` holds the command table. `cmd/remedy` only passes the GitHub forge and the beads claim store to `remedy.Main`, which wires the real git runner.
- The package `lease` builds the claims over the rows and finds the project of a repository in the config folder, for `cmd/beehave` and `cmd/remedy` alike.
- A `command` step runs `remedy <subcommand>` and reads `rc` and `stdout`, for the read-only checks (`report`, `gatelist`, `audit`, `papers`). The forge writes of a round (`sync`, `sync land`, `cut`, `integration`, `board` and `take`) are engine steps that run as the writer role: `round_sync`, `round_land`, `round_cut`, `integration_pull`, `board_card` and `pr_take`. They call the same packages (`roundsync`, `roundcut`, `roundintegration`, `takeover`) as the subcommands. The subcommands stay for a person at a terminal, who runs them with the login of that person.

The prompts of the example hold the text that a lane reads. `examples/remediation/prompts/` has one Markdown file for each step, and the `agent` step renders it. No Go code builds that text. The tools only judge a brief that a lane has written, in the package `examples/remediation/tools/brief`.

### Exit codes

Each subcommand follows the exit codes of beehave:

| Code | Meaning |
|---|---|
| 0 | a pass |
| 1 | a defect found |
| 2 | a refusal or a failed read |

A bad argument is a refusal. The tool finds the repository from the `origin` remote of its working folder.

### `remedy cut`

`remedy cut --old <branch> --new <branch> --pr <number> [--trunk <branch>] [--dry]` runs `roundcut`. It:

- refuses unless the integration pull request is merged,
- makes or resumes the new base only at the trunk tip,
- moves every open pull request on the old base to it,
- never deletes the old branch. The forge interface has no call that deletes a branch.

A dry run takes the same path and prints `WOULD:` lines, and it makes no write call. The trunk is `main` when `--trunk` is not given. The number of the new round is the text after the last dash of the new branch.

### `remedy sync`

`remedy sync --base <branch> [--trunk <branch>] [--label <name>]` runs `roundsync`. The pull request that it opens carries the label `--label`, which is `made-by-beehave` when the flag is not given, and an empty value adds none. The command `remedy integration` takes the same flag for the integration pull request. The round workflow passes no label, so the engine steps `round_sync` and `integration_pull` use the `pr_label` of the project (`made-by-beehave` when the project sets none). The forge creates the label first when the repository has none. It prints one line, `state=<state>` followed by the fields that the state has, and the workflow branches on `state`.

| State | Meaning |
|---|---|
| `current` | The round base holds the trunk tip. Nothing is written. |
| `synced` | The branch `sync/<base>/<first 8 characters of the trunk tip>` holds a merge commit of the base tip and the trunk tip, and a draft pull request into the base is open. The line gives `branch`, `pr`, `head` and `base_tip`. |
| `conflict` | A merge into the sync branch conflicts, and that merge writes nothing. The line gives `source`, the branch whose merge conflicted: the trunk, or the round base when the base moved after the branch was cut. An agent merges `source` on the sync branch and pushes. The next call merges what is left and opens the pull request, or names the next conflict. |
| `refused` | The forge refuses the merge for the workflow permission. The line has `reason=sync-refused`. |

GitHub's merges endpoint (`POST /repos/{owner}/{repo}/merges`) makes a merge commit with two parents, the base tip first, also when the base tip is an ancestor of the head: it never fast-forwards. It answers 201 with the commit, 204 with no body when the base holds the head, and 409 `Merge conflict` with nothing written. The tool reads a 409 as a conflict. It reads a refusal as `sync-refused` only when the forge's message holds `create or update workflow`, the text of GitHub's refused push of a workflow file. The merges endpoint's text for that refusal is not measured, and GitHub lists the endpoint under the contents permission only. Any refusal with another text is exit code 2, so an unknown text stops the round loudly and is never a silent pass.

The forge does each merge, so no checkout is written, and the round branch is never rebased. A repeat call finds the branch, the merge and the pull request. A base that moved after the branch was cut is merged into the branch, so the gated head holds every commit that the landing keeps. Any other failure of the forge is exit code 2.

`remedy sync land --pr <number> --head <sha> --base-tip <sha>` merges the pull request with a merge commit pinned to `--head`. It lands only when the base is still at `--base-tip`. A moved base is `state=stale` and a moved head is `state=head-moved`. Both are exit code 2 and write nothing. A landing prints `state=landed`.

GitHub does not merge a draft: `gh pr merge` on one exits 1 with `GraphQL: Pull Request is still a draft (mergePullRequest)` (measured on a scratch repository with gh 2.101.0). The sync opens its pull request as a draft. The landing marks the draft ready only after the base and head checks pass, then merges it pinned to `--head`.

When the merge fails, or the tool gets SIGINT during the merge, the landing makes the pull request a draft again. That call runs on a context that the SIGINT does not end, with its own limit of 30 seconds. If that call fails, its failure is part of the error of the landing. A sync that finds the open pull request of its sync branch ready makes it a draft before any merge into that branch. If the draft call fails, the sync stops before the merge.

A kill of the landing, or a SIGINT during the ready call, can leave the sync pull request ready on a head that passed its gate and review. The stop of a round and a step timeout kill the group of the tool with SIGKILL, which no process can catch. No error line says that the pull request is still ready. It stays ready until a `remedy sync` on the same sync branch makes it a draft. A sync for a newer trunk tip cuts a new sync branch and does not read the old pull request. Once the sync holds its own open pull request, it closes every other open sync pull request into the round base, not only older ones, matched by the branch prefix `sync/<base>/`, with a comment that names the pull request that replaces it. A repeat call closes what a failed close left open. A failed close or a failed list is exit code 2 with the error of the forge. A person who merges the ready pull request merges that gated head. A person who marks the pull request ready by hand can also merge it.

#### The sync step of the round

The round of `examples/remediation/workflow.star` runs the sync at the start of each pass, before `queue-audit-<n>` and before any spawn. The engine has no builtin for it: the sync is command steps, one agent step and a gate of the workflow.

| Step | What it does |
|---|---|
| `sync-<n>` | The engine step `round_sync` with the round base and `base_branch`, run as the writer. A forge failure ends the run with its error. |
| `sync-resolve-<n>` | Runs only on `conflict`. One agent step (`prompts/sync.md`) merges `source` into the sync branch, resolves, commits the merge commit and pushes. |
| `sync-again-<n>` | Runs `round_sync` again after the agent. A second `conflict` ends the pass. |
| `sync-fetch-<n>`, `sync-checkout-<n>` | Put the folder of the round at the head of a `synced` line, so the gate and the review read that head. |
| `sync-gate-<n>` | The gate with no base: the gate command only, with no new-tests check and no mutation step. |
| `sync-gate-<n>-host-<k>` | Runs only when `sync-gate-<n>` is void with a host cause (the engine proved a lost host job). The gate runs again, k from 1, until the engine counts 5 host losses in the tree of the run. |
| `sync-review-<n>` | A read-only review (`prompts/sync_review.md`) pinned to the gated head. It writes the verdict `approve` or `changes`. |
| `sync-land-<n>` | The engine step `round_land`, pinned to the gated head and the base tip of the line. `stale` and `head-moved` land nothing, and the next pass syncs again. |

A dry run shows the trunk as `current`. A state that the round does not know, or a result that lacks a field that its state needs, stops the run.

Five results hold the round. Each has a reason:

| Reason | Cause |
|---|---|
| `sync-conflict` | The agent did not resolve the conflict. |
| `sync-refused` | The forge refused the merge for the workflow permission. |
| `sync-red` | The gate did not pass on the sync head. |
| `sync-review` | The review did not approve the sync pull request. Its findings are in the reply of the step `sync-review-<n>`. |
| `host-loss` | The engine counted 5 host losses or more in the tree of the run, and a loss came after the last answer of the owner. The sync gate can give it, and so can a child result or the landing. |

The round reads the count of the engine: the `host_losses` of each child value, of the landing and of the sync gate. It keeps the highest as `seen`, and no count of its own. A child that stopped at the limit returns the reason `host-loss` and its count, and its item gets no comment and no hold status. After the answer CONTINUE to a `host-loss` hold, the round asks again only when `seen` is higher than it was at the answer.

A landing that ends `stale` or `head-moved` holds nothing: the same pass goes on to `round-queue-<n>` and `queue-audit-<n>` on the round base as it is, with the folder of the round still at the sync head, and the next pass syncs again.

A held round starts no sync and no item in a later pass. It gathers and lands the child runs that are open, so it never returns while one is open. A step that stops the run, such as an exit code 2 of `sync-<n>` in a later pass, ends the round with its open runs. With none open, it waits in `round-wait` with a question that names the reason and what the owner does. CONTINUE starts a pass that checks the trunk again. STOP or the timeout returns `{"state": "waiting", "base": ..., "pr": ..., "reason": <reason>}`; the reason is an empty text when no sync held the round.

When no sync held the round and no item can move, the question names each listed item, its state and the step of the owner: `beehave resume <item>` for a failed item, then `beehave cancel <item>` when the resume refuses the run, so the next pass starts a new attempt. A round with no item list gets the count of the rows in each state and the ids of the failed rows. The round starts no new attempt for a failed item on its own.

### `remedy audit`

`remedy audit authored <span>` and `remedy audit present <tip> <base>` run `audit`. The audit reads the merges of a range through git, with `-z` path lists.

- `Authored` lists the paths of a merge that differ from every parent, as resolved or authored, with a mark on sensitive paths.
- `Present` lists the lines a merge added that the tip lost, and the files deleted at the tip.

A line that marks a finding is exit code 1. A range that git cannot read is a refusal, never a clean report.

### `remedy take`

`remedy take <pr> --me <login> --idle <hours> --grace <hours> [--head <sha>] [--item <id>] [--apply | --take [--reply-read]]` runs `takeover`. It decides whether to take a pull request that somebody else holds.

| Step | What it does |
|---|---|
| No step flag | Reads and says what it would do |
| `--apply` | Posts the notice |
| `--take` | Checks the notice, the grace period and the replies |

Every refusal is exit code 2. `--me` is a flag, so the tool keeps no state.

#### Rules for every step

- A pull request that is not open, or is ours, is refused first, in every step.
- A failed read refuses.
- `--head` names the head commit that the caller works on. If the pull request has another head, the step refuses with exit code 2, and the lines say that the author pushed. With no `--head`, any head passes.

#### The notice

The notice needs a `CHANGES_REQUESTED` review of ours. It names its basis (assigned to us, or idle) and the grace period. A second notice is never posted: right before the post, `--apply` asks the forge for a notice of ours. If one exists, it says so, writes nothing and exits 0.

The text of the notice is Markdown in `takeover/text`, read through one `embed` variable.

#### `--take` refusals and replies

`--take` refuses in this order:

1. no notice,
2. a notice younger than the grace period,
3. a pull request touched inside the idle threshold,
4. a reply or a push after the notice. A push after the notice is an objection with no words.

| Reply | Effect |
|---|---|
| Says `KEEP` | Stops the take |
| Says `RELEASE` | Takes the pull request without the grace wait or the idle wait |
| Neither word | Prints every reply and refuses. `--reply-read` lets the take go on with a note. |

A token stands alone as a word when no Unicode letter, digit or underscore touches it. So `KEEPER`, `KEEP_ALL` and `KEEPé` do not say `KEEP`.

#### Time

`takeover` never calls `time.Now`. `remedy` puts the time in the context, so every age is a pure function of the input. A time that is not a UTC stamp is a refusal. A pull request with no timestamp at all is unreadable, not idle.

#### Claims

`remedy take` never writes a claim. A claim lapses after 5 minutes unless something renews it, and the tool exits at once. The run of the item in the engine takes the claim and renews it.

`--apply` and `--take` read the claim store of the project that owns the origin repository, from the beehave config folder, through `internal/lease`. This is the same code that `beehave run` uses.

- The store opens only when the claim must be read, after every guard and rule that needs no store. So a store that does not open never hides a refusal.
- A store that does not open is exit code 2.
- `--apply` refuses when another owner holds the pull request, before it posts. `--take` refuses the same way.
- When every check passes, `--take` prints one line: the pull request may be taken now, and the run of the item holds the claim. It never prints TAKEN.

`--item` names the tracker item that the run claims. The claim read checks that item, never the pull request number. `--apply` and `--take` refuse with exit code 2 when `--item` is missing. The dry step needs no item.

The engine gives each run a random owner token, and no login maps to an owner. So the read asks as the fixed owner `remedy`, which never holds a claim, and any live claim on the item is another owner's. So run `remedy take` before a run claims the item. Inside the item's own run, it sees the run's claim and always refuses.

The forge of the tools has no call that assigns a pull request, so the take does not co-assign it.

### `remedy admit`

`remedy admit <kind> ...` runs the gates before a lane starts.

- An impl lane needs its acceptance slots.
- A script needs its result at the base.
- A fix or rework lane names one mechanism. A pull request with two patch rounds on that mechanism and no design lane since gets a design lane next.
- A lane of new behavior needs a design lane in the ledger.
- A lane that follows an earlier fix lane of the same key needs a handoff that passes. A handoff with two regressions sends the next brief to a design lane.

A pass is exit code 0. A gate that holds the brief back is exit code 1, with the lines for the orchestrator. Any other refusal is exit code 2.

### Other checks

| Command | Checks | Exit 1 | Exit 2 |
|---|---|---|---|
| `remedy papers <base>..<head> [--globs <patterns>]` | Lists the source paths that the range changes | A source path | |
| `remedy handoff <file> [--name <text>] [--transcript <file>] [--cap <tokens>]` | Checks a handoff | A refused handoff | |
| `remedy authors <file> --email <address> --login <name>` | Checks `<sha> <email> <login>` lines against the identity of the owner | A commit of the owner under another address | A missing identity or an empty file |
| `remedy spend <transcript> [--hours <n>] [--cap <tokens>]` | Prints the hour table and one row for each lane | A lane over the cap | |
| `remedy gatelist [--steps <name>]... [--map <json>] <workflow-file>...` | Derives the gate steps from the run steps of the CI workflow files, and names the steps and jobs that it leaves out | `--steps`, `--map`: the gate step names of the round and the `gate_map` of the project | A file that is not a workflow, files that give no step, a map that does not match the derived commands, a map of another form or given twice, a map with no gate steps |
| `remedy job <script> --list <file> [--job-dir <folder>]` | Checks a host job against the gate list | A defect in the job | A gate list that cannot be read |
| `remedy report <file> --head <sha>` | Checks the four tables of a lane report | A defect | |

None of `papers`, `handoff`, `authors` and `spend` reads the state folder. None of `gatelist`, `job` and `report` reads an environment variable.

`--job-dir` names the folder that a sourced file is read from. When it is not given, it is the folder of the script.

### `remedy size`

`remedy size <brief> [--cap n]` reads a filled brief. The required reading is the brief plus the files under its read-first list. The limit is 160,000 bytes.

- It lists every file the brief names, follows the briefs that those files name, and counts each file once.
- It warns about a file above 8,000 bytes, a chain of briefs, and an acceptance script with no expected behavior. A warning is exit code 1.
- It refuses a reading above the limit with exit code 2.

The limit and the file warning are constants, not settings.

### Boundaries and tests

`depguard` keeps the engine free of the tools: no file under `internal/` or `cmd/beehave/` imports `examples/`. The rule has no exception, test files included.

- The `testscript` scripts of `remedy` are a test in `examples/remediation/tools/cmd/remedy`.
- A test in `examples/remediation/tools/remedy` reads the calls of `workflow.star`. It fails when a `remedy` command in it names a subcommand that is not in the table.
