package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/srt"
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
	// home and lookPath find the home folder and the srt program. Nil means os.UserHomeDir and exec.LookPath.
	home     func() (string, error)
	lookPath func(string) (string, error)
}

// Run runs the gate of the project on the worktree of the item. internal/gate refuses a tree with uncommitted changes,
// has a host gate check out the head that it reports, and refuses a tree that changes during the run.
// A non-empty job is a host job that Submit started: the gate waits for it and submits none.
func (g projectGate) Run(ctx context.Context, project, item string, job flow.GateJob) (flow.GateResult, error) {
	request, err := g.request(ctx, project, item)
	if err != nil {
		return flow.GateResult{}, err
	}

	request.Resume, request.ResumeHead = model.JobID(job.ID), job.Head

	boxed, cleanup, err := g.boxed(ctx, project, request)
	if err != nil {
		return flow.GateResult{}, fmt.Errorf("run the gate of item %s: %w", item, err)
	}

	defer cleanup()

	result, err := boxed.Run(ctx, request)
	if err != nil {
		return flow.GateResult{}, fmt.Errorf("run the gate of item %s: %w", item, err)
	}

	return flow.GateResult{Passed: result.Passed, Report: result.Report, Head: result.Head, Tree: result.Tree, Steps: gateSteps(result.Steps)}, nil
}

// Submit starts the host job of the gate of the project and returns at once, so the engine can record the job before it waits.
// A project that builds here has no host job, and its job is empty.
func (g projectGate) Submit(ctx context.Context, project, item string) (flow.GateJob, error) {
	request, err := g.request(ctx, project, item)
	if err != nil {
		return flow.GateJob{}, err
	}

	job, err := g.gate(project).Submit(ctx, request)
	if err != nil {
		return flow.GateJob{}, fmt.Errorf("start the gate of item %s: %w", item, err)
	}

	return flow.GateJob{ID: string(job.ID), Head: job.Head}, nil
}

func (g projectGate) gate(project string) gate.Gate {
	return gate.New(g.shell, g.tree, func(model.Project) (gate.Remote, error) { return g.programs.hosts(project) })
}

// sandboxed is true when the gate command of the project runs in the sandbox: the project builds on this machine and its sandbox is on.
// A gate on a build host runs outside the sandbox, because the sandbox covers only a command that obee starts here.
func (g projectGate) sandboxed(project string) bool {
	found := g.projects[project]

	return found.BuildHost == config.LocalHost && found.SandboxByDefault()
}

// boxed returns the gate for one run. For a gate that runs in the sandbox, it checks that srt is installed, makes the plan with the denied reads of an
// agent step and the network list and write list of the project, and gives the gate a shell that runs the gate command through srt. The cleanup
// removes the settings file of srt. A plan that cannot start refuses the run before any program starts.
func (g projectGate) boxed(ctx context.Context, project string, request gate.Request) (gate.Gate, func(), error) {
	plain := g.gate(project)
	if !g.sandboxed(project) {
		return plain, func() {}, nil
	}

	plan, home, err := g.gatePlan(ctx, project, request.Dir)
	if err != nil {
		return gate.Gate{}, nil, err
	}

	args, cleanup, err := g.wrapped(ctx, plan, home, request)
	if err != nil {
		return gate.Gate{}, nil, err
	}

	return plain.WithCommand(func(ctx context.Context, dir, _ string) (string, int) {
		return g.run(ctx, srt.Program, dir, args)
	}), cleanup, nil
}

// wrapped writes the settings file for the gate command and probes srt with it. A refused probe removes the file.
func (g projectGate) wrapped(ctx context.Context, plan agent.SandboxPlan, home string, request gate.Request) ([]string, func(), error) {
	args, cleanup, err := srt.Wrap(plan, home, request.Project.Gate)
	if err != nil {
		return nil, nil, err
	}

	if err = g.probe(ctx, request.Dir, args); err != nil {
		cleanup()

		return nil, nil, err
	}

	return args, cleanup, nil
}

// probe runs srt once on the command `true`. A start failure of srt or of the program under it (a settings file that srt rejects, a bubblewrap
// that the system blocks) then refuses the run. Without the probe, the failure is the exit code of the gate command and reads as a red gate.
func (g projectGate) probe(ctx context.Context, dir string, args []string) error {
	out, code := g.run(ctx, srt.Program, dir, append(slices.Clone(args[:len(args)-1]), "true"))
	if code != 0 {
		return fmt.Errorf("%w: the sandbox could not start (srt exit code %d): %s", errGateSandbox, code, strings.TrimSpace(out))
	}

	return nil
}

// srtHome checks that srt is installed and returns the home folder.
func (g projectGate) srtHome() (string, error) {
	lookPath, homeOf := g.lookPath, g.home
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	if homeOf == nil {
		homeOf = os.UserHomeDir
	}

	if note := srt.Missing(lookPath); note != "" {
		return "", fmt.Errorf("%w: %s", errGateSandbox, note)
	}

	home, err := homeOf()
	if err != nil {
		return "", fmt.Errorf("%w: obee cannot find the home folder, so the sandbox cannot deny it", errGateSandbox)
	}

	return home, nil
}

// gatePlan checks that srt is installed and makes the plan of the gate command with the lists of the project. It returns the plan and the home folder.
func (g projectGate) gatePlan(ctx context.Context, project, dir string) (agent.SandboxPlan, string, error) {
	home, err := g.srtHome()
	if err != nil {
		return agent.SandboxPlan{}, "", err
	}

	found := g.projects[project].Project

	plan, note := planOf(ctx, home, found, dir, "", model.Sandbox{Network: found.GateNetwork, AllowWrite: found.GateAllowWrite})
	if note != "" {
		return agent.SandboxPlan{}, "", fmt.Errorf("%w: %s", errGateSandbox, strings.ReplaceAll(note, " The step did not start.", ""))
	}

	return plan, home, nil
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
