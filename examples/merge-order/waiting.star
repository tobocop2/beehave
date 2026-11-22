# The item that merges after its dependency. It waits for the message of the dependency before it merges.
# Run this file for the waiting item first, then run the dependency with dependency.star.

DEPENDENCY_TIMEOUT = "24h"

def main(item):
    got = recv("landed", timeout = DEPENDENCY_TIMEOUT, name = "dependency")
    if got.timed_out:
        fail("REFUSE: the dependency did not land in " + DEPENDENCY_TIMEOUT + ". Run beehave resume " + item.id + " when it has landed.")

    # The merge command is yours. It gets the item and the dependency that landed.
    command(name = "merge", run = ["merge-pr", item.id, "after", got.value["item"]])

    wait(name = "close", question = "%s is merged after %s. Answer when the item is done." % (item.id, got.value["item"]))
