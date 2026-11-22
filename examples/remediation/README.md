# Remediation example

`workflow.star` runs the remediation method. Its `command` steps call `remedy`, the tool in `tools/cmd/remedy`. The beehave release does not hold `remedy`.

## Build the tool

From the source checkout, run `make remedy`. The command writes `bin/remedy` in the checkout. It installs nothing outside the checkout.

## Run the round

A `command` step finds `remedy` on the `PATH` of the beehave process. Start the round with the `bin` folder of the checkout first on the `PATH`:

    PATH="$PWD/bin:$PATH" beehave <the command that starts the round>

If `remedy` is not on the `PATH`, the step ends with exit code 127 and the message "remedy: command not found".
