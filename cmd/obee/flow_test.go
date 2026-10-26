package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

func TestNotBuilt_EveryStepSaysThisBuildLacksIt(t *testing.T) {
	t.Parallel()

	pending := notBuilt{}
	ctx := t.Context()

	_, briefErr := pending.Write(ctx, flow.BriefRequest{})
	_, agentErr := agents{}.Run(ctx, flow.AgentRequest{})
	_, gateErr := gate{}.Run(ctx, "p", "1")
	_, verdictErr := pending.Read(ctx, "p", "1", 0)
	_, claimErr := pending.Claim(ctx, "p", "1")
	_, beatErr := pending.Beat(ctx, "p", "owner")
	_, pullErr := pending.Pull(ctx, "p", "1")
	_, pathErr := pending.Path(ctx, "p", "1")

	for _, err := range []error{briefErr, agentErr, gateErr, verdictErr, claimErr, beatErr, pullErr, pathErr, pending.Ready(ctx, "p", "1"), pending.Release(ctx, "p", "1", "owner")} {
		require.ErrorIs(t, err, flow.ErrNotBuilt)
	}
}

func TestLogins_PickTheRunnersOfTheProjectTheItemBelongsTo(t *testing.T) {
	t.Parallel()

	steps := config.DefaultSteps()
	steps[model.StepImplement] = config.StepSettings{Runner: "b", FreshRunner: "c"}
	other := config.DefaultSteps()
	other[model.StepImplement] = config.StepSettings{Runner: "d"}

	picked := newLogins(config.Config{
		Runners:  []model.Runner{{Name: "a"}},
		Projects: map[string]config.Project{"p": {Steps: steps}, "q": {Steps: other}},
	})

	tests := []struct {
		project   string
		wantLogin string
		wantFresh string
	}{
		{"p", "b", "c"},
		{"q", "d", ""},
	}

	for _, test := range tests {
		t.Run(test.project, func(t *testing.T) {
			t.Parallel()

			login, err := picked.Pick(t.Context(), test.project, "1")
			require.NoError(t, err)
			assert.Equal(t, test.wantLogin, login)

			fresh, err := picked.PickFresh(t.Context(), test.project, "1")
			require.NoError(t, err)
			assert.Equal(t, test.wantFresh, fresh)
		})
	}
}

func TestLogins_NeverGuessAProjectTheConfigLacks(t *testing.T) {
	t.Parallel()

	picked := newLogins(config.Config{Runners: []model.Runner{{Name: "a"}}, Projects: map[string]config.Project{}})

	_, pickErr := picked.Pick(t.Context(), "zzz", "1")
	_, freshErr := picked.PickFresh(t.Context(), "zzz", "1")

	require.ErrorIs(t, pickErr, errUnknownProject)
	require.ErrorIs(t, freshErr, errUnknownProject)
}

func TestFlowFactory_OpensTheClientAndTheEngineOverTheStateFolder(t *testing.T) {
	t.Parallel()

	opts := flow.Options{StateDir: t.TempDir() + "/state", Version: "v1", MaxConcurrent: 1, Env: map[string]string{}}
	factory := flowFactory()

	client, err := factory.Client(t.Context(), opts)
	require.NoError(t, err)
	require.NoError(t, client.Close())

	engine, err := factory.Engine(t.Context(), config.Config{Projects: map[string]config.Project{}}, opts)
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	bad := opts
	bad.StateDir = "relative"

	_, err = factory.Client(t.Context(), bad)
	require.ErrorIs(t, err, flow.ErrBadOptions)

	_, err = factory.Engine(t.Context(), config.Config{}, bad)
	require.ErrorIs(t, err, flow.ErrBadOptions)
}
