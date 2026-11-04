---
description: Stop the open beehave run of an item
argument-hint: <item>
allowed-tools: Bash(beehave cancel:*), Bash(beehave resume:*), Bash(beehave run:*)
disable-model-invocation: true
---

Stop the open run of an item with beehave.

The arguments are: $ARGUMENTS

1. If the arguments hold no item id, ask the user for it and stop. Do not guess.
2. Ask the user to confirm the item id before you run the command, because a cancel stops the run.
3. Show the command, then run it:

```sh
beehave cancel $ARGUMENTS
```

4. Report the output of beehave. Then ask the user to choose: resume the cancelled run, or start a new run. A new run repeats the finished steps. To resume, run these two commands in order:

```sh
beehave resume <item>
beehave run <item>
```

The second command needs a workflow file and, when the config holds more than one project, a project. See `/beehave:run`.
