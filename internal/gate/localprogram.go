package gate

import (
	"context"
	"fmt"
	"os"

	"github.com/tobocop2/beehave/internal/model"
)

// localProgram runs the gate of a project with a gate_program on this machine. It runs the job text of the host gate in a new folder: a clone of the head,
// the base files copied over the gate paths, the gate command, and the final compare. The tree of the item is only read, so the head code cannot reach it.
func (g Gate) localProgram(ctx context.Context, request Request, head string) (Result, error) {
	if err := request.needBase(); err != nil {
		return Result{}, err
	}

	root, err := os.MkdirTemp("", clonePrefix)
	if err != nil {
		return Result{}, fmt.Errorf("make the folder of the gate copy: %w", err)
	}

	defer os.RemoveAll(root)

	src := jobSource{from: request.Dir, cloneURL: request.CloneURL, tools: g.jobTools, local: true}
	command := jobCommand(request.Project, request.pins, src, head, root+"/job", "", request.anchor())
	out, code := g.runJob(ctx, request.Dir, root, command)

	return localVerdict(ctx, request, head, out, code)
}

// runJob runs the command of a gate_program job with the ProgramShell of the gate, or with its Shell in root.
func (g Gate) runJob(ctx context.Context, from, root, command string) (string, int) {
	if g.program != nil {
		return g.program(ctx, from, root, command)
	}

	return g.shell(ctx, root, command)
}

// localVerdict reads the result of a gate_program job from its exit code and its log, as the host gate reads the log of a failed job.
// A job that exited 0 passed the gate command, the test check and the final compare, so its log holds nothing that changes the verdict.
func localVerdict(ctx context.Context, request Request, head, out string, code int) (Result, error) {
	project := request.Project

	switch {
	case code > signalBase:
		return signaled(ctx, project, head, out, code)
	case code == ExitTimedOut:
		return timedOut(request, head, out), nil
	case code == 0:
		return Result{Passed: true, Head: head, Steps: hostSteps(project, StatusPass, StatusPass)}, nil
	}

	return failedJob(project, head, out), nil
}

// failedJob is the result of a gate_program job that exited with a code other than 0: the first marker line of its log, else the output and the step lines.
func failedJob(project model.Project, head, out string) Result {
	if marked, found := markedResult(out, project); found {
		marked.Head = head
		marked.Report = tail(marked.Report + "\nthe log of the gate job:\n" + out)

		return marked
	}

	steps := failedSteps(project)
	if len(project.GateSteps) > 0 {
		steps = stepMarkerResults(out, project)
	}

	return Result{Report: tail(out), Head: head, Steps: steps}
}
