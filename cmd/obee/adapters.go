package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/forge"
	forgegithub "github.com/tobocop2/obee/internal/forge/github"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/beads"
	storegithub "github.com/tobocop2/obee/internal/store/github"
)

// adapters is the one place that maps an adapter name to its constructor. No other package dispatches on a name.
// The claim store is not a choice: the claim rows live in beads whatever tracker the project uses, so it has no table.
type adapters struct {
	trackers map[model.TrackerName]func(model.Project) (store.Tracker, error)
	forges   map[model.ForgeName]func(model.Project) (forge.Forge, error)
	agents   map[model.Agent]func() agent.Adapter
	bd       proc.Runner
}

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
		bd: bd,
	}
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
	build, found := a.trackers[project.Tracker]
	if !found {
		return nil, fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	return build(project)
}

// forge builds the forge the project names.
func (a adapters) forge(project model.Project) (forge.Forge, error) { //nolint:ireturn // the table builds whichever adapter the project names
	build, found := a.forges[project.Forge]
	if !found {
		return nil, fmt.Errorf("project %s: no forge adapter named %q", project.Name, project.Forge)
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

// leases builds the claim store of the project. It is beads, so the project must name its beads prefix.
func (a adapters) leases(project model.Project) (*beads.Store, error) {
	return beads.New(a.bd, project.BeadsPrefix)
}

// check builds the tracker, the forge and the claim store of the project and reports why it cannot.
// The claim store needs a bd that has leases and the guards of bd update, so check also refuses an older bd.
// `obee check config` calls it for each project.
func (a adapters) check(ctx context.Context, project model.Project) error {
	if _, err := a.tracker(project); err != nil {
		return err
	}

	if _, err := a.forge(project); err != nil {
		return err
	}

	if _, err := a.leases(project); err != nil {
		return err
	}

	return beads.RequireBd(ctx, a.bd)
}
