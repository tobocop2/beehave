package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/pool"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/review"
)

var errUnknownProject = errors.New("the config has no project of this name")

// notBuilt stands in for each collaborator of the workflow that a later milestone adds. Each call names the missing step.
type notBuilt struct{}

func (notBuilt) Write(context.Context, flow.BriefRequest) (string, error) {
	return "", flow.ErrNotBuilt
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

func (notBuilt) Beat(context.Context, string, string) (string, error) {
	return "", flow.ErrNotBuilt
}

// gate is notBuilt under the name of the Gate method.
type gate struct{ notBuilt }

func (gate) Run(context.Context, string, string) (flow.GateResult, error) {
	return flow.GateResult{}, flow.ErrNotBuilt
}

// logins picks the runner of the implement step of the project an item belongs to. A step with no runner takes the pool pick.
// pool returns the name of the runner that has room under the usage caps.
type logins struct {
	projects map[string]config.Project
	pool     func(ctx context.Context) (string, error)
}

// newLogins reads the steps of each project of the config. The pool pick reads the usage of each claude runner.
func newLogins(cfg config.Config, opts flow.Options) logins {
	return logins{projects: cfg.Projects, pool: poolPick(cfg, claude.UsageReader{Base: opts.Env}.Read)}
}

// poolPick returns the name of the claude runner that read finds under both usage caps.
func poolPick(cfg config.Config, read pool.Reader) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		runner, err := pool.Select(ctx, cfg.Runners, cfg.Pool, model.AgentClaude, read)
		if err != nil {
			return "", fmt.Errorf("pick a runner from the pool: %w", err)
		}

		return runner.Name, nil
	}
}

// implement returns the implement step of the named project. A project that the config lacks is an error: no guess.
func (l logins) implement(project string) (config.StepSettings, error) {
	found, ok := l.projects[project]
	if !ok {
		return config.StepSettings{}, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	return found.Steps[model.StepImplement], nil
}

func (l logins) Pick(ctx context.Context, project, _ string) (string, error) {
	step, err := l.implement(project)
	if err != nil || step.Runner != "" {
		return step.Runner, err
	}

	return l.pool(ctx)
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

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: pending, Trees: newWorktrees(nil, opts, nil)})
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
	table := newAdapters(proc.NewExecRunner("bd", ""))
	trees := newWorktrees(cfg.Projects, opts, table.forge)

	return flow.NewEngine(ctx, opts, flow.Deps{
		Briefs: pending, Agents: newAgents(cfg, opts, trees, table), Gate: gate{}, Verdicts: pending, Logins: newLogins(cfg, opts), Lifecycle: pending, Forge: pending, Watch: pending, Trees: trees, Beats: pending,
		Commands: newPrograms(cfg, trees, table), Items: trackerItems{projects: cfg.Projects, tracker: table.tracker},
	})
}

// newWorktrees finds item worktrees in the trees folder of the state folder. It adds a missing one from the clone of the project in the repos folder.
// The client of the commands only reads worktrees and passes no projects.
// Every git call sets GIT_TERMINAL_PROMPT=0, so a private repository fails and never waits for a password.
func newWorktrees(projects map[string]config.Project, opts flow.Options, forges func(model.Project) (forge.Forge, error)) worktrees {
	return worktrees{
		root: filepath.Join(opts.StateDir, "trees"), repos: filepath.Join(opts.StateDir, "repos"), projects: projects, forge: forges,
		git: func(dir string) proc.Runner {
			runner := proc.NewExecRunner("git", dir)
			runner.Env = []string{"GIT_TERMINAL_PROMPT=0"}

			return runner
		},
	}
}

// newPrograms runs command steps in the worktree of the item, or on the host of the project.
func newPrograms(cfg config.Config, trees treeFinder, table adapters) programs {
	return programs{
		trees: trees,
		local: func(binary, dir string, timeout time.Duration) proc.Runner {
			return proc.ExecRunner{Binary: binary, Dir: dir, Timeout: timeout}
		},
		hosts: func(project string) (host.Host, error) {
			found, ok := cfg.Projects[project]
			if !ok {
				return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
			}

			if found.Host == nil {
				return nil, errNoHost
			}

			return table.host(found.Project)
		},
	}
}
