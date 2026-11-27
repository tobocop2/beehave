# The item that lands first. When its pull request is merged, it tells the item that waits on it.
# Add the two items with `beehest item add`, then run each one with `beehest run <item> <project> --workflow <file>`.
# Set WAITER to the id of the item that waits, and run this file for the dependency.

WAITER = env("WAITER")

def main(item):
    # The merge command is yours: it merges the pull request of the item.
    command(name = "merge", run = ["merge-pr", item.id])

    # The message goes to the open run of the waiting item in this project. A run that restarts here sends nothing a second time.
    send(to = "item:" + WAITER, topic = "landed", value = {"item": item.id}, name = "tell")

    # The run stays open until the owner closes it, so the waiting item can still read the message.
    wait(name = "close", question = "The pull request of %s is merged and %s is told. Answer when the item is done." % (item.id, WAITER))
