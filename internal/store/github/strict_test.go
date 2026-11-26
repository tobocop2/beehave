package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// lenient answers every call with an empty list and refuses nothing.
type lenient struct{}

func (lenient) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{Stdout: "[]"}
}

func TestStrict_RefusesAnUnknownFieldOverEveryKindOfRunner(t *testing.T) {
	t.Parallel()

	runners := map[string]proc.Runner{"scripted": &proctest.Scripted{}, "other": lenient{}}

	for name, run := range runners {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			done := strict(run).Run(t.Context(), []string{"issue", "list", "--json", "number,authorAssociation"})

			assert.Equal(t, 1, done.ExitCode)
			assert.True(t, strings.HasPrefix(done.Stderr, `Unknown JSON field: "authorAssociation"`), done.Stderr)
		})
	}
}

func TestStore_IsBuiltOverAStrictRunnerInEveryTest(t *testing.T) {
	t.Parallel()

	sites, err := ghcontract.UnwrappedCalls(".", "github", "New", "strict")
	require.NoError(t, err)

	assert.Empty(t, sites, "a test builds the store over a runner that skips the table of gh: write github.New(repo, strict(run))")

	control, err := ghcontract.UnwrappedCalls(".", "github", "New", "noSuchWrapper")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(control), 10, "the scan finds the calls of github.New: with a wrapper that no test uses, every call is listed")
}
