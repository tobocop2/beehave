package main

import (
	"fmt"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/lease"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store/beads"
)

// openClaims opens the claim store of the project that owns the repository, from the obee config folder that env names.
// The claims are built by the code that `obee run` uses.
func openClaims(env map[string]string, bd proc.Runner) func(repo string) (*claim.Claims, error) {
	return func(repo string) (*claim.Claims, error) {
		project, err := lease.ProjectOf(env, repo)
		if err != nil {
			return nil, err
		}

		return claimsOf(bd, project)
	}
}

// claimsOf builds the claims of a project. Only beads rows are built; the local claim store comes later.
func claimsOf(bd proc.Runner, project model.Project) (*claim.Claims, error) {
	switch project.Claims {
	case model.ClaimsBeads:
		rows, err := beads.New(bd, project.BeadsPrefix)
		if err != nil {
			return nil, err
		}

		return lease.Open(rows)
	case model.ClaimsLocal:
		return nil, fmt.Errorf("project %s: the local claim store is not built yet; set claims = \"beads\" in the project file", project.Name)
	default:
		return nil, fmt.Errorf("project %s: no claim store named %q", project.Name, project.Claims)
	}
}
