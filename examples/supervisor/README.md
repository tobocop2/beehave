# Supervisor

A supervisor starts one worker run and reads what the worker agent says while it works. When the worker runs a forbidden command, the supervisor cancels the worker.

## How it runs

- **Spawn.** The supervisor calls `spawn` with the item id of the variable `TASK`. The item runs the function `worker`, which has one agent step.
- **Stream.** The supervisor calls `stream(child, after = cursor, timeout = WATCH)` in a loop. The call returns the entries that the agent wrote after the cursor, with the fields `seq`, `attempt`, `kind` and `text`. The kind is `text` or `call`. The text is redacted, as in `beehest status --follow`.
- **Cursor.** The field `next` of one call is the `after` of the next call. A call that finds no new entry waits up to `WATCH`, and then returns no entry and the same cursor.
- **Forbidden command.** An entry of kind `call` whose text contains `FORBIDDEN` makes the supervisor log the text and call `cancel(child)` once.
- **End.** The loop stops when the field `ended` is true: the worker ended and no entry is left after `next`. A canceled worker ends the same way.

Each `stream` call is a recorded step with a recorded deadline. A restart of the supervisor during a wait reads the entries after the same cursor and logs each entry once.

## Files

- `workflow.star`: the supervisor (`main`) and the worker entry.
- `prompts/work.md`: the prompt of the worker agent.

## Variables

`TASK`: the item id of the worker (default `21`). Optional: `FORBIDDEN` (default `rm -rf`) and `WATCH` (default `5s`).
