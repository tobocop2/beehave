# The workflow API

This page is the contract of the builtins and keywords that a workflow file can call. It lists every builtin and every keyword that the engine registers, the fields that `facts()` returns, and where an example uses each pair.

## The rule

Every builtin and every keyword that the engine registers has one use in `examples/` or in the embedded library `internal/workflow/lib`, and a test for that use. A pair with no use is removed. The test `TestExamples_EveryBuiltinAndKeywordHasAUse` in `internal/flow` enforces the rule. It reads the registry of the engine, the table below and every call in the Starlark files. It fails for these cases:

- A builtin or keyword of the code with no row in the table.
- A pair of the code that no call uses.
- A row of the table that names no builtin or keyword of the code.

A second test checks that `workflow.Builtins` and the registry hold the same builtins.

A use counts only when an entry reaches it. The entries are the code at the top of each file, the functions `main` and `finish` of each file that runs (a file that another file loads does not run), and each function that the keyword `entry` of a call lists. The keyword `entry` lists a string, a parameter that a call fills with a string, or a variable: a variable lists the strings of the values that it is bound to. A function is reached when reached code reads its name, in a call or as a value. The test binds each name with the resolver of `go.starlark.net`, as `beehave check workflow` does. A keyword of a call, an attribute, a local variable and a string are no reads. A function that no entry reaches gives no use. One exception stays: `item_create.shared` is used only by `foreign_followup` of the remediation example, and `pr_take` (all its keywords) only by `take_over` of the same file. No entry calls them while the engine refuses every foreign pull request, and #438 wires it in. The test names these pairs and fails when an entry reaches one of them, so the exception ends with #438.

Two more tests read this page. One checks that the Use column of each row gives the count of files and the first two files of the calls, and that the Change column holds `unchanged` or `#<number>: <text>`. The other checks that the table of `facts()` holds the ten fields that the check of workflow files and the `facts` struct hold.

A call uses a keyword when it names the keyword, when it passes the value at the position of the keyword, or when it splats a dict that the same file builds with that key.

## The fields of `facts()`

`facts()` returns the ten fields below. Every other setting of a project lives under `vars`.

| Field | Meaning |
|---|---|
| `base_branch` | The default branch of the repository. |
| `build_host` | The build host of the project. |
| `forge` | The forge adapter. |
| `gate` | The gate command of the project. |
| `gate_steps` | The steps that the gate must run. |
| `repo` | The repository, as `owner/name`. |
| `runners` | The runners that the project allows. |
| `source` | The local checkout of the repository. |
| `tracker` | The tracker adapter. |
| `vars` | The dict of the project settings: the people, the rules, the limits, the board and the settings of a round. |

## The fields of a member of `merge`

A member of the list `prs` of `merge` is a `struct`. It is not a keyword, so the table below has no row for its fields.

| Field | Meaning |
|---|---|
| `item` | The item of the pull request. |
| `head` | The head commit that the gate ran on. |
| `approved` | True when the review approved the head. |
| `closes` | Required. The list of the issue numbers that the pull request must close. Each number is 1 or more, and none repeats. The merge refuses a pull request when the issues that the forge lists as closed by it differ from this list. Write `closes = []` for a pull request that closes none. |

## The builtins and keywords

The Change column says how the pair differs from the contract that the five pieces of the API shrink started with. The Use column gives the number of files that use the pair and the first two.

<!-- contract:target -->
| Builtin | Keyword | Change | Use |
|---|---|---|---|
| `agent` | `-` | unchanged | 16: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 14 more |
| `agent` | `allow_paths` | unchanged | 1: `internal/workflow/lib/review_fix.star` |
| `agent` | `data` | #693: new, a dict that the engine writes to the data file that the prompt names as `data_file` | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `agent` | `edits` | #497: also picks the review queue | 13: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 11 more |
| `agent` | `forbid_commands` | unchanged | 11: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 9 more |
| `agent` | `idle_timeout` | unchanged | 2: `examples/swarm/workflow.star`, `examples/ticket-pipeline/partition.star` |
| `agent` | `inputs` | unchanged | 16: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 14 more |
| `agent` | `label` | #497: new, a free text for the ledger and the status line | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `agent` | `max_budget_usd` | unchanged | 1: `examples/swarm/workflow.star` |
| `agent` | `max_context_percent` | #873: new, the share of the context window in percent at which the step stops with the stop `context` | 1: `examples/swarm/workflow.star` |
| `agent` | `model` | unchanged | 16: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 14 more |
| `agent` | `name` | unchanged | 16: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 14 more |
| `agent` | `output_tries` | unchanged | 3: `examples/remediation/workflow.star`, `examples/research/workflow.star` and 1 more |
| `agent` | `outputs` | unchanged | 12: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 10 more |
| `agent` | `pin` | unchanged | 3: `examples/local/workflow.star`, `examples/remediation/workflow.star` and 1 more |
| `agent` | `prompt` | unchanged | 16: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 14 more |
| `agent` | `protect_paths` | unchanged | 3: `examples/maintenance/bump.star`, `examples/remediation/workflow.star` and 1 more |
| `agent` | `resume` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `agent` | `runner` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `agent` | `sandbox` | unchanged | 12: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 10 more |
| `agent` | `skill` | #787: new, the skill that the agent CLI loads before the first turn of the model | 1: `examples/remediation/workflow.star` |
| `agent` | `timeout` | unchanged | 6: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 4 more |
| `base_tip` | `-` | #503: new, reads the tip commit of a base on the forge and its tree | 1: `examples/remediation/workflow.star` |
| `base_tip` | `base` | #503: new | 1: `examples/remediation/workflow.star` |
| `base_tip` | `name` | #503: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `-` | #381: new, the engine puts a pull request or an issue on the project board and reads the column back, as the writer | 1: `examples/remediation/workflow.star` |
| `board_card` | `board` | #381: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `issue` | #381: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `owner` | #381: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `pr` | #381: new | 1: `examples/remediation/workflow.star` |
| `board_card` | `status` | #381: new | 1: `examples/remediation/workflow.star` |
| `cancel` | `-` | #593: new, cancels one run that spawn returned, with its children | 2: `examples/planner/workflow.star`, `examples/supervisor/workflow.star` |
| `cancel` | `name` | #593: new | 2: `examples/planner/workflow.star`, `examples/supervisor/workflow.star` |
| `cancel` | `run` | #593: new | 2: `examples/planner/workflow.star`, `examples/supervisor/workflow.star` |
| `closing_references` | `-` | #586: new, reads the issues that the pull request of the item closes | 1: `internal/workflow/lib/review_fix.star` |
| `closing_references` | `text` | #586: new | 1: `internal/workflow/lib/review_fix.star` |
| `command` | `-` | unchanged | 19: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 17 more |
| `command` | `backoff` | #594: new, the wait before the first retry, which doubles before each later retry | 1: `examples/research-desk/workflow.star` |
| `command` | `env` | unchanged | 4: `examples/research-desk/workflow.star`, `examples/research/workflow.star` and 2 more |
| `command` | `failure` | #753: new, `"return"` gives an error of the step as the field `error` of the result, with `rc` -1 | 1: `examples/remediation/workflow.star` |
| `command` | `host` | unchanged | 1: `examples/remediation/workflow.star` |
| `command` | `max_new_processes` | unchanged | 1: `examples/maintenance/bump.star` |
| `command` | `name` | unchanged | 19: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 17 more |
| `command` | `retries` | #594: new, a try that fails starts again, up to 10 times; the result has `attempts` | 1: `examples/research-desk/workflow.star` |
| `command` | `run` | unchanged | 19: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 17 more |
| `command` | `sandbox` | #381: new, runs the program under the enforcer of the gate | 1: `examples/planner/workflow.star` |
| `command` | `slots` | unchanged | 1: `examples/remediation/workflow.star` |
| `command` | `stdin` | unchanged | 9: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 7 more |
| `command` | `stop_on_rc` | unchanged | 2: `examples/remediation/workflow.star`, `examples/research/workflow.star` |
| `command` | `timeout` | unchanged | 7: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 5 more |
| `earlier_calls` | `-` | unchanged | 1: `internal/workflow/lib/review_fix.star` |
| `earlier_calls` | `call` | unchanged | 1: `internal/workflow/lib/review_fix.star` |
| `earlier_calls` | `name` | unchanged | 1: `internal/workflow/lib/review_fix.star` |
| `earlier_reads` | `-` | #500: new, returns the recorded results of the earlier steps of one kind and name, newest run first | 1: `internal/workflow/lib/scope.star` |
| `earlier_reads` | `item` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `earlier_reads` | `kind` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `earlier_reads` | `name` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `earlier_reads` | `own_run` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `env` | `-` | unchanged | 20: `examples/approvals/audit.star`, `examples/approvals/policy.star` and 18 more |
| `env` | `default` | unchanged | 13: `examples/approvals/policy.star`, `examples/desk/eval.star` and 11 more |
| `env` | `name` | unchanged | 20: `examples/approvals/audit.star`, `examples/approvals/policy.star` and 18 more |
| `facts` | `-` | unchanged | 4: `examples/local/workflow.star`, `examples/remediation/workflow.star` and 2 more |
| `first_of` | `-` | #591: new, waits for the first of a run that ended, a message and a timeout | 4: `examples/planner/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `first_of` | `name` | #591: new | 4: `examples/planner/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `first_of` | `runs` | #591: new | 4: `examples/planner/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `first_of` | `timeout` | #591: new | 1: `examples/planner/workflow.star` |
| `first_of` | `topics` | #591: new | 1: `examples/planner/workflow.star` |
| `gate` | `-` | unchanged | 5: `examples/local/workflow.star`, `examples/maintenance/bump.star` and 3 more |
| `gate` | `base` | unchanged | 1: `internal/workflow/lib/review_fix.star` |
| `gate` | `head` | unchanged | 1: `examples/remediation/workflow.star` |
| `gate` | `host` | unchanged | 1: `examples/remediation/workflow.star` |
| `gate` | `name` | unchanged | 5: `examples/local/workflow.star`, `examples/maintenance/bump.star` and 3 more |
| `gate` | `pin_base` | unchanged | 1: `examples/remediation/workflow.star` |
| `gate` | `run` | unchanged | 2: `examples/remediation/workflow.star`, `examples/research-desk/workflow.star` |
| `gate` | `timeout` | unchanged | 1: `examples/remediation/workflow.star` |
| `gather` | `-` | unchanged | 7: `examples/desk/eval.star`, `examples/desk/workflow.star` and 5 more |
| `gather` | `fail` | unchanged | 5: `examples/desk/eval.star`, `examples/desk/workflow.star` and 3 more |
| `gather` | `name` | unchanged | 7: `examples/desk/eval.star`, `examples/desk/workflow.star` and 5 more |
| `gather` | `on_timeout` | #593: new, `cancel` or `return` for the runs still open at the deadline | 1: `examples/planner/workflow.star` |
| `gather` | `runs` | unchanged | 7: `examples/desk/eval.star`, `examples/desk/workflow.star` and 5 more |
| `gather` | `timeout` | #593: new, the deadline of the group | 1: `examples/planner/workflow.star` |
| `hand_off` | `-` | #503: new, marks the pull requests ready and waits for the owner to merge them. It is the hand-off of `merge` with its own name | 1: `examples/remediation/workflow.star` |
| `hand_off` | `base` | #503: new | 1: `examples/remediation/workflow.star` |
| `hand_off` | `name` | #503: new | 1: `examples/remediation/workflow.star` |
| `hand_off` | `prs` | #503: new | 1: `examples/remediation/workflow.star` |
| `hand_off` | `repo` | #503: new | 1: `examples/remediation/workflow.star` |
| `host_teardown` | `-` | unchanged | 1: `examples/remediation/workflow.star` |
| `host_teardown` | `host` | unchanged | 1: `examples/remediation/workflow.star` |
| `host_teardown` | `name` | unchanged | 1: `examples/remediation/workflow.star` |
| `host_up` | `-` | unchanged | 1: `examples/remediation/workflow.star` |
| `host_up` | `host` | unchanged | 1: `examples/remediation/workflow.star` |
| `host_up` | `name` | unchanged | 1: `examples/remediation/workflow.star` |
| `integration_pull` | `-` | #381: new, the engine finds or opens the integration pull request and writes its body, as the writer | 1: `examples/remediation/workflow.star` |
| `integration_pull` | `base` | #381: new | 1: `examples/remediation/workflow.star` |
| `integration_pull` | `into` | #381: new | 1: `examples/remediation/workflow.star` |
| `integration_pull` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `integration_pull` | `pr` | #381: new | 1: `examples/remediation/workflow.star` |
| `issue_assignees` | `-` | #499: new, returns the logins that hold an issue, on the repository of the link | 1: `internal/workflow/lib/claims.star` |
| `issue_assignees` | `name` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `issue_assignees` | `number` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `issue_assignees` | `repo` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `issue_state` | `-` | #503: new, reads the state of an issue, OPEN or CLOSED | 1: `examples/remediation/workflow.star` |
| `issue_state` | `name` | #503: new | 1: `examples/remediation/workflow.star` |
| `issue_state` | `number` | #503: new | 1: `examples/remediation/workflow.star` |
| `item_comment` | `-` | unchanged | 14: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 12 more |
| `item_comment` | `forbid_text` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_comment` | `id` | unchanged | 14: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 12 more |
| `item_comment` | `key` | unchanged | 14: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 12 more |
| `item_comment` | `name` | unchanged | 14: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 12 more |
| `item_comment` | `text` | unchanged | 14: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 12 more |
| `item_create` | `-` | unchanged | 8: `examples/desk/eval.star`, `examples/desk/workflow.star` and 6 more |
| `item_create` | `assignee` | unchanged | 1: `examples/ticket-pipeline/backfill.star` |
| `item_create` | `body` | unchanged | 8: `examples/desk/eval.star`, `examples/desk/workflow.star` and 6 more |
| `item_create` | `forbid_text` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_create` | `key` | unchanged | 8: `examples/desk/eval.star`, `examples/desk/workflow.star` and 6 more |
| `item_create` | `name` | unchanged | 8: `examples/desk/eval.star`, `examples/desk/workflow.star` and 6 more |
| `item_create` | `operator` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_create` | `parent` | unchanged | 7: `examples/desk/eval.star`, `examples/desk/workflow.star` and 5 more |
| `item_create` | `severity` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_create` | `shared` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_create` | `title` | unchanged | 8: `examples/desk/eval.star`, `examples/desk/workflow.star` and 6 more |
| `item_list` | `-` | #498: new, reads the open items of the project, with one tracker label | 1: `internal/workflow/lib/queue.star` |
| `item_list` | `label` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `item_list` | `name` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `item_read` | `-` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_read` | `name` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_read` | `of` | unchanged | 1: `examples/remediation/workflow.star` |
| `item_status` | `-` | unchanged | 13: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 11 more |
| `item_status` | `id` | unchanged | 13: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 11 more |
| `item_status` | `name` | unchanged | 13: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 11 more |
| `item_status` | `status` | unchanged | 13: `examples/approvals/audit.star`, `examples/approvals/workflow.star` and 11 more |
| `item_stop` | `-` | #499: new, records a stop of the item in the result of the run | 1: `internal/workflow/lib/claims.star` |
| `item_stop` | `code` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `item_stop` | `id` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `item_stop` | `name` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `item_stop` | `reason` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `item_stop` | `step` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `json` | `-` | unchanged | 14: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 12 more |
| `keep_issues` | `-` | #500: new, reads the issues of an owner line in the form `keep #N[, owner/repo#N ...]`, the form that the merge reads to trust `kept` | 1: `internal/workflow/lib/scope.star` |
| `keep_issues` | `text` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `merge` | `-` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `base` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `expect_tree` | #503: new, the merge stops as unsettled when the base tip has another tree | 1: `examples/remediation/workflow.star` |
| `merge` | `forbid_text` | #585: from `batch_merge` | 1: `examples/remediation/workflow.star` |
| `merge` | `gate` | #503: new, the result of a gate call of this run. The engine posts the statuses of that gate and trusts none that the run did not record | 1: `examples/remediation/workflow.star` |
| `merge` | `gate_run` | #585: from `batch_merge` | 1: `examples/remediation/workflow.star` |
| `merge` | `hand_off` | #653: new, marks the pull requests ready and waits for the owner to merge them | 1: `examples/remediation/workflow.star` |
| `merge` | `gated_tree` | #503: new, the tree that the gate ran on | 1: `examples/remediation/workflow.star` |
| `merge` | `hold` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `integration` | #585: from `batch_merge` | 1: `examples/remediation/workflow.star` |
| `merge` | `member` | #503: new, lands one pull request | 1: `examples/remediation/workflow.star` |
| `merge` | `name` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `own` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `prs` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `merge` | `repo` | #585: from `batch_merge` | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `now` | `-` | unchanged | 2: `examples/remediation/workflow.star`, `examples/swarm/workflow.star` |
| `paths_outside` | `-` | #586: new, lists the changed paths outside an allowed set | 1: `internal/workflow/lib/review_fix.star` |
| `paths_outside` | `allow` | #586: new | 1: `internal/workflow/lib/review_fix.star` |
| `paths_outside` | `paths` | #586: new | 1: `internal/workflow/lib/review_fix.star` |
| `patched` | `-` | #933: new, true for a run that has not passed the call and false for a run that has, so a change before the reached step of an open run replays | 1: `examples/local/workflow.star` |
| `patched` | `name` | #933: new | 1: `examples/local/workflow.star` |
| `pr_closing` | `-` | #500: new, reads the issues that the pull request of an item closes | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/scope.star` |
| `pr_closing` | `item` | #500: new | 1: `internal/workflow/lib/scope.star` |
| `pr_closing` | `name` | #500: new | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/scope.star` |
| `pr_closing` | `pr` | #503: new, reads the pull request with this number in place of the pull request of an item | 1: `examples/remediation/workflow.star` |
| `pr_comment` | `-` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_comment` | `forbid_text` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_comment` | `key` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_comment` | `name` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_comment` | `text` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_history` | `-` | #700: new, the pull requests of the item in every state, with the closing comment of each closed or merged one | 1: `examples/remediation/workflow.star` |
| `pr_history` | `item` | #501: new, reads the pull requests of another item, such as the parent | 1: `examples/remediation/workflow.star` |
| `pr_history` | `name` | #700: new | 1: `examples/remediation/workflow.star` |
| `pr_list` | `-` | #498: new, reads the open pull requests of the project, on one base | 1: `internal/workflow/lib/queue.star` |
| `pr_list` | `base` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `pr_list` | `name` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `pr_open` | `-` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `pr_open` | `base` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `pr_open` | `body` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `pr_open` | `forbid_text` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_open` | `name` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `pr_open` | `parent` | unchanged | 1: `examples/remediation/workflow.star` |
| `pr_open` | `title` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `pr_read` | `-` | #499: new, returns the facts of the pull request of an item: state, draft flag, labels, author, assignees, reviewers and linked issues | 1: `internal/workflow/lib/claims.star` |
| `pr_read` | `id` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `pr_read` | `name` | #499: new | 1: `internal/workflow/lib/claims.star` |
| `pr_state` | `-` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/queue.star` |
| `pr_state` | `name` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/queue.star` |
| `pr_state` | `pr` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/queue.star` |
| `pr_take` | `-` | #381: new, the engine posts the notice for a pull request of somebody else, or takes it, as the writer | 1: `examples/remediation/workflow.star` |
| `pr_take` | `grace` | #381: new | 1: `examples/remediation/workflow.star` |
| `pr_take` | `idle` | #381: new | 1: `examples/remediation/workflow.star` |
| `pr_take` | `me` | #381: new | 1: `examples/remediation/workflow.star` |
| `pr_take` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `pr_take` | `pr` | #381: new | 1: `examples/remediation/workflow.star` |
| `pr_take` | `step` | #381: new, `apply` posts the notice and `take` takes the pull request | 1: `examples/remediation/workflow.star` |
| `preflight` | `-` | unchanged | 3: `examples/queue/workflow.star`, `examples/remediation/workflow.star` and 1 more |
| `preflight` | `base` | unchanged | 2: `examples/queue/workflow.star`, `examples/remediation/workflow.star` |
| `preflight` | `head` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `preflight` | `id` | unchanged | 2: `examples/remediation/workflow.star`, `internal/workflow/lib/review_fix.star` |
| `preflight` | `name` | unchanged | 3: `examples/queue/workflow.star`, `examples/remediation/workflow.star` and 1 more |
| `progress` | `-` | #589: new, reads what each child run published under a key, and does not wait | 1: `examples/planner/workflow.star` |
| `progress` | `key` | #589: new | 1: `examples/planner/workflow.star` |
| `progress` | `name` | #589: new | 1: `examples/planner/workflow.star` |
| `progress` | `runs` | #589: new | 1: `examples/planner/workflow.star` |
| `publish` | `-` | #589: new, sets the value of a key of this run for a parent to read | 1: `examples/planner/workflow.star` |
| `publish` | `key` | #589: new | 1: `examples/planner/workflow.star` |
| `publish` | `name` | #589: new | 1: `examples/planner/workflow.star` |
| `publish` | `value` | #589: new | 1: `examples/planner/workflow.star` |
| `push` | `-` | #697: new, the engine pushes the head of a step to a branch with a lease | 4: `examples/local/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `push` | `branch` | #697: new | 4: `examples/local/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `push` | `expect` | #697: new | 4: `examples/local/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `push` | `head` | #697: new | 4: `examples/local/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `push` | `name` | #697: new | 4: `examples/local/workflow.star`, `examples/queue/workflow.star` and 2 more |
| `recv` | `-` | #588: new, waits for the next message of a topic on the inbox of the run | 2: `examples/merge-order/waiting.star`, `examples/review-loop/workflow.star` |
| `recv` | `name` | #588: new | 2: `examples/merge-order/waiting.star`, `examples/review-loop/workflow.star` |
| `recv` | `timeout` | #588: new, required | 2: `examples/merge-order/waiting.star`, `examples/review-loop/workflow.star` |
| `recv` | `topic` | #588: new | 2: `examples/merge-order/waiting.star`, `examples/review-loop/workflow.star` |
| `round_cut` | `-` | #381: new, the engine cuts the next round branch as the writer and moves the open pull requests to it | 1: `examples/remediation/workflow.star` |
| `round_cut` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_cut` | `new` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_cut` | `old` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_cut` | `pr` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_cut` | `trunk` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_land` | `-` | #381: new, the engine lands the sync pull request pinned to the gated head, as the writer | 1: `examples/remediation/workflow.star` |
| `round_land` | `base_tip` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_land` | `head` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_land` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_land` | `pr` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_sync` | `-` | #381: new, the engine merges the trunk into the round branch through a sync branch, as the writer | 1: `examples/remediation/workflow.star` |
| `round_sync` | `base` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_sync` | `name` | #381: new | 1: `examples/remediation/workflow.star` |
| `round_sync` | `trunk` | #381: new | 1: `examples/remediation/workflow.star` |
| `sandbox` | `-` | unchanged | 12: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 10 more |
| `sandbox` | `network` | unchanged | 1: `examples/swarm/workflow.star` |
| `run_read` | `-` | #498: new, reads the free slots and the last run of each listed item | 1: `internal/workflow/lib/queue.star` |
| `run_read` | `items` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `run_read` | `name` | #498: new | 1: `internal/workflow/lib/queue.star` |
| `send` | `-` | #588: new, puts a message on the inbox of the parent run, of a child run or of the open run of an item of the project | 2: `examples/merge-order/dependency.star`, `examples/review-loop/workflow.star` |
| `send` | `name` | #588: new | 2: `examples/merge-order/dependency.star`, `examples/review-loop/workflow.star` |
| `send` | `to` | #854: `"item:<id>"` added to `"parent"` and a run that `spawn` returned | 2: `examples/merge-order/dependency.star`, `examples/review-loop/workflow.star` |
| `send` | `topic` | #588: new | 2: `examples/merge-order/dependency.star`, `examples/review-loop/workflow.star` |
| `send` | `value` | #588: new, a value that encodes as JSON | 2: `examples/merge-order/dependency.star`, `examples/review-loop/workflow.star` |
| `sleep` | `-` | #590: new, a durable sleep that a recovered run does not start again | 1: `examples/queue/workflow.star` |
| `sleep` | `duration` | #590: new | 1: `examples/queue/workflow.star` |
| `sleep` | `name` | #590: new | 1: `examples/queue/workflow.star` |
| `spawn` | `-` | unchanged | 11: `examples/desk/eval.star`, `examples/desk/workflow.star` and 9 more |
| `spawn` | `base` | unchanged | 1: `examples/remediation/workflow.star` |
| `spawn` | `entry` | unchanged | 11: `examples/desk/eval.star`, `examples/desk/workflow.star` and 9 more |
| `spawn` | `items` | unchanged | 11: `examples/desk/eval.star`, `examples/desk/workflow.star` and 9 more |
| `spawn` | `name` | unchanged | 11: `examples/desk/eval.star`, `examples/desk/workflow.star` and 9 more |
| `spawn` | `on_open` | #934: new, `"join"` (the default) refuses an item that has an open run, because a joined run would have no parent row, and `"replace"` cancels the open run with its children and starts the next attempt; `beehave run` with no option joins the open run | 1: `examples/planner/workflow.star` |
| `spawn` | `retries` | #594: new, `gather` starts a child that failed again as its next attempt; the result of `gather` has `attempts` | 1: `examples/desk/eval.star` |
| `stream` | `-` | #853: new, reads the redacted agent stream of a child run after a cursor | 1: `examples/supervisor/workflow.star` |
| `stream` | `after` | #853: new, the `next` of an earlier call, or empty | 1: `examples/supervisor/workflow.star` |
| `stream` | `name` | #853: new | 1: `examples/supervisor/workflow.star` |
| `stream` | `run` | #853: new, a run that `spawn` returned | 1: `examples/supervisor/workflow.star` |
| `stream` | `timeout` | #853: new, required | 1: `examples/supervisor/workflow.star` |
| `struct` | `-` | unchanged | 18: `examples/approvals/workflow.star`, `examples/desk/clerk.star` and 16 more |
| `wait` | `-` | unchanged | 13: `examples/approvals/workflow.star`, `examples/desk/eval.star` and 11 more |
| `wait` | `choices` | unchanged | 4: `examples/approvals/workflow.star`, `examples/maintenance/bump.star` and 2 more |
| `wait` | `name` | unchanged | 13: `examples/approvals/workflow.star`, `examples/desk/eval.star` and 11 more |
| `wait` | `question` | unchanged | 9: `examples/approvals/workflow.star`, `examples/desk/eval.star` and 7 more |
| `wait` | `quorum` | unchanged | 1: `examples/approvals/workflow.star` |
| `wait` | `remind` | unchanged | 1: `examples/approvals/workflow.star` |
| `wait` | `timeout` | unchanged | 7: `examples/approvals/workflow.star`, `examples/incident/workflow.star` and 5 more |
| `wait` | `to` | unchanged | 2: `examples/approvals/workflow.star`, `examples/remediation/workflow.star` |
| `write` | `-` | unchanged | 2: `examples/desk/workflow.star`, `examples/remediation/workflow.star` |
| `write` | `backoff` | #594: new, as on `command` | 1: `examples/desk/workflow.star` |
| `write` | `env` | unchanged | 1: `examples/remediation/workflow.star` |
| `write` | `name` | unchanged | 2: `examples/desk/workflow.star`, `examples/remediation/workflow.star` |
| `write` | `read_back` | unchanged | 1: `examples/desk/workflow.star` |
| `write` | `retries` | #594: new, as on `command`; each try runs the read-back first | 1: `examples/desk/workflow.star` |
| `write` | `run` | unchanged | 2: `examples/desk/workflow.star`, `examples/remediation/workflow.star` |
| `write` | `stdin` | unchanged | 2: `examples/desk/workflow.star`, `examples/remediation/workflow.star` |
| `write` | `timeout` | unchanged | 1: `examples/remediation/workflow.star` |
| `write` | `undo` | unchanged | 1: `examples/desk/workflow.star` |
<!-- /contract:target -->
