# The project file for examples/local/workflow.star. Copy it to <config folder>/projects/<name>.star.
# The project has no tracker and no forge argument, so its items are in local.db and obee needs no beads, no gh and no network.

project(
    source = "~/work/your-repo",  # the repository that gets the branch; a git URL or an absolute path
    base_branch = "main",
    gate = "make check",
    test_report = "reports/*.xml",  # the JUnit XML files that the gate writes; a gate that exits 0 with no test fails
)
