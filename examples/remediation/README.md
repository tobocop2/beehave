# Remediation example

`workflow.star` runs the remediation method. Its `command` steps call `remedy`, the tool in `tools/cmd/remedy`. The beehest release does not hold `remedy`.

## Build the tool

From the source checkout, run `make remedy`. The command writes `bin/remedy` in the checkout. It installs nothing outside the checkout. `make build` does not make `bin/remedy`. Run `make test-examples` to run the tests of the examples folder.

## Run the round

A `command` step finds `remedy` on the `PATH` of the beehest process. Start the round with the `bin` folder of the checkout first on the `PATH`:

    make remedy
    PATH="$PWD/bin:$PATH" beehest run round/<base> <project> --workflow examples/remediation/workflow.star

`<base>` is the base branch of the round, and `<project>` is the project file of the round. Its `vars.round` entry sets the round (see `docs/examples.md`). The item `round/<base>` must exist: add it with `beehest item add <project> round/<base> --title "The round" --body-file <file>`.

## Credentials and the round branch

The round calls `merge` for the batch. A project file with no `credentials` key keeps the guards of a project with no writer. If the round branch requires `beehest/gate` or `beehest/review`, `merge` refuses and says that the project needs credentials. With a `writer` entry in `credentials`, `merge` refuses unless the round branch requires a pull request and both statuses (docs/design/trust.md, section "The ruleset"). Add the entry before you post the ruleset on `integration/**`.

If `beehest serve` runs the engine (see `docs/serve.md`), start that engine with the same `PATH`. `beehest run` then gives the item to it, and the steps use the `PATH` of the engine.

If `remedy` is not on the `PATH`, the step ends with exit code 127 and the message "remedy: command not found".
