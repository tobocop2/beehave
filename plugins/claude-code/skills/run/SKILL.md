---
description: Start an beehave run for an item, or continue its open run
argument-hint: <item> [project] [--workflow <file>]
allowed-tools: Bash(beehave run:*), Bash(beehave resume:*), Bash(beehave fork:*)
disable-model-invocation: true
---

Take one item through its workflow with beehave.

The arguments are: $ARGUMENTS

1. If the arguments hold no item id, ask the user for it and stop. Do not guess.
2. `beehave run` needs a workflow file: `workflow.star` in the current folder, or the file that `--workflow` names. It needs a project when the config holds more than one. If beehave names the workflow file or the project as missing, ask the user for it. Do not guess.
3. Show the command, then run it with the arguments:

```sh
beehave run $ARGUMENTS
```

4. A run can last longer than the time limit of the Bash tool. Set the longest limit the tool allows. If the tool stops the command, the run stays open: run `/beehave:status`, then run the same command again to continue it.
5. If beehave says another `beehave run` owns the state folder, the first command still lives. Do not run the command again. Run `/beehave:status` and wait for the run to end or to pause.
6. Report the output and the exit code of beehave.
7. If beehave says the last run failed, tell the user which step it names. If the last run was cancelled, `beehave run` does not refuse: it starts a new run that repeats the finished steps. Ask the user to choose: resume the old run, or start a new run. To resume, run these two commands in order:

```sh
beehave resume <item>
beehave run <item>
```

To start again from a later step, or with other vars, fork the run. The fork keeps the finished steps before the step it names:

```sh
beehave fork <run id> --from <step> [--vars <file>]
beehave run <item>
```

If the resume refuses the run, tell the user to run `/beehave:cancel <item>`, then `beehave run <item>`: the cancel ends the failed run, and the run after it starts a new attempt.

8. If the run pauses with a question, run `/beehave:status` to show it. The user answers with `/beehave:answer`.
