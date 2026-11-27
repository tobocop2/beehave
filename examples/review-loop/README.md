# Review loop

Two agents improve one draft by messages. The author writes a draft, and a reviewer judges it. The loop stops when the reviewer approves, or after three drafts.

## How it runs

- **Two runs.** The author files a reviewer item under the item and starts it with `spawn`. The reviewer runs the entry `reviewer` of the same file.
- **Messages.** The author sends each draft to the run of the reviewer with `send`. The reviewer waits for it with `recv`, judges it, and replies with `send(to = "parent")`. The author waits for the reply with `recv` and a timeout.
- **The loop.** At `approve`, the author keeps the draft as a comment on the item. At `changes`, a revise step applies the notes and the author sends the new draft. After three drafts with no approval, the run fails and names the cause.
- **Restarts.** Each message has an id made of the run and the step, so a resumed run sends no message twice and loses none.

## Files

- `workflow.star`: the author (`main`) and the reviewer (`reviewer`).
- `project.star`: the project file to copy to `projects/<name>.star`. It needs no tracker, no forge and no repository.
- `prompts/draft.md`, `prompts/review.md`, `prompts/revise.md`: the prompts of the three agent steps.

## A message from the command line

`beehest send <item> --topic <topic> <text>` puts a message on the inbox of the open run of an item. A `recv` call with the same topic receives it.
