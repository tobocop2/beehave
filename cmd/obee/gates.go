package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

// gateTimeout is the time that the gate command of a project may run on this machine.
const gateTimeout = time.Hour

var errGateSandbox = errors.New("the gate did not run")

var errNoGateFolder = errors.New("the item has no worktree for the gate of the project to judge. Make the worktree of the item first, or give the gate call a program")

// headAndTree is the number of lines that `git rev-parse HEAD HEAD^{tree}` prints.
const headAndTree = 2

// projectGate gives the gate step of the workflow to internal/gate, with the worktree of the item, the host of the project and the clone URL of its forge.
type projectGate struct {
	projects map[string]config.Project
	programs programs
	forge    func(model.Project) (forge.Forge, error)
	// timeout is the time that a local gate command may run. Zero means gateTimeout.
	timeout time.Duration
}

// Run runs the gate of the project on the worktree of the item. internal/gate refuses a tree with uncommitted changes,
// has a host gate check out the head that it reports, and refuses a tree that changes during the run.
// A non-empty job is a host job that Submit started: the gate waits for it and submits none.
func (g projectGate) Run(ctx context.Context, project, item, cluster string, job flow.GateJob) (flow.GateResult, error) {
	request, err := g.request(ctx, project, item, cluster)
	if err != nil {
		return flow.GateResult{}, err
	}

	request.Resume, request.ResumeHead, request.Plan = model.JobID(job.ID), job.Head, job.Plan

	result, err := g.gate(project).Run(ctx, request)
	if err != nil {
		return flow.GateResult{}, fmt.Errorf("run the gate of item %s: %w", item, err)
	}

	return flow.GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: gateSteps(result.Steps)}, nil
}

// Submit starts the host job of the gate of the project and returns at once, so the engine can record the job before it waits.
// A project that builds here has no host job, and its job is empty.
func (g projectGate) Submit(ctx context.Context, project, item, cluster string) (flow.GateJob, error) {
	request, err := g.request(ctx, project, item, cluster)
	if err != nil {
		return flow.GateJob{}, err
	}

	job, err := g.gate(project).Submit(ctx, request)
	if err != nil {
		return flow.GateJob{}, fmt.Errorf("start the gate of item %s: %w", item, err)
	}

	return flow.GateJob{ID: string(job.ID), Head: job.Head, Plan: job.Plan}, nil
}

func (g projectGate) gate(project string) gate.Gate {
	return gate.New(g.shellFor(project), g.tree, func(chosen model.Project) (gate.Remote, error) { return g.programs.hosts(project, chosen.BuildHost) })
}

// shellFor returns the function that runs the gate command of the project: under the sandbox when the project has it on and builds here, else as before.
func (g projectGate) shellFor(project string) func(context.Context, string, string) (string, int) {
	return func(ctx context.Context, dir, command string) (string, int) {
		if g.programs.sandbox.applies(project, false) {
			return g.programs.sandbox.run(ctx, project, dir, []string{"sh", "-c", command}, nil, cmp.Or(g.timeout, gateTimeout))
		}

		return g.shell(ctx, dir, command)
	}
}

// gateRefusal gives the engine the refusal of a gate that would run on this machine under the sandbox when no enforcer can run here.
// The engine asks at the start of each gate step, so a refused gate reads no key, reuses no earlier pass and runs no program.
// program is true for a gate that names its own program. A project the config does not hold is not refused.
// refusal gives the reason for a project and a kind of gate, or "" when the gate runs.
func gateRefusal(projects map[string]config.Project, refusal cli.GateRefusal) func(string, bool) error {
	return func(project string, program bool) error {
		found, ok := projects[project]
		if !ok {
			return nil
		}

		if reason := refusal(found, program); reason != "" {
			return fmt.Errorf("%w: %s", errGateSandbox, reason)
		}

		return nil
	}
}

// Key reads the key of the gate that Run would start on the worktree of the item.
func (g projectGate) Key(ctx context.Context, project, item, cluster string) (flow.GateKey, error) {
	request, err := g.request(ctx, project, item, cluster)
	if err != nil {
		return flow.GateKey{}, err
	}

	key, err := gate.New(g.shellFor(project), g.tree, nil).Key(ctx, request)
	if err != nil {
		return flow.GateKey{}, fmt.Errorf("read the gate key of item %s: %w", item, err)
	}

	return flow.GateKey{ID: key.ID, Head: key.Head, Tree: key.Tree}, nil
}

// gateSteps gives the steps of a gate result to the engine.
func gateSteps(steps []gate.Step) []flow.GateStep {
	out := make([]flow.GateStep, 0, len(steps))
	for _, s := range steps {
		out = append(out, flow.GateStep{Name: s.Name, Status: string(s.Status)})
	}

	return out
}

// request names the project, the worktree of the item and, for a project with a build host, the URL that the host clones the repository from.
// The project in the request has the cluster that the run recorded as its build host, so the gate, its key and its host all use that cluster.
// An empty cluster is the default build host. A cluster that the project does not declare is refused.
func (g projectGate) request(ctx context.Context, project, item, cluster string) (gate.Request, error) {
	found, err := g.onCluster(project, cluster)
	if err != nil {
		return gate.Request{}, err
	}

	dir, err := g.programs.trees.Path(ctx, project, item)
	if err != nil {
		return gate.Request{}, fmt.Errorf("find the folder of item %s: %w", item, err)
	}

	if dir == "" {
		return gate.Request{}, fmt.Errorf("%w: item %s of project %s", errNoGateFolder, item, project)
	}

	url, err := g.cloneURL(found)
	if err != nil {
		return gate.Request{}, err
	}

	return gate.Request{Project: found.Project, Dir: dir, CloneURL: url}, nil
}

// onCluster returns the project with the cluster of the run as its build host.
func (g projectGate) onCluster(project, cluster string) (config.Project, error) {
	found, ok := g.projects[project]
	if !ok {
		return config.Project{}, fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	chosen, err := found.HostFor(cluster)
	if err != nil {
		return config.Project{}, err
	}

	found.BuildHost = chosen

	return found, nil
}

// cloneURL returns the URL that a build host clones the repository of the project from: its source, else the URL of its forge. A project that builds here needs none.
func (g projectGate) cloneURL(project config.Project) (string, error) {
	if project.BuildHost == config.LocalHost {
		return "", nil
	}

	url, err := cloneSource(project.Project, g.forge)
	if err != nil {
		return "", fmt.Errorf("find the repository of project %s: %w", project.Name, err)
	}

	return url, nil
}

// shell runs the gate command in dir on this machine and returns its output and exit code.
func (g projectGate) shell(ctx context.Context, dir, command string) (string, int) {
	return g.run(ctx, "sh", dir, []string{"-c", command})
}

// run starts a program in dir on this machine and returns its output and exit code. The time limit ends it, and the code is then gate.ExitTimedOut.
func (g projectGate) run(ctx context.Context, program, dir string, args []string) (string, int) {
	result := g.programs.local(program, dir, cmp.Or(g.timeout, gateTimeout)).Run(ctx, args)

	if result.TimedOut {
		return result.Stdout + result.Stderr, gate.ExitTimedOut
	}

	return result.Stdout + result.Stderr, result.ExitCode
}

// tree reads the head of the worktree in dir, the tree of that head and whether it has no uncommitted change.
func (g projectGate) tree(ctx context.Context, dir string) (gate.State, error) {
	// One call reads both, so the tree always belongs to the head.
	ids, err := g.programs.git(ctx, dir, gateTimeout, "git rev-parse", "rev-parse", "HEAD", "HEAD^{tree}")
	if err != nil {
		return gate.State{}, err
	}

	lines := strings.Split(ids, "\n")
	if len(lines) != headAndTree {
		return gate.State{}, fmt.Errorf("git rev-parse in %s printed %d lines, and the head and the tree make %d", dir, len(lines), headAndTree)
	}

	head, tree := lines[0], lines[1]

	changes, err := g.programs.git(ctx, dir, gateTimeout, "git status", "status", "--porcelain")
	if err != nil {
		return gate.State{}, err
	}

	return gate.State{Head: head, Tree: tree, Clean: changes == ""}, nil
}
