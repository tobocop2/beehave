# Copy this file to <config folder>/projects/incident.star and change source to the git repository that holds this folder.
# The desk changes no code, and the workflows merge nothing, so the gate only has to pass. The loader still needs the test_report key.
project(
    source = "~/ops",
    base_branch = "main",
    gate = "true",
    test_report = "reports/*.xml",
)
