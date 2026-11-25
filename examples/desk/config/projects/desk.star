# The project file for the refund desk. Copy it to <config folder>/projects/desk.star and change source to the git repository that holds this folder.
# The desk changes no code, and the workflows merge nothing, so the gate only has to pass. The loader still needs the test_report key.
project(
    source = "~/desk",
    base_branch = "main",
    gate = "true",
    sandbox = "on",  # the clerk and the judge read the case that the workflow passes and reach no host
    test_report = "reports/*.xml",
)
