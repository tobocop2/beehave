package main

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/local"
)

func TestDepsOn_TheSentinelsOfTheHostEntriesReachTheEngineAsKeptErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("a host sentinel")
	table := newAdapters(nil, nil, nil)
	table.hosts = map[model.HostProvider]hostAdapter{model.HostSSH: {sentinels: []error{sentinel}}}

	deps := depsOn(config.Config{}, flow.Options{}, logins{}, table)

	require.Equal(t, []error{sentinel}, deps.Kept, "a gate or a merge keeps the sentinels that the host entries declare, so errors.Is finds them")
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

func TestFlowFactory_TheEngineOfTheRunCommandPicksTheRunnerFromTheUsageTheClaudeAdapterReads(t *testing.T) {
	t.Parallel()

	cfg, opts := usageFixture(t)
	cfg.Projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", Claims: model.ClaimsLocal, Tracker: model.TrackerLocal}}}
	opts.StateDir = t.TempDir() + "/state"
	opts.Version = "v1"
	opts.MaxConcurrent = 1
	opts.MaxReviewConcurrent = 1

	require.NoError(t, os.Mkdir(opts.StateDir, 0o700))

	items, err := local.Open(t.Context(), opts.StateDir, "p", time.Now)
	require.NoError(t, err)
	require.NoError(t, items.Add(t.Context(), "12", "an item", "its body"))
	require.NoError(t, items.Close())

	engine, err := flowFactory().Engine(t.Context(), cfg, opts)
	require.NoError(t, err)

	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(dir+"/prompts", 0o700))
	require.NoError(t, os.WriteFile(dir+"/prompts/a.md", []byte("Write to {{ .output_file }}\n"), 0o600))
	file := dir + "/workflow.star"
	require.NoError(t, os.WriteFile(file, []byte("def main(item):\n    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\")\n"), 0o600))

	_, err = engine.RunFile("12", "p", file)

	require.ErrorContains(t, err, "claude exited 7: fake-claude-ran")
}
