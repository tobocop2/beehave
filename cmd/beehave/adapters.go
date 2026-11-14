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

// adapters is the one place that maps an adapter name to its registration: its constructor, the project keys and programs it needs, and its doctor checks.
// No other package dispatches on a name. state returns the state folder, which holds local.db. A build that needs no local store may pass nil.
type adapters struct {
	trackers map[model.TrackerName]trackerAdapter
	claims   map[model.ClaimsName]claimsAdapter
	forges   map[model.ForgeName]forgeAdapter
	agents   map[model.Agent]agentAdapter
	hosts    map[model.HostProvider]hostAdapter
	bd       proc.Runner
	state    func() (string, error)
	// diskPoll is how often a job that waits for disk space reads the disk again.
	diskPoll time.Duration
}

// registration is what the entries of one kind share: what the config reads, and the version check of each program the adapter needs, by tool name.
type registration struct {
	config.Entry
	versions map[string]func(context.Context) error
}

// trackerAdapter is the registration of a tracker: the constructor, and the opener of the item list when the tracker holds one locally.
type trackerAdapter struct {
	registration
	build trackerBuilder
	// check says why the tracker cannot be built. A nil check builds the tracker.
	check func(ctx context.Context, project model.Project) error
	// items opens the item list of the tracker. It is nil for a tracker that keeps its items elsewhere.
	items cli.ItemOpener
}

// claimsAdapter is the registration of a claim store: the opener, and the check that opens no file.
type claimsAdapter struct {
	registration
	open  func(ctx context.Context, project model.Project) (claimStore, error)
	check func(project model.Project) error
}

// forgeAdapter is the registration of a forge: the constructor.
type forgeAdapter struct {
	registration
	build func(model.Project) (forge.Forge, error)
}

// agentAdapter is the registration of an agent: its constructor and its doctor fix texts. The usage read is a method of the adapter that build returns.
// An agent with no constructor has no adapter yet: its runners are valid in the config and do not start.
type agentAdapter struct {
	registration
	build     func() agent.Adapter
	configFix func(string) string
	tokenFix  func(string) string
}

// hostAdapter is the registration of a host provider: the constructor and the sentinel errors of a failed run.
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

// beadsPrefixKey is the project key that the beads tracker and the beads claim store require.
const beadsPrefixKey = "beads_prefix"

// agentEntries returns the agent entries. env is the environment of the caller, which the usage read of an agent passes to its CLI.
// The opencode agent has no adapter yet, so its entry has no constructor.
func agentEntries(env map[string]string) map[model.Agent]agentAdapter {
	return map[model.Agent]agentAdapter{
		model.AgentClaude: {
			build:     func() agent.Adapter { return claude.New(env) },
			configFix: claude.ConfigFix,
			tokenFix:  claude.TokenFix,
		},
		model.AgentOpenCode: {},
	}
}

// beadsRegistration is what the beads tracker and the beads claim store share: the key they require, the program they need and its version check.
func beadsRegistration(bd proc.Runner) registration {
	return registration{
		Entry:    config.Entry{Keys: []string{beadsPrefixKey}, Tools: []string{beads.Tool}},
		versions: map[string]func(context.Context) error{beads.Tool: func(ctx context.Context) error { return beads.RequireBd(ctx, bd) }},
	}
}

// githubRegistration is what the GitHub tracker and the GitHub forge share: the repo key, and the program that the adapter names.
func githubRegistration(tool string) registration {
	return registration{Entry: config.Entry{Keys: []string{repoKey}, Tools: []string{tool}}}
}

// repoKey is the project key that the GitHub tracker and the GitHub forge require.
const repoKey = "repo"

// newAdapters returns the table. bd is the command line of beads, for the beads tracker and the claim store.
// state returns the state folder, for the local tracker and the local claim store. env is the environment of the caller, for the usage read of the agents.
func newAdapters(bd proc.Runner, state func() (string, error), env map[string]string) adapters {
	return adapters{
		trackers: trackerEntries(bd, state),
		claims:   claimsEntries(bd, state),
		forges: map[model.ForgeName]forgeAdapter{
			model.ForgeGithub: {registration: githubRegistration(forgegithub.Tool), build: newGithubForge},
		},
		agents:   agentEntries(env),
		hosts:    hostEntries(),
		bd:       bd,
		state:    state,
		diskPoll: defaultDiskPoll,
	}
}

// trackerEntries returns the tracker entries.
func trackerEntries(bd proc.Runner, state func() (string, error)) map[model.TrackerName]trackerAdapter {
	return map[model.TrackerName]trackerAdapter{
		model.TrackerLocal:  localTracker(state),
		model.TrackerBeads:  {registration: beadsRegistration(bd), build: func(_ context.Context, project model.Project) (store.Tracker, error) { return newBeads(bd, project) }},
		model.TrackerGithub: {registration: githubRegistration(storegithub.Tool), build: func(_ context.Context, project model.Project) (store.Tracker, error) { return newIssues(project) }},
	}
}

// localTracker is the entry of the local tracker: it builds and lists over local.db, and a check opens no file.
func localTracker(state func() (string, error)) trackerAdapter {
	return trackerAdapter{
		build: func(ctx context.Context, project model.Project) (store.Tracker, error) {
			return openLocalTracker(ctx, state, project)
		},
		check: func(context.Context, model.Project) error { return nil },
		items: func(ctx context.Context, project model.Project) (cli.ItemStore, error) {
			return openLocalItems(ctx, state, project)
		},
	}
}

// openLocalTracker opens local.db as the tracker of the project. A failed open gives a nil interface, not a nil pointer inside one.
func openLocalTracker(ctx context.Context, state func() (string, error), project model.Project) (store.Tracker, error) { //nolint:ireturn // the table stores constructors of the contract
	opened, err := openLocal(ctx, state, project)
	if err != nil {
		return nil, err
	}

	return opened, nil
}

// openLocalItems opens local.db as the item list of the project. A failed open gives a nil interface, not a nil pointer inside one.
func openLocalItems(ctx context.Context, state func() (string, error), project model.Project) (cli.ItemStore, error) { //nolint:ireturn // the opener returns the list through the interface of the command line
	opened, err := openLocal(ctx, state, project)
	if err != nil {
		return nil, err
	}

	return opened, nil
}

// claimsEntries returns the claim store entries.
func claimsEntries(bd proc.Runner, state func() (string, error)) map[model.ClaimsName]claimsAdapter {
	return map[model.ClaimsName]claimsAdapter{
		model.ClaimsLocal: {
			open: func(ctx context.Context, project model.Project) (claimStore, error) {
				return openLocalClaims(ctx, state, project)
			},
			check: func(model.Project) error { return nil },
		},
		model.ClaimsBeads: {
			registration: beadsRegistration(bd),
			open:         func(_ context.Context, project model.Project) (claimStore, error) { return newBeadsClaims(bd, project) },
			check:        func(project model.Project) error { return checkBeadsClaims(bd, project) },
		},
	}
}

// hostEntries returns the host provider entries.
func hostEntries() map[model.HostProvider]hostAdapter {
	return map[model.HostProvider]hostAdapter{
		model.HostSkyPilot: {
			registration: registration{Entry: config.Entry{Idle: true, HostBlock: skypilotBlock}},
			build:        newSkyPilotHost,
			sentinels:    []error{skypilot.ErrConfig, link.ErrConfig},
		},
		model.HostSSH: {
			registration: registration{Entry: config.Entry{HostBlock: sshBlock}},
			build:        newSSHHost,
			sentinels:    []error{link.ErrConfig, ssh.ErrLinger, ssh.ErrSystemd, ssh.ErrDraining},
		},
		model.HostSlurm: {
			registration: registration{Entry: config.Entry{HostBlock: sshBlock}},
			build:        newSlurmHost,
			sentinels:    []error{link.ErrConfig},
		},
	}
}

// The nested blocks of the host block that a provider takes. The ssh and the slurm providers share the ssh block.
const (
	skypilotBlock = "skypilot"
	sshBlock      = "ssh"
)

// registry returns the names, keys and tools of the table as the config reads them.
func (a adapters) registry() config.Adapters {
	return config.Adapters{
		Trackers: configEntries(a.trackers, trackerAdapter.entryOf),
		Claims:   configEntries(a.claims, claimsAdapter.entryOf),
		Forges:   configEntries(a.forges, forgeAdapter.entryOf),
		Hosts:    configEntries(a.hosts, hostAdapter.entryOf),
		Agents:   configEntries(a.agents, agentAdapter.entryOf),
	}
}

// entryOf returns the config entry of the registration.
func (r registration) entryOf() *config.Entry { return &r.Entry }

// configEntries returns the config entry of each adapter of one kind, by name.
func configEntries[K comparable, A any](table map[K]A, entry func(A) *config.Entry) map[K]*config.Entry {
	entries := make(map[K]*config.Entry, len(table))

	for name, adapter := range table {
		entries[name] = entry(adapter)
	}

	return entries
}

// trackerBuilder builds the tracker of a project. The context is for the adapter that opens a file.
type trackerBuilder func(context.Context, model.Project) (store.Tracker, error)

// openLocalClaims opens local.db in the state folder for the claim rows of the project.
func openLocalClaims(ctx context.Context, state func() (string, error), project model.Project) (claimStore, error) {
	rows, err := openLocal(ctx, state, project)
	if err != nil {
		return claimStore{}, err
	}

	return claimStore{Rows: rows, closer: rows}, nil
}

func newBeadsClaims(bd proc.Runner, project model.Project) (claimStore, error) {
	rows, err := beads.New(bd, project.BeadsPrefix)
	if err != nil {
		return claimStore{}, err
	}

	return claimStore{Rows: rows}, nil
}

// checkBeadsClaims says why the beads claim store of the project cannot be built. It opens no file.
func checkBeadsClaims(bd proc.Runner, project model.Project) error {
	_, err := beads.New(bd, project.BeadsPrefix)

	return err
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
		maps.Copy(checks, entry.versions)
	}

	for _, entry := range a.claims {
		maps.Copy(checks, entry.versions)
	}

	return checks
}

// logins returns the usage read of each agent that has an adapter, by agent.
func (a adapters) logins() map[model.Agent]func(context.Context, model.Runner) model.Reading {
	logins := map[model.Agent]func(context.Context, model.Runner) model.Reading{}

	for name, entry := range a.agents {
		if entry.build != nil {
			logins[name] = entry.build().ReadUsage
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

// readUsage reads the usage of one runner through the adapter of its agent. A runner of an agent with no entry is unknown, so the pool pick never chooses it.
func readUsage(entries map[model.Agent]agentAdapter) pool.Reader {
	return func(ctx context.Context, runner model.Runner) model.Reading {
		entry, found := entries[runner.Agent]
		if !found || entry.build == nil {
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
	entry, found := a.claims[project.Claims]
	if !found {
		return claimStore{}, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}

	return entry.open(ctx, project)
}

// itemOpener returns what the command line calls to open the item list of a project, by the tracker that owns the list. The caller closes the list.
func (a adapters) itemOpener() map[model.TrackerName]cli.ItemOpener {
	openers := map[model.TrackerName]cli.ItemOpener{}

	for name, entry := range a.trackers {
		if entry.items != nil {
			openers[name] = entry.items
		}
	}

	return openers
}

// checkTrackerAndForge says why the tracker or the forge of the project cannot be built. The local tracker needs no file, so a check leaves the state folder alone.
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
func (a adapters) checkClaims(project model.Project) error {
	entry, found := a.claims[project.Claims]
	if !found {
		return fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}

	return entry.check(project)
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

	return a.checkTools(ctx, project)
}

// checkTools runs the version check of each tool that the project needs, in the order of project.Tools. A tool with no version check passes.
func (a adapters) checkTools(ctx context.Context, project model.Project) error {
	versions := a.toolVersions()

	for _, tool := range project.Tools {
		if versionCheck := versions[tool]; versionCheck != nil {
			if err := versionCheck(ctx); err != nil {
				return err
			}
		}
	}

	return nil
}
