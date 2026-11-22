# The project file for examples/review-loop/workflow.star. Copy it to <config folder>/projects/<name>.star.
# The project has no tracker, no forge and no source. Its items are in local.db, and the work folder of an item is a plain folder in the state folder.

# The agent steps read and write the draft in their work folder and reach no host, so each runs in the sandbox with no network.

project(sandbox = "on")
