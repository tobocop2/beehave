package main

import (
	"context"
	"maps"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// The round scripts run examples/remediation in the real engine, over the stand-in forge of the swarm scripts.
// The project file of a round script names a build host and holds a host block, as the project of a real round does. No script has a machine to build on, so the engine builds on this machine:
// it gets the config with the project built locally. The slot classes of the host block stay, and the programs that name the host answer from the file mutations, as in the other scripts.
// Only a host job prints the lines of the checks before review, so the gate of a candidate adds a clean record of them.

// roundGate is the gate of the engine.
type roundGate interface {
	Submit(ctx context.Context, project, item, host, base, _, _ string, slot model.Slot) (flow.GateJob, error)
	Run(ctx context.Context, project, item, host string, job flow.GateJob) (flow.GateResult, error)
	Key(ctx context.Context, project, item, host, base, _ string) (flow.GateKey, error)
}

// checkedGate gives the gate of a candidate, the gate with a base, a clean record of the host checks: one new test, a clean secret scan, and a revert run in which the test fails.
type checkedGate struct{ roundGate }

func (g checkedGate) Run(ctx context.Context, project, item, host string, job flow.GateJob) (flow.GateResult, error) {
	result, err := g.roundGate.Run(ctx, project, item, host, job)
	if err != nil || job.Plan.Base == "" {
		return result, err
	}

	result.Checks = &flow.GateChecks{NewTests: []string{"round TestE2E"}, New: 1, Secrets: new(int), Complete: true, Revert: model.RevertPass, RevertCases: 1}

	return result, nil
}

// roundCommands runs a program that names no host as it is, and answers a program that names a host (the mutation run) from the file mutations of the script.
type roundCommands struct {
	programs interface {
		Run(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error)
	}
	answers e2eCommands
}

func (c roundCommands) Run(ctx context.Context, request flow.CommandRequest) (flow.CommandOutput, error) {
	if request.Host == "" {
		return c.programs.Run(ctx, request)
	}

	return c.answers.Run(ctx, request)
}

// builtLocally is the config with every project built on this machine.
func builtLocally(cfg config.Config) config.Config {
	local := cfg
	local.Projects = maps.Clone(cfg.Projects)

	for name, project := range local.Projects {
		project.BuildHost, project.BuildHosts, project.Host = config.LocalHost, nil, nil
		local.Projects[name] = project
	}

	return local
}

// e2eRound is the beehaveround command of the scripts.
func e2eRound() {
	e2eStandIn(builtLocally, func(cfg config.Config, deps flow.Deps) flow.Deps {
		deps.Gate = checkedGate{deps.Gate}
		deps.Commands = roundCommands{programs: deps.Commands, answers: e2eCommands{dir: config.ProcessEnv()[e2eDirVariable]}}
		deps.Slots = slotsOf(cfg.Projects)

		return deps
	})
}
