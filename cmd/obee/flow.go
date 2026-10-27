package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

var errUnknownProject = errors.New("the config has no project of this name")

// notBuilt stands in for each collaborator of the workflow that a later milestone adds. Each call names the missing step.
type notBuilt struct{}

func (notBuilt) Run(context.Context, flow.AgentRequest) (agent.Result, error) {
	return agent.Result{}, flow.ErrNotBuilt
}

func (notBuilt) Read(context.Context, string, string, int) (flow.Review, error) {
	return flow.Review{}, flow.ErrNotBuilt
}

func (notBuilt) Claim(context.Context, string, string) (string, error) {
	return "", flow.ErrNotBuilt
}

func (notBuilt) Ready(context.Context, string, string) error { return flow.ErrNotBuilt }
func (notBuilt) Release(context.Context, string, string, string) error {
	return flow.ErrNotBuilt
}

func (notBuilt) Pull(context.Context, string, string) (review.Standing, error) {
	return review.Standing{}, flow.ErrNotBuilt
}

func (notBuilt) Watch(context.Context, int) (review.Outcome, error) {
	return review.Outcome{}, flow.ErrNotBuilt
}

func (notBuilt) SetDraft(context.Context, int, bool) error { return flow.ErrNotBuilt }

func (notBuilt) Path(context.Context, string, string) (string, error) {
	return "", flow.ErrNotBuilt
}

func (notBuilt) Beat(context.Context, string, string) (string, error) {
	return "", flow.ErrNotBuilt
}

// gate is notBuilt under the name of the Gate method.
type gate struct{ notBuilt }

func (gate) Run(context.Context, string, string) (flow.GateResult, error) {
	return flow.GateResult{}, flow.ErrNotBuilt
}

// agents is notBuilt under the name of the Agents method.
type agents struct{ notBuilt }

// logins picks the runner of the implement step of the project an item belongs to. Until the pool lands, every item of a project uses that runner.
type logins struct {
	projects map[string]config.Project
}

// newLogins reads the steps of each project of the config.
func newLogins(cfg config.Config) logins {
	return logins{projects: cfg.Projects}
}

// implement returns the implement step of the named project. A project that the config lacks is an error: no guess.
func (l logins) implement(project string) (config.StepSettings, error) {
	found, ok := l.projects[project]
	if !ok {
		return config.StepSettings{}, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	return found.Steps[model.StepImplement], nil
}

func (l logins) Pick(_ context.Context, project, _ string) (string, error) {
	step, err := l.implement(project)

	return step.Runner, err
}

func (l logins) PickFresh(_ context.Context, project, _ string) (string, error) {
	step, err := l.implement(project)

	return step.FreshRunner, err
}

// flowFactory builds the engine and the client of the commands. version is the build version.
func flowFactory() cli.Flow {
	return cli.Flow{
		Engine: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			engine, err := newEngine(ctx, cfg, opts)
			if err != nil {
				return nil, err
			}

			return engine, nil
		},
		Client: func(ctx context.Context, opts flow.Options) (cli.Steering, error) {
			pending := notBuilt{}

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: pending, Trees: pending})
			if err != nil {
				return nil, err
			}

			return client, nil
		},
	}
}

// newEngine builds the engine of the run command.
func newEngine(ctx context.Context, cfg config.Config, opts flow.Options) (*flow.Engine, error) {
	pending := notBuilt{}

	return flow.NewEngine(ctx, opts, flow.Deps{
		Briefs: newBriefs(config.ProcessEnv()), Agents: agents{}, Gate: gate{}, Verdicts: pending, Logins: newLogins(cfg), Lifecycle: pending, Forge: pending, Watch: pending, Trees: pending, Beats: pending,
	})
}
