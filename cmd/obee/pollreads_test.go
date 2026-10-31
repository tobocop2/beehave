package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

// loginForge is a forge that tells its login. The other methods panic, so a test shows every call the code makes.
type loginForge struct {
	forge.Forge

	login string
	err   error
}

func (f loginForge) Login(context.Context) (string, error) { return f.login, f.err }

func pollReadsOver(f forge.Forge) pollReads {
	projects := map[string]config.Project{"zeta": {}, "alpha": {}}

	return pollReads{projects: projects, pulls: pulls{projects: projects, forge: func(model.Project) (forge.Forge, error) { return f, nil }}}
}

func TestPollReads_ProjectsListsTheConfiguredNamesInOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"alpha", "zeta"}, pollReadsOver(nil).Projects())
}

func TestPollReads_SourceReturnsTheForgeAndTheLoginOfTheActiveAccount(t *testing.T) {
	t.Parallel()

	source, login, err := pollReadsOver(loginForge{login: "octo"}).Source(t.Context(), "alpha")

	require.NoError(t, err)
	assert.Equal(t, "octo", login)
	assert.NotNil(t, source)
}

func TestPollReads_SourceHidesTheWriteMethodsOfTheForge(t *testing.T) {
	t.Parallel()

	source, _, err := pollReadsOver(loginForge{login: "octo"}).Source(t.Context(), "alpha")
	require.NoError(t, err)

	_, writes := source.(forge.Forge)
	assert.False(t, writes, "a type assertion cannot reach the full forge")

	_, login := source.(loginReader)
	assert.False(t, login)
}

func TestPollReads_SourceRefusesWhatItCannotReadAndSkipsAProjectWithNoForge(t *testing.T) {
	t.Parallel()

	_, _, err := pollReadsOver(loginForge{err: errFake}).Source(t.Context(), "alpha")
	require.ErrorIs(t, err, errFake)

	_, _, err = pollReadsOver(&pullForge{}).Source(t.Context(), "alpha")
	require.ErrorIs(t, err, errNoLogin)

	_, _, err = pollReadsOver(nil).Source(t.Context(), "nobody")
	require.ErrorIs(t, err, errUnknownProject)

	reads := pollReadsOver(nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	_, _, err = reads.Source(t.Context(), "alpha")
	require.ErrorIs(t, err, flow.ErrNoPollSource)
}
