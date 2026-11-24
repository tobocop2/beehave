# The project file that the e2e scripts copy with the fixture command. A script replaces the source and the sandbox mode of its copy.
# The project has no tracker and no forge argument, so its items are in local.db and the script needs no beads, no gh and no network.

project(
    source = "~/work/your-repo",  # a script replaces this with its own origin folder
    base_branch = "main",
    sandbox = "on",  # a script that runs a fake agent outside the sandbox replaces this with "off"
    gate = "make check",  # a script that runs the gate replaces this with its own gate program
    test_report = "reports/*.xml",  # a gate that exits 0 with no test fails, so the project names the JUnit XML files
)
