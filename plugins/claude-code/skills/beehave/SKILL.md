---
name: beehave
description: Use when the user asks to queue a task, start or resume a run, check run status, answer a question that a run waits on, or cancel a run with beehave. Every action is an beehave command.
---

# beehave

beehave takes an item through a workflow and keeps the state of each run. This plugin holds no method of its own. Each action is one beehave command that you run and report.

## Rules

- Run the beehave command. Do not do the work of the command by hand, and do not edit the state of beehave.
- Show the exact command before you run it. Report its output and its exit code as they are.
- If the command fails, show the message of beehave. Do not retry with other flags unless the message says what to change.
- If `beehave` is not found, tell the user to install it and stop.
- If an argument is missing, ask the user for it. Do not guess an item id, a project or an answer.

## Actions

| The user wants to | Command | Slash command |
| --- | --- | --- |
| Add a task to the backlog | `beehave item add <project> <id> --title '<title>' --body-file <file>` | `/beehave:queue` |
| Start a run, or continue an open run | `beehave run <item> [project]` | `/beehave:run` |
| See the runs and the questions that wait | `beehave status` | `/beehave:status` |
| Answer the question of a paused item | `beehave answer <item> '<text>'` | `/beehave:answer` |
| Stop the open run of an item | `beehave cancel <item>` | `/beehave:cancel` |

If the last run of an item failed, `beehave run` refuses and names the step. Run `beehave resume <item>`, then `beehave run <item>`.

If the last run was cancelled, `beehave run` does not refuse: it starts a new run that repeats the finished steps. Ask the user to choose: resume the old run (`beehave resume <item>`, then `beehave run <item>`) or start a new run.

`beehave run` needs a workflow file: `workflow.star` in the current folder, or the file that `--workflow` names. It needs a project when the config holds more than one. If beehave names one as missing, ask the user for it.

If beehave says another `beehave run` owns the state folder, do not run the command again. Run `beehave status` and wait.

Pass the free text of an answer and the title of an item as one single-quoted argument, and write each `'` as `'\''`. Pass item ids and flags as they are.

After `beehave answer`, `beehave status` still shows the question as paused until the run continues. Do not answer twice.

## Notes

- An item id is a name such as `fix-login`, or a number such as `12`.
- `beehave run` holds the terminal until the run ends or pauses. If the run pauses with a question, run `beehave status`, ask the user, then run `beehave answer`.
- Every command takes `--config <folder>` to name the config folder. Add it only if the user names one.
