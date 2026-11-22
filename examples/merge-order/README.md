# Merge order

Two items agree on a merge order. The item that waits does not merge until its dependency has landed, and the dependency tells it so.

## How it runs

- **Two runs, two files.** `waiting.star` runs for the item that waits. `dependency.star` runs for the item that lands first.
- **The message.** After its own merge, the dependency sends `landed` to `item:<id>` of the waiting item. The waiting item waits for it with `recv` and a timeout.
- **One project.** The send reaches only an item of the project of the sender. An item with no open run is an error, so run the waiting item first.
- **Restarts.** The run that a send names is a recorded step, `send:tell#1.to`. A resumed dependency sends no second message and finds no other run. The tests of the engine kill a run between that step and the send. The crash test of this example kills both runs while they wait for the owner, after the message arrived, and shows that the merge and the message do not repeat.

## Files

- `waiting.star`: the item that waits (`main`).
- `dependency.star`: the item that lands first (`main`). It reads the id of the waiting item from `WAITER`.
- `project.star`: the project file to copy to `projects/<name>.star`. It needs no tracker, no forge and no repository.
