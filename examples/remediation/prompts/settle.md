The review loop used all of its rounds, and the owner ruled on each open finding.

The owner's ruling:

{{ .ruling }}

For each finding that the owner says to fix, fix it in the item worktree and commit. For each finding that the owner says to park, write down the cost and do not change the code. Do not start a new review.

## Filing the parked findings

Each finding that stays open after your work is filed as its own issue by the workflow. You write the issue text; you do not file it. A finding that you fixed is not filed.

Write the output `parked` as a text that holds a JSON list, for example `{"parked":"[]"}` when no finding stays open. Each entry of the list is an object with these fields:

- `id`: words in lower case joined by hyphens that name the finding. The same finding always gets the same id. The workflow files one issue for each id and never two.
- `title`: a short title that names the problem.
- `body`: the issue text in the issue template of the repository. Read the template among the convention files of the facts below. Write the problem, the fix you would make, and the result you expect. Write no ID of a tracker item, no lane name, no round path and no attribution.
- `found`: how the finding was found. Name the commit and the exact command or input, and the output verbatim. A stranger must repeat it from the issue alone.
- `severity`: exactly one of `P0`, `P1`, `P2` or `P3`. Severity is operator-first: an issue that an operator hits in normal use is never below `P2`, and any other issue is never above `P2`.
- `operator`: `true` when an operator hits the finding in normal use, otherwise `false`.

Do not assign the issue to anyone. An issue with no assignee stays in the queue.

Write it to {{ .output_file }} as JSON.

## Facts of the project

The run read these facts from the project file at its start. A fact that is empty holds no fact: ask the owner, and do not guess it.

- Repository identity and fork topology: repo `{{ .facts.repo }}`, source `{{ .facts.source }}`, forge `{{ .facts.forge }}`.
- Build host: `{{ .facts.build_host }}`.
- Base branch: `{{ .facts.base_branch }}`.
- The gate is `{{ .facts.gate }}`. The gate list comes from these workflow files:{{ range .facts.gate_files }} `{{ . }}`{{ end }}
- Tracker: `{{ .facts.tracker }}`. Queue to epic:{{ range $queue, $epic := .facts.epics }} `{{ $queue }}` is `{{ $epic }}`;{{ end }}
- People and the idle threshold for a third-party pull request:{{ range $role, $who := .facts.people }} `{{ $role }}` is `{{ $who }}`;{{ end }}
- Review against these convention files:{{ range .facts.conventions }} `{{ . }}`{{ end }}

Brief rules of the project:
{{ range .facts.brief_rules }}
- {{ . }}
{{ end }}
Rules for a pull request that stacks on another:
{{ range .facts.stacked_rules }}
- {{ . }}
{{ end }}
Token rules, cross-repository steps and release steps:
{{ range .facts.token_rules }}
- {{ . }}
{{ end }}
Hard limits, commands never run and real user data:
{{ range .facts.limits }}
- {{ . }}
{{ end }}
Measured traps and ownership rules:
{{ range .facts.traps }}
- {{ . }}
{{ end }}
