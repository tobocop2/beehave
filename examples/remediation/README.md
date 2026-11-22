# Remediation example

`workflow.star` runs the remediation method. Its `command` steps call `remedy`, the tool in `tools/cmd/remedy`. The beehave release does not hold `remedy`.

## Build the tool

From the source checkout, run `make remedy`. The command writes `bin/remedy` in the checkout. It installs nothing outside the checkout.

## Run the round

A `command` step finds `remedy` on the `PATH` of the beehave process. Start the round with the `bin` folder of the checkout first on the `PATH`:

    make remedy
    PATH="$PWD/bin:$PATH" beehave run round/<base> <project> --workflow examples/remediation/workflow.star

`<base>` is the base branch of the round, and `<project>` is the project file of the round. Its `vars.round` entry sets the round (see `docs/examples.md`). The item `round/<base>` must exist: add it with `beehave item add <project> round/<base> --title "The round" --body-file <file>`.

If `beehave serve` runs the engine (see `docs/serve.md`), start that engine with the same `PATH`. `beehave run` then gives the item to it, and the steps use the `PATH` of the engine.

If `remedy` is not on the `PATH`, the step ends with exit code 127 and the message "remedy: command not found".
