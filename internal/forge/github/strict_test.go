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

			done := strict(run).Run(t.Context(), []string{"pr", "list", "--json", "number,authorAssociation"})

			assert.Equal(t, 1, done.ExitCode)
			assert.True(t, strings.HasPrefix(done.Stderr, `Unknown JSON field: "authorAssociation"`), done.Stderr)
		})
	}
}

// shapeOwnCheck is the one test that builds the forge over a runner that is not made strict: it records every call and checks each one against the table itself.
const shapeOwnCheck = "TestForge_EveryCallShapeIsOneGhAccepts"

func TestForge_IsBuiltOverAStrictRunnerInEveryTest(t *testing.T) {
	t.Parallel()

	sites, err := ghcontract.UnwrappedCalls(".", "github", "New", "strict")
	require.NoError(t, err)

	var lenientSites []string

	for _, site := range sites {
		if !strings.HasSuffix(site, ":"+shapeOwnCheck) {
			lenientSites = append(lenientSites, site)
		}
	}

	assert.Empty(t, lenientSites, "a test builds the forge over a runner that skips the table of gh: write github.New(repo, strict(run)) or use newForge")
	assert.Len(t, sites, 1, "the one exception is the shape test, which checks each recorded call itself")
}
