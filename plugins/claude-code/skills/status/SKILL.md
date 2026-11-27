---
description: Show the beehest runs and the questions that wait for an answer
argument-hint: [--config <folder>]
allowed-tools: Bash(beehest status:*)
disable-model-invocation: true
---

Show the state of the runs with beehest.

The arguments are: $ARGUMENTS

Show the command, then run it with the arguments:

```sh
beehest status $ARGUMENTS
```

Report the output as it is. Point out each question that waits for an answer, with its item id. The user answers with `/beehest:answer <item> <text>`.
