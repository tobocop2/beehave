package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/pool"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

// adapters is the one place that maps an adapter name to its constructor. No other package dispatches on a name.
// The claim store has no table: leases picks beads or the local store by the claims name of the project.
// state returns the state folder, which holds local.db. A build that needs no local store may pass nil.
type adapters struct {
	trackers map[model.TrackerName]trackerBuilder
	forges   map[model.ForgeName]func(model.Project) (forge.Forge, error)
	agents   map[model.Agent]agentAdapter
	hosts    map[model.HostProvider]hostAdapter
	bd       proc.Runner
	state    func() (string, error)
	// diskPoll is how often a job that waits for disk space reads the disk again.
	diskPoll time.Duration
}

// agentAdapter is the registration of an agent: its constructor and its doctor fix texts. The usage read is a method of the adapter that build returns.
type agentAdapter struct {
	build     func() agent.Adapter
	configFix func(string) string
	tokenFix  func(string) string
}

// hostAdapter is the registration of a host provider: the constructor and the sentinel errors of a failed run.
type hostAdapter struct {
	build     func(model.Project) (host.Host, error)
	sentinels []error
}

// defaultDiskPoll is the gap between two disk readings of a job that waits for space.
const defaultDiskPoll = 30 * time.Second

// claimStore is the claim rows of one project and the way to end its use. closer is nil for a store that holds no file.
type claimStore struct {
	claim.Rows

	closer io.Closer
}

// Close ends the use of the store.
func (c claimStore) Close() error {
	if c.closer == nil {
		return nil
	}

	return c.closer.Close()
}

// skyTimeout is the longest one sky call may run. A launch creates a machine, which takes minutes.
const skyTimeout = 30 * time.Minute

// agentEntries returns the agent entries. env is the environment of the caller, which the usage read of an agent passes to its CLI.
func agentEntries(env map[string]string) map[model.Agent]agentAdapter {
	return map[model.Agent]agentAdapter{
		model.AgentClaude: {
			build:     func() agent.Adapter { return claude.New(env) },
			configFix: claude.ConfigFix,
			tokenFix:  claude.TokenFix,
		},
	}
}

// newAdapters returns the table. bd is the command line of beads, for the beads tracker and the claim store.
// state returns the state folder, for the local tracker and the local claim store. env is the environment of the caller, for the usage read of the agents.
func newAdapters(bd proc.Runner, state func() (string, error), env map[string]string) adapters {
	return adapters{
		trackers: newTrackers(bd, state),
		forges: map[model.ForgeName]func(model.Project) (forge.Forge, error){
			model.ForgeGithub: newGithubForge,
		},
		agents: agentEntries(env),
		hosts: map[model.HostProvider]hostAdapter{
			model.HostSkyPilot: {build: newSkyPilotHost, sentinels: []error{skypilot.ErrConfig, link.ErrConfig}},
			model.HostSSH:      {build: newSSHHost, sentinels: []error{link.ErrConfig, ssh.ErrLinger, ssh.ErrSystemd, ssh.ErrDraining}},
			model.HostSlurm:    {build: newSlurmHost, sentinels: []error{link.ErrConfig}},
		},
		bd:       bd,
		state:    state,
		diskPoll: defaultDiskPoll,
	}
}

// trackerBuilder builds the tracker of a project. The context is for the adapter that opens a file.
type trackerBuilder func(context.Context, model.Project) (store.Tracker, error)

// newTrackers returns the constructor of each tracker.
func newTrackers(bd proc.Runner, state func() (string, error)) map[model.TrackerName]trackerBuilder {
	return map[model.TrackerName]trackerBuilder{
		model.TrackerLocal: func(ctx context.Context, project model.Project) (store.Tracker, error) {
			opened, err := openLocal(ctx, state, project)
			if err != nil {
				return nil, err
			}

			return opened, nil
		},
		model.TrackerBeads: func(_ context.Context, project model.Project) (store.Tracker, error) {
			return newBeads(bd, project)
		},
		model.TrackerGithub: func(_ context.Context, project model.Project) (store.Tracker, error) { return newIssues(project) },
	}
}

// openLocal opens local.db in the state folder for the project.
func openLocal(ctx context.Context, state func() (string, error), project model.Project) (*local.Store, error) {
	dir, err := state()
	if err != nil {
		return nil, err
	}

	return local.Open(ctx, dir, project.Name, time.Now)
}

func newSkyPilotHost(project model.Project) (host.Host, error) { //nolint:ireturn // the table stores constructors of the contract
	sky := proc.ExecRunner{Binary: "sky", Timeout: skyTimeout}

	h, err := skypilot.New(project.BuildHost, *project.Host, sky, proc.NewExecRunner("ssh", ""), slurmQueue)
	if err != nil {
		return nil, err
	}

	return h, nil
}

// slurmQueue builds the Slurm queue that SkyPilot runs its jobs on.
func slurmQueue(l *link.Link, config model.HostConfig, slot skypilot.Slot) skypilot.Queue { //nolint:ireturn // SkyPilot takes the queue as a builder that returns the interface it owns
	return slurm.New(l, config, slot)
}

// newSSHHost builds the host of a machine that the owner provides. It runs no ssh: the first call to the machine is Up.
func newSSHHost(project model.Project) (host.Host, error) { //nolint:ireturn // the table stores constructors of the contract
	h, err := ssh.New(project.BuildHost, *project.Host, proc.NewExecRunner("ssh", ""))
	if err != nil {
		return nil, err
	}

	return h, nil
}

// newSlurmHost builds the host of a Slurm cluster that the owner provides, reached at its head node over ssh. It runs no ssh: the first call is Up.
func newSlurmHost(project model.Project) (host.Host, error) { //nolint:ireturn // the table stores constructors of the contract
	h, err := slurm.NewHost(project.BuildHost, *project.Host, proc.NewExecRunner("ssh", ""))
	if err != nil {
		return nil, err
	}

	return h, nil
}

func newBeads(bd proc.Runner, project model.Project) (store.Tracker, error) { //nolint:ireturn // the table stores constructors of the contract
	tracker, err := beads.New(bd, project.BeadsPrefix)
	if err != nil {
		return nil, err
	}

	return tracker, nil
}

func newIssues(project model.Project) (store.Tracker, error) { //nolint:ireturn // the table stores constructors of the contract
	issues, err := storegithub.New(project.Repo, proc.NewExecRunner("gh", ""))
	if err != nil {
		return nil, err
	}

	return issues, nil
}

func newGithubForge(project model.Project) (forge.Forge, error) { //nolint:ireturn // the table stores constructors of the contract
	gh, err := forgegithub.NewExec(project.Repo)
	if err != nil {
		return nil, err
	}

	return gh, nil
}

// tracker builds the tracker the project names.
func (a adapters) tracker(ctx context.Context, project model.Project) (store.Tracker, error) { //nolint:ireturn // the table builds whichever adapter the project names
	build, found := a.trackers[project.Tracker]
	if !found {
		return nil, fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	return build(ctx, project)
}

// forge builds the forge the project names. A project with no forge gets an error that matches forge.ErrNoForge.
func (a adapters) forge(project model.Project) (forge.Forge, error) { //nolint:ireturn // the table builds whichever adapter the project names
	if project.Forge == "" {
		return nil, fmt.Errorf("project %s: %w; set forge = \"github\" in the project file", project.Name, forge.ErrNoForge)
	}

	build, found := a.forges[project.Forge]
	if !found {
		return nil, fmt.Errorf("project %s: no forge adapter named %q", project.Name, project.Forge)
	}

	return build(project)
}

// host builds the build host the project names. A project with no host block builds locally and has none.
func (a adapters) host(project model.Project) (host.Host, error) { //nolint:ireturn // the host block names the adapter to build
	if project.Host == nil {
		return nil, fmt.Errorf("project %s: no host block, so no build host", project.Name)
	}

	entry, found := a.hosts[project.Host.Provider]
	if !found {
		return nil, fmt.Errorf("project %s: no host adapter named %q", project.Name, project.Host.Provider)
	}

	built, err := entry.build(project)
	if err != nil {
		return nil, err
	}

	wait := time.Duration(project.Host.DiskWaitMinutes) * time.Minute

	return host.WithDiskWait(built, project.Host.DiskFloorGB, wait, a.diskPoll), nil
}

// agent builds the adapter of the agent. It returns nil for an agent that has no adapter yet, and a run with no adapter does not start.
func (a adapters) agent(name model.Agent) agent.Adapter { //nolint:ireturn // the table builds whichever adapter the runner names
	entry, found := a.agents[name]
	if !found {
		return nil
	}

	return entry.build()
}

// toolVersions returns the version check of each tool that has one, by tool name.
func (a adapters) toolVersions() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		model.ToolBd: func(ctx context.Context) error { return beads.RequireBd(ctx, a.bd) },
	}
}

// logins returns the usage read of each agent, by agent.
func (a adapters) logins() map[model.Agent]func(context.Context, model.Runner) model.Reading {
	logins := map[model.Agent]func(context.Context, model.Runner) model.Reading{}

	for name, entry := range a.agents {
		logins[name] = entry.build().ReadUsage
	}

	return logins
}

// configFixes returns the fix of the config folder of each agent that declares one, by agent.
func (a adapters) configFixes() map[model.Agent]func(string) string {
	fixes := map[model.Agent]func(string) string{}

	for name, entry := range a.agents {
		if entry.configFix != nil {
			fixes[name] = entry.configFix
		}
	}

	return fixes
}

// tokenFixes returns the fix of the Keychain entry of each agent that declares one, by agent.
func (a adapters) tokenFixes() map[model.Agent]func(string) string {
	fixes := map[model.Agent]func(string) string{}

	for name, entry := range a.agents {
		if entry.tokenFix != nil {
			fixes[name] = entry.tokenFix
		}
	}

	return fixes
}

// readUsage reads the usage of one runner through the adapter of its agent. A runner of an agent with no entry is unknown, so the pool pick never chooses it.
func readUsage(entries map[model.Agent]agentAdapter) pool.Reader {
	return func(ctx context.Context, runner model.Runner) model.Reading {
		entry, found := entries[runner.Agent]
		if !found {
			return model.Unknown(fmt.Sprintf("the %s agent cannot report usage", runner.Agent))
		}

		return entry.build().ReadUsage(ctx, runner)
	}
}

// hostErrors lists the sentinel errors that the host entries declare, each once, in the order of the provider names.
func (a adapters) hostErrors() []error {
	var sentinels []error

	for _, name := range slices.Sorted(maps.Keys(a.hosts)) {
		for _, sentinel := range a.hosts[name].sentinels {
			if !slices.Contains(sentinels, sentinel) {
				sentinels = append(sentinels, sentinel)
			}
		}
	}

	return sentinels
}

// leases opens the claim store the project names: the beads rows, or the claim rows of local.db. The caller closes it.
func (a adapters) leases(ctx context.Context, project model.Project) (claimStore, error) {
	switch project.Claims {
	case model.ClaimsBeads:
		rows, err := beads.New(a.bd, project.BeadsPrefix)
		if err != nil {
			return claimStore{}, err
		}

		return claimStore{Rows: rows}, nil
	case model.ClaimsLocal:
		rows, err := openLocal(ctx, a.state, project)
		if err != nil {
			return claimStore{}, err
		}

		return claimStore{Rows: rows, closer: rows}, nil
	default:
		return claimStore{}, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}
}

// itemOpener returns what the command line calls to open the item list of a project with the local tracker. The caller closes the list.
func (a adapters) itemOpener() cli.ItemOpener {
	return func(ctx context.Context, project model.Project) (cli.ItemStore, error) {
		opened, err := openLocal(ctx, a.state, project)
		if err != nil {
			return nil, err
		}

		return opened, nil
	}
}

// checkTrackerAndForge says why the tracker or the forge of the project cannot be built. The local tracker needs no file, so a check leaves the state folder alone.
func (a adapters) checkTrackerAndForge(ctx context.Context, project model.Project) error {
	if project.Tracker != model.TrackerLocal {
		if _, err := a.tracker(ctx, project); err != nil {
			return err
		}
	}

	if project.Forge == "" {
		return nil
	}

	_, err := a.forge(project)

	return err
}

// checkClaims says why the claim store of the project cannot be built. It opens no file.
func (a adapters) checkClaims(project model.Project) error {
	switch project.Claims {
	case model.ClaimsBeads:
		_, err := beads.New(a.bd, project.BeadsPrefix)

		return err
	case model.ClaimsLocal:
		return nil
	default:
		return fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}
}

// check builds what the project names and reports why it cannot: the tracker, the forge, the build host and the claim store.
// A local tracker and a local claim store open no file here. A project that names beads needs a bd that has leases and the guards of bd update, so check also refuses an older bd.
// A project that names no beads never calls bd. `beehave check config` calls check for each project.
func (a adapters) check(ctx context.Context, project model.Project) error {
	if err := a.checkTrackerAndForge(ctx, project); err != nil {
		return err
	}

	if err := a.checkClaims(project); err != nil {
		return err
	}

	if project.Host != nil {
		if _, err := a.host(project); err != nil {
			return err
		}
	}

	if !slices.Contains(project.Tools(), model.ToolBd) {
		return nil
	}

	return beads.RequireBd(ctx, a.bd)
}
