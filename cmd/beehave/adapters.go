package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
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

// The names the table registers that no adapter package owns yet.
const (
	openCodeName model.Agent = "opencode"
	repoKey                  = "repo"
)

// adapters is the one place that maps an adapter name to its registration: its constructor, its config block,
// the project keys and programs it needs, and the doctor checks of its kind. No other package dispatches on a name.
// state returns the state folder, which holds local.db. A build that needs no local store may pass nil.
type adapters struct {
	trackers map[model.TrackerName]trackerAdapter
	claims   map[model.ClaimsName]claimsAdapter
	forges   map[model.ForgeName]forgeAdapter
	agents   map[model.Agent]agentAdapter
	hosts    map[model.HostProvider]hostAdapter
	bd       proc.Runner
	state    func() (string, error)
	env      map[string]string
	// diskPoll is how often a job that waits for disk space reads the disk again.
	diskPoll time.Duration
}

// registration is what every adapter kind shares: the config block, the project keys, the programs it needs,
// and the version check of a program, by tool name, for the doctor.
type registration struct {
	config.Entry
	versions map[string]func(context.Context) error
}

// trackerAdapter is the registration of a tracker: its block and tools, and the constructor.
type trackerAdapter struct {
	registration
	build trackerBuilder
	// check says why the tracker cannot be built, or is nil when a check opens no file.
	check func(ctx context.Context, project model.Project) error
}

// claimsAdapter is the registration of a claim store: its block and tools, and the constructor.
type claimsAdapter struct {
	registration
	open  func(ctx context.Context, project model.Project) (claimStore, error)
	check func(ctx context.Context, project model.Project) error
}

// forgeAdapter is the registration of a forge: its project keys and tools, and the constructor.
type forgeAdapter struct {
	registration
	build func(model.Project) (forge.Forge, error)
}

// agentAdapter is the registration of an agent: its constructor, its usage read and its doctor fix texts.
type agentAdapter struct {
	registration
	build     func() agent.Adapter
	configFix func(string) string
	tokenFix  func(string) string
}

// hostAdapter is the registration of a host provider: its block, the constructor and the sentinel errors of a failed run.
type hostAdapter struct {
	registration
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

// blockOf returns a pointer to a declared block, for the registry.
func blockOf(declared config.Block) *config.Block { return &declared }

// newAdapters returns the table. bd is the command line of beads, for the beads tracker and the claim store.
// state returns the state folder, for the local tracker and the local claim store. env is the environment that the agent usage read passes to its CLI.
func newAdapters(bd proc.Runner, state func() (string, error), env map[string]string) adapters {
	return adapters{
		trackers: map[model.TrackerName]trackerAdapter{
			local.Tracker: {
				build: func(ctx context.Context, project model.Project) (store.Tracker, error) {
					return openLocal(ctx, state, project)
				},
				check: func(context.Context, model.Project) error { return nil },
			},
			beads.Tracker: {
				registration: registration{
					Entry:    config.Entry{Block: blockOf(beads.Block()), Tools: []string{beads.Tool}},
					versions: map[string]func(context.Context) error{beads.Tool: func(ctx context.Context) error { return beads.RequireBd(ctx, bd) }},
				},
				build: func(_ context.Context, project model.Project) (store.Tracker, error) { return newBeads(bd, project) },
			},
			storegithub.Tracker: {
				registration: registration{Entry: config.Entry{Keys: []string{repoKey}, Tools: []string{storegithub.Tool}}},
				build:        func(_ context.Context, project model.Project) (store.Tracker, error) { return newIssues(project) },
			},
		},
		claims: map[model.ClaimsName]claimsAdapter{
			local.Claims: {
				open: func(ctx context.Context, project model.Project) (claimStore, error) { return openLocalClaims(ctx, state, project) },
				check: func(context.Context, model.Project) error {
					return nil
				},
			},
			beads.Claims: {
				registration: registration{
					Entry:    config.Entry{Block: blockOf(beads.Block()), Tools: []string{beads.Tool}},
					versions: map[string]func(context.Context) error{beads.Tool: func(ctx context.Context) error { return beads.RequireBd(ctx, bd) }},
				},
				open:  func(_ context.Context, project model.Project) (claimStore, error) { return newBeadsClaims(bd, project) },
				check: func(_ context.Context, project model.Project) error { return checkBeadsClaims(bd, project) },
			},
		},
		forges: map[model.ForgeName]forgeAdapter{
			forgegithub.Name: {
				registration: registration{Entry: config.Entry{Keys: []string{repoKey}, Tools: []string{forgegithub.Tool}}},
				build:        newGithubForge,
			},
		},
		agents: map[model.Agent]agentAdapter{
			claude.Name: {
				build:     func() agent.Adapter { return claude.New(env) },
				configFix: claude.ConfigFix,
				tokenFix:  claude.TokenFix,
			},
			openCodeName: {},
		},
		hosts: map[model.HostProvider]hostAdapter{
			skypilot.Name: {
				registration: registration{Entry: config.Entry{Block: blockOf(skypilot.Block()), Idle: true}},
				build:        newSkyPilotHost,
				sentinels:    []error{skypilot.ErrConfig, link.ErrConfig},
			},
			ssh.Name: {
				registration: registration{Entry: config.Entry{Block: blockOf(ssh.Block())}},
				build:        newSSHHost,
				sentinels:    []error{link.ErrConfig, ssh.ErrLinger, ssh.ErrSystemd, ssh.ErrDraining},
			},
			slurm.Name: {
				registration: registration{Entry: config.Entry{Block: blockOf(slurm.Block())}},
				build:        newSlurmHost,
				sentinels:    []error{link.ErrConfig},
			},
		},
		bd:       bd,
		state:    state,
		env:      env,
		diskPoll: defaultDiskPoll,
	}
}

// registry returns the names, blocks and tools of the table as the config reads them.
func (a adapters) registry() config.Adapters {
	registered := config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{},
		Claims:   map[model.ClaimsName]*config.Entry{},
		Forges:   map[model.ForgeName]*config.Entry{},
		Hosts:    map[model.HostProvider]*config.Entry{},
		Agents:   map[model.Agent]*config.Entry{},
	}

	for name, entry := range a.trackers {
		registered.Trackers[name] = &entry.Entry
	}

	for name, entry := range a.claims {
		registered.Claims[name] = &entry.Entry
	}

	for name, entry := range a.forges {
		registered.Forges[name] = &entry.Entry
	}

	for name, entry := range a.hosts {
		registered.Hosts[name] = &entry.Entry
	}

	for name, entry := range a.agents {
		registered.Agents[name] = &entry.Entry
	}

	return registered
}

// trackerBuilder builds the tracker of a project. The context is for the adapter that opens a file.
type trackerBuilder func(context.Context, model.Project) (store.Tracker, error)

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
func slurmQueue(l *link.Link, config model.HostConfig, slot skypilot.Slot) skypilot.Queue {
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
	tracker, err := beads.New(bd, project.Block)
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

// openLocalClaims opens local.db in the state folder for the claim rows of the project.
func openLocalClaims(ctx context.Context, state func() (string, error), project model.Project) (claimStore, error) {
	rows, err := openLocal(ctx, state, project)
	if err != nil {
		return claimStore{}, err
	}

	return claimStore{Rows: rows, closer: rows}, nil
}

func newBeadsClaims(bd proc.Runner, project model.Project) (claimStore, error) {
	rows, err := beads.New(bd, project.Block)
	if err != nil {
		return claimStore{}, err
	}

	return claimStore{Rows: rows}, nil
}

func checkBeadsClaims(bd proc.Runner, project model.Project) error {
	_, err := beads.New(bd, project.Block)

	return err
}

// tracker builds the tracker the project names.
func (a adapters) tracker(ctx context.Context, project model.Project) (store.Tracker, error) { //nolint:ireturn // the table builds whichever adapter the project names
	entry, found := a.trackers[project.Tracker]
	if !found {
		return nil, fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	return entry.build(ctx, project)
}

// forge builds the forge the project names. A project with no forge gets an error that matches forge.ErrNoForge.
func (a adapters) forge(project model.Project) (forge.Forge, error) { //nolint:ireturn // the table builds whichever adapter the project names
	if project.Forge == "" {
		return nil, fmt.Errorf("project %s: %w; set forge = \"github\" in the project file", project.Name, forge.ErrNoForge)
	}

	entry, found := a.forges[project.Forge]
	if !found {
		return nil, fmt.Errorf("project %s: no forge adapter named %q", project.Name, project.Forge)
	}

	return entry.build(project)
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
	if !found || entry.build == nil {
		return nil
	}

	return entry.build()
}

// toolVersions returns the version check of each tool that an entry declares, by tool name.
func (a adapters) toolVersions() map[string]func(context.Context) error {
	checks := map[string]func(context.Context) error{}

	for _, entry := range a.trackers {
		addVersions(checks, entry.registration.versions)
	}

	for _, entry := range a.claims {
		addVersions(checks, entry.registration.versions)
	}

	return checks
}

// addVersions adds the version checks of one entry to checks.
func addVersions(checks, versions map[string]func(context.Context) error) {
	for tool, check := range versions {
		checks[tool] = check
	}
}

// logins returns the usage read of each agent that can report it, by agent.
func (a adapters) logins() map[model.Agent]func(context.Context, model.Runner) model.Reading {
	logins := map[model.Agent]func(context.Context, model.Runner) model.Reading{}

	for name, entry := range a.agents {
		if entry.build == nil {
			continue
		}

		logins[name] = func(ctx context.Context, runner model.Runner) model.Reading {
			return a.agent(name).ReadUsage(ctx, runner)
		}
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

// readUsage reads the usage of one runner through the adapter of its agent. An agent that reports it cannot read usage keeps its runner out of the pool pick.
func (a adapters) readUsage() pool.Reader {
	return func(ctx context.Context, runner model.Runner) model.Reading {
		adapter := a.agent(runner.Agent)
		if adapter == nil {
			return model.Unknown(fmt.Sprintf("the %s agent cannot report usage", runner.Agent))
		}

		return adapter.ReadUsage(ctx, runner)
	}
}

// hostErrors lists the sentinel errors that the host entries declare, each once.
func (a adapters) hostErrors() []error {
	var sentinels []error

	for _, entry := range a.hosts {
		for _, sentinel := range entry.sentinels {
			if !slices.Contains(sentinels, sentinel) {
				sentinels = append(sentinels, sentinel)
			}
		}
	}

	return sentinels
}

// leases opens the claim store the project names: the beads rows, or the claim rows of local.db. The caller closes it.
func (a adapters) leases(ctx context.Context, project model.Project) (claimStore, error) {
	entry, found := a.claims[project.Claims]
	if !found {
		return claimStore{}, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}

	return entry.open(ctx, project)
}

// itemOpener returns what the command line calls to open the item list of a project, by the tracker that owns the list.
func (a adapters) itemOpener() map[model.TrackerName]cli.ItemOpener {
	return map[model.TrackerName]cli.ItemOpener{
		local.Tracker: func(ctx context.Context, project model.Project) (cli.ItemStore, error) {
			return openLocal(ctx, a.state, project)
		},
	}
}

// checkTrackerAndForge says why the tracker or the forge of the project cannot be built. A local tracker needs no file, so a check leaves the state folder alone.
func (a adapters) checkTrackerAndForge(ctx context.Context, project model.Project) error {
	entry, found := a.trackers[project.Tracker]
	if !found {
		return fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	if entry.check != nil {
		if err := entry.check(ctx, project); err != nil {
			return err
		}
	} else if _, err := entry.build(ctx, project); err != nil {
		return err
	}

	if project.Forge == "" {
		return nil
	}

	_, err := a.forge(project)

	return err
}

// checkClaims says why the claim store of the project cannot be built. It opens no file.
func (a adapters) checkClaims(ctx context.Context, project model.Project) error {
	entry, found := a.claims[project.Claims]
	if !found {
		return fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}

	if entry.check == nil {
		return nil
	}

	return entry.check(ctx, project)
}

// check builds what the project names and reports why it cannot: the tracker, the forge, the build host and the claim store.
// A local tracker and a local claim store open no file here. A project that names beads needs a bd that has leases and the guards of bd update, so check also refuses an older bd.
// A project that names no beads never calls bd. `beehave check config` calls check for each project.
func (a adapters) check(ctx context.Context, project model.Project) error {
	if err := a.checkTrackerAndForge(ctx, project); err != nil {
		return err
	}

	if err := a.checkClaims(ctx, project); err != nil {
		return err
	}

	if project.Host != nil {
		if _, err := a.host(project); err != nil {
			return err
		}
	}

	if !slices.Contains(project.Tools, beads.Tool) {
		return nil
	}

	return beads.RequireBd(ctx, a.bd)
}
