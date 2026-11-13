package config_test

import (
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/config"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

// beadsBlock, skyBlock, sshBlock and slurmBlock are the blocks that the adapter packages declare.
var (
	beadsBlock = beads.Block()
	skyBlock   = skypilot.Block()
	sshBlock   = ssh.Block()
	slurmBlock = slurm.Block()
)

// registry returns the adapters of the build, as the table of cmd/beehave registers them.
func registry() config.Adapters {
	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{
			local.Tracker:         {},
			beads.Tracker:         {Block: &beadsBlock, Tools: []string{beads.Tool}},
			storegithub.Tracker:   {Keys: []string{"repo"}, Tools: []string{storegithub.Tool}},
		},
		Claims: map[model.ClaimsName]*config.Entry{
			local.Claims: {},
			beads.Claims: {Block: &beadsBlock, Tools: []string{beads.Tool}},
		},
		Forges: map[model.ForgeName]*config.Entry{
			forgegithub.Name: {Keys: []string{"repo"}, Tools: []string{forgegithub.Tool}},
		},
		Hosts: map[model.HostProvider]*config.Entry{
			skypilot.Name: {Block: &skyBlock, Idle: true},
			ssh.Name:      {Block: &sshBlock},
			slurm.Name:    {Block: &slurmBlock},
		},
		Agents: map[model.Agent]*config.Entry{
			claude.Name: {},
			"opencode":  {},
		},
	}
}
