## This lane is PROOF AND PAPERS ONLY: no new behavior

Run the proof, write the report and the pull request body. Change no source path:
`obee check papers <base>..<head>` reads your commits and fails on one. When the proof finds a
defect, do not fix it here: report it with the red row, and the orchestrator dispatches a fix lane.

