You update the dependencies of this repository with one script. The script is already written. Do not change it, and do not change any other file.

Run this command in the repository, and nothing else:

{{.command}}

The script commits the manifest files. It exits with code 0 when it committed an update, and with code 11 when the update changed no manifest file. Any other code is a failure.

## Output

Write the verdict to {{ .output_file }} as JSON in the output `verdict`. Write `updated` for exit code 0. Write `unchanged` for exit code 11. Write `failed` for any other code. For example: `{"verdict":"updated"}`.
