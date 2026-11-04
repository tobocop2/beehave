# Weekly dependency upkeep

Once a week, for each repository in a list, this example updates the dependencies, runs the tests, and opens a draft pull request. An agent fixes the code only when the tests fail after the bump. A person reviews the pull request. Nothing merges by itself.

## Parts

- `repos.tsv` lists the repositories: name, `owner/repo`, base branch and gate command. Fields are split by one tab.
- `scripts/tick.sh` is the entry point. The scheduler starts it once a week. For each repository it syncs a clone, decides, and runs `bump.star` only when there is news. Then it runs `report.star`.
- `bump.star` is the workflow of one repository: update, gate, fix rounds, a wait for a person when the tests stay red, then the draft pull request.
- `report.star` writes the weekly report from the run log and posts it once for each week.
- `launchd/com.upkeep.weekly.plist` and a crontab line start the tick. Change `CHANGE_ME` to your user name.
- `config/projects/upkeep.star` is the project of the report item. `scripts/mkprojects.sh` writes one project file for each row of `repos.tsv`. It keeps a file that a person wrote.

## Setup

1. Copy this folder to `~/.upkeep`, or set `UPKEEP_HOME` to the folder.
2. Edit `repos.tsv`.
3. Run `sh ~/.upkeep/scripts/mkprojects.sh <config folder>`.
4. Optional: set `STATUS_REPO` and `STATUS_ISSUE` in `~/.upkeep/settings.sh`. The engine gives a step no other environment. The report posts as a comment on that issue.
5. Load the plist, or add the crontab line from its first comment.

`RED_TIMEOUT` (default `72h`) sets how long a red run waits for an answer. Set it only to shorten a trial.

## The gate needs a test report

The project gate counts tests from a JUnit file (`test_report = "reports/*.xml"`). A plain `go test ./...` writes none, so the gate would call every bump red. The gate command of each row in `repos.tsv` therefore writes the file: `gotestsum --junitfile reports/junit.xml -- ./...` for Go, and `jest-junit` for npm. Each repository must list `reports/` in its `.gitignore`: an untracked report makes the tree unclean, and the gate refuses a tree that changed while it ran.

## Rules that skip a repository

A skip costs no worktree, no agent and no run. The tick writes one row to `runs.tsv` with the reason.

- Nothing is outdated.
- The updates have the same fingerprint as the last bump, so no new version came out.
- A bump pull request of the repository is still open.
- The repository already ran in this week.

## When the tests stay red

The gate runs after the update. If it fails, a fix agent runs for at most two rounds. If the tests are still red, the run waits with `wait(choices = ["retry", "hold"])`. Answer `retry` for two more fix rounds, or `hold`. With no answer before `RED_TIMEOUT`, the item holds. Nothing is pushed while the tests fail. A restart of beehave keeps the question and the deadline.

## Tests

`cmd/beehave/testdata/e2e/maint-*.txt` run the real scripts and the real workflows with stand-ins for `go`, `npm`, `gh` and the agent. Git runs for real, on local bare remotes.

- `maint-open`: a tick over three repositories, two pull requests, one skip, the report.
- `maint-skips`: the three skip rules over four weeks.
- `maint-red`: red tests, the answer `hold`.
- `maint-red-restart`: a kill during the wait, then the answer `retry`.
- `maint-sync-fails`: a failed sync, a repository with no row, one post for each week.

## Gaps in the engine

The example works around these. Each one is a reason to change the engine or to keep a script.

- beehave has no schedule. A job outside starts the week, and a missed week leaves no trace in beehave. The report lists a repository with no row as "No record".
- No step makes the worktree without an agent. A deterministic update followed by a gate needs a small agent step first, because the engine makes the worktree at the first agent step.
- This example runs the repositories one after the other. `tick.sh` loops over `repos.tsv` and starts one `beehave run` for each repository with news. The engine does not force this: a repository is a project, and one tick can start the runs side by side.
- The update step is an agent that runs one script and reports its exit code. If the agent writes no verdict, the tick logs the repository as `error`.
- After a crash or a power cut, `tick.sh` can leave its lock folder behind, and every later tick exits 75. Remove the folder that `tick.sh` names on stderr.
- A step cannot read the last run or the comments of its item. The scripts keep the state in `state/<repo>.env`.
- Finished runs are deleted after seven days. The report reads its own log, `runs.tsv`.
- A full usage cap blocks a run that needs no agent. The tick logs the exit code 3 as `deferred`.
- beehave has no cost cap for a week, and no forbid kind that refuses a push but allows a commit.
- The scripts are outside the pinned workflow. A script change in the middle of a run reaches a replay of that run.
