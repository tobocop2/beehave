---
description: Stop the open beehest run of an item
argument-hint: <item>
allowed-tools: Bash(beehest cancel:*), Bash(beehest resume:*), Bash(beehest run:*)
disable-model-invocation: true
---

Stop the open run of an item with beehest.

The arguments are: $ARGUMENTS

1. If the arguments hold no item id, ask the user for it and stop. Do not guess.
2. Ask the user to confirm the item id before you run the command, because a cancel stops the run.
3. Show the command, then run it:

```sh
beehest cancel $ARGUMENTS
```

4. Report the output of beehest. Then ask the user to choose: resume the cancelled run, or start a new run. A new run repeats the finished steps. To resume, run these two commands in order:

```sh
beehest resume <item>
beehest run <item>
```

The second command needs a workflow file and, when the config holds more than one project, a project. See `/beehest:run`.

If the resume refuses the run, tell the user to run `beehest run <item>` instead. If the refusal says that a canceled run holds the claim, the run frees the claim, and `beehest resume <item>` works again after it. If the refusal names the version that made the run, tell the user to start that build, or to run `beehest resume <item> --on-this-build`. For any other refusal, the cancel has already ended the old run, so the run starts a new attempt.
