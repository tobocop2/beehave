---
name: obee
description: Use when the user asks to queue a task, start or resume a run, check run status, answer a question that a run waits on, or cancel a run with obee. Every action is an obee command.
---

# obee

obee takes an item through a workflow and keeps the state of each run. This plugin holds no method of its own. Each action is one obee command that you run and report.

## Rules

- Run the obee command. Do not do the work of the command by hand, and do not edit the state of obee.
- Show the exact command before you run it. Report its output and its exit code as they are.
- If the command fails, show the message of obee. Do not retry with other flags unless the message says what to change.
- If `obee` is not found, tell the user to install it and stop.
- If an argument is missing, ask the user for it. Do not guess an item id, a project or an answer.

## Actions

| The user wants to | Command | Slash command |
| --- | --- | --- |
| Add a task to the backlog | `obee item add <project> <id> --title '<title>' --body-file <file>` | `/obee:queue` |
| Start a run, or continue an open run | `obee run <item> [project]` | `/obee:run` |
| See the runs and the questions that wait | `obee status` | `/obee:status` |
| Answer the question of a paused item | `obee answer <item> '<text>'` | `/obee:answer` |
| Stop the open run of an item | `obee cancel <item>` | `/obee:cancel` |

If the last run of an item failed, `obee run` refuses and names the step. Run `obee resume <item>`, then `obee run <item>`.

If the last run was cancelled, `obee run` does not refuse: it starts a new run that repeats the finished steps. Ask the user to choose: resume the old run (`obee resume <item>`, then `obee run <item>`) or start a new run.

`obee run` needs a workflow file: `workflow.star` in the current folder, or the file that `--workflow` names. It needs a project when the config holds more than one. If obee names one as missing, ask the user for it.

If obee says another `obee run` owns the state folder, do not run the command again. Run `obee status` and wait.

Pass the free text of an answer and the title of an item as one single-quoted argument, and write each `'` as `'\''`. Pass item ids and flags as they are.

After `obee answer`, `obee status` still shows the question as paused until the run continues. Do not answer twice.

## Notes

- An item id is a name such as `fix-login`, or a number such as `12`.
- `obee run` holds the terminal until the run ends or pauses. If the run pauses with a question, run `obee status`, ask the user, then run `obee answer`.
- Every command takes `--config <folder>` to name the config folder. Add it only if the user names one.
