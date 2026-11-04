package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/lease"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/local"
)

// openClaims opens the claim store of the project that owns the repository, from the beehave config folder that env names.
// The claims are built by the code that `beehave run` uses.
func openClaims(env map[string]string, bd proc.Runner) func(repo string) (*claim.Claims, error) {
	return func(repo string) (*claim.Claims, error) {
		project, err := lease.ProjectOf(env, repo)
		if err != nil {
			return nil, err
		}

		return claimsOf(bd, func() (string, error) { return config.StateDir(env) }, project)
	}
}

// claimsOf builds the claims of a project over the claim store that it names: the beads rows, or local.db in the state folder.
// The local store stays open until the process ends, which is one command.
func claimsOf(bd proc.Runner, state func() (string, error), project model.Project) (*claim.Claims, error) {
	switch project.Claims {
	case model.ClaimsBeads:
		rows, err := beads.New(bd, project.BeadsPrefix)
		if err != nil {
			return nil, err
		}

		return lease.Open(rows)
	case model.ClaimsLocal:
		return localClaims(state, project)
	default:
		return nil, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}
}

// localClaims builds the claims over the rows of local.db in the state folder.
func localClaims(state func() (string, error), project model.Project) (*claim.Claims, error) {
	dir, err := state()
	if err != nil {
		return nil, err
	}

	rows, err := local.Open(context.Background(), dir, project.Name, time.Now)
	if err != nil {
		return nil, err
	}

	return lease.Open(rows)
}
