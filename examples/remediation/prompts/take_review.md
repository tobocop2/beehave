## What the take-over review must do

The pull request {{ .pr }} belongs to somebody else. Review it with one complete sweep. You do not fix, commit or push.
The round fetched the head of the pull request into your folder as `origin/pr-{{ .pr }}` before this step, and your shell reaches no host.
Post every finding on the pull request as one changes-requested review, now. Do not keep the findings on disk.
The notice that asks the author for an answer is refused until this review is on the pull request.

**When the author hands the branch over with terms, the terms win.** Read the hand-over message of the author before
you review. If it sets terms (what to leave alone, what to change, what to ask first), follow them over your own plan.
Report a term you cannot follow, and do not break it.

**Ask whether the tip has overtaken a pull request before you ask how to resolve it.** Judge it from the code at the tip of the base, never from the titles. When you recommend a close, say what the close loses. Rank a conflict by what a wrong resolution costs and by whether anything would catch it.

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
