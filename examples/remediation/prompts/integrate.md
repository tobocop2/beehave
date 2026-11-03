## What the integration step must do

The pull request {{ .pr }} for the item {{ .item.id }} merged into the base of the round. The integration pull request is {{ .integration }}. Its body is the record of the round. Do the steps below in this order, now, before any other item starts. You change no source file.

**Add the integration row.** Add one row for the merged pull request to the table of merged pull requests in the body of the integration pull request. Use the column layout that the table already has. Do not add a row twice.
**Recount the heading and the title from the rows.** Count the rows of the table. Write that number in the heading of the section. When the title has the form `Remediation round <name>: <n> reviewed pull requests, <m> issues`, write the true counts there too. Never hand-edit a count: the count is the number of rows.
**Add a closing row for each issue that the merge resolves.** The issues are: {{ .closes }}. Add one `Closes #N` line to the closes list of the body for each of them, and recount the issue count of the title. An empty list means no closing row.
**Close each resolved issue with the evidence.** Comment on the issue with the pull request, the merged head and the line of the proof that shows the fix, then close it. Never close an issue with no such comment.
**Closing keywords belong to the integration pull request only.** Every other pull request of the round says `Addresses #N`. A closing keyword fires only on a merge into the default branch, so until then the body of the integration pull request is the record of what the round closes. Never edit the body of a pull request that is not yours.
**A finding that survives is filed before the merge.** If the review left a finding that the pull request does not fix, file an issue for it now.
**Never use the merge queue of GitHub.** Do not add a pull request to a queue and do not run `gh pr merge --auto`.

The step after you reads the integration pull request again. It fails when the heading or the title says another number than the rows, or when the row count did not change. Write the changes to the pull request itself, read it back, and say in your report what you wrote.
