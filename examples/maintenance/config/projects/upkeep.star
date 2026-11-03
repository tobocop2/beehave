# Written by scripts/mkprojects.sh. Shown here as the shape of the files it makes.
# One project for the report: the source is the upkeep folder (a git repository), and the gate does nothing.
project(
    source = "~/.upkeep",
    base_branch = "main",
    gate = "true",
    test_report = "reports/*.xml",
)

# One project for each repository of repos.tsv, for example <config folder>/projects/api.star:
#
# project(
#     repo = "acme/api",       # the origin of each worktree is GitHub, so the push reaches the real repository
#     forge = "github",
#     base_branch = "main",
#     gate = "go test ./...",  # the test command of the repository
#     test_report = "reports/*.xml",
# )
