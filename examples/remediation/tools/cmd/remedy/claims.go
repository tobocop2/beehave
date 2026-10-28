package main

import (
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

// claimsOf builds the claims of a project. The rows are beads, as they are for every project.
func claimsOf(bd proc.Runner, project model.Project) (*claim.Claims, error) {
	rows, err := beads.New(bd, project.BeadsPrefix)
	if err != nil {
		return nil, err
	}

	return lease.Open(rows)
}
