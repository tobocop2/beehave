# The project file for examples/local/workflow.hcl. Copy it to <config folder>/projects/<name>.hcl.
# The project has no tracker and no forge key, so its items are in local.db and obee needs no beads, no gh and no network.

source      = "~/work/your-repo"   # the repository that gets the branch; a git URL or an absolute path
base_branch = "main"
gate        = "make check"
