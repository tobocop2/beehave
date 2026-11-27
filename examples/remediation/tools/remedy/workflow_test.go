package remedy_test

import (
	"os"
	"path"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehest/internal/workflow/workflowtest"
)

const toolName = "remedy"

// subcommands returns the subcommand of every `remedy` program that the command and gate calls of the file run.
// The scan reads the first word of the run list, with or without a path. A run that is not a list of literals is not found.
func subcommands(t *testing.T, src string) []string {
	t.Helper()

	calls, err := workflowtest.Calls("workflow.star", []byte(src))
	require.NoError(t, err)

	var found []string

	for _, call := range calls {
		if call.Builtin != "command" && call.Builtin != "gate" {
			continue
		}

		if words := workflowtest.Literals(call.Args["run"]); len(words) > 1 && path.Base(words[0]) == toolName {
			found = append(found, words[1])
		}
	}

	return found
}

// unknownSubcommands returns the subcommands that the file runs and the command table does not hold.
func unknownSubcommands(t *testing.T, src string) []string {
	t.Helper()

	var unknown []string

	for _, name := range subcommands(t, src) {
		if !slices.Contains(remedy.Names(), name) {
			unknown = append(unknown, name)
		}
	}

	return unknown
}

func TestWorkflow_EveryRemedySubcommandOfTheExampleIsInTheCommandTable(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("../../workflow.star")
	require.NoError(t, err)

	assert.Empty(t, unknownSubcommands(t, string(src)))
}

func TestWorkflow_ASubcommandThatIsNotInTheTableFailsTheComparison(t *testing.T) {
	t.Parallel()

	unknown := unknownSubcommands(t, "command(run = [\"remedy\", \"bake\"])\ngate(run = [\"remedy\", \"size\"])\n")

	assert.Equal(t, []string{"bake"}, unknown)
}

func TestWorkflow_RemedyBehindAPathIsFound(t *testing.T) {
	t.Parallel()

	got := subcommands(t, "command(run = [\"bin/remedy\", \"bake\"])\ncommand(run = [\"/usr/local/bin/remedy\", \"size\"])\ncommand(run = [\"notremedy\", \"bake\"])\n")

	assert.Equal(t, []string{"bake", "size"}, got)
}

func TestWorkflow_SubcommandsAreReadFromCommandAndGateCalls(t *testing.T) {
	t.Parallel()

	src := `
def main(item):
    command(name = "size", run = ["remedy", "size", "brief.md"])
    gate(name = "check", run = ["remedy", "report", "report.md", "--head", "tip"])
    command(name = "other", run = ["make", "check"])
    gate(name = "plain")
    for _ in range(2):
        command(name = "inner", run = ["remedy", "bake"])
`

	got := subcommands(t, src)

	assert.True(t, slices.Equal([]string{"size", "report", "bake"}, got), "got %v", got)
}
