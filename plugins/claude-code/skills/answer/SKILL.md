---
description: Answer the question of a paused obee item
argument-hint: <item> <text>
allowed-tools: Bash(obee answer:*)
disable-model-invocation: true
---

Answer the question of a paused item with obee.

The arguments are: $ARGUMENTS

1. The first word of the arguments is the item id. The rest is the answer.
2. If the item id or the answer is missing, ask the user for it and stop. If the user does not know the item, run `/obee:status` to list the questions that wait.
3. Show the command, then run it. Pass the answer as one single-quoted argument, and write each `'` in the answer as `'\''`. The shell then keeps `&`, `;`, `*`, `$` and `#` as text. For the answer `don't use *`, the command is:

```sh
obee answer <item> 'don'\''t use *'
```

4. Report the output of obee. Do not answer the same question twice: after the answer, `/obee:status` still shows the question as paused until the run continues. To continue the run, the user runs `/obee:run <item>`.
