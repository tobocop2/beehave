---
description: Show the beehave runs and the questions that wait for an answer
argument-hint: [--config <folder>]
allowed-tools: Bash(beehave status:*)
disable-model-invocation: true
---

Show the state of the runs with beehave.

The arguments are: $ARGUMENTS

Show the command, then run it with the arguments:

```sh
beehave status $ARGUMENTS
```

Report the output as it is. Point out each question that waits for an answer, with its item id. The user answers with `/beehave:answer <item> <text>`.
