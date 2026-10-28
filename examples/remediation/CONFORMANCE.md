# Conformance of the remediation example

This file lists the rules of the method and says where `workflow.hcl` holds each one. It has one row for each row of the parity matrices, so a row can cover several rules. The ID is the first rule ID of the row, or `SC-<n>` when the row has no rule ID.

Home says who owns the rule:

- `core`: a general mechanism of the engine.
- `file`: a value or a policy in `workflow.hcl` or a prompt.
- `skill`: judgment, kept as a skill.
- `drop`: a rule only for a human or AI orchestrator, which the engine does not need.

Status says how far the example goes:

- `gap`: the example does not hold the rule yet.
- `expressed`: `workflow.hcl` holds the rule and `obee check workflow` passes on it.
- `proven`: a behavior test runs the rule.
- `n/a`: the rule does not apply to the engine.

Where is `workflow.hcl <kind> <name>` for an `expressed` row. Test names the Go test that checks the row. A `proven` row must name a test. An `expressed` row may leave Test as `-`. When a row names a test, `func <Name>(` must exist in a `*_test.go` file of the repository.

Covers lists every rule ID that the row covers, its own ID first. A row with no rule ID (`SC-`) shows `-`. The Status holds for the Rule text only: in an `expressed` row, the parts of the covered rules that the Rule text leaves out are still gaps.

The review in this workflow is fresh in each round (`agent "review"` has no `resume_from`). The method keeps a conflict open between a resumed reviewer (S1-L-12) and a fresh agent in late rounds (S1-L-19). This example takes the fresh side. An implementer iterating the review to zero, and a reviewer running one sweep, are prose in the prompts and not in the file.

The prompts hold the rules of the brief text that the `brief` package once built from 19 embedded files, in plain words. The test `TestPrompts_HoldEveryRuleOfTheBriefText` keeps one phrase of each rule in the prompts, and fails when a prompt loses one. The rules for the build host, compiled code and a union are in the prompts too. The long-job and handoff-before-launch parts are not in the prompts: the engine submits and waits for each job, so an agent launches none (S1-L-56, drop). The prompts also state rules that the core is meant to enforce. Each of these rules has a row with Home `core` and Status `gap`: S1-P-04 (a reviewer does not fix, commit or push), S1-G-01 and S1-G-11 (gate steps and a result for each step), S1-L-06 (the context cap), S1-Q-08 (the check for an existing pull request) and S1-G-23 (the four report tables). The prompts keep the prose until the core enforces it.

| ID | Rule | Home | Status | Where | Test | Covers |
|---|---|---|---|---|---|---|
| S1-Q-01 | Build the queue from forge and tracker, one row per item with a state | core | gap | - | - | S1-Q-01, S1-Q-02, S1-Q-31 |
| S1-Q-03 | Order of work: others first, then change requests, in-flight, new by priority | core | gap | - | - | S1-Q-03, S1-Q-04, S1-Q-06, S1-Q-07, S1-Q-09, S1-Q-37 |
| S1-Q-05 | Two queues, review and implement, with a lane split that lends slots | core | gap | - | - | S1-Q-05, S1-Q-30 |
| S1-Q-08 | Refresh the queue at each batch boundary; check for an existing PR | core | gap | - | - | S1-Q-08, S1-Q-34 |
| S1-Q-10 | Work assigned to someone else is not ours; read the assignee before starting | core | gap | - | - | S1-Q-10, S1-Q-11 |
| S1-Q-12 | Say which filter produced a number | drop | n/a | - | - | S1-Q-12 |
| S1-Q-13 | Rank on container membership and severity together | core | gap | - | - | S1-Q-13, S1-Q-14 |
| S1-Q-15 | Never route a held item | core | gap | - | - | S1-Q-15, S1-Q-16 |
| S1-Q-17 | An item that needs the user goes on hold with options and a recommendation | skill | gap | - | - | S1-Q-17, S1-Q-18, S1-Q-19 |
| S1-Q-20 | Run the queue until no work remains; recompute after every merge | core | gap | - | - | S1-Q-20, S1-Q-21 |
| S1-Q-22 | Every terminal state dispatches the next item | core | gap | - | - | S1-Q-22 |
| S1-Q-23 | Dispatch independent items to free slots while one item loops | core | gap | - | - | S1-Q-23 |
| S1-Q-24 | An item is the scope of one PR; batch issues that share a mechanism | skill | gap | - | - | S1-Q-24 |
| S1-Q-25 | Scope is fixed at the first review; a new closed issue needs an owner line | core | gap | tools/remedy scope | TestScope_ANewClosedIssueIsExitCodeTwoAndNamesTheCommandForTheOwnerRow | S1-Q-25 |
| S1-Q-26 | Work that belongs to someone outside the system gets an owner token | core | gap | - | - | S1-Q-26 |
| S1-Q-27 | A filed issue has no assignee by default | file | gap | - | - | S1-Q-27 |
| S1-Q-28 | The issue store is pluggable; an unknown value is refused | core | gap | - | - | S1-Q-28, S1-Q-29 |
| S1-Q-32 | Triage far-behind PRs; rank a conflict by the cost of a wrong resolution | skill | gap | - | - | S1-Q-32, S1-Q-33 |
| S1-Q-35 | A ready file holds items that wait for a lane | drop | n/a | - | - | S1-Q-35, S1-Q-36 |
| S1-Q-38 | Re-arm the watcher and the lane files after a round ends | drop | n/a | - | - | S1-Q-38 |
| S1-C-01 | Learn whether another orchestrator runs; fail closed on an unreadable store | core | gap | - | - | S1-C-01, S1-C-02, S1-C-03 |
| S1-C-04 | Print owners, holdings and free tokens | drop | n/a | - | - | S1-C-04 |
| S1-C-05 | Claim before reading in depth; claim each PR opened; walk down on refusal | core | gap | - | - | S1-C-05, S1-C-06 |
| S1-C-07 | A claim re-reads after writing; a fixed tiebreak decides a conflict | core | gap | - | - | S1-C-07, S1-C-08, S1-C-09 |
| S1-C-10 | Release when done; release clears the assignee | core | gap | - | - | S1-C-10 |
| S1-C-11 | An unclaimed item is not free if a lane or open PR is on it | core | gap | - | - | S1-C-11 |
| S1-C-12 | The lock coordinates our sessions only; its state stays out of the forge | file | gap | - | - | S1-C-12, S1-C-13 |
| S1-C-14 | A second run needs its own build host | core | gap | - | - | S1-C-14 |
| S1-C-15 | Per-letter lane and queue files; the stop guard checks the watcher | drop | n/a | - | - | S1-C-15, S1-C-16, S1-C-17, S1-C-22 |
| S1-C-18 | Re-read the base tip twice before a merge when another session can merge | core | gap | - | - | S1-C-18 |
| S1-C-19 | Merge or retarget PRs based on the head branch first | core | gap | - | - | S1-C-19 |
| S1-C-20 | Hand over between orchestrator sessions with a parked row | drop | n/a | - | - | S1-C-20, S1-C-21 |
| SC-01 | Take over an idle foreign PR after notice, grace and idle hours | core | gap | tools/remedy take | TestTake_TakeAfterTheGracePeriodPasses | - |
| S1-L-01 | Name the model in every dispatch; a step without a model is a defect | file | expressed | workflow.hcl workflow remediation | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-L-01, S1-L-02, S1-L-03, S1-L-04 |
| S1-L-05 | Turn count beats token price; keep a mid-tier floor | drop | n/a | - | - | S1-L-05 |
| S1-L-06 | Replace a lane near the context cap at a step boundary | core | gap | tools/remedy spend | TestSpend_TheCapFlagMovesTheLine | S1-L-06, S1-L-07, S1-L-08, S1-L-10, S1-L-11 |
| S1-L-09 | Message a lane that has no handoff at the next wake | drop | n/a | - | - | S1-L-09, S1-L-51 |
| S1-L-12 | One fresh context per phase, started from files | core | gap | - | - | S1-L-12, S1-L-13 |
| S1-L-14 | One PR per reviewer; split a fix round into fix, proof and review lanes | file | gap | - | - | S1-L-14, S1-L-15 |
| S1-L-16 | Every log a lane reads has a summary command | skill | gap | - | - | S1-L-16 |
| S1-L-17 | Hourly spend lines per live lane; flag over-cap lanes | core | gap | tools/remedy spend | TestSpend_ALaneOverTheCapIsExitCodeOne | S1-L-17 |
| S1-L-18 | A handoff holds regressions, tokens, compile state and unread jobs | core | gap | - | - | S1-L-18, S1-L-23, S1-L-54, S1-L-55 |
| S1-L-19 | Five fix rounds: rounds 1 to 3 resume, rounds 4 and 5 start fresh one tier up and are told the round | core | expressed | workflow.hcl agent fix | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-L-19, S1-L-20 |
| S1-L-21 | At five rounds, adjudicate each open finding | skill | expressed | workflow.hcl wait adjudicate | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-L-21 |
| S1-L-22 | Lane ledger counts lanes by kind and flags the sixth lane | core | gap | tools/remedy ledger count | TestLedger_AddRecordsALaneAndCountReadsItBack | S1-L-22, S1-L-59, S1-L-24, S1-L-25 |
| S1-L-26 | Design lane rules: coverage, cheapest evidence, repeat-offender, build-versus-buy | skill | gap | - | - | S1-L-26, S1-L-86, S1-L-97, S1-L-98, S1-L-99, S1-L-100, S1-L-104 |
| S1-L-27 | Keep a capped number of lanes busy; a parked lane holds no slot | core | gap | - | - | S1-L-27, S1-L-28, S1-L-31 |
| S1-L-29 | Retire a lane only when its worktree is clean and pushed | core | gap | - | - | S1-L-29, S1-L-30, S1-L-32 |
| S1-L-33 | Never touch a live lane's worktree; a stale log is not a stale lane | drop | n/a | - | - | S1-L-33, S1-L-34, S1-L-35, S1-L-36, S1-L-38, S1-L-40, S1-L-42, S1-L-43, S1-L-44, S1-L-45, S1-L-46, S1-L-47, S1-L-48 |
| S1-L-37 | An overload error is a pause; do not infer from a line count | drop | n/a | - | - | S1-L-37, S1-L-39, S1-L-88, S1-L-89, S1-L-90, S1-L-91, S1-L-93 |
| S1-L-41 | A lane stops every background command and reports none left | core | gap | - | - | S1-L-41 |
| S1-L-49 | A lane blocked 30 minutes stops and reports | core | gap | - | - | S1-L-49, S1-L-50 |
| S1-L-52 | A replacement brief is self-contained; rulings go in one file | core | gap | - | - | S1-L-52, S1-L-53, S1-L-81, S1-B-04 |
| S1-L-56 | No stacked edit-only lanes; handoff before launch; wait rules for jobs. The engine submits and waits for each job, so an agent launches none | drop | n/a | - | - | S1-L-56, S1-L-57, S1-L-58 |
| S1-L-60 | Lane rules: one issue, host only, never merge, comment or file | skill | gap | - | - | S1-L-60, S1-L-61, S1-L-62, S1-L-63, S1-L-64, S1-L-65, S1-L-66, S1-L-67, S1-L-68, S1-L-69, S1-L-81 |
| S1-L-70 | Orchestrator cost discipline: output to files, count calls, hourly cadence | drop | n/a | - | - | S1-L-70, S1-L-71, S1-L-72, S1-L-73, S1-L-76 |
| S1-L-74 | Compaction keeps facts; state on disk at every transition | drop | n/a | - | - | S1-L-74, S1-L-75, S1-L-77, S1-L-78, S1-L-79 |
| S1-L-80 | A cheapest-tier lane drafts briefs | drop | n/a | - | - | S1-L-80 |
| S1-L-82 | Derive the target directory from the lane name; never delete a target that looks idle | core | gap | - | - | S1-L-82, S1-L-83, S1-L-84, S1-L-85 |
| S1-L-87 | Do not race a gate lane: merge nothing while it runs | core | gap | - | - | S1-L-87 |
| S1-L-92 | A reviewer reuses the implementer's build when the tree hashes match | core | gap | - | - | S1-L-92 |
| S1-L-94 | A lane never sets a commit author | core | gap | - | - | S1-L-94 |
| S1-L-95 | Implementer and reviewer skills run in order, named skills through the skill tool | skill | gap | - | - | S1-L-95, S1-L-96, S1-L-106 |
| S1-L-101 | Commit and push follow the profile; edits are scripts off the host | file | gap | - | - | S1-L-101, S1-L-103, S1-L-105 |
| S1-L-102 | Frame security reviews of our validators as verification | skill | gap | - | - | S1-L-102 |
| S1-B-01 | One prompt file per agent | core | expressed | workflow.hcl agent implement | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-B-01, S1-B-02, S1-B-03, S1-B-35, S1-B-26 |
| S1-B-05 | Put everything in the brief; do not edit another item's brief | core | gap | - | - | S1-B-05, S1-B-06, S1-B-07, S1-B-36 |
| S1-B-08 | Splice the profile's brief rules; refuse a brief without preflight sections | core | gap | prompts/implement.md, the rules in the prompt | TestPrompts_HoldEveryRuleOfTheBriefText | S1-B-08, S1-B-09, S1-B-10, S1-B-11, S1-B-27, S1-B-33 |
| S1-B-12 | Refuse a brief above the reading budget; list tokens per file | core | gap | tools/remedy size | TestSize_AReadingAboveTheLimitIsARefusal | S1-B-12, S1-B-13 |
| S1-B-14 | An implementer brief needs acceptance and base result lines | core | gap | tools/remedy admit | TestAdmit_ALaneWithNoAcceptanceFieldIsRefused | S1-B-14, S1-B-15, S1-B-16, S1-B-17 |
| S1-B-18 | Ruling lines, fewer cells, skill choice, blast-radius floor in the brief | skill | gap | - | - | S1-B-18, S1-B-19, S1-B-20, S1-B-21, S1-B-29, S1-B-30, S1-B-31, S1-B-32 |
| S1-B-22 | A papers brief kind; fail when a papers commit touches source | core | gap | tools/remedy papers | TestPapers_ASourcePathIsExitCodeOne | S1-B-22 |
| S1-B-23 | Probe rules; no touching the laptop checkout; delete and shell bans | core | gap | - | - | S1-B-23, S1-B-24, S1-B-25 |
| S1-B-28 | Closing reference by base: Addresses on a round branch, Closes on the default | core | gap | - | - | S1-B-28 |
| S1-B-34 | Derive the dispatch gate from CI through the profile gate source | file | gap | - | - | S1-B-34 |
| S1-H-01 | Size the host by queue length; reuse a host; run locally without a cloud CLI | core | gap | - | - | S1-H-01, S1-H-02, S1-H-71 |
| S1-H-03 | Disk binds per lane; share a compile cache, not a target directory | file | gap | - | - | S1-H-03, S1-H-09 |
| S1-H-04 | Read the host verdict before any resize, freeze or launch | core | gap | - | - | S1-H-04 |
| S1-H-05 | A gate queue on the host with a cap, wait states and a grant | core | gap | - | - | S1-H-05, S1-H-06, S1-H-07, S1-H-08, S1-H-10, S1-H-13, S1-H-14, S1-H-15, S1-H-17, S1-H-19, S1-H-20, S1-H-21, S1-H-22, S1-H-23, S1-H-24, S1-H-25 |
| S1-H-11 | A disk floor makes a job wait; an unparseable reading is a refusal | core | gap | - | - | S1-H-11, S1-H-29, S1-H-30, S1-H-32, S1-H-33 |
| S1-H-12 | Job checks: delete the clone after the marker; every test step under a timeout. The test covers the timeout part only; no test covers the clone delete | core | gap | tools/remedy job | TestJob_ATestCommandWithNoTimeoutIsExitCodeOne | S1-H-12, S1-H-34, S1-H-60, S1-H-62, S1-H-63, S1-H-67 |
| S1-H-16 | Job scripts live under a jobs folder on the host, never in tmp | file | gap | - | - | S1-H-16 |
| S1-H-18 | Freeze and continue jobs by priority; stop a superseded job | core | gap | - | - | S1-H-18, S1-H-66 |
| S1-H-26 | Jobs outside the queue go through a slot; the reviewed item gets the host first | core | gap | - | - | S1-H-26, S1-H-27, S1-H-28, S1-H-64 |
| S1-H-31 | A lane deletes its target directory at once; prune docker volumes | core | gap | - | - | S1-H-31, S1-H-44 |
| S1-H-35 | Tear the host down when the round stops, in a fixed order, and audit it | core | gap | - | - | S1-H-35, S1-H-36, S1-H-37, S1-H-40 |
| S1-H-38 | An idle watchdog deletes host and disks; a max-run deadline | core | gap | - | - | S1-H-38, S1-H-41, S1-H-42 |
| S1-H-39 | Touch the heartbeat on the host every turn | core | gap | - | - | S1-H-39 |
| S1-H-43 | Browser rounds need the reaper installed and reported | core | gap | - | - | S1-H-43 |
| S1-H-45 | Public repository: never copy the laptop to the box; bucket rules | core | gap | - | - | S1-H-45, S1-H-46, S1-H-47, S1-H-48, S1-H-49 |
| S1-H-50 | Job wait states: done, died after 20 minutes stale, lost on ssh drop | core | gap | - | - | S1-H-50, S1-H-51, S1-H-52, S1-H-53, S1-H-54, S1-H-55, S1-H-56, S1-H-57, S1-H-58, S1-H-59, S1-H-61 |
| S1-H-65 | A handoff line says whether the code compiled and at which SHA | core | gap | - | - | S1-H-65 |
| S1-H-69 | Hook commits on the build host; never build on the laptop; local-only profiles | file | gap | - | - | S1-H-69, S1-H-70, S1-H-72, S1-H-73, S1-H-74 |
| SC-02 | Run a many-process probe under a timeout and a process cap | core | gap | - | - | - |
| S1-G-01 | One gate list per round; each job checked against it; a gate prints a result per step. The file holds only `gate "check" {}`, so the prompts state the rule until the core enforces it | core | gap | tools/remedy job | TestJob_AStepThatTheJobDoesNotRunIsExitCodeOne | S1-G-01, S1-G-02, S1-G-03, S1-G-07, S1-D-08 |
| S1-G-04 | A pinned tool at another version voids the step; a base red is not counted | core | gap | - | - | S1-G-04, S1-G-05 |
| S1-G-06 | The merge gate needs every step at the head that merges, one report row each | core | gap | tools/remedy report | TestReport_AWholeReportPassesWithTheSummary | S1-G-06, S1-R-26, S1-G-09, S1-G-43, S1-R-47, S1-R-46 |
| S1-G-08 | A mutation run is a separate job after the gate | core | gap | - | - | S1-G-08 |
| S1-G-10 | Capture the exit code before any pipe. The test covers the fixed-status marker only; no test covers a pipe before the capture | core | gap | tools/remedy job | TestExitMarker_RefusesAMarkerLineThatPrintsALiteralExitStatus | S1-G-10, S1-G-40 |
| S1-G-14 | A while-read loop must not feed stdin to commands | core | gap | tools/remedy job | - | S1-G-14 |
| S1-G-11 | Assert a non-zero test count; a missing result line is a failure | core | gap | tools/remedy job | - | S1-G-11, S1-G-13 |
| S1-G-12 | A completion marker is not the artifact; gate on the result line | core | gap | - | - | S1-G-12 |
| S1-G-15 | A task killed by our timeout is unresolved, not green or red | core | gap | - | - | S1-G-15 |
| S1-G-16 | Base-red entries name the CI command; exclusion lists expire | skill | gap | - | - | S1-G-16, S1-G-17, S1-G-19, S1-G-22, S1-G-29, S1-G-30, S1-G-32, S1-G-34, S1-G-41, S1-G-42, S1-G-47, S1-G-49 |
| S1-G-18 | After an aborting gate stops, every later step is unknown | core | gap | - | - | S1-G-18 |
| S1-G-20 | Derive the gate list from the CI files; screen steps that publish. The test covers the derived-from line only; no test covers the screen for steps that publish | core | gap | tools/remedy gatelist | TestGatelist_AListThatNamesNoSourceWorkflowIsExitCodeTwo | S1-G-20, S1-G-21, S1-S-20 |
| S1-G-23 | Every lane report holds claims, hunks, inputs and siblings tables | core | gap | tools/remedy report | TestReport_AReportWithNoBackgroundLineIsExitCodeOne | S1-G-23, S1-P-01, S1-P-02, S1-P-03, S1-R-28, S1-R-31, S1-R-32 |
| S1-G-24 | Review method: controls, mutation proof, every changed cell against the base | skill | gap | - | - | S1-G-24, S1-G-25, S1-G-28, S1-R-13, S1-R-14, S1-R-15, S1-R-30, S1-R-33, S1-R-34, S1-R-35, S1-R-36, S1-R-38, S1-R-40, S1-R-41, S1-R-42, S1-R-43, S1-R-44, S1-R-45, S1-R-48, S1-R-50, S1-R-55, S1-R-57, S1-R-58, S1-R-61, S1-S-25 |
| S1-G-26 | A secrets differential for every item, with a planted key as control | core | gap | - | - | S1-G-26, S1-G-46, S1-R-27, S1-M-09 |
| S1-G-27 | Unknown is not zero: a failed read never lets a request through | core | gap | - | - | S1-G-27, S1-X-17 |
| S1-G-31 | A regeneration is evidence for its own tree; compare against a fresh one | file | gap | - | - | S1-G-31, S1-G-50, S1-PS-12 |
| S1-G-33 | A disjointness claim carries the SHA it was measured at | skill | gap | - | - | S1-G-33 |
| S1-G-35 | Landed means merged or on the vehicle commit; compare semantics for ahead and behind | core | gap | - | - | S1-G-35, S1-G-36, S1-R-36 |
| S1-G-37 | Lists have limits; a listing is not a search | core | gap | - | - | S1-G-37, S1-G-38 |
| S1-G-39 | A diff-scoped gate escalates to a full run for shared foundations or an unexplained red | core | gap | - | - | S1-G-39, S1-R-11 |
| S1-G-44 | Each profile states its gate command and the commands never run here | file | gap | - | - | S1-G-44, S1-G-45, S1-PS-03, S1-PS-14 |
| S1-G-48 | Route a gate red the lane did not cause before a merge | core | gap | - | - | S1-G-48, S1-R-49 |
| S1-P-04 | A reviewer builds its own tables and does not fix, commit or push | core | gap | - | - | S1-P-04, S1-R-59 |
| S1-P-05 | Run the transcript check before accepting a report | core | gap | forbid_commands on workflow.hcl agent implement; the example shows the implement violations to the review, and the gate on violations is a gap until a loop can read the previous round | TestAgents_ABashCallOfAListedKindIsAViolationAndTheRunStillSucceeds | S1-P-05 |
| S1-P-06 | A handoff that launched a job needs the unread-job reason | core | gap | tools/remedy handoff | TestHandoff_ALaunchedJobThatNobodyReadIsExitCodeOneAndNamesTheCap | S1-P-06 |
| S1-P-07 | Report length limits per item and per batch | core | gap | - | - | S1-P-07, S1-P-08 |
| S1-P-09 | Dump issue and PR threads into files and name them in the brief | core | gap | - | - | S1-P-09 |
| S1-P-10 | Read the PR body before editing it; write suspicion down | drop | n/a | - | - | S1-P-10, S1-P-17, S1-P-18, S1-R-03 |
| S1-P-11 | A papers or design lane changes no source path | core | gap | tools/remedy papers | TestPapers_ASourcePathIsExitCodeOne | S1-P-11 |
| S1-P-12 | Write each measured deliverable to a file; report days since the last | core | gap | - | - | S1-P-12, S1-D-06 |
| S1-P-13 | Owner dashboard as a profile section | core | gap | - | - | S1-P-13, S1-D-14, S1-PS-13 |
| S1-P-14 | Never paste a lane report into GitHub; rewrite it | skill | gap | - | - | S1-P-14, S1-F-13 |
| S1-P-15 | A reproduction is self-contained, with no round paths or lane names | core | gap | - | - | S1-P-15, S1-F-03, S1-F-02 |
| S1-P-16 | Estimated tokens per file and in total | core | gap | tools/remedy size | TestSize_PrintsTheTokenTableAndPassesASmallReading | S1-P-16 |
| S1-R-01 | Review your own work: a fresh reviewer that did not write the code | core | expressed | workflow.hcl agent review | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-R-01, S1-R-02, S1-R-04, S1-R-62, S1-M-13 |
| S1-R-05 | Findings on our own PR go to the author lane | core | expressed | workflow.hcl agent fix | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-R-05, S1-R-06, S1-F-15 |
| S1-R-07 | Read every review and inline comment before ready and before merge | core | gap | - | - | S1-R-07, S1-M-04, S1-M-10, S1-M-11 |
| S1-R-08 | Review at a pinned SHA | core | expressed | workflow.hcl agent review | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-R-08, S1-M-07 |
| S1-R-09 | An implementer must call the review skill before it reports | skill | expressed | workflow.hcl agent implement | TestCheckWorkflow_ExitsZeroForTheRemediationExample | S1-R-09, S1-R-10, S1-R-12, S1-R-16 |
| S1-R-17 | A verdict blocks when the change caused the defect or lost data | skill | gap | - | - | S1-R-17, S1-R-19, S1-R-20, S1-R-21, S1-R-23, S1-R-25, S1-R-29, S1-R-37, S1-R-39, S1-R-56 |
| S1-R-18 | Verdict values accept, needs a commit, reject; clean or findings for merged work | core | gap | - | - | S1-R-18 |
| S1-R-22 | A reviewer is finished when idle, not when it sends a verdict | drop | n/a | - | - | S1-R-22 |
| S1-R-24 | Read the existing review first; label each finding new or already raised | core | gap | - | - | S1-R-24 |
| S1-R-51 | Review sweep: open PRs, latest review per reviewer, unresolved threads | core | gap | - | - | S1-R-51, S1-R-52, S1-M-27 |
| S1-R-53 | Hourly review poll over open PRs not by the user | core | gap | - | - | S1-R-53, S1-R-54, S1-W-09, S1-W-17 |
| S1-R-60 | A reviewer report ends with blocking, should-fix and non-blocking | core | gap | - | - | S1-R-60 |
| S1-R-63 | A derived verdict ledger: last review, SHA reviewed, still-head flag | core | gap | - | - | S1-R-63, S1-R-64, S1-R-65, S1-R-66, S1-D-05 |
| S1-R-67 | Modes: take-over, audit of merged work, queue mode | file | gap | - | - | S1-R-67, S1-R-68, S1-R-69, S1-O-06, S1-X-14 |
| S1-M-01 | Ready means done, reviewed, with no changes requested | core | gap | - | - | S1-M-01, S1-M-02, S1-M-03, S1-M-22 |
| S1-M-05 | A ready PR is watched until it merges; read its state before each action | core | gap | - | - | S1-M-05, S1-W-14, S1-X-22 |
| S1-M-06 | A merge gate of six conditions, including a verdict from another lane | core | gap | - | - | S1-M-06, S1-M-17 |
| S1-M-08 | No AI attribution in commits or PR body | core | gap | - | - | S1-M-08, S1-S-12, S1-O-05 |
| S1-M-12 | Never dismiss a maintainer's review to unblock a merge | core | gap | - | - | S1-M-12, S1-S-22 |
| S1-M-13 | Nobody merges their own work; our PRs merge after another lane accepts | core | gap | - | - | S1-M-13, S1-M-14, S1-X-13 |
| S1-M-15 | Merge the proven union in stacking order and gate once; bisect a red batch | core | gap | - | - | S1-M-15, S1-M-16 |
| S1-M-18 | Do not use the merge queue of GitHub | file | gap | - | - | S1-M-18 |
| S1-M-19 | After a merge, assert the head is an ancestor of the default branch | core | gap | - | - | S1-M-19 |
| S1-M-20 | After a merge, add the integration row and recount heading and title | core | gap | - | - | S1-M-20, S1-M-21, S1-I-02, S1-I-03, S1-I-04, S1-D-07, S1-F-17 |
| S1-M-23 | Open a draft PR at the first checkpoint; push every later commit | core | gap | - | - | S1-M-23, S1-M-24, S1-F-09 |
| S1-M-25 | A bundle is banked when it is off the box and verified | core | gap | - | - | S1-M-25, S1-X-20 |
| S1-M-26 | A reported SHA is not a reachable SHA; verify the branch | core | gap | - | - | S1-M-26 |
| S1-M-27 | A dependent PR goes into a native stack | core | gap | - | - | S1-M-27, S1-M-28, S1-M-29, S1-M-30 |
| S1-M-31 | Held items are not merged | core | gap | - | - | S1-M-31 |
| S1-M-32 | One integration branch per repository; every PR targets it | file | gap | - | - | S1-M-32, S1-I-01, S1-I-09, S1-D-16, S1-O-01 |
| S1-M-33 | A maintainer's approval makes a PR safe to merge | file | gap | - | - | S1-M-33, S1-X-16 |
| S1-M-34 | No no-verify, stash, config writes or force-push of a shared branch | core | gap | - | - | S1-M-34, S1-S-15, S1-S-21 |
| S1-M-35 | Do not edit a branch a merge depends on; file a surviving finding first | skill | gap | - | - | S1-M-35, S1-M-36, S1-M-38 |
| S1-M-37 | Our commits carry our identity and theirs keep theirs | core | gap | tools/remedy authors | TestAuthors_ACommitOfOursUnderAnotherAddressIsExitCodeOne | S1-M-37, S1-S-11, S1-L-94 |
| S1-I-05 | Close the resolved issue with evidence; file a surviving finding before merge | core | gap | tools/remedy ledger closes | TestLedger_TheFirstReviewRecordsTheIssuesOfTheBodyFile | S1-I-05, S1-I-11, S1-F-04, S1-F-06, S1-F-07 |
| S1-I-06 | Closing keywords only on the integration PR, except on the default branch | core | gap | tools/remedy ledger add | TestLedger_TheFirstReviewReadsTheBodyOfThePullRequestThroughTheForge | S1-I-06, S1-B-28 |
| S1-I-07 | Audit closing keywords at the tip; read closing references after each batch | core | gap | tools/remedy scope | TestScope_TheKeyFlagNamesTheLedgerKeyOfAnItemWhosePullRequestHasAnotherNumber | S1-I-07, S1-F-20 |
| S1-I-08 | At round end report remaining work, release locks and tear down | core | gap | - | - | S1-I-08 |
| S1-I-10 | A rulings file holds expected behavior, references, harness and base build | core | gap | - | - | S1-I-10, S1-D-11, S1-R-16 |
| S1-W-01 | A background watcher arms at round start and after each wake | drop | n/a | - | - | S1-W-01, S1-W-02, S1-W-03, S1-W-04, S1-W-05, S1-W-07, S1-W-08, S1-W-15, S1-W-16, S1-W-19, S1-D-09, S1-D-10 |
| S1-W-06 | A stop guard fails a turn when the watcher is dead | drop | n/a | - | - | S1-W-06, S1-W-10, S1-W-11, S1-W-12, S1-W-13, S1-W-18 |
| S1-W-20 | Spend instrumentation per lane with an hourly flag | core | gap | tools/remedy spend | TestSpend_TheHoursFlagKeepsTheLastHoursOfTheTable | S1-W-20, S1-D-13 |
| S1-D-01 | Queue, lane and gate records are edited through scripts | drop | n/a | - | - | S1-D-01, S1-D-02, S1-D-03, S1-D-15 |
| S1-D-04 | A lane ledger: one row per lane per PR with a kind | core | gap | tools/remedy ledger add | TestLedger_AddRecordsALaneAndCountReadsItBack | S1-D-04 |
| S1-D-12 | Handoff files at a fixed path, at most 300 words | core | gap | tools/remedy handoff | TestHandoff_AWholeHandoffPasses | S1-D-12 |
| S1-D-17 | One record of known base reds with a timestamp and the tip | file | gap | - | - | S1-D-17, S1-PS-12 |
| S1-X-01 | Take-over: review first, then notice with a token ask, then claim | core | gap | tools/remedy take | TestTake_ApplyPostsTheNoticeAndPasses | S1-X-01, S1-X-02, S1-X-08, S1-X-09, S1-X-10, S1-X-11, S1-X-12, S1-F-16, S1-R-67 |
| S1-X-03 | Stale means an hour of no author activity and an hour of no queue activity | file | gap | tools/remedy take | TestTake_IdleAndGraceFlagsAreHours | S1-X-03 |
| S1-X-04 | A take after the stale hour gets one line on the PR first | core | gap | tools/remedy take | TestTake_RefusalOfARuleIsExitCodeTwoWithTheLinesOnTheOutput | S1-X-04 |
| S1-X-05 | Every take co-assigns the PR and its linked issues | core | gap | - | - | S1-X-05 |
| S1-X-06 | When the author hands over a branch with terms, the terms win | skill | gap | - | - | S1-X-06, S1-X-07, S1-M-38 |
| S1-X-15 | Check the forge head against the worktree head before each dispatch | core | gap | - | - | S1-X-15 |
| S1-X-18 | After an interrupted action, ask the provider what exists | core | gap | - | - | S1-X-18, S1-X-23, S1-X-21 |
| S1-X-19 | Resume a lane after a stop; refuse an over-cap resume | core | gap | - | - | S1-X-19 |
| S1-S-01 | No delete from shell variables; no bulk staging; no remote delete | core | proven | forbid_commands on workflow.hcl agent implement; the example shows the implement violations to the review, and the gate on violations is a gap until a loop can read the previous round | TestScripts | S1-S-01, S1-S-02, S1-S-03, S1-S-05, S1-S-06, S1-S-07, S1-S-16 |
| S1-S-04 | Never pass a script string to a shell | core | gap | forbid_commands on workflow.hcl agent implement; the example shows the implement violations to the review, and the gate on violations is a gap until a loop can read the previous round | TestAgents_ABashCallOfAListedKindIsAViolationAndTheRunStillSucceeds | S1-S-04 |
| S1-S-08 | A call-time hook refuses bulk staging, deletes and unparseable quoting | core | gap | forbid_commands on workflow.hcl agent implement; the example shows the implement violations to the review, and the gate on violations is a gap until a loop can read the previous round | TestAgents_ABashCallOfAListedKindIsAViolationAndTheRunStillSucceeds | S1-S-08 |
| S1-S-09 | No startup-hook files or search-path overrides in a lane | core | gap | - | - | S1-S-09 |
| S1-S-10 | A many-process probe runs under a process cap | core | gap | - | - | S1-S-10, S1-S-23 |
| S1-S-13 | Tell the lane that its config's attribution instruction does not apply | skill | gap | - | - | S1-S-13 |
| S1-S-14 | Never print a config file with a key; read named keys only | core | gap | - | - | S1-S-14 |
| S1-S-17 | The laptop is not a build host; public data rules | core | gap | - | - | S1-S-17, S1-S-18, S1-S-24 |
| S1-S-19 | Never edit a PR description you did not write | core | gap | - | - | S1-S-19 |
| S1-F-01 | An issue follows the repository template and adds how it was found | core | gap | - | - | S1-F-01, S1-F-12 |
| S1-F-05 | Severity is operator-first with exactly one P label | core | gap | - | - | S1-F-05 |
| S1-F-08 | PR and commit text: problem, fix, result; no issue IDs; no attribution | core | gap | - | - | S1-F-08, S1-F-10, S1-F-11, S1-F-14, S1-S-12, S1-O-05, S1-O-10, S1-PS-10 |
| S1-F-18 | A changelog entry for a change callers can see | core | gap | - | - | S1-F-18 |
| S1-F-19 | Check TODO and FIXME attribution before each push | core | gap | - | - | S1-F-19 |
| S1-O-02 | A profile per repository, read at session start, written from CI if absent | file | gap | - | - | S1-O-02, S1-O-03, S1-O-04, S1-O-08, S1-O-09 |
| S1-O-07 | Sweep docker volumes as a backstop | core | gap | - | - | S1-O-07 |
| S1-PS-01 | Profile: repository identity and fork topology | file | gap | - | - | S1-PS-01 |
| S1-PS-02 | Profile: build host, sync method, worktree place, laptop lock | file | gap | - | - | S1-PS-02 |
| S1-PS-04 | Profile: gate source, the workflow files the gate list comes from | file | gap | - | - | S1-PS-04 |
| S1-PS-05 | Profile: brief-rules file and its sections | file | gap | - | - | S1-PS-05 |
| S1-PS-06 | Profile: base branch and integration topology | file | gap | - | - | S1-PS-06 |
| S1-PS-07 | Profile: stacked-PR rules | file | gap | - | - | S1-PS-07 |
| S1-PS-08 | Profile: authorship, people and the third-party idle threshold | file | gap | - | - | S1-PS-08 |
| S1-PS-09 | Profile: issue tracker and queue mapping to epics | file | gap | - | - | S1-PS-09 |
| S1-PS-11 | Profile: convention surface to review against | file | gap | - | - | S1-PS-11 |
| S1-PS-13 | Profile: token discipline, dashboard, cross-repo and release steps | file | gap | - | - | S1-PS-13 |
| S1-PS-14 | Profile: hard limits, commands never run, real user data | file | gap | - | - | S1-PS-14 |
| S1-PS-15 | Profile: measured traps and ownership rules | file | gap | - | - | S1-PS-15 |
| SC-03 | Cut the next round only after the previous integration PR merged | core | gap | tools/remedy cut | TestCut_MergedPullRequestMovesTheRoundAndPasses | - |
| SC-04 | For each merge in a range, show which lines the tip still has | core | gap | tools/remedy audit | TestAudit_PresentWithAMergeThatLeftTheBranchIsADefect | - |
| SC-05 | Green baseline and mutation arms with a hardcoded path | drop | n/a | - | - | - |
| SC-06 | Merge helpers: all checks green, our commits carry our email, orphan list | core | gap | - | - | - |
| SC-07 | Pinned asynchronous merge for a stacked PR with polling | core | gap | - | - | - |
| SC-08 | Stop in fixed order: merged, closed, branch gone, hold, draft, head moved | core | gap | tools/remedy cut | TestCut_PullRequestThatIsNotMergedIsARefusal | - |
| SC-09 | Add a closing row, recount heading and title, assert the count changed | core | gap | - | - | - |
| SC-10 | Sync a project board from issues and PRs with a read-back | core | gap | - | - | - |
| SC-11 | A tmux view of lanes, forge, logs and host | drop | n/a | - | - | - |
| SC-12 | Read-only proof that the machinery works; a dry run of a round | core | gap | - | - | - |
