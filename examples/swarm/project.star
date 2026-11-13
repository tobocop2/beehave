# The project file for examples/swarm/workflow.star. Copy it to <config folder>/projects/<name>.star and change the three values that name your repository.
# The workflow files one item for each task, so the tracker must be one that accepts new items. A forge is required, because batch_merge merges pull requests.

project(
    repo = "your-org/your-repo",  # owner/name; the same value as the variable REPO of the run
    tracker = "github",           # "github" or "beads"; the new task items go here. For "beads", add beads = beads(prefix = "...").
    forge = "github",
    base_branch = "main",         # the variable BASE of the run names the integration branch that receives the pull requests
    gate = "make check",          # one run of this command covers the union of all the heads
    test_report = "reports/*.xml",  # the JUnit XML files that the gate writes; a gate that exits 0 with no test fails
)
