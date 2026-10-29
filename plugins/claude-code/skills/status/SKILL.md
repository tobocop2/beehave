---
description: Show the obee runs and the questions that wait for an answer
argument-hint: [--config <folder>]
allowed-tools: Bash(obee status:*)
disable-model-invocation: true
---

Show the state of the runs with obee.

The arguments are: $ARGUMENTS

Show the command, then run it with the arguments:

```sh
obee status $ARGUMENTS
```

Report the output as it is. Point out each question that waits for an answer, with its item id. The user answers with `/obee:answer <item> <text>`.
