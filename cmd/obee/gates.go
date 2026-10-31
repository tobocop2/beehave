package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

// gateTimeout is the time that the gate command of a project may run on this machine.
const gateTimeout = time.Hour

var errNoGateFolder = errors.New("the item has no worktree for the gate of the project to judge. Make the worktree of the item first, or give the gate call a program")

// headAndTree is the number of lines that `git rev-parse HEAD HEAD^{tree}` prints.
const headAndTree = 2

// projectGate gives the gate step of the workflow to internal/gate, with the worktree of the item, the host of the project and the clone URL of its forge.
type projectGate struct {
	projects map[string]config.Project
	programs programs
	forge    func(model.Project) (forge.Forge, error)
}

// Run runs the gate of the project on the worktree of the item. internal/gate refuses a tree with uncommitted changes,
// has a host gate check out the head that it reports, and refuses a tree that changes during the run.
func (g projectGate) Run(ctx context.Context, project, item string) (flow.GateResult, error) {
	request, err := g.request(ctx, project, item)
	if err != nil {
		return flow.GateResult{}, err
	}

	hosts := func(model.Project) (gate.Remote, error) { return g.programs.hosts(project) }

	result, err := gate.New(g.shell, g.tree, hosts).Run(ctx, request)
	if err != nil {
		return flow.GateResult{}, fmt.Errorf("run the gate of item %s: %w", item, err)
	}

	return flow.GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: gateSteps(result.Steps)}, nil
}

// Key reads the key of the gate that Run would start on the worktree of the item.
func (g projectGate) Key(ctx context.Context, project, item string) (flow.GateKey, error) {
	request, err := g.request(ctx, project, item)
	if err != nil {
		return flow.GateKey{}, err
	}

	key, err := gate.New(g.shell, g.tree, nil).Key(ctx, request)
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
func (g projectGate) request(ctx context.Context, project, item string) (gate.Request, error) {
	found, ok := g.projects[project]
	if !ok {
		return gate.Request{}, fmt.Errorf("%w: %q", errUnknownProject, project)
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
	result := g.programs.local("sh", dir, gateTimeout).Run(ctx, []string{"-c", command})

	return result.Stdout + result.Stderr, result.ExitCode
}

// tree reads the head of the worktree in dir, the tree of that head and whether it has no uncommitted change.
func (g projectGate) tree(ctx context.Context, dir string) (gate.State, error) {
	// One call reads both, so the tree always belongs to the head.
	ids, err := g.git(ctx, dir, "rev-parse", "HEAD", "HEAD^{tree}")
	if err != nil {
		return gate.State{}, err
	}

	lines := strings.Split(ids, "\n")
	if len(lines) != headAndTree {
		return gate.State{}, fmt.Errorf("git rev-parse in %s printed %d lines, and the head and the tree make %d", dir, len(lines), headAndTree)
	}

	head, tree := lines[0], lines[1]

	changes, err := g.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return gate.State{}, err
	}

	return gate.State{Head: head, Tree: tree, Clean: changes == ""}, nil
}

// git runs git in dir and returns what it printed, without the space at both ends.
func (g projectGate) git(ctx context.Context, dir string, args ...string) (string, error) {
	result := g.programs.local("git", dir, gateTimeout).Run(ctx, args)
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git %s in %s: %s", args[0], dir, strings.TrimSpace(result.Stderr))
	}

	return strings.TrimSpace(result.Stdout), nil
}
