package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/config"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
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
		project, err := lease.ProjectOf(env, repo, registry())
		if err != nil {
			return nil, err
		}

		return claimsOf(bd, func() (string, error) { return config.StateDir(env) }, project)
	}
}

// sshBlock is the host block of the ssh and slurm hosts. This binary may not import those host packages, so it names the hosts and the block here.
const sshBlock = "ssh"

// registry is the small adapter table that this binary declares: the config validates the projects that remedy reads against it.
func registry() config.Adapters {
	beadsBlock := beads.Block()
	beadsEntry := &config.Entry{Block: &beadsBlock, Tools: []string{beads.Tool}}
	issues := &config.Entry{Keys: []string{"repo"}, Tools: []string{forgegithub.Tool}}

	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{local.Tracker: {}, beads.Tracker: beadsEntry, "github": issues},
		Claims:   map[model.ClaimsName]*config.Entry{local.Claims: {}, beads.Claims: beadsEntry},
		Forges:   map[model.ForgeName]*config.Entry{forgegithub.Name: {Keys: []string{"repo"}, Tools: []string{forgegithub.Tool}}},
		Agents:   map[model.Agent]*config.Entry{"claude": {}, opencode.Name: {}},
		Hosts: map[model.HostProvider]*config.Entry{
			"skypilot": {Idle: true, HostBlock: "skypilot"},
			sshBlock:   {HostBlock: sshBlock},
			"slurm":    {HostBlock: sshBlock},
		},
	}
}

// claimsOf builds the claims of a project over the claim store that it names: the beads rows, or local.db in the state folder.
// The local store stays open until the process ends, which is one command.
func claimsOf(bd proc.Runner, state func() (string, error), project model.Project) (*claim.Claims, error) {
	switch project.Claims {
	case beads.Claims:
		rows, err := beads.New(bd, project.Block)
		if err != nil {
			return nil, err
		}

		return lease.Open(rows)
	case local.Claims:
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
