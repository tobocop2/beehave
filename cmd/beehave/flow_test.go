package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
)

func TestBackendErrors_TheSentinelOfTheHostBackendSurvivesAGateOrAMerge(t *testing.T) {
	t.Parallel()

	require.Contains(t, backendErrors(), skypilot.ErrConfig)
	require.Contains(t, backendErrors(), link.ErrConfig)
	require.Contains(t, backendErrors(), ssh.ErrLinger)
	require.Contains(t, backendErrors(), ssh.ErrSystemd)
	require.Contains(t, backendErrors(), ssh.ErrDraining)
}

func TestFactsOf_TheFactsOfTheProjectInTheConfigReachTheEngineAndAnUnknownProjectHasNone(t *testing.T) {
	t.Parallel()

	facts := factsOf(map[string]config.Project{"p": {Project: model.Project{Name: "p", Limits: []string{"never run rm on a glob"}}}})

	require.Equal(t, []string{"never run rm on a glob"}, facts("p").Limits)
	require.Equal(t, model.Project{}, facts("other"))
}

func TestFlowFactory_ARunForAnUnknownProjectEndsWithAnErrorAndNoPanic(t *testing.T) {
	t.Parallel()

	opts := flow.Options{StateDir: t.TempDir() + "/state", Version: "v1", MaxConcurrent: 1, MaxReviewConcurrent: 1, Env: map[string]string{}}

	engine, err := flowFactory().Engine(t.Context(), config.Config{Projects: map[string]config.Project{}}, opts)
	require.NoError(t, err)

	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := t.TempDir() + "/workflow.star"
	require.NoError(t, os.WriteFile(file, []byte("def main(item):\n    gate(name = \"check\", run = [\"true\"])\n"), 0o600))

	_, err = engine.RunFile("12", "zzz", file)

	require.ErrorContains(t, err, errUnknownProject.Error())
}

func TestLogins_NeverGuessAProjectTheConfigLacks(t *testing.T) {
	t.Parallel()

	picked := newLogins(config.Config{Runners: []model.Runner{{Name: "a"}}, Projects: map[string]config.Project{}}, flow.Options{})

	_, pickErr := picked.Pick(t.Context(), "zzz", "1", nil, false)

	require.ErrorIs(t, pickErr, errUnknownProject)
}

func TestFlowFactory_OpensTheClientAndTheEngineOverTheStateFolder(t *testing.T) {
	t.Parallel()

	opts := flow.Options{StateDir: t.TempDir() + "/state", Version: "v1", MaxConcurrent: 1, MaxReviewConcurrent: 1, Env: map[string]string{}}
	factory := flowFactory()

	client, err := factory.Client(t.Context(), config.Config{}, opts)
	require.NoError(t, err)
	require.NoError(t, client.Close())

	engine, err := factory.Engine(t.Context(), config.Config{Projects: map[string]config.Project{}}, opts)
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	bad := opts
	bad.StateDir = "relative"

	_, err = factory.Client(t.Context(), config.Config{}, bad)
	require.ErrorContains(t, err, "invalid flow options")

	_, err = factory.Engine(t.Context(), config.Config{}, bad)
	require.ErrorContains(t, err, "invalid flow options")
}
