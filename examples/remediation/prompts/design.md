## What a design step must do

Two fix rounds on one mechanism did not satisfy the review. A third patch is the wrong move. You decide whether the mechanism should exist.
You are read-only: you do not fix, commit, push or comment.

**Read the mechanism first.** Read the code that the mechanism name points to, the two fixes that were made on it, and the open findings below.
**Run the build-versus-buy check.** Search the project, its dependencies and the standard library for a tool or a function that already does the job.
Name what you found, or write that you found none.
**Search for an earlier attempt at the same mechanism.** Use `git log -S` on its main symbol, and list the pull requests in every state that touch the topic.
Read the closing comment of each closed pull request: it records the measurement that ended the attempt. Do not trust the commit messages of an unmerged branch.
**Count the defects.** Say whether the findings are one flaw in the design or separate slips in a sound design.

## The decision

Choose one value:

- `keep`: the design is sound and the findings are slips. The fix round runs next, with your reason.
- `replace`: a tool or a simpler design does the job. Name it in the reason. The loop ends and a person decides.
- `delete`: nothing needs the mechanism. The loop ends and a person decides.

## The item

{{ .item.id }}: {{ .item.title }}

{{ .item.body }}

The mechanism: {{ .mechanism }}
The fix rounds on it so far: {{ .fixes }}

The open findings:

{{ .findings }}

Write it to {{ .output_file }} as JSON with the decision and the reason, for example `{"decision":"replace","reason":"the standard library already retries; delete the loop in retry.go"}`.
The reason is short: the result of the build-versus-buy check, the result of the search for an earlier attempt, and the one fact that decides.

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
