package main

import (
	"context"
	"fmt"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
)

// adapters is the one place that maps an adapter name to its constructor. No other package dispatches on a name.
// The lease store is not a choice: the claim rows live in beads whatever tracker the project uses, so it has no table.
type adapters struct {
	trackers map[model.TrackerName]func(model.Project) (model.Tracker, error)
	forges   map[model.ForgeName]func(model.Project) (model.Forge, error)
	bd       store.Runner
}

// newAdapters returns the table. bd is the command line of beads, for the beads tracker and the lease store.
func newAdapters(bd store.Runner) adapters {
	return adapters{
		trackers: map[model.TrackerName]func(model.Project) (model.Tracker, error){
			model.TrackerBeads:  func(project model.Project) (model.Tracker, error) { return newBeads(bd, project) },
			model.TrackerGithub: newIssues,
		},
		forges: map[model.ForgeName]func(model.Project) (model.Forge, error){
			model.ForgeGithub: newGithubForge,
		},
		bd: bd,
	}
}

func newBeads(bd store.Runner, project model.Project) (model.Tracker, error) { //nolint:ireturn // the table stores constructors of the contract
	beads, err := store.NewBeadsStore(bd, project.BeadsPrefix)
	if err != nil {
		return nil, err
	}

	return beads, nil
}

func newIssues(project model.Project) (model.Tracker, error) { //nolint:ireturn // the table stores constructors of the contract
	issues, err := store.NewGithubStore(project.Repo, proc.NewExecRunner("gh", ""))
	if err != nil {
		return nil, err
	}

	return issues, nil
}

func newGithubForge(project model.Project) (model.Forge, error) { //nolint:ireturn // the table stores constructors of the contract
	github, err := forge.NewExec(project.Repo)
	if err != nil {
		return nil, err
	}

	return github, nil
}

// tracker builds the tracker the project names.
func (a adapters) tracker(project model.Project) (model.Tracker, error) { //nolint:ireturn // the table builds whichever adapter the project names
	build, found := a.trackers[project.Tracker]
	if !found {
		return nil, fmt.Errorf("project %s: no tracker adapter named %q", project.Name, project.Tracker)
	}

	return build(project)
}

// forge builds the forge the project names.
func (a adapters) forge(project model.Project) (model.Forge, error) { //nolint:ireturn // the table builds whichever adapter the project names
	build, found := a.forges[project.Forge]
	if !found {
		return nil, fmt.Errorf("project %s: no forge adapter named %q", project.Name, project.Forge)
	}

	return build(project)
}

// leases builds the lease store of the project. It is beads, so the project must name its beads prefix.
func (a adapters) leases(project model.Project) (*store.BeadsStore, error) {
	return store.NewBeadsStore(a.bd, project.BeadsPrefix)
}

// check builds the tracker, the forge and the lease store of the project and reports why it cannot.
// The lease store needs a bd that has the guards of bd update, so check also refuses an older bd.
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

	return store.RequireBd(ctx, a.bd)
}
