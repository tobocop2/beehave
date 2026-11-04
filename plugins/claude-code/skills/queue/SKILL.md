---
description: Add an item to the backlog of an beehave project that has the local tracker
argument-hint: <project> <id> <title>
allowed-tools: Bash(beehave item add:*)
disable-model-invocation: true
---

Add one item to the backlog with beehave.

The arguments are: $ARGUMENTS

1. Read the project, the item id and the title from the arguments. The id is a name such as `fix-login`. The title is the rest of the line.
2. If the project, the id or the title is missing, ask the user for it and stop. Do not guess.
3. Ask the user for the body of the item if the chat does not already hold it. The agent that runs the item reads the body as its task.
4. Write the body to a new file in the temporary folder of the system with the Write tool. The user approves this one write.
5. Show the command, then run it with the file you wrote. Pass the title as one single-quoted argument, and write each `'` in the title as `'\''`. For the title `Don't crash`, the command is:

```sh
beehave item add <project> <id> --title 'Don'\''t crash' --body-file <file>
```

6. Report the output of beehave. To start the item, the user runs `/beehave:run <id>`.
