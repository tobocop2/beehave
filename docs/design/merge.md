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

The round cut is the engine step `round_cut`.
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

The workflow scope guard acts only on a pull request that changes `.github/workflows/`. In a project with `credentials`, it holds the change for the owner with code 3 and reads nothing: the writer token cannot change CI (see [trust](trust.md#the-ci-lock)). In a project with no `credentials`, it asks the forge whether the token can change CI. `yes` passes. `no` is code 3, with the command that adds the scope. `unknown`, and a read that fails, refuse with code 2.

### Order of the guards

After guard 0, the guards refuse in this order:

1. the repository and the base branch of the merge request,
2. a gated commit id that is not 40 characters,
3. the protection of the base,
4. the state,
5. the base,
6. the head,
7. the mergeable flag,
8. the hold label,
9. the closing references,
10. children,
11. checks,
12. the workflow scope,
13. change requests,
14. open threads,
15. the forbidden text of `forbid_text`,
16. commits with no linked account,
17. the draft rule.

#### Protection of the base

The protection guard reads the rules of the base from the forge, before any write, with the optional `Statuses` side of the forge.

- A project with a `writer` entry in `credentials` needs a base whose protection has a pull request rule and requires `beehave/gate` and `beehave/review`. The refusal names the base, what is missing, and the ruleset to add (see [trust.md](trust.md#the-ruleset)). The forge enforces the rules. The merge only refuses to merge into a base that would not.
- A forge that cannot read the protection refuses such a project, and the refusal names the forge. A read that fails is a transient refusal.
- A project with no `credentials` key keeps its guards. If the base requires `beehave/gate` or `beehave/review`, the merge refuses and says that the project needs credentials, because nothing posts those statuses. A forge that cannot read the protection, or a read that fails, does not refuse such a project: the forge still enforces its own rules.

#### Reads

The first read asks for every field in one call. The tip of the base is read at the start of the run, after the gated commit id. Then the merge reads the head once more, reads the tip of the base again, and reads the stack of the pull request.

- A head or a base that moved is refused.
- A base tip that cannot be read is refused.

#### Closing references

The closing-references guard runs for every member. Each member of `members` carries a required `closes` list of issue numbers, and the guard compares it with the issues that the forge lists. A member that closes nothing has `closes = []`. A member with no `closes` fails at the check. A project that the config lacks is an error before the request exists.

The guard reads the issues that GitHub lists as closed by the pull request. It refuses when they are not exactly the `closes` list, with a line that names both lists. A forge that cannot read them refuses with a transient refusal.

GitHub lists no issues for a pull request into a branch other than the default branch. For such a pull request, the forge reads the closing keywords of the body and of the commit messages instead. The keywords are `close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`, `resolves` and `resolved`, in any case, with an optional colon, and a number written as `#N`, `owner/repo#N` or an issue URL. A keyword for another repository is read as the full reference `owner/repo#N`, and the guard of the item does not compare it. The scope check holds it (see Scope check). A pull request with more than 100 commits is refused, because the later messages are unknown.

A member of a batch is audited like any other member. The read after `merge` covers the issues of the batch as a whole.

The audit does not report a close of an issue in another repository. The scope check asks the owner about it, and a commit message with such a keyword is refused unless the owner kept it.

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

`BaseTipTree` is read by the union merge, not by `Run`.

### The author check of a body

The author check is inside `SetBody`, the forge write that sets a body. It is the only such write, so no caller (the body of the integration pull request of a round) can write a body without the check.

- The author must be the login of the token that the forge command line uses.
- A body of another author is never edited.
- A body whose author login or token login cannot be read is never edited.

A merge writes no row into the body of an integration pull request. The step never edits a title. Ready, draft and base changes are not covered by this rule, because beehave merges third-party work that is reviewed.

## Batch

The batch lands a set of reviewed pull requests with one gate run for the whole set. It is the library function `batch_merge` of `lib/batch.star`, which a workflow loads with `load("@beehave//batch.star", "batch_merge")`. The library is policy: the fold, the split, the retry of held members and the audit. The engine gives it the builtins that touch the world: `base_tip`, `command` for git, `gate`, the union `merge`, `landing_proof`, `issue_state` and `pr_closing`. A project that wants another policy copies the file and changes it.

### The union merge

`merge(members, base, repo, gate, hold, own, forbid_text, name)` lands one gated union. The step `merge:<name>.plan` reads the base tip and folds the member heads onto it in order with git, without a working copy. A conflict, or a fold whose last tree is not the tree of the gate, merges nothing and returns `moved`. Each member then merges through `Run` of this package, pinned to its head and to the tree of its prefix. The engine trusts only a gate that this run recorded and that passed. `hand_off` is its own builtin and lands nothing.

### Git version

The union merge needs git 2.38 or newer on the gate host, for `git merge-tree --write-tree`. `landing.CheckGit` reads `git --version` in the plan step, before the fold, and fails with "git 2.38 or newer is needed for merge".

No maintained Go library does this fold: go-git has no three-way tree merge, and libgit2 bindings need cgo.

### Head check

Before the fold, the library checks that the head of each member is in the repository (`git cat-file -e <head>^{commit}`). A member with a missing head is rejected with "head X of M is not in the repository; fetch it". The check exists because `git merge-tree` exits with the conflict code for an unknown commit.

### The union

The library folds the members onto the base tip in item order. Each step is:

1. a `git merge-tree --write-tree` of the fold so far and the next head,
2. then a `git commit-tree` with both parents, so the next step has a commit to merge into.

Every git command runs through `command` in the worktree of the item, which `base_tip` makes.

- A member that conflicts with the base tip alone is rejected with the reason `conflict`, before any gate runs.
- A member that conflicts only with an earlier member is held out.

### Scope check

The scope check is the library `scope.star`, and the workflow calls it before `batch_merge`. The library reads the issues that the pull request of each member closes, with the recorded step `pr_closing`. It compares them with the issues of the first review of that item. For the item of the run, the first review is the result of `fix_scope` in the run, which the call takes as `own`. For another item, it is the first review of the newest earlier run of that item that is named `scope`, read with `earlier_reads`. A run that fixed its own scope gives no scope to a member of another item.

The scope holds each issue as a reference: `#N` for an issue of the repository of the pull request, and `owner/repo#N` (in lower case) for an issue of another repository. An issue of another repository is new, and the run asks the owner about it, in the same way as for an own issue. The number of an own issue does not keep an issue of another repository with the same number.

A new issue with no owner line makes the run ask the owner. The question lists the new issues and, in the form `(then: #12 #14)`, the issues of the first review. The question and the refusal name the run, so `beehave answer` with the item of the run answers the wait, and the item of the member does not. The accepted answer is `keep #N[, #N ...]`, and `keep owner/repo#N` for an issue of another repository. A later call of the member, in this run or in a new run of the same item, reads the kept issues from the owner waits of the earlier calls and asks nothing for them. An owner line keeps only an issue that an earlier check of the member read as a closing issue: an issue that the question did not ask about is asked about when it appears. The kept set only grows: a kept issue stays kept when a later pull request of the member no longer closes it. Any other answer rejects that member with the reason, as the batch rule rejects any member. The other members regate and land. A batch of one lands nothing. A member with no first review has nothing to compare.

The library gives each member two fields, `kept` and `closing`. The closing keyword guard allows a kept issue in a commit message. The merge refuses a pull request whose closing set at the merge differs from `closing`, the set that the check read, and the line says to run the scope check again. A member with no `closing` pins no set. The check does not read the item id, so it also holds for an item of a tracker other than GitHub, and the line of the closing-references guard for such an item says so.

A project with no forge has no scope to fix. The read then records that the scope check is off, and `beehave status` shows `scope check off: the project has no forge` on the run. A workflow that does not load the library has no scope check.

### Commit statuses

After a gate passes on a union, the merge of each member carries its commit statuses. The merge run of the member posts them as the `writer` role of the project after every guard passed, and right before its merge call:

- `beehave/gate` with the state `success` on the head of the member. The description names the union commit.
- `beehave/review` with the state `success` on the head of the member when its `approved` field is true.

A member that a guard rejects or defers gets no status, and so does a member whose base moved before the merge call. A red or void gate posts nothing. A status belongs to one commit, so a new push to a member leaves its new head with no status. beehave keeps no list of the statuses, and the post is not a recorded step: the forge is the record. A resume after a post that worked and a merge that failed posts again and merges. A status with the same context, state and commit replaces the last one, so the second post changes nothing.

A project with no `credentials` key posts none, and the merge says so in a `WARN:` line. A forge that cannot post statuses does the same. A post that the forge refuses stops that member before its merge call, and the member is deferred. The members that merged before it stay merged.

The forge methods are `PostStatus` and `Protection` of `forge.Statuses`. `Protection` reads the pull request rule and the required status contexts of a branch. The `merge` builtin reads it in its protection guard (see [Protection of the base](#protection-of-the-base)).

### Splitting a red union

A red union is split in two halves in item order. Each half is gated on the tip that the earlier half left.

- A single red member is rejected.
- A union with the tree id of a union that was gated red is split without a gate run.
- A member that fails only on top of merged members is rejected with the reason "red on top of the merged members".

A void gate (every step unknown, for example a pinned tool at another version) is not red. The batch stops with an error that names the reason. It does not split, and no member is blamed.

### Held-out members

After the first round, each held-out member is folded once on the new tip. The folded members are landed with the same split. A member that conflicts there is rejected with "conflicts with" and the name of the member or `base`. There is no second retry.

### Gate run bound

For N members and k bad members, the gate runs at most `min(2N - 1, 1 + 2k * ceil(log2 N))` times.

With H held-out members and R retried members, total gate runs are at most that bound for N - H members plus the bound for R members.

Each member that the merge refuses (own work, hold, checks, review) costs one more gate round on the members left after it. So with r refusals, the total can reach about r + 1 times that sum.

### Merge outcomes

The union merge answers for each member:

| Outcome | Effect |
|---|---|
| A refusal by a guard of the member (own work, hold label, checks, review, a head that moved while preparing) | Rejects that member with the reason: every line of the `Refusal` of `merge.Run`, one per line. The members after it come back in `rest`, and the library folds and gates them again on the tip. |
| Unsettled (`mergeable` stayed unknown for three reads, 10 seconds apart, in the steps `merge:<name>.poll#<item>`) | Defers that member and all members not yet decided, with the reason |
| A transient refusal of `merge.Run` (`merge.Transient`: a failed read of the forge, checks that still run, a base that moved while preparing, an error of the forge merge call) | Defers the same way, with every line of the `Refusal` as the reason and no second read, because the merge step of a member never runs twice in one attempt |
| A pull request that is not OPEN at the intent read | Rejects that member, and the call goes on |
| An error | Stops the call |

### Recorded steps

The union merge records `merge:<name>.plan`, then for each member `merge:<name>.intent#<item>` before its merge, and `merge:<name>.tip` after the call. The intent step records the pull request as OPEN, so a resume after a crash counts a merge that the forge already made as done.

The library records each read through the builtin it calls: the steps of `base_tip`, `command`, `gate`, `landing_proof`, `issue_state` and `pr_closing`, named after the `name` of the call. A resume replays them, so the fold, the split and the audit decide the same way.

### Green gate and tree checks

A project gate counts as green only when it reports `passed` and every one of its steps is `pass`. An unknown step, or a gate with no step, is red, and no member of that union lands.

The gate reports the tree id it ran on. A tree id that differs from the tree of the union is the error "batch <name>: the gate ran on tree X, and the union is tree Y".

Before each member merge, the intent step reads the tip and requires the tree of the members merged so far. Another tree means that someone else pushed, and the rest of the members are deferred.

### Landing proof

After the last member merge, the library calls `landing_proof` once, and it checks each merged member against the base.

A member has landed when the forge compare shows its head in the history of the base: `behind_by` is 0 and the status is ahead or identical. If the head is not there, as after a squash, the member has landed on the vehicle commit only when each file it changed against the tip where the batch started has the same content on the base.

Every other member, and every member whose read failed, is a line of the result field `unproven`. The check fails closed, so a member that shares a path with another member is unproven. The call does not fail, because the merge cannot be undone.

## Remediation tools

`examples/remediation/tools/` holds the tools of the remediation example, in the same Go module as the engine.

### Binary and packages

- The one binary is `examples/remediation/tools/cmd/remedy`. `make remedy` makes `bin/remedy`. `make build` does not make it. The release archive and the cask hold only `beehave`. A `command` step finds `remedy` on the `PATH` of the beehave process, so a round starts with `bin` on its `PATH`. See `examples/remediation/README.md`.
- The package `remedy` holds the command table. `cmd/remedy` only passes the GitHub forge to `remedy.Main`, which wires the real git runner.
- A `command` step runs `remedy <subcommand>` and reads `rc` and `stdout`, for the read-only check `report`. The gate list derive is `beehave check gate-list`. The forge writes of a round (the sync, the landing of a sync, the cut, the integration pull request, the board card and the takeover) are engine steps that run as the writer role: `round_sync`, `round_land`, `round_cut`, `integration_pull`, `board_card` and `pr_take`. The tool has no subcommand for the sync, the landing, the cut, the integration pull request, the board card or the takeover.

The prompts of the example hold the text that a lane reads. `examples/remediation/prompts/` has one Markdown file for each step, and the `agent` step renders it. No Go code builds that text. The tools only judge a brief that a lane has written, in the package `examples/remediation/tools/brief`.

### Exit codes

Each subcommand follows the exit codes of beehave:

| Code | Meaning |
|---|---|
| 0 | a pass |
| 1 | a defect found |
| 2 | a refusal or a failed read |

A bad argument is a refusal. The tool finds the repository from the `origin` remote of its working folder.

### `round_cut`

`round_cut(old = <branch>, new = <branch>, pr = <number>, trunk = <branch>)` runs `roundcut`. It:

- refuses unless the integration pull request is merged,
- makes or resumes the new base only at the trunk tip,
- moves every open pull request on the old base to it,
- reads the trunk tip again right before it creates the new base, and refuses if the tip moved,
- reads each pull request again right before it moves it, and refuses if it is not open or no longer targets the old base,
- never deletes the old branch. The forge interface has no call that deletes a branch.

The step has no dry run. The `--dry` flag of the old `remedy cut` has no engine step, and the library keeps the dry path for tests only. The `trunk` argument is required, and the workflow passes the base branch of the project. The number of the new round is the text after the last dash of the new branch.

### `round_sync`

`round_sync(base = <branch>, trunk = <branch>)` runs `roundsync`. The pull request that it opens carries the `pr_label` of the project (`made-by-beehave` when the project sets none). The step `integration_pull` labels the integration pull request the same way. The forge creates the label first when the repository has none. It prints one line, `state=<state>` followed by the fields that the state has, and the workflow branches on `state`.

| State | Meaning |
|---|---|
| `current` | The round base holds the trunk tip. Nothing is written. |
| `synced` | The branch `sync/<base>/<first 8 characters of the trunk tip>` holds a merge commit of the base tip and the trunk tip, and a draft pull request into the base is open. The line gives `branch`, `pr`, `head` and `base_tip`. |
| `conflict` | A merge into the sync branch conflicts, and that merge writes nothing. The line gives `source`, the branch whose merge conflicted: the trunk, or the round base when the base moved after the branch was cut. An agent merges `source` on the sync branch and pushes. The next call merges what is left and opens the pull request, or names the next conflict. |
| `refused` | The forge refuses the merge for the workflow permission. The line has `reason=sync-refused`. |

GitHub's merges endpoint (`POST /repos/{owner}/{repo}/merges`) makes a merge commit with two parents, the base tip first, also when the base tip is an ancestor of the head: it never fast-forwards. It answers 201 with the commit, 204 with no body when the base holds the head, and 409 `Merge conflict` with nothing written. The tool reads a 409 as a conflict. It reads a refusal as `sync-refused` only when the forge's message holds `create or update workflow`, the text of GitHub's refused push of a workflow file. The merges endpoint's text for that refusal is not measured, and GitHub lists the endpoint under the contents permission only. Any refusal with another text is exit code 2, so an unknown text stops the round loudly and is never a silent pass.

The forge does each merge, so no checkout is written, and the round branch is never rebased. A repeat call finds the branch, the merge and the pull request. A base that moved after the branch was cut is merged into the branch, so the gated head holds every commit that the landing keeps. Any other failure of the forge is exit code 2.

`round_land(pr = <number>, head = <sha>, base_tip = <sha>)` merges the pull request with a merge commit pinned to `head`. It lands only when the base is still at `base_tip`. A moved base is `state=stale` and a moved head is `state=head-moved`. Both are exit code 2 and write nothing. A landing prints `state=landed`.

GitHub does not merge a draft: `gh pr merge` on one exits 1 with `GraphQL: Pull Request is still a draft (mergePullRequest)` (measured on a scratch repository with gh 2.101.0). The sync opens its pull request as a draft. The landing marks the draft ready only after the base and head checks pass, then merges it pinned to `head`.

When the merge fails, or the tool gets SIGINT during the merge, the landing makes the pull request a draft again. That call runs on a context that the SIGINT does not end, with its own limit of 30 seconds. If that call fails, its failure is part of the error of the landing. A sync that finds the open pull request of its sync branch ready makes it a draft before any merge into that branch. If the draft call fails, the sync stops before the merge.

A kill of the landing, or a SIGINT during the ready call, can leave the sync pull request ready on a head that passed its gate and review. The stop of a round and a step timeout kill the group of the tool with SIGKILL, which no process can catch. No error line says that the pull request is still ready. It stays ready until a `round_sync` on the same sync branch makes it a draft. A sync for a newer trunk tip cuts a new sync branch and does not read the old pull request. Once the sync holds its own open pull request, it closes every other open sync pull request into the round base, not only older ones, matched by the branch prefix `sync/<base>/`, with a comment that names the pull request that replaces it. A repeat call closes what a failed close left open. A failed close or a failed list is exit code 2 with the error of the forge. A person who merges the ready pull request merges that gated head. A person who marks the pull request ready by hand can also merge it.

#### The sync step of the round

The round of `examples/remediation/workflow.star` runs the sync at the start of each pass, before `queue-audit-<n>` and before any spawn. The engine has no builtin for it: the sync is command steps, one agent step and a gate of the workflow.

| Step | What it does |
|---|---|
| `sync-<n>` | The engine step `round_sync` with the round base and `base_branch`, run as the writer. A forge failure ends the run with its error. |
| `sync-resolve-fetch-<n>` | Runs only on `conflict`. A `command` step fetches the sync branch and `source` into the folder of the round, so the agent needs no network. A failed fetch stops the run. The step sits behind `patched`, so an open round that recorded the resolve agent before the step existed replays with no fetch. |
| `sync-resolve-<n>` | Runs only on `conflict`. One agent step (`prompts/sync.md`), in a sandbox with no network, merges `source` into the sync branch, resolves and commits the merge commit. The engine pushes it (`push-sync-<n>`). |
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

### The takeover step `pr_take`

`pr_take(pr, me, idle, grace, step)` runs `takeover`. It decides whether to take a pull request that somebody else holds.

| Step | What it does |
|---|---|
| `apply` | Posts the notice |
| `take` | Checks the notice, the grace period and the replies |

Every refusal ends the item with the lines of the step and the exit code 2. The `code` that the step returns tells the two kinds apart: 2 for a guard (a pull request that is not open, or is ours) and 3 for a rule. `me` is an argument, so the step keeps no state.

#### Rules for every step

- A pull request that is not open, or is ours, is refused first, in every step.
- A failed read refuses.

#### The notice

The notice needs a `CHANGES_REQUESTED` review of ours. It names its basis (assigned to us, or idle) and the grace period. A second notice is never posted: right before the post, `apply` asks the forge for a notice of ours. If one exists, it says so, writes nothing and exits 0.

The same read asks for the state, the author and the head again. If the pull request closed or became ours, `apply` posts nothing and refuses with code 2. If a notice of ours is already on the pull request, `apply` reports it and exits 0, whatever the head is. If no notice exists and the head is not the one the plan read, `apply` posts nothing and refuses with code 2. The refusal of a moved head names the old head and the new head. The step result of the workflow carries that code and those lines. A stop with code 2 ends the run as stopped. The next plan reads the new head only when the item runs again.

The text of the notice is Markdown in `takeover/text`, read through one `embed` variable.

#### `take` refusals and replies

`take` refuses in this order:

1. no notice,
2. a notice younger than the grace period,
3. a pull request touched inside the idle threshold,
4. a reply or a push after the notice. A push after the notice is an objection with no words.

| Reply | Effect |
|---|---|
| Says `KEEP` | Stops the take |
| Says `RELEASE` | Takes the pull request without the grace wait or the idle wait |
| Neither word | Prints every reply and refuses. |

A token stands alone as a word when no Unicode letter, digit or underscore touches it. So `KEEPER`, `KEEP_ALL` and `KEEPé` do not say `KEEP`.

#### Time

`takeover` never calls `time.Now`. The engine puts the time in the context, so every age is a pure function of the input. A time that is not a UTC stamp is a refusal. A pull request with no timestamp at all is unreadable, not idle.

#### Claims

The step runs inside the run of the item, and the engine writes only while that run holds the claim of the item. So the claim check of the takeover always passes, and the step reads no claim store and writes no claim.

The take assigns the pull request and its linked issues to `me` through `gh`, as the writer of the project. Right before the first assignment, the take reads the state, the author and the head again. If the pull request closed or became ours, the take refuses with code 2. If the head is not the one the plan read, the take refuses with code 2 and names the old head and the new head. In both cases it assigns nothing.

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
| `remedy handoff <file> [--name <text>] [--transcript <file>] [--cap <tokens>]` | Checks a handoff | A refused handoff | |
| `remedy authors <file> --email <address> --login <name>` | Checks `<sha> <email> <login>` lines against the identity of the owner | A commit of the owner under another address | A missing identity or an empty file |
| `remedy spend <transcript> [--hours <n>] [--cap <tokens>]` | Prints the hour table and one row for each lane | A lane over the cap | |
| `remedy report <file> --head <sha>` | Checks the four tables of a lane report | A defect | |

None of `handoff`, `authors` and `spend` reads the state folder. `report` reads no environment variable.

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
