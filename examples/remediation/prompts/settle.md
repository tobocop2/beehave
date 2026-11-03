The review loop used all of its rounds, and the owner ruled on each open finding.

The owner's ruling:

{{ .ruling }}

For each finding that the owner says to fix, fix it in the item worktree and commit. For each finding that the owner says to park, write down the cost and do not change the code. Do not start a new review.

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
