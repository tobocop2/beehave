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
