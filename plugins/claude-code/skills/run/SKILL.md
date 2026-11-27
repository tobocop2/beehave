---
description: Start an beehest run for an item, or continue its open run
argument-hint: <item> [project] [--workflow <file>]
allowed-tools: Bash(beehest run:*), Bash(beehest resume:*), Bash(beehest fork:*)
disable-model-invocation: true
---

Take one item through its workflow with beehest.

The arguments are: $ARGUMENTS

1. If the arguments hold no item id, ask the user for it and stop. Do not guess.
2. `beehest run` needs a workflow file: `workflow.star` in the current folder, or the file that `--workflow` names. It needs a project when the config holds more than one. If beehest names the workflow file or the project as missing, ask the user for it. Do not guess.
3. Show the command, then run it with the arguments:

```sh
beehest run $ARGUMENTS
```

4. A run can last longer than the time limit of the Bash tool. Set the longest limit the tool allows. If the tool stops the command, the run stays open: run `/beehest:status`, then run the same command again to continue it.
5. If beehest says another `beehest run` owns the state folder, the first command still lives. Do not run the command again. Run `/beehest:status` and wait for the run to end or to pause.
6. Report the output and the exit code of beehest.
7. If beehest says the last run failed, tell the user which step it names. If the last run was cancelled, `beehest run` does not refuse: it starts a new run that repeats the finished steps. Ask the user to choose: resume the old run, or start a new run. To resume, run these two commands in order:

```sh
beehest resume <item>
beehest run <item>
```

To start again from a later step, or with other vars, fork the run. The fork keeps the finished steps before the step it names:

```sh
beehest fork <run id> --from <step> [--vars <file>]
beehest run <item>
```

If the resume refuses the run, tell the user to run `/beehest:cancel <item>`, then `beehest run <item>`: the cancel ends the failed run, and the run after it starts a new attempt.

If the user wants a new run in place of the open one, run `beehest run <item> --on-open replace`. It cancels the open run with its children and starts the next attempt. It does not replace a failed run.

If the user starts the same item many times in a short time, run `beehest run <item> --debounce 30s`. The start waits for the window, a later start in the window takes its place, and one run starts with the last start.

8. If the run pauses with a question, run `/beehest:status` to show it. The user answers with `/beehest:answer`.
