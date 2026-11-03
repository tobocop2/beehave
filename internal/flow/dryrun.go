package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/review"
	trackerstore "github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/workflow/workflowtest"
)

const (
	dryProject  = "dry-run"
	dryLogin    = "dry-run"
	dryOwner    = "dry-run-owner"
	dryPull     = 1
	dryTree     = "dry-run-tree"
	dryOutLimit = 160
	dryPoll     = 25 * time.Millisecond
	dryWait     = time.Millisecond
	dryDirMode  = 0o700
	dryFileMode = 0o600
)

// dryNow is the reading of now() in every dry run, so two dry runs of one workflow print the same text.
func dryNow() time.Time { return time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC) }

// dryReading returns the clock of one dry run. Its first reading is dryNow, and each later one adds the real time that passed since the first,
// so now() starts at a fixed moment and the deadline of a wait still passes.
func dryReading() func() time.Time {
	var (
		once  sync.Once
		began time.Time
	)

	return func() time.Time {
		first := false

		once.Do(func() { began, first = time.Now(), true })

		if first {
			return dryNow()
		}

		return dryNow().Add(time.Since(began))
	}
}

// DryRun takes one item through a workflow file over fakes, in a state folder that it deletes at the end, and prints each step in order with its result.
// The engine gets only the fakes of this file, so no forge, agent, tracker or host is reachable. A step that needs a collaborator the fakes lack fails.
// A failed run prints the failed step and its error, and returns the error.
// The dry fakes return fixed values and never reach a real service. The fakes of the tests keep their own knobs and are a separate set.
func DryRun(ctx context.Context, file, item string, env map[string]string, out io.Writer) (err error) {
	root, err := os.MkdirTemp("", "obee-dry-run-")
	if err != nil {
		return fmt.Errorf("make the dry-run folder: %w", err)
	}

	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()

	opts := Options{StateDir: filepath.Join(root, "state"), Version: "dry-run", MaxConcurrent: minMaxConcurrent, MaxReviewConcurrent: minMaxConcurrent, Env: env}

	deps, err := dryPrepare(ctx, root, file, item)
	if err != nil {
		return err
	}

	runErr := dryWalk(ctx, opts, deps, item, file)
	last, printErr := printDrySteps(ctx, opts, out)

	return errors.Join(printErr, dryFailure(out, last, runErr))
}

// dryPrepare makes the worktree of the item in the folder of the run and returns the fakes that serve it.
func dryPrepare(ctx context.Context, root, file, item string) (Deps, error) {
	tree := filepath.Join(root, "tree")

	head, err := dryRepo(ctx, tree, filepath.Join(root, "origin.git"))
	if err != nil {
		return Deps{}, err
	}

	skills, err := dryRequiredSkills(file)
	if err != nil {
		return Deps{}, err
	}

	return dryDeps(tree, head, item, skills), nil
}

// dryWalk opens an engine over the deps, takes the item through the file and stops the engine.
func dryWalk(ctx context.Context, opts Options, deps Deps, item, file string) error {
	engine, err := NewEngine(ctx, opts, deps)
	if err != nil {
		return err
	}

	err = engine.Launch() //nolint:contextcheck // the engine owns the context of its runs
	if err == nil {
		_, err = engine.RunFile(item, dryProject, file)
	}

	return errors.Join(err, engine.Shutdown())
}

// ErrDryRunEndedEarly is returned by DryRun when the run ends at a step that the dry run does not fake. The steps after it are not checked.
var ErrDryRunEndedEarly = errors.New("the dry run ended early")

// dryFailure reports the end of a failed run. A step that needs a collaborator the dry run leaves out is not an error: the line says what the step does, and the run ends clean.
// Any other error prints with its file and line, then the last step that ran on its own line, and is returned.
func dryFailure(out io.Writer, last string, err error) error {
	var absent *absentError

	switch {
	case err == nil:
		return nil
	case onlyAbsent(err) && errors.As(err, &absent):
		// A failed write has no recovery.
		_, _ = fmt.Fprintf(out, "not run in a dry run: %s\nthe dry run ended early at a step it does not fake: %s; the steps after it were not checked\n", absent.step, absent.step)

		return ErrDryRunEndedEarly
	}

	// A failed write has no recovery.
	_, _ = fmt.Fprintf(out, "error: %v\nthe run stopped after %s\n", err, last)

	return err
}

// onlyAbsent reports whether every error in the tree of err is an absent-collaborator error. One other error among them makes the run a failure.
func onlyAbsent(err error) bool {
	switch wrapped := err.(type) { //nolint:errorlint // the walk visits each node of the tree itself
	case interface{ Unwrap() []error }:
		return !slices.ContainsFunc(wrapped.Unwrap(), func(child error) bool { return !onlyAbsent(child) })
	case interface{ Unwrap() error }:
		return onlyAbsent(wrapped.Unwrap())
	}

	var absent *absentError

	return errors.As(err, &absent)
}

// dryDeps holds the only collaborators of a dry run. Each one answers from a fixed value and touches nothing outside the folder of the run.
func dryDeps(tree, head, item string, skills []string) Deps {
	return Deps{
		Agents: dryAgents{skills: skills}, Gate: dryGate{head: head}, Logins: dryLogins{}, Lifecycle: dryLifecycle{}, Beats: dryBeats{},
		Forge: dryForge{head: head}, Watch: dryWatch{}, Trees: dryTrees{dir: tree}, Commands: dryCommands{}, Items: dryItems{item: item},
		Writer: dryWriter{}, Preflight: dryPreflight{},
		Clock: clock{Poll: dryPoll, Watch: dryWait, Settle: dryWait, Reading: dryReading()},
	}
}

// dryRepo makes the folder of the item: a git repository with one empty commit, and a second, empty bare repository that is its origin.
// The origin lets the cleanup steps read the branches of a remote. Both folders sit in the folder of the run. It returns the commit.
func dryRepo(ctx context.Context, dir, remote string) (string, error) {
	for _, folder := range []string{dir, remote} {
		if err := os.MkdirAll(folder, dryDirMode); err != nil {
			return "", fmt.Errorf("make the dry-run worktree: %w", err)
		}
	}

	var out string

	for _, step := range dryRepoSteps(dir, remote) {
		var err error
		if out, err = dryGit(ctx, step.dir, step.args); err != nil {
			return "", err
		}
	}

	return out, nil
}

type dryGitStep struct {
	dir  string
	args []string
}

// dryRepoSteps lists the git commands that make the repositories. The last one prints the commit.
func dryRepoSteps(dir, remote string) []dryGitStep {
	return []dryGitStep{
		{remote, []string{"init", "-q", "--bare", "--template="}},
		{dir, []string{"init", "-q", "--template="}},
		{dir, []string{"remote", "add", remoteName, remote}},
		{dir, []string{"-c", "user.name=dry", "-c", "user.email=dry@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "dry run"}},
		{dir, []string{"show", "-s", "--format=%H"}},
	}
}

// dryGit runs one git command in a folder and returns its output.
// The command ignores the global and system git config of the user, so a hook path, a template or a signing setting cannot run or fail.
func dryGit(ctx context.Context, dir string, args []string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...) //nolint:gosec // the arguments are the fixed lists of dryRepo
	command.Dir = dir
	command.Env = append(command.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")

	out, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("prepare the dry-run worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return strings.TrimSpace(string(out)), nil
}

// dryRequiredSkills lists the skills that the agent steps of the file require, so that the fake agent reports them as used.
func dryRequiredSkills(file string) ([]string, error) {
	text, err := os.ReadFile(file) //nolint:gosec // the owner names the workflow file
	if err != nil {
		return nil, fmt.Errorf("read the workflow file: %w", err)
	}

	calls, err := workflowtest.Calls(file, text)
	if err != nil {
		return nil, fmt.Errorf("read the workflow file: %w", err)
	}

	var skills []string

	for _, call := range calls {
		if call.Builtin == "agent" {
			skills = append(skills, workflowtest.Literals(call.Args["require_skills"])...)
		}
	}

	return skills, nil
}

// printDrySteps prints the steps of the run in order and returns the name of the last one. A step that failed shows its error.
func printDrySteps(ctx context.Context, opts Options, out io.Writer) (string, error) {
	client, err := OpenClient(ctx, opts, ReleaseDeps{Lifecycle: dryLifecycle{}, Trees: dryTrees{}})
	if err != nil {
		return "the start", err
	}

	lines, last, err := dryLines(client)

	for _, line := range lines {
		// A failed write has no recovery.
		_, _ = fmt.Fprintln(out, line)
	}

	return last, errors.Join(err, client.Close())
}

// dryLines reads the steps of the runs in the store of the client, in order. last names the final step, or "the start" when none ran.
func dryLines(client *Client) (lines []string, last string, err error) {
	runs, err := dbos.ListWorkflows(client.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName))
	last = "the start"

	for _, run := range runs {
		steps, stepErr := dbos.GetWorkflowSteps(client.flow, run.ID, dbos.WithStepsLoadOutput(true))
		err = errors.Join(err, stepErr)

		for _, step := range steps {
			lines = append(lines, dryLine(len(lines)+1, step))
			last = fmt.Sprintf("step %d %s", len(lines), step.StepName)
		}
	}

	return lines, last, err
}

// dryLine is one line for a step: its number, its name, then its fake result or its error.
func dryLine(n int, step dbos.StepInfo) string {
	if step.Error != nil {
		return fmt.Sprintf("step %d %s: FAILED %s", n, step.StepName, shorten(step.Error.Error()))
	}

	text, ok := step.Output.(string)
	if !ok {
		text = fmt.Sprint(step.Output)
	}

	return fmt.Sprintf("step %d %s: %s", n, step.StepName, shorten(text))
}

// shorten puts a text on one line and cuts it at dryOutLimit characters.
func shorten(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > dryOutLimit {
		return string(runes[:dryOutLimit]) + "..."
	}

	return text
}

// dryAgents ends every agent as done. It writes the output file of the request with one fixed value for each declared output (the verdict approve), and reports the required skills as used.
type dryAgents struct{ skills []string }

func (a dryAgents) Run(_ context.Context, request AgentRequest) (agent.Result, error) {
	if len(request.Outputs) > 0 {
		// The file holds one string for each declared output, and an empty findings list for a call that forbids follow-ups.
		fields := []string{`"findings":[]`}
		for _, name := range request.Outputs {
			fields = append(fields, strconv.Quote(name)+":"+strconv.Quote(dryOutput(name)))
		}

		raw := []byte("{" + strings.Join(fields, ",") + "}")

		if err := errors.Join(os.MkdirAll(filepath.Dir(request.OutputFile), dryDirMode), os.WriteFile(request.OutputFile, raw, dryFileMode)); err != nil {
			return agent.Result{}, fmt.Errorf("write the fake outputs: %w", err)
		}
	}

	return agent.Result{Outcome: agent.OutcomeExited, Session: "dry-session", Transcript: "/dry.jsonl", Text: "dry run: no agent ran", Skills: a.skills}, nil
}

// dryOutput is the fixed value of a declared output in a dry run: a review approves, and any other output holds the text dry.
func dryOutput(name string) string {
	if name == outputVerdict {
		return "approve"
	}

	return "dry"
}

// dryGate passes on the commit of the dry-run folder.
type dryGate struct{ head string }

func (dryGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (dryGate) Submit(context.Context, string, string) (GateJob, error) { return GateJob{}, nil }

func (g dryGate) Run(context.Context, string, string, GateJob) (GateResult, error) {
	return GateResult{Passed: true, Head: g.head, Tree: dryTree}, nil
}

type dryLogins struct{}

func (dryLogins) Pick(context.Context, string, string, []string, bool) (string, error) {
	return dryLogin, nil
}

type dryLifecycle struct{}

func (dryLifecycle) Claim(context.Context, string, string) (string, error) { return dryOwner, nil }

func (dryLifecycle) Release(context.Context, string, string, string) error { return nil }

type dryBeats struct{}

func (dryBeats) Beat(context.Context, string, string) (string, error) { return "", nil }

func (dryBeats) Renewed(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, nil
}

// dryForge shows an open pull request at the dry-run commit.
type dryForge struct{ head string }

func (f dryForge) Pull(context.Context, string, string) (review.Standing, error) {
	return review.Standing{Number: dryPull, Head: f.head}, nil
}

func (f dryForge) Reviewed(ctx context.Context, project, item, _ string) (review.Standing, error) {
	return f.Pull(ctx, project, item)
}

func (dryForge) Ready(context.Context, string, string) error { return nil }

func (dryForge) ClosingIssues(context.Context, string, string) ([]int, bool, error) {
	return nil, true, nil
}

// dryWatch shows the pull request as merged.
type dryWatch struct{}

func (dryWatch) Watch(context.Context, string, int) (review.Outcome, error) {
	return review.Outcome{Kind: review.Merged}, nil
}

func (dryWatch) SetDraft(context.Context, string, int, bool) error { return nil }

type dryTrees struct{ dir string }

func (t dryTrees) Path(context.Context, string, string) (string, error) { return t.dir, nil }

func (t dryTrees) Ensure(context.Context, string, string) (string, error) { return t.dir, nil }

// dryCommands runs no program: each command call exits with code 0 and prints nothing.
type dryCommands struct{}

func (dryCommands) Run(context.Context, CommandRequest) (CommandOutput, error) {
	return CommandOutput{}, nil
}

type dryItems struct{ item string }

func (i dryItems) Fields(context.Context, string, string) (map[string]string, error) {
	return map[string]string{"id": i.item, "title": "a dry-run item", "body": "P0\tno tracker was read\tno body"}, nil
}

// dryWriter accepts every write to the tracker and keeps none.
type dryWriter struct{}

func (dryWriter) Create(context.Context, string, string, trackerstore.NewItem) (string, bool, error) {
	return "dry-1", true, nil
}

func (dryWriter) SetStatus(context.Context, string, string, trackerstore.Status) error { return nil }

func (dryWriter) Comment(context.Context, string, string, string, string) error { return nil }

// dryPreflight finds the pull request as the owner left it.
type dryPreflight struct{}

func (dryPreflight) Preflight(context.Context, string, string, preflight.Request, string) (preflight.Result, error) {
	return preflight.Result{Stop: preflight.Unchanged, Summary: "dry run"}, nil
}
