package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/roundcut"
	"github.com/tobocop2/beehave/internal/roundintegration"
	"github.com/tobocop2/beehave/internal/roundsync"
	"github.com/tobocop2/beehave/internal/step"
	trackerstore "github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/takeover"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	dryProject  = "dry-run"
	dryLogin    = "dry-run"
	dryOwner    = "dry-run-owner"
	dryPull     = 1
	dryTree     = "dry-run-tree"
	dryNewTest  = "dry TestDryRun"
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

// dryChoice holds what the caller chose for a dry run.
type dryChoice struct {
	redGate, sendBack bool
	// voidGates is the number of the first gates that give no verdict.
	voidGates        int
	vars             map[string]any
	runnerSkillLoads map[string]bool
	entry            string
	skills           []string
	// spawnDepth is the max_spawn_depth of the project. 0 means config.DefaultMaxSpawnDepth.
	spawnDepth int
	// sandbox is the sandbox mode that the run stores: off unless WithSandbox gives the mode of a project.
	sandbox model.SandboxMode
}

// DryOption is a choice for a dry run.
type DryOption func(*dryChoice)

// WithRedGate makes the first gate of the dry run red, with one failed step, so that the run walks the fix round of the workflow. Each later gate passes.
func WithRedGate() DryOption { return func(c *dryChoice) { c.redGate = true } }

// WithVoidGates makes the first count gates of the dry run void: each one did not run, so its step is unknown and it gives no verdict. Each later gate passes.
// The command beehave check has no flag for it: the option is for tests of the library.
func WithVoidGates(count int) DryOption { return func(c *dryChoice) { c.voidGates = count } }

// WithSendBack makes the first review of the dry run end with the verdict changes, so that the run walks a send-back and a fix round. Each later review approves.
func WithSendBack() DryOption { return func(c *dryChoice) { c.sendBack = true } }

// WithVars gives the facts of the dry run the vars of a project. The vars are static config, so the dry run reads them as a real run does. Every other fact stays empty.
func WithVars(vars map[string]any) DryOption { return func(c *dryChoice) { c.vars = vars } }

// WithRunnerSkillLoads gives the facts of the dry run the runners of the config, as a real run has them: the fact loads_skills of each runner by name.
// The dry run then refuses a step on a runner that has no skill line, as a real run does.
func WithRunnerSkillLoads(loads map[string]bool) DryOption {
	return func(c *dryChoice) { c.runnerSkillLoads = loads }
}

// WithSpawnDepth gives the dry run the max_spawn_depth of a project, so that a spawn past the cap of the project fails as it does in a real run.
func WithSpawnDepth(depth int) DryOption { return func(c *dryChoice) { c.spawnDepth = depth } }

// WithSandbox gives the dry run the sandbox mode of a project, so that it records the sandbox of a step that sets none and refuses a sandbox = None step, as a run does.
func WithSandbox(mode model.SandboxMode) DryOption { return func(c *dryChoice) { c.sandbox = mode } }

// WithEntry starts the dry run at the named entry of the file, as a run of the engine does, so that a test walks the file it tests.
// An entry run skips the finish function. The command beehave check has no flag for it: the option is for tests of the library.
func WithEntry(name string) DryOption { return func(c *dryChoice) { c.entry = name } }

// chooseDry applies the options of a dry run in order.
func chooseDry(options []DryOption) dryChoice {
	chosen := dryChoice{sandbox: model.SandboxOff}

	for _, option := range options {
		option(&chosen)
	}

	return chosen
}

// DryRun takes one item through a workflow file over fakes, in a state folder that it deletes at the end, and prints each step in order with its result.
// The engine gets only the fakes of this file, so no forge, agent, tracker or host is reachable. A step that needs a collaborator the fakes lack fails.
// A failed run prints the failed step and its error, and returns the error.
// The dry fakes return fixed values and never reach a real service. The fakes of the tests keep their own knobs and are a separate set.
// Every gate passes, unless the call has WithRedGate.
func DryRun(ctx context.Context, file, item string, env map[string]string, out io.Writer, options ...DryOption) (err error) {
	chosen := chooseDry(options)

	root, err := os.MkdirTemp("", "beehave-dry-run-")
	if err != nil {
		return fmt.Errorf("make the dry-run folder: %w", err)
	}

	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()

	opts := Options{StateDir: filepath.Join(root, "state"), Version: "dry-run", MaxConcurrent: minMaxConcurrent, MaxReviewConcurrent: minMaxConcurrent, MaxSpawnDepth: chosen.spawnDepth, Env: env, Modules: []step.Module{StepModule()}}

	deps, err := dryPrepare(ctx, root, item, file, chosen)
	if err != nil {
		return err
	}

	runErr := dryWalk(ctx, opts, deps, item, file, chosen.entry, chosen.sandbox)
	last, printErr := printDrySteps(ctx, opts, out)

	return errors.Join(printErr, dryFailure(out, last, runErr))
}

// dryPrepare makes the worktree of the item in the folder of the run and returns the fakes that serve it, with the skills that the file checks for.
func dryPrepare(ctx context.Context, root, item, file string, chosen dryChoice) (Deps, error) {
	tree := filepath.Join(root, "tree")

	head, err := dryRepo(ctx, tree, filepath.Join(root, "origin.git"))
	if err != nil {
		return Deps{}, err
	}

	chosen.skills = workflow.RequiredSkills(file)

	for _, key := range workflow.RequiredSkillVars(file) {
		if name, named := chosen.vars[key].(string); named {
			chosen.skills = append(chosen.skills, name)
		}
	}

	return dryDeps(tree, head, item, chosen), nil
}

// dryWalk opens an engine over the deps, takes the item through the file and stops the engine.
func dryWalk(ctx context.Context, opts Options, deps Deps, item, file, entry string, sandbox model.SandboxMode) error {
	engine, err := NewEngine(ctx, opts, deps)
	if err != nil {
		return err
	}

	err = engine.Launch() //nolint:contextcheck // the engine owns the context of its runs
	if err == nil {
		_, err = engine.runEntry(item, dryProject, file, entry, RunSandbox(sandbox)) //nolint:contextcheck // the engine owns the context of its runs
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
func dryDeps(tree, head, item string, chosen dryChoice) Deps {
	agents := dryAgents{skills: chosen.skills}
	if chosen.sendBack {
		agents.sendBack = new(atomic.Bool)
		agents.sendBack.Store(true)
	}

	gate := newDryGate(tree, head, chosen.redGate)
	gate.void = chosen.voidGates

	return Deps{
		Agents: agents, Gate: gate, Logins: dryLogins{}, Lifecycle: dryLifecycle{}, Beats: dryBeats{},
		Forge: dryForge{head: head}, Rounds: dryRounds{}, Trees: dryTrees{dir: tree}, Commands: dryCommands{}, Items: dryItems{item: item},
		Writer: dryWriter{}, Reader: dryReader{}, Preflight: dryPreflight{}, Slots: drySlots, SkillLine: drySkillLine,
		Facts: func(string) model.Project {
			return model.Project{Source: tree, Vars: chosen.vars, MaxSpawnDepth: chosen.spawnDepth, RunnerSkillLoads: chosen.runnerSkillLoads}
		},
		Clock: clock{Poll: dryPoll, Settle: dryWait, Reading: dryReading()},
	}
}

// dryRepo makes the folder of the item: a git repository with two empty commits, and a bare repository that is its origin.
// The first commit is the base that the dry gate gives the remote branch of a base. Both folders sit in the folder of the run. It returns the second commit.
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
	commit := []string{"-c", "user.name=dry", "-c", "user.email=dry@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m"}

	return []dryGitStep{
		{remote, []string{"init", "-q", "--bare", "--template="}},
		{dir, []string{"init", "-q", "--template="}},
		{dir, []string{"remote", "add", remoteName, remote}},
		{dir, append(slices.Clone(commit), "dry run base")},
		{dir, append(slices.Clone(commit), "dry run")},
		{dir, []string{"show", "-s", "--format=%H"}},
	}
}

// dryGit runs one git command in a folder and returns its output.
// The command ignores the global and system git config of the user, so a hook path, a template or a signing setting cannot run or fail.
func dryGit(ctx context.Context, dir string, args []string) (string, error) {
	result := dryRunner(dir).Run(ctx, args)
	out := strings.TrimSpace(result.Stdout + result.Stderr)

	if result.ExitCode != 0 {
		return "", fmt.Errorf("prepare the dry-run worktree: git exited with code %d: %s", result.ExitCode, out)
	}

	return strings.TrimSpace(result.Stdout), nil
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
	runs, err := client.store().List(dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName))
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
		return fmt.Sprintf("step %d %s: FAILED %s", n, step.StepName, model.Clip(step.Error.Error(), dryOutLimit, "..."))
	}

	text, ok := step.Output.(string)
	if !ok {
		text = fmt.Sprint(step.Output)
	}

	return fmt.Sprintf("step %d %s: %s", n, step.StepName, model.Clip(text, dryOutLimit, "..."))
}

// dryAgents ends every agent as done. It writes the output file of the request with one fixed value for each declared output (the verdict approve, the output open empty, any other output 0).
// With sendBack set, the first agent that declares a verdict writes changes instead; the flag then clears.
type dryAgents struct {
	sendBack *atomic.Bool
	skills   []string
}

// outputVerdict is the output of a review call that holds its verdict. verdictChanges is the verdict of a head that needs a commit.
// outputOpen is the output that holds the open findings of a review as text, empty when none is open.
// outputUncaused is the output that holds the reds a lane did not cause, as a JSON list.
// outputFindings, outputResolved and outputWontfix are the outputs of the review ledger, each a JSON list.
const (
	outputVerdict  = "verdict"
	verdictChanges = "changes"
	outputOpen     = "open"
	outputUncaused = "uncaused"
	outputFindings = "findings"
	outputResolved = "resolved"
	outputWontfix  = "wontfix"
)

func (a dryAgents) Run(_ context.Context, request AgentRequest) (agent.Result, error) {
	if len(request.Outputs) > 0 {
		raw := a.outputs(request.Outputs)

		if err := errors.Join(os.MkdirAll(filepath.Dir(request.OutputFile), dryDirMode), os.WriteFile(request.OutputFile, raw, dryFileMode)); err != nil {
			return agent.Result{}, fmt.Errorf("write the fake outputs: %w", err)
		}
	}

	return agent.Result{Outcome: agent.OutcomeExited, Session: "dry-session", Transcript: "/dry.jsonl", Text: "dry run: no agent ran", Skills: a.skills}, nil
}

// outputs is the content of the output file: one string for each declared output.
func (a dryAgents) outputs(declared []string) []byte {
	sendBack := a.sendBack != nil && slices.Contains(declared, outputVerdict) && a.sendBack.Swap(false)
	fields := make([]string, 0, len(declared))

	for _, name := range declared {
		value := dryOutput(name)
		if sendBack && name == outputVerdict {
			value = verdictChanges
		}

		fields = append(fields, strconv.Quote(name)+":"+strconv.Quote(value))
	}

	return []byte("{" + strings.Join(fields, ",") + "}")
}

// dryOutput is the fixed value of a declared output in a dry run: a review approves with no open finding, the reds a lane did not cause and the outputs of the review ledger are an empty list, and any other output holds the text 0.
// The text 0 reads as a count of none, so a workflow that reads a count from an output walks the path of an approved item.
func dryOutput(name string) string {
	switch name {
	case outputVerdict:
		return "approve"
	case outputOpen:
		return ""
	case outputUncaused, outputFindings, outputResolved, outputWontfix:
		return "[]"
	}

	return "0"
}

// dryGate runs on the commit of the dry-run folder and passes. With red set, its first run is red, with one failed step, so that a dry run walks the fix round of a workflow.
// A gate with a base gets a clean check record: one new test and a clean secret scan. With void set, its first void runs give no verdict: the step is unknown.
type dryGate struct {
	dir, head string
	red       bool
	void      int
	runs      *atomic.Int32
}

func newDryGate(dir, head string, red bool) dryGate {
	return dryGate{dir: dir, head: head, red: red, runs: new(atomic.Int32)}
}

func (dryGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

// Submit records the base in the plan of the job, as the host gate does. As a fetch of the base would, it points the remote branch of the base
// at the parent of the head, so the checks before review find a head that descends from the base.
func (g dryGate) Submit(ctx context.Context, _, _, _, base, _, _ string, _ model.Slot) (GateJob, error) {
	if base != "" {
		if _, err := dryGit(ctx, g.dir, []string{"update-ref", "refs/remotes/" + remoteName + "/" + base, g.head + "^"}); err != nil {
			return GateJob{}, err
		}
	}

	return GateJob{Plan: gatepkg.Plan{Base: base}}, nil
}

func (g dryGate) Run(_ context.Context, _, _, _ string, job GateJob) (GateResult, error) {
	result := GateResult{Passed: true, Head: g.head, Tree: dryTree, Steps: []GateStep{{Name: gatepkg.StepCommand, Status: passStatus}}}
	run := int(g.runs.Add(1))
	if run == 1 && g.red {
		result.Passed, result.Steps = false, []GateStep{{Name: gatepkg.StepCommand, Status: failStatus}}
	}

	if run <= g.void {
		result.Passed, result.Steps = false, []GateStep{{Name: gatepkg.StepCommand, Status: unknownStatus}}
	}

	if job.Plan.Base != "" {
		result.Checks = &GateChecks{NewTests: []string{dryNewTest}, New: 1, Secrets: new(int), Complete: true, Revert: model.RevertPass, RevertCases: 1}
	}

	return result, nil
}

type dryLogins struct{}

func (dryLogins) Pick(context.Context, string, string, []string, bool) (string, error) {
	return dryLogin, nil
}

func (dryLogins) Reset(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// Headroom is false: a dry run holds the agents queue at its cap and adapts nothing.
func (dryLogins) Headroom(context.Context) bool { return false }

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

func (dryForge) ClosingIssues(context.Context, string, string) ([]int, bool, error) {
	return nil, true, nil
}

// Comment shows a comment as posted on the dry-run pull request.
func (dryForge) Comment(context.Context, string, string, string, string) (int, error) {
	return dryPull, nil
}

// Open shows the dry-run pull request as found, in no stack.
func (dryForge) Open(context.Context, string, string, OpenRequest) (OpenResult, error) {
	return OpenResult{Number: dryPull}, nil
}

// Push shows the head as pushed and writes nothing to the remote.
func (dryForge) Push(context.Context, string, string, string, string, string) error { return nil }

// History shows an item with no earlier pull request.
func (dryForge) History(context.Context, string, string) ([]model.PriorPull, error) {
	return nil, nil
}

// dryRounds shows every forge write of a round as done and writes nothing: the trunk is current, a landing lands, a cut is done, the integration pull request is the open dry-run
// pull request, a card shows the column that was asked for and a take passes.
type dryRounds struct{}

func (dryRounds) Sync(context.Context, string, roundsync.Request) (roundsync.Result, error) {
	return roundsync.Result{State: roundsync.Current}, nil
}

func (dryRounds) Land(_ context.Context, _ string, req roundsync.LandRequest) (roundsync.Result, error) {
	return roundsync.Result{State: roundsync.Landed, PR: req.PR, Head: req.Head, BaseTip: req.BaseTip}, nil
}

func (dryRounds) Cut(context.Context, string, roundcut.Request) (roundcut.Result, error) {
	return roundcut.Result{Stop: roundcut.Done}, nil
}

func (dryRounds) Integrate(context.Context, string, roundintegration.Request) (roundintegration.Result, error) {
	return roundintegration.Result{PR: dryPull, State: roundintegration.StateOpen}, nil
}

func (dryRounds) Place(_ context.Context, _ string, card model.Card) (string, error) {
	return card.Status, nil
}

func (dryRounds) Take(context.Context, string, TakeRequest) (takeover.Result, error) {
	return takeover.Result{}, nil
}

type dryTrees struct{ dir string }

func (t dryTrees) WithTree(_ context.Context, _, _ string, fn func(string)) error {
	fn(t.dir)

	return nil
}

// Heads reads the branches of the bare remote that the dry run made, with no global or system git config.
func (t dryTrees) Heads(ctx context.Context, _ string) (string, error) {
	return dryGit(ctx, t.dir, []string{"ls-remote", "--heads", remoteName})
}

func (t dryTrees) Ensure(context.Context, string, string, string, string) (string, error) {
	return t.dir, nil
}

// Git runs git in the folder of the dry run with no global or system git config.
func (dryTrees) Git(dir string) proc.Runner { //nolint:ireturn // the trees contract returns the interface
	return dryRunner(dir)
}

// dryRunner is the git runner of the dry run: the folder is a temporary one that the dry run made. Like every runner of gitexec, it reads no global or system git config.
func dryRunner(dir string) gitexec.Runner {
	return gitexec.New(dir, 0)
}

// dryCommands runs no program: each command call exits with code 0 and prints nothing.
type dryCommands struct{}

func (dryCommands) Run(context.Context, CommandRequest) (CommandOutput, error) {
	return CommandOutput{}, nil
}

// drySlots gives every slot class one slot on a fake host, so a command call with `slots` runs its fake program like any other call of a dry run.
func drySlots(_, cluster, _ string) (SlotGrant, error) {
	return SlotGrant{Host: cluster, Count: 1}, nil
}

// drySkillLine gives every runner a line that loads a skill, so a step with the skill keyword runs its fake agent like any other call of a dry run.
func drySkillLine(_, skill string) string {
	return "/dry-skills:" + skill
}

type dryItems struct{ item string }

func (i dryItems) Fields(context.Context, string, string) (map[string]string, error) {
	return map[string]string{"id": i.item, titleKeyword: "a dry-run item", bodyKeyword: "P0\tno tracker was read\tno body", severityField: string(model.SeverityUnknown)}, nil
}

// dryReader reads nothing from the tracker: an item has no comments and no parent.
type dryReader struct{}

func (dryReader) Comments(context.Context, string, string) ([]string, error) { return nil, nil }

func (dryReader) Parent(context.Context, string, string) (string, error) { return "", nil }

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
