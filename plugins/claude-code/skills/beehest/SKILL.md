---
name: beehest
description: Use when the user asks to queue a task, start or resume a run, check run status, answer a question that a run waits on, or cancel a run with beehest. Every action is an beehest command.
---

# beehest

beehest takes an item through a workflow and keeps the state of each run. This plugin holds no method of its own. Each action is one beehest command that you run and report.

## Rules

- Run the beehest command. Do not do the work of the command by hand, and do not edit the state of beehest.
- Show the exact command before you run it. Report its output and its exit code as they are.
- If the command fails, show the message of beehest. Do not retry with other flags unless the message says what to change.
- If `beehest` is not found, tell the user to install it and stop.
- If an argument is missing, ask the user for it. Do not guess an item id, a project or an answer.

## Actions

| The user wants to | Command | Slash command |
| --- | --- | --- |
| Add a task to the backlog | `beehest item add <project> <id> --title '<title>' --body-file <file>` | `/beehest:queue` |
| Start a run, or continue an open run | `beehest run <item> [project]` | `/beehest:run` |
| See the runs and the questions that wait | `beehest status` | `/beehest:status` |
| Answer the question of a paused item | `beehest answer <item> '<text>'` | `/beehest:answer` |
| Stop the open run of an item | `beehest cancel <item>` | `/beehest:cancel` |

If the last run of an item failed, `beehest run` refuses and names the step. Run `beehest resume <item>`, then `beehest run <item>`.

If the last run was cancelled, `beehest run` does not refuse: it starts a new run that repeats the finished steps. Ask the user to choose: resume the old run (`beehest resume <item>`, then `beehest run <item>`) or start a new run.

`beehest run` needs a workflow file: `workflow.star` in the current folder, or the file that `--workflow` names. It needs a project when the config holds more than one. If beehest names one as missing, ask the user for it.

If beehest says another `beehest run` owns the state folder, do not run the command again. Run `beehest status` and wait.

Pass the free text of an answer and the title of an item as one single-quoted argument, and write each `'` as `'\''`. Pass item ids and flags as they are.

After `beehest answer`, `beehest status` still shows the question as paused until the run continues. Do not answer twice.

## Notes

- An item id is a name such as `fix-login`, or a number such as `12`.
- `beehest run` holds the terminal until the run ends or pauses. If the run pauses with a question, run `beehest status`, ask the user, then run `beehest answer`.
- Every command takes `--config <folder>` to name the config folder. Add it only if the user names one.
