package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/forge"
	forgegithub "github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/skypilot"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/beads"
	storegithub "github.com/tobocop2/obee/internal/store/github"
)

// adapters is the one place that maps an adapter name to its constructor. No other package dispatches on a name.
// The claim store has no table: only beads is built, and a project that names the local tracker or the local claim store is refused until the local adapters exist.
type adapters struct {
	trackers map[model.TrackerName]func(model.Project) (store.Tracker, error)
	forges   map[model.ForgeName]func(model.Project) (forge.Forge, error)
	agents   map[model.Agent]func() agent.Adapter
	hosts    map[model.HostProvider]func(model.Project) (host.Host, error)
	bd       proc.Runner
}

// skyTimeout is the longest one sky call may run. A launch creates a machine, which takes minutes.
const skyTimeout = 30 * time.Minute

// newAdapters returns the table. bd is the command line of beads, for the beads tracker and the claim store.
func newAdapters(bd proc.Runner) adapters {
	return adapters{
		trackers: map[model.TrackerName]func(model.Project) (store.Tracker, error){
			model.TrackerBeads:  func(project model.Project) (store.Tracker, error) { return newBeads(bd, project) },
			model.TrackerGithub: newIssues,
		},
		forges: map[model.ForgeName]func(model.Project) (forge.Forge, error){
			model.ForgeGithub: newGithubForge,
		},
		agents: map[model.Agent]func() agent.Adapter{
			model.AgentClaude: func() agent.Adapter { return claude.New() },
		},
		hosts: map[model.HostProvider]func(model.Project) (host.Host, error){
			model.HostSkyPilot: newSkyPilotHost,
		},
		bd: bd,
	}
}

func newSkyPilotHost(project model.Project) (host.Host, error) { //nolint:ireturn // the table stores constructors of the contract
	sky := proc.ExecRunner{Binary: "sky", Timeout: skyTimeout}

	h, err := skypilot.New(project.BuildHost, *project.Host, sky, proc.NewExecRunner("ssh", ""))
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
func (a adapters) tracker(project model.Project) (store.Tracker, error) { //nolint:ireturn // the table builds whichever adapter the project names
	if project.Tracker == model.TrackerLocal {
		return nil, fmt.Errorf("project %s: the local tracker is not built yet; set tracker = \"beads\" or \"github\" in the project file", project.Name)
	}

	build, found := a.trackers[project.Tracker]
	if !found {
		return nil, fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	return build(project)
}

// forge builds the forge the project names.
func (a adapters) forge(project model.Project) (forge.Forge, error) { //nolint:ireturn // the table builds whichever adapter the project names
	if project.Forge == "" {
		return nil, fmt.Errorf("project %s: no forge; set forge = \"github\" in the project file", project.Name)
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

	build, found := a.hosts[project.Host.Provider]
	if !found {
		return nil, fmt.Errorf("project %s: no host adapter named %q", project.Name, project.Host.Provider)
	}

	return build(project)
}

// agent builds the adapter of the agent. It returns nil for an agent that has no adapter yet, and a run with no adapter does not start.
func (a adapters) agent(name model.Agent) agent.Adapter { //nolint:ireturn // the table builds whichever adapter the runner names
	build, found := a.agents[name]
	if !found {
		return nil
	}

	return build()
}

// leases builds the claim store of the project. Only beads is built, so the project must name its beads prefix.
func (a adapters) leases(project model.Project) (*beads.Store, error) {
	switch project.Claims {
	case model.ClaimsBeads:
		return beads.New(a.bd, project.BeadsPrefix)
	case model.ClaimsLocal:
		return nil, fmt.Errorf("project %s: the local claim store is not built yet; set claims = \"beads\" in the project file", project.Name)
	default:
		return nil, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}
}

// itemFields returns the item fields that the tracker and the forge of the project declare, the tracker first. A project with no forge has the fields of its tracker only.
func (a adapters) itemFields(_ context.Context, project model.Project) ([]string, error) {
	tracker, err := a.tracker(project)
	if err != nil {
		return nil, err
	}

	if project.Forge == "" {
		return tracker.ItemFields(), nil
	}

	forge, err := a.forge(project)
	if err != nil {
		return nil, err
	}

	return append(tracker.ItemFields(), forge.ItemFields()...), nil
}

// check builds the tracker, the forge (when the project has one), the build host (when the project has one) and the claim store of the project and reports why it cannot.
// The claim store needs a bd that has leases and the guards of bd update, so check also refuses an older bd.
// `obee check config` calls it for each project.
func (a adapters) check(ctx context.Context, project model.Project) error {
	if _, err := a.itemFields(ctx, project); err != nil {
		return err
	}

	if _, err := a.leases(project); err != nil {
		return err
	}

	if project.Host != nil {
		if _, err := a.host(project); err != nil {
			return err
		}
	}

	return beads.RequireBd(ctx, a.bd)
}
