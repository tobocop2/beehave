package shell_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"

	"github.com/tobocop2/beehave/internal/check/shell"
)

// callsOf returns each simple command of the script with the unit that holds it.
func callsOf(t *testing.T, script string) ([]*syntax.CallExpr, shell.Unit) {
	t.Helper()

	units, bad := shell.ParseUnits(script)
	require.Empty(t, bad)
	require.Len(t, units, 1)

	var calls []*syntax.CallExpr

	syntax.Walk(units[0].File, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			calls = append(calls, call)
		}

		return true
	})

	return calls, units[0]
}

func TestReader_ExpandsQuotesAndKeepsAVariableAsItsNameText(t *testing.T) {
	t.Parallel()

	calls, unit := callsOf(t, "cargo 'a b' \"$X\" plain")
	reader := shell.NewReader()

	require.Len(t, calls, 1)
	assert.Equal(t, []string{"cargo", "a b", "$X", "plain"}, reader.Words(unit, calls[0].Args))
	assert.Equal(t, "a b", reader.Literal(unit, calls[0].Args[1]))
}

func TestHeadOf_DropsTheWrappersBeforeTheCommand(t *testing.T) {
	t.Parallel()

	head, dropped := shell.HeadOf([]string{"FOO=1", "timeout", "5", "cargo", "test"})

	assert.Equal(t, []string{"cargo", "test"}, head)
	assert.Equal(t, []string{"FOO=1", "timeout", "5"}, dropped)

	head, dropped = shell.HeadOf([]string{"time", "-p", "cargo", "test"})

	assert.Equal(t, []string{"cargo", "test"}, head)
	assert.Equal(t, []string{"time", "-p"}, dropped)
}

func TestIsShell_NamesTheShellsThatReadAScript(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"sh", "bash", "zsh", "dash"} {
		assert.True(t, shell.IsShell(name), name)
	}

	assert.False(t, shell.IsShell("ssh"))
}

func TestIsTimeout_NamesBothTimeoutWrappers(t *testing.T) {
	t.Parallel()

	assert.True(t, shell.IsTimeout(shell.TimeoutName))
	assert.True(t, shell.IsTimeout(shell.GtimeoutName))
	assert.False(t, shell.IsTimeout(shell.SSHName))
}

func TestOneOf_FindsTheWordInTheOptions(t *testing.T) {
	t.Parallel()

	assert.True(t, shell.OneOf("b", "a", "b"))
	assert.False(t, shell.OneOf("c", "a", "b"))
	assert.False(t, shell.OneOf("c"))
}

func TestLone_ReadsAVariableAlone(t *testing.T) {
	t.Parallel()

	calls, _ := callsOf(t, `run "$A" '$B' plain $(date) "${C}"`)
	args := calls[0].Args

	name, ok := shell.Lone(args[1])
	assert.True(t, ok)
	assert.Equal(t, "A", name)

	name, ok = shell.Lone(args[5])
	assert.True(t, ok)
	assert.Equal(t, "C", name)

	_, ok = shell.Lone(args[3])
	assert.False(t, ok)
}
