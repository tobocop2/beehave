The review skill is loaded. Run it on the change that you made in this session.

Iterate until a pass finds no defect. Commit each fix as its own commit. Never amend, squash or reset: the history stays.

Run only the tests that you change, by name, in the work folder. The engine runs the gate.

## Report

Write the report again as the output `report`, with the same four tables. The run checks it: each row of `## Claims` names the head that you end at, and a line says "no background commands left".
Add the number of passes that the sweep made. Write it to {{ .output_file }} as JSON, as a text, for example `{"report":"# Report\n..."}`.

Send no separate message with the report: the end-of-turn text goes to the orchestrator already.
