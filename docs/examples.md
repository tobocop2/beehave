# Examples

Each folder under `examples` holds a workflow, a project file and the prompts. This page lists the settings of each one.

## examples/remediation

`examples/remediation` takes issues from beads and GitHub to reviewed pull requests. Set `MERGE` to `true` to let the workflow merge an approved pull request through the merge guards. With any other value, the owner merges. A run fails if `MERGE` is not set. Set `SOURCE_CHECKOUT` to the absolute path of the checkout of the owner: the implement, review and fix agents cannot write in it, and a git command there that is not a read is refused. A run that starts an agent fails if `SOURCE_CHECKOUT` is not set. Set `EXPECT_HEAD` to the full SHA the owner reviewed, and the run stops before any agent starts if the head of the pull request moved. When `EXPECT_HEAD` is not set, no head is checked. Set `LANE_KIND` to `papers` or `design` for a lane that changes no source path, and set `ALLOW_PATHS` to the globs it may change, split by spaces: the implement step and each fix step fail when they change any other path. The default `LANE_KIND` is `impl`, which has no path limit. A stop of this kind shows in the result of the run as `stopped`, with the step, the exit code and the reason. Set `LAND` to `id=sha` pairs, split by spaces, and the run starts no agent. Each `sha` is the full SHA the owner reviewed for that item. The run applies the stops of one item to each item, then lands the items that pass with one batch merge: one gate run, the same guards, and a split of the union if the gate is red. An item that stops, or whose head moved since the review, gets its reason as a comment and the hold status, and the other items still land. In that mode, `MERGE` works as for one item: a run fails if `MERGE` is not set, and a `MERGE` that is not `true` lands nothing.

## examples/local

`examples/local` needs no tracker and no forge. The items are in `local.db`, and the result is a branch in a git repository on this machine. Copy `examples/local/project.star` to `<config folder>/projects/<name>.star`, set `source`, and run:

```bash
obee item add <name> fix-login --title "Fix the login" --body-file task.md
obee run fix-login <name> --workflow examples/local/workflow.star
```

`obee run` prints a line when the run starts and when each step starts and ends. To see a run later, list the runs, then name one:

```bash
obee status
obee status item-fix-login-r1
```

The first command prints one line for each run, starting with the run id. The second prints one line for each step of that run: the name, the start time, the duration, the outcome and the fields that decide the flow. The last line gives the state of the run and, for a failed run, its error. Add `--json` to the second command for one JSON object with the fields `run`, `state`, `error` and `steps`. `obee run` writes its progress lines to stderr.

## examples/research

`examples/research` answers a question with notes and sources instead of code. It needs no tracker and no forge, and the result is a report file. Set `RESEARCH_TOOLS` to the absolute path of `examples/research/tools` and `RESEARCH_OUT` to the folder for the report.

## examples/swarm

`examples/swarm` splits a job into tasks, runs one agent for each task, and lands the pull requests with one `batch_merge` and one gate run. Put one task on each line of the body of the parent item. Set `REPO`, `BASE`, `OWNER_LOGIN` and `OWNER_EMAIL` to name your repository and your identity. A pull request that the gate or a guard rejects gets a comment and the status `hold`.

## examples/queue

`examples/queue` runs the open items of a project until no work remains. The parent reads `queue`, starts the ready items in the free slots, and starts the next item when any run ends. A failed item gets a comment and the status `hold`, and the loop goes on. Set `BASE` to the integration branch, and `OWNER_LOGIN` to your own login: each item reads the forge before its agent starts, and an open pull request by another login holds the item. One login runs one agent step at a time. The pick of a login prefers a login with no agent step running or queued, so the items spread over your logins. The parent run holds no login. With one login, the items run one at a time.
