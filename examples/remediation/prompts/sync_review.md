## What the review of a sync must do

The sync pull request {{ .pr }} merges the trunk into the round branch `{{ .base }}`. Its head is `{{ .head }}` on the branch `{{ .branch }}`, and your folder is at that head. The gate command passed on it. You review it with one sweep. You do not fix, commit or push.

The trunk commits are already reviewed. Review what the merge adds on top of them:

- Each conflict resolution of the merge commit: both sides keep their intent, and no test of either side is lost.
- A line of the round branch that the merge changes or drops with no conflict that needs it.
- A file that holds a conflict marker.

Approve when you find none of these. Write `changes` with the findings in your reply text when you find one. The round then waits for the owner with the reason `sync-review`, and nothing lands.

Write it to {{ .output_file }} as JSON, for example `{"verdict":"approve"}`. The verdict is `approve` or `changes`.

## Facts of the project

The run read these facts from the project file at its start. A fact that is empty holds no fact: ask the owner, and do not guess it.

- Repository identity and fork topology: repo `{{ .facts.repo }}`, source `{{ .facts.source }}`, forge `{{ .facts.forge }}`.
- Base branch: `{{ .facts.base_branch }}`.
- The gate is `{{ .facts.gate }}`. The gate list comes from these workflow files:{{ range index .facts.vars "gate_files" }} `{{ . }}`{{ end }}
- Tracker: `{{ .facts.tracker }}`. Queue to epic:{{ range $queue, $epic := index .facts.vars "epics" }} `{{ $queue }}` is `{{ $epic }}`;{{ end }}
- People and the idle threshold for a third-party pull request:{{ range $role, $who := index .facts.vars "people" }} `{{ $role }}` is `{{ $who }}`;{{ end }}
- Review against these convention files:{{ range index .facts.vars "conventions" }} `{{ . }}`{{ end }}

Brief rules of the project:
{{ range index .facts.vars "brief_rules" }}
- {{ . }}
{{ end }}
Rules for a pull request that stacks on another:
{{ range index .facts.vars "stacked_rules" }}
- {{ . }}
{{ end }}
Token rules, cross-repository steps and release steps:
{{ range index .facts.vars "token_rules" }}
- {{ . }}
{{ end }}
Hard limits, commands never run and real user data:
{{ range index .facts.vars "limits" }}
- {{ . }}
{{ end }}
Measured traps and ownership rules:
{{ range index .facts.vars "traps" }}
- {{ . }}
{{ end }}
