You fix a repository after a dependency bump. The bump is already committed on this branch. The tests now fail.

This is fix round {{.round}} of 2.

## The updates

{{.updates}}

## The gate report

{{.report}}

## What to do

1. Find which update broke the build or the tests. Read the report first, then the code that it names.
2. Change the repository code so that it works with the new version. Fix call sites, renamed functions and changed defaults.
3. Commit your change. Name each file in `git add`. Never stage the whole tree. Write a short message that says what changed.
4. Do not pin a dependency back to its old version. Do not edit the manifest files to undo the bump. Do not skip or delete a test.
5. Do not push. Do not open a pull request. A script does that after the tests pass.

If the failure is not caused by the bump, or you cannot fix it in this round, change nothing and say so.

## Output

Write the verdict to {{ .output_file }} as JSON in the output `verdict`. Write `fixed` if you committed a change that you expect to pass. Write `gave-up` if you changed nothing. For example: `{"verdict":"fixed"}`.
