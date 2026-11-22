package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/fstree"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/pool"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/sandbox/enforcers"
	"github.com/tobocop2/beehave/internal/step"
)

var (
	errUnknownProject = errors.New("the config has no project of this name")
	errUnknownRunner  = errors.New("the config has no runner of this name")
)

// logins picks the runner of an agent step that names none, from the pool.
// pool returns the name of the runner that has room under the usage caps. It prefers a runner that busy does not name.
// overCap does the same pick for a run that may go over a cap. reset returns when a runner gets room again, from the same usage read.
type logins struct {
	projects map[string]config.Project
	pool     func(ctx context.Context, busy []string) (string, error)
	overCap  func(ctx context.Context, busy []string) (string, error)
	reset    func(ctx context.Context, login string) (time.Time, bool, error)
}

// newLogins reads the projects of the config. The pool pick reads the usage of each runner through the agent entries.
func newLogins(cfg config.Config, opts flow.Options) logins {
	return loginsReading(cfg, readUsage(agentEntries(opts.Env)))
}

// loginsReading is newLogins with the reader of the usage given. A test of the whole command gives a reader that needs no login.
func loginsReading(cfg config.Config, read pool.Reader) logins {
	return logins{projects: cfg.Projects, pool: poolPick(cfg, stepAgent, read, pool.Select), overCap: poolPick(cfg, stepAgent, read, pool.SelectOverCap), reset: poolReset(cfg, read)}
}

// poolReset returns the function that reads the usage of one runner and gives the time when it gets room under the usage caps, and false when the read knows none.
func poolReset(cfg config.Config, read pool.Reader) func(ctx context.Context, login string) (time.Time, bool, error) {
	return func(ctx context.Context, login string) (time.Time, bool, error) {
		for _, runner := range cfg.Runners {
			if runner.Name == login {
				reset, known := pool.EarliestReset(map[string]model.Reading{login: read(ctx, runner)}, cfg.Pool)

				return reset, known, nil
			}
		}

		return time.Time{}, false, fmt.Errorf("%w: %q", errUnknownRunner, login)
	}
}

// Reset returns when the login gets room again, from its usage read. A project that the config lacks is an error: no guess.
func (l logins) Reset(ctx context.Context, project, login string) (time.Time, bool, error) {
	if _, ok := l.projects[project]; !ok {
		return time.Time{}, false, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	return l.reset(ctx, login)
}

// poolPick returns the name of the runner of the agent kind that read finds with the select function: under both usage caps for pool.Select, the lowest use for pool.SelectOverCap.
// A runner of another agent kind is never returned, whatever its usage.
func poolPick(cfg config.Config, kind model.Agent, read pool.Reader, selectRunner selector) func(ctx context.Context, busy []string) (string, error) {
	return func(ctx context.Context, busy []string) (string, error) {
		runner, err := selectRunner(ctx, cfg.Runners, cfg.Pool, kind, read, busy...)
		if err != nil {
			return "", fmt.Errorf("pick a runner from the pool: %w", err)
		}

		return runner.Name, nil
	}
}

// selector is the signature of pool.Select and pool.SelectOverCap.
type selector func(ctx context.Context, runners []model.Runner, caps config.Pool, agent model.Agent, read pool.Reader, busy ...string) (model.Runner, error)

// Pick returns the pool pick for an item. busy lists the logins that have an agent step running or queued. A project that the config lacks is an error: no guess.
// overCap takes the runner with the lowest use even when it is over a cap.
func (l logins) Pick(ctx context.Context, project, _ string, busy []string, overCap bool) (string, error) {
	if _, ok := l.projects[project]; !ok {
		return "", fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	if overCap {
		return l.overCap(ctx, busy)
	}

	return l.pool(ctx, busy)
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
			// The client never reads usage, so the table needs no environment.
			table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), nil)

			client, err := flow.OpenClient(ctx, opts, releaseDeps(cfg, opts, table))
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
	table := newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), opts.Env)
	if err := table.writers.start(ctx, cfg, opts); err != nil {
		return nil, err
	}

	return flow.NewEngine(ctx, withModules(opts), newDeps(cfg, opts, table))
}

// stepModules lists the step modules that the engine runs.
func stepModules() []step.Module {
	return []step.Module{flow.StepModule()}
}

// withModules gives the options the step modules of the binary.
func withModules(opts flow.Options) flow.Options {
	opts.Modules = stepModules()

	return opts
}

// newDeps builds what the engine of the run command needs. The pool pick reads the usage through the agent entries.
func newDeps(cfg config.Config, opts flow.Options, table adapters) flow.Deps {
	return depsOn(cfg, opts, newLogins(cfg, opts), table)
}

// newEngineReading builds the engine with the reader of the usage that the pool pick uses.
func newEngineReading(ctx context.Context, cfg config.Config, opts flow.Options, read pool.Reader) (*flow.Engine, error) {
	return newEngineOn(ctx, cfg, opts, read, newAdapters(proc.NewExecRunner("bd", ""), stateFolder(opts.StateDir), opts.Env))
}

// newEngineOn builds the engine over a table of adapters. A test passes a table with one adapter replaced.
func newEngineOn(ctx context.Context, cfg config.Config, opts flow.Options, read pool.Reader, table adapters) (*flow.Engine, error) {
	if err := table.writers.start(ctx, cfg, opts); err != nil {
		return nil, err
	}

	return flow.NewEngine(ctx, withModules(opts), depsOn(cfg, opts, loginsReading(cfg, read), table))
}

// depsOn builds the deps of the engine over a pool pick and a table of adapters.
func depsOn(cfg config.Config, opts flow.Options, picks logins, table adapters) flow.Deps {
	trees := newWorktrees(cfg.Projects, opts, table.forge, table.writers)
	life := newLife(cfg, table, trees)
	folders := &fstree.Folders{}
	commands := newPrograms(cfg, trees, cloneLock{repos: trees.repos}, table, folders)

	return flow.Deps{
		Agents: newAgents(cfg, opts, trees, table, folders), Gate: projectGate{projects: cfg.Projects, programs: commands, forge: table.forge}, Logins: picks, Lifecycle: life, Forge: life, Merge: life, Preflight: life, Trees: trees, Beats: life, Others: life,
		Commands: commands, Items: trackerItems{projects: cfg.Projects, tracker: table.tracker}, Rounds: roundWrites{pulls: life.pulls},
		Writer: trackerWriter{projects: cfg.Projects, tracker: table.tracker}, Reader: trackerReader{projects: cfg.Projects, tracker: table.tracker},
		Batch: batchHost{pulls: life.pulls, programs: commands, base: trees},
		Hosts: hostStop{programs: commands},
		Queue: queueReads{items: trackerItems{projects: cfg.Projects, tracker: table.tracker}, pulls: life.pulls},
		Poll:  pollReads{projects: cfg.Projects, pulls: life.pulls},
		Kept:  table.hostErrors(),

		SandboxDefault: cfg.SandboxDefault,
		RefuseGate:     gateRefusal(cfg.Projects, enforcers.Refusal),
		Repository:     repositoryOf(cfg.Projects),
		Facts:          factsOf(cfg, table), Slots: slotsOf(cfg.Projects), SkillLine: skillLineOf(cfg.Runners, table),
	}
}

// skillLineOf returns the line that makes the agent CLI of a runner load a skill before its first turn. It is empty for a runner that the config lacks,
// for a runner whose agent has no adapter, and for an agent CLI with no such line.
func skillLineOf(runners []model.Runner, table adapters) func(runner, skill string) string {
	return func(runner, skill string) string {
		for _, found := range runners {
			if found.Name != runner {
				continue
			}

			if adapter := table.agent(found.Agent); adapter != nil {
				if line := adapter.Profile().SkillLine; line != nil {
					return line(skill)
				}
			}
		}

		return ""
	}
}

// factsOf returns the project as the config holds it, with the runner facts that the agents of the table give. A project that the config does not hold has no facts.
func factsOf(cfg config.Config, table adapters) func(string) model.Project {
	loads := runnerSkillLoads(cfg.Runners, table)

	return func(project string) model.Project {
		found, ok := cfg.Projects[project]
		if !ok {
			return model.Project{}
		}

		facts := found.Project
		facts.RunnerSkillLoads = loads

		return facts
	}
}

// runnerSkillLoads says for each runner whether its agent CLI has a line that loads a skill before the first turn. A runner whose agent has no adapter is left out.
func runnerSkillLoads(runners []model.Runner, table adapters) map[string]bool {
	loads := make(map[string]bool, len(runners))

	for _, runner := range runners {
		if adapter := table.agent(runner.Agent); adapter != nil {
			loads[runner.Name] = adapter.Profile().SkillLine != nil
		}
	}

	return loads
}

// repositoryOf tells whether a project has a git repository. A project that the config does not hold counts as one that has it.
func repositoryOf(projects map[string]config.Project) func(string) bool {
	return func(project string) bool {
		found, ok := projects[project]

		return !ok || found.HasRepository()
	}
}

// releaseDeps builds what the client needs: the claim to take and give back, the lease reader of a move to this build, and the worktrees.
// The projects tell the worktrees and the release which projects have no repository, so a release never reads git in a plain folder. The forge of the
// table gives the address at which the release reads the branches of the remote.
func releaseDeps(cfg config.Config, opts flow.Options, table adapters) flow.ReleaseDeps {
	trees := newWorktrees(cfg.Projects, opts, table.forge, table.writers)
	life := newLife(cfg, table, trees)

	return flow.ReleaseDeps{Lifecycle: life, Beats: life, Trees: trees, Repository: repositoryOf(cfg.Projects), Merge: life}
}

// newLife builds the claim, the renewal and the forge calls of the merge step. The pull request of an item is on the branch named for the item.
// The same value is the merger of a merge step with `merge = true`. The runners of the config count as own identities there. A push runs in a worktree of trees.
func newLife(cfg config.Config, table adapters, trees worktrees) itemLife {
	runners := make([]string, len(cfg.Runners))
	for i, runner := range cfg.Runners {
		runners[i] = runner.Name
	}

	return itemLife{
		leases: leases{projects: cfg.Projects, rows: table.leases, owner: ownerToken},
		pulls:  pulls{projects: cfg.Projects, forge: table.forge, runners: runners, trees: trees, copier: newHeadCopy(), writers: table.writers},
	}
}

// newWorktrees finds item worktrees in the trees folder of the state folder. It adds a missing one from the clone of the project in the repos folder.
// The client of the commands only reads worktrees; it passes the projects so that it knows which have no repository.
// Every git call sets GIT_TERMINAL_PROMPT=0, so a private repository fails and never waits for a password. A git call in the clone or a worktree is
// local (see gitexec), so no setting in the clone config can send it to the network. roles is the writers table of the caller, so no worktrees value exists
// without it: a call to a remote of a project with credentials runs as its writer, or is refused when the roles are not loaded, as in the client.
func newWorktrees(projects map[string]config.Project, opts flow.Options, forges func(model.Project) (forge.Forge, error), roles *writers) worktrees {
	repos := reposFolder(opts.StateDir)
	found := gitexec.NewRepositories(repos)

	return worktrees{
		root: filepath.Join(opts.StateDir, "trees"), repos: repos, projects: projects, forge: forges, roles: roles,
		git: func(dir string) proc.Runner { return noPrompt(found.Runner(dir, proc.DefaultTimeout)) },
	}
}

// reposFolder returns the folder of the clones of the projects in the state folder.
func reposFolder(stateDir string) string {
	return filepath.Join(stateDir, "repos")
}

// noPromptEnv is the git setting that fails a call that would ask for a password.
const noPromptEnv = "GIT_TERMINAL_PROMPT=0"

// noPrompt sets GIT_TERMINAL_PROMPT=0 on the runner.
func noPrompt(runner gitexec.Runner) proc.Runner { //nolint:ireturn // the worktrees take the runner as the interface
	runner.Env = []string{noPromptEnv}

	return runner
}

// localRunner returns the runner of a program of a project with the environment of the process. A git call runs with the hooks of the repository off,
// with the git of the project (see gitOf), and it is local (see gitexec).
func localRunner(projects map[string]config.Project, found gitexec.Repositories) func(project, binary, dir string, timeout time.Duration) proc.Runner {
	return func(project, binary, dir string, timeout time.Duration) proc.Runner {
		if gitexec.IsGit(binary) {
			return gitOf(projects, project, found).Runner(dir, timeout)
		}

		return proc.ExecRunner{Binary: binary, Dir: dir, Timeout: timeout}
	}
}

// newPrograms runs command steps in the worktree of the item, or on the host of the project.
// The sandbox of a gate holds the lock of the clone, clones, while it adds or removes the worktree of its copy.
func newPrograms(cfg config.Config, trees programTrees, clones cloneLock, table adapters, folders *fstree.Folders) programs {
	return programs{
		trees:    trees,
		sandbox:  newGateSandbox(cfg.Projects, clones, folders),
		local:    localRunner(cfg.Projects, gitexec.NewRepositories(clones.repos)),
		exec:     func(spec proc.ExecRunner) proc.Runner { return spec },
		hosts:    func(project, cluster string) (host.Host, error) { return buildHostOf(cfg, table, project, cluster) },
		declared: func(project, cluster string) (string, error) { return declaredHostOf(cfg, project, cluster) },
	}
}

// buildHostOf builds the host of the cluster that a run chose for the project. The adapter gets the project with that cluster as its build host.
func buildHostOf(cfg config.Config, table adapters, project, cluster string) (host.Host, error) { //nolint:ireturn // the host contract
	found, ok := cfg.Projects[project]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	if found.Host == nil {
		return nil, errNoHost
	}

	chosen, err := found.HostFor(cluster)
	if err != nil {
		return nil, err
	}

	found.BuildHost = chosen

	return table.host(found.Project)
}

// declaredHostOf gives the cluster a run chose for the project, or refuses a cluster that the project file does not declare.
func declaredHostOf(cfg config.Config, project, cluster string) (string, error) {
	found, ok := cfg.Projects[project]
	if !ok {
		return "", fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	return found.HostFor(cluster)
}
