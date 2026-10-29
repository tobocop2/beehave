# The project file for examples/research/workflow.star. Copy it to <config folder>/projects/<name>.star.
# The project has no tracker and no forge argument, so its items are in local.db and obee needs no beads, no gh and no network for the engine.
# The agents write their notes in the work folder of the item. The report goes to the folder that RESEARCH_OUT names.

project(
    source = "~/work/your-repo",  # any git repository; the work folder of the item is a branch of it
    base_branch = "main",
    gate = "true",  # the workflow runs no gate step, so the command does not matter
)
