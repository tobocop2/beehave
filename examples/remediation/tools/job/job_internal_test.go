package job

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

func TestWordReads_TellQuotingAndExpansion(t *testing.T) {
	t.Parallel()

	file, err := syntax.NewParser().Parse(strings.NewReader(`run "$A" '$B' plain $(date) $((1+2))`), "")
	require.NoError(t, err)

	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	require.True(t, ok)

	args := call.Args

	assert.True(t, isQuoted(args[1]))
	assert.True(t, isQuoted(args[2]))
	assert.False(t, isQuoted(args[3]))
	assert.True(t, hasExpansion(args[1]))
	assert.False(t, hasExpansion(args[2]))
	assert.False(t, hasExpansion(args[3]))
	assert.True(t, hasExpansion(args[4]))
	assert.True(t, hasExpansion(args[5]))
}

func TestUvRunsSkipsOptionsAndTheirValues(t *testing.T) {
	t.Parallel()

	rows := map[string]bool{
		"run pytest": true, "tool run pytest": true, "-q run pytest": true, "--directory x run pytest": true, "-q tool run pytest": true,
		"pip install pytest": false, "sync --extra pytest": false, "tool install pytest": false, "": false, "-q": false, "--directory x sync": false,
	}

	for text, want := range rows {
		assert.Equal(t, want, uvRuns(strings.Fields(text)), text)
	}
}

func TestUvRunsStaysLinearInTheNumberOfOptions(t *testing.T) {
	t.Parallel()

	words := strings.Fields(strings.Repeat("-a x ", 5000) + "sync")

	assert.False(t, uvRuns(words))
	assert.True(t, uvRuns(append(words[:len(words)-1:len(words)-1], "run")))
}
