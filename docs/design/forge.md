# Forge abilities

A forge is the service that holds the repository and its pull requests. Each backend is a child package of `internal/forge`. The core names no backend.

## Required and optional

The `Forge` interface holds the calls that every backend must make. A backend adds an optional ability by implementing one more interface. The engine finds an ability by type assertion on the forge. There is no registry to keep in step.

`forge.Has` tells whether a forge holds one ability. `forge.Need` checks a list of abilities and returns one error that names each missing ability. The error matches `forge.ErrNoCapability`. A backend can also implement `forge.Prober`, which makes a call to confirm that an ability works for the repository, for example when the host has switched code scanning off.

## Abilities

| Ability | Interface | If the forge lacks it |
|---|---|---|
| commit statuses | `Statuses` | The start refuses a project with a writer. The review verdict and the protection read need statuses. |
| opening pull requests | `Opener` | The step `pr_open` refuses with `ErrNoCapability`. |
| native stacks | `Stacker` | `pr_open` opens the pull request on the branch of its parent and makes no stack. |
| syncing a branch | `Syncer` | The round steps `round_sync` and the land step refuse. |
| issue assignees | `AssigneeReads` | The assignee watch of the preflight refuses. |
| the association of an author | `AssociationReads` | The engine cannot tell an item of the owner from a foreign one, and refuses. |
| ready marks | `ReadyReads` | The review poll refuses. |
| project boards | `Boarder` | The step `board_card` refuses. |
| queued merges | `Queuer` | A pull request in a stack keeps the old refusal. |
| commit ancestry | `Ancestry` | The proofs of a merge that need it are not run. |
| commit trees | `Trees` | The tree proofs of a merge are not run. |
| check logs | `Logs` | A failed check shows no log. |
| security alerts | `AlertReads` | The start refuses a project with `security_alerts = True`. |

An optional step refuses when it is used. The start checks only the abilities that a project needs: statuses for a project with a writer, and security alerts for a project with `security_alerts`.

## Security alerts

A project with `security_alerts = True` makes each merge read the open alerts of the pull request at the gated head. One open alert refuses the merge before any write. A failed read also refuses the merge. The refusal text names each alert.

On GitHub the read is code scanning: `GET repos/{repo}/code-scanning/alerts?state=open&pr={n}`, filtered to the alerts of the gated head. A 403 or 404 means code scanning is off or the token lacks the scope, and the start refuses the project.

An empty answer means no open alert at the head. It does not tell a clean scan from a head that has no analysis yet. To tell them apart, the read must list `GET repos/{repo}/code-scanning/analyses?sha={sha}`, which is one more call.

## Mapping

| Ability | GitHub | GitLab | Forgejo and Gitea |
|---|---|---|---|
| commit statuses | statuses API | external status checks | statuses API |
| native stacks | `gh stack` | not available | not available |
| security alerts | code scanning | vulnerability findings of the merge request | not available. `security_alerts` refuses the start. |

Only the GitHub backend exists. The other columns record the intended mapping.

## CI policy of a forge

The text of a merge names no forge. The backend registers the paths of its CI files and the command that gives a token the scope to change them. The merge request carries both values, so the refusal for a token that cannot change CI names the right file and the right fix for the forge of the project.
