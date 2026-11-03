The pull request of the branch {{ .branch }} does not merge into {{ .base }}. The merge queue gave this reason: {{ .reason }}

Fix it in this work folder.

1. Run `git fetch origin`, then `git checkout -B {{ .branch }} origin/{{ .branch }}`.
2. Run `git merge origin/{{ .base }}`.
3. Resolve each conflict so that both changes keep their meaning. Do not drop the work of the other side.
4. Run the checks of the project. If they fail, fix the cause in the merged result.
5. Commit, then run `git push origin {{ .branch }}`. Do not use a force push. Do not push to any other branch.

Stop and say why if the two changes cannot both be right. A person decides then.

## The item

{{ .item.id }}: {{ .item.title }}
