package remedy_test

import (
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/remedy"
	"github.com/tobocop2/obee/internal/workflow"
)

const toolName = "remedy"

// literal is the text of an argument that holds a plain string. An argument that is not set, or that reads a value, gives no text.
func literal(expr hcl.Expression) string {
	if expr == nil {
		return ""
	}

	value, diags := expr.Value(nil)
	if diags.HasErrors() || value.IsNull() {
		return ""
	}

	return value.AsString()
}

// subcommands returns the subcommand of every `remedy` command that the command and gate steps of the file run.
// The scan reads the first word of the run text, with or without a path. A remedy command after `&&`, or in a run that is not a plain string, is not found.
func subcommands(t *testing.T, src string) []string {
	t.Helper()

	flow, err := workflow.LoadBytes("workflow.hcl", []byte(src))
	require.NoError(t, err)

	var found []string

	flow.Walk(func(step workflow.Step, _ int) {
		var run string

		switch step := step.(type) {
		case *workflow.Command:
			run = literal(step.Run)
		case *workflow.Gate:
			run = literal(step.Run)
		}

		if words := strings.Fields(run); len(words) > 1 && path.Base(words[0]) == toolName {
			found = append(found, words[1])
		}
	})

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

	src, err := os.ReadFile("../../workflow.hcl")
	require.NoError(t, err)

	assert.Empty(t, unknownSubcommands(t, string(src)))
}

func TestWorkflow_ASubcommandThatIsNotInTheTableFailsTheComparison(t *testing.T) {
	t.Parallel()

	unknown := unknownSubcommands(t, "workflow \"w\" {}\ncommand \"x\" { run = \"remedy bake\" }\ngate \"g\" { run = \"remedy cut\" }\n")

	assert.Equal(t, []string{"bake"}, unknown)
}

func TestWorkflow_RemedyBehindAPathIsFound(t *testing.T) {
	t.Parallel()

	got := subcommands(t, "workflow \"w\" {}\ncommand \"x\" { run = \"bin/remedy bake\" }\ncommand \"y\" { run = \"/usr/local/bin/remedy cut\" }\ncommand \"z\" { run = \"notremedy bake\" }\n")

	assert.Equal(t, []string{"bake", "cut"}, got)
}

func TestWorkflow_SubcommandsAreReadFromCommandAndGateSteps(t *testing.T) {
	t.Parallel()

	src := `
workflow "w" {}
command "cut" { run = "remedy cut --old a --new b-II --pr 3" }
gate "check" { run = "remedy audit present tip base" }
command "other" { run = "make check" }
gate "plain" {}
loop "again" {
  max   = 2
  until = command.inner.rc == 0
  command "inner" { run = "remedy bake" }
}
`

	got := subcommands(t, src)

	assert.True(t, slices.Equal([]string{"cut", "audit", "bake"}, got), "got %v", got)
}
