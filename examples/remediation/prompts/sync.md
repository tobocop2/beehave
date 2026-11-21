## What the sync step must do

The round merges the trunk into the round branch `{{ .base }}` through the sync branch `{{ .branch }}`. The merge of `{{ .source }}` into the sync branch conflicts, so the forge wrote nothing. You resolve that one merge.

1. In your folder, run `git fetch origin {{ .branch }} {{ .source }}`.
2. Run `git checkout --detach origin/{{ .branch }}`.
3. Run `git merge --no-ff origin/{{ .source }}`.
4. Resolve each conflict so that both sides keep their intent. Keep every test of both sides. Commit the merge commit with `git commit --no-edit`.

Do not push: the engine pushes the merge commit to `{{ .branch }}` after this step. Make one merge commit with the two parents (the sync branch tip, then `origin/{{ .source }}`). Do not rebase, amend or squash. Do not change a line that the conflict does not need. Do not open a pull request: the round opens it after the push.

If you cannot resolve a conflict, commit nothing, and say in your report which files conflict and why. The round then waits for the owner with the reason `sync-conflict`.

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
