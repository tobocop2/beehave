## This is a DESIGN lane: the rule and the acceptance script, no product code

Write the rule of the item as an acceptance script, run it at the base, and commit the script with its
recorded result. Change no product code: `obee check papers <base>..<head>` reads your commits
and fails on a source path. The script covers:

- **every code path the item has** (each converter, each backend, the fast path and the slow path);
- **every option mode**: each mode equals the base unless the item lists it as changed;
- **every context that must NOT change**: each row again inside each such context, head equals base;
- **the CI's own checks with the CI's base**, as "CI checks and their base" below names them.

Use a generator for the inputs, and an oracle that is not the code under test. Report the script path,
the result path, the number of inputs and the rows that are red at the base.

**When several outputs, formats, routes or settings render the changed value by different rules, write
the expected-behavior table first.** One row for each input, output and setting: the value at the
base, and beside it the value expected at the head. Take the expected column from the issue and from
what the base does. A cell that the issue does not name keeps its base value. Put the table under
`## Expected behavior` in `<S>/rulings/<item>.md`, and make the script print one row for each cell.
Report each cell where the issue leaves the answer open, and do not choose: that is an owner question.
Write the paths of the harness and of the build of the base into the same file: every later lane of
the item uses them.

