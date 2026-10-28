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
)

var errUnknownProject = errors.New("the config has no project of this name")

// logins picks the runner of an agent step that names none, from the pool.
// pool returns the name of the runner that has room under the usage caps.
type logins struct {
	projects map[string]config.Project
	pool     func(ctx context.Context) (string, error)
}

// newLogins reads the projects of the config. The pool pick reads the usage of each claude runner.
func newLogins(cfg config.Config, opts flow.Options) logins {
	return loginsReading(cfg, claude.UsageReader{Base: opts.Env}.Read)
}

// loginsReading is newLogins with the reader of the usage given. A test of the whole command gives a reader that needs no login.
func loginsReading(cfg config.Config, read pool.Reader) logins {
	return logins{projects: cfg.Projects, pool: poolPick(cfg, read)}
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

// Pick returns the pool pick for an item. A project that the config lacks is an error: no guess.
func (l logins) Pick(ctx context.Context, project, _ string) (string, error) {
	if _, ok := l.projects[project]; !ok {
		return "", fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	return l.pool(ctx)
}

// flowFactory builds the engine and the client of the commands. version is the build version.
func flowFactory() cli.Flow {
	return flowFactoryFrom(newEngine)
}

// flowFactoryFrom is flowFactory with the builder of the engine given. A test of the whole command builds the real engine with a reader of the usage that needs no login.
func flowFactoryFrom(build func(context.Context, config.Config, flow.Options) (*flow.Engine, error)) cli.Flow {
	return cli.Flow{
		Engine: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Runner, error) {
			engine, err := build(ctx, cfg, opts)
			if err != nil {
				return nil, err
			}

			return engine, nil
		},
		Client: func(ctx context.Context, cfg config.Config, opts flow.Options) (cli.Steering, error) {
			table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir))

			client, err := flow.OpenClient(ctx, opts, flow.ReleaseDeps{Lifecycle: newLife(cfg, table), Trees: newWorktrees(nil, opts, nil)})
			if err != nil {
				return nil, err
			}

			return client, nil
		},
	}
}

// stateFolder returns the state folder of the options as the answer of a function, which is how the table of adapters asks for it.
func stateFolder(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

// newEngine builds the engine of the run command.
func newEngine(ctx context.Context, cfg config.Config, opts flow.Options) (*flow.Engine, error) {
	return newEngineReading(ctx, cfg, opts, claude.UsageReader{Base: opts.Env}.Read)
}

// newEngineReading builds the engine with the reader of the usage that the pool pick uses.
func newEngineReading(ctx context.Context, cfg config.Config, opts flow.Options, read pool.Reader) (*flow.Engine, error) {
	table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir))
	trees := newWorktrees(cfg.Projects, opts, table.forge)
	life := newLife(cfg, table)
	commands := newPrograms(cfg, trees, table)

	return flow.NewEngine(ctx, opts, flow.Deps{
		Agents: newAgents(cfg, opts, trees, table), Gate: projectGate{projects: cfg.Projects, programs: commands, forge: table.forge}, Logins: loginsReading(cfg, read), Lifecycle: life, Forge: life, Watch: life, Merge: life, Trees: trees, Beats: life,
		Commands: commands, Items: trackerItems{projects: cfg.Projects, tracker: table.tracker},
	})
}

// newLife builds the claim, the renewal and the forge calls of the merge step. The pull request of an item is on the branch named for the item.
// The same value is the merger of a merge step with `merge = true`. The runners of the config count as own identities there.
func newLife(cfg config.Config, table adapters) itemLife {
	runners := make([]string, len(cfg.Runners))
	for i, runner := range cfg.Runners {
		runners[i] = runner.Name
	}

	return itemLife{
		leases: leases{projects: cfg.Projects, rows: table.leases, owner: ownerToken},
		pulls:  pulls{projects: cfg.Projects, forge: table.forge, runners: runners},
	}
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
