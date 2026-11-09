package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// Outcome says how an agent run ended.
type Outcome string

// The ways an agent run ends.
const (
	OutcomeExited     Outcome = "exited"
	OutcomeTimedOut   Outcome = "timed-out"
	OutcomeIdle       Outcome = "idle"
	OutcomeCancelled  Outcome = "cancelled" //nolint:misspell // the same spelling as the note proc prints
	OutcomeNotStarted Outcome = "not-started"
	// OutcomeTurnCap and OutcomeBudgetCap mean the run stopped at the cap of turns or of dollars. The caller sets them from the result line of the CLI.
	// A profile without a turn flag gets OutcomeTurnCap from the runner, when the run starts more shell calls than the cap.
	// A profile with StreamsCost gets OutcomeBudgetCap from the runner when the dollars on the output pass the cap, and also when a capped run reports no dollars.
	// A cap crossed on the last line (read after the process exited) ends the run as OutcomeBudgetCap too.
	OutcomeTurnCap   Outcome = "turn-cap"
	OutcomeBudgetCap Outcome = "budget-cap"
)

const (
	// floatBits is the bit size of a dollar amount in a flag.
	floatBits = 64
	// runDirMode and launchFileMode are the modes of the folders and the files that a launch writes.
	runDirMode     = 0o700
	launchFileMode = 0o600
)

var (
	errTotalTimeout = errors.New("total timeout")
	errIdleTimeout  = errors.New("idle timeout")
	errTurnCap      = errors.New("turn cap")
	errBudgetCap    = errors.New("budget cap")
)

// Spec describes one agent run. A zero Timeout or IdleTimeout means no limit of that kind.
// OnStart, when set, receives the pid of the agent process as soon as it runs.
// A non-empty Model or Resume adds the model flag or the resume flag of the profile; a profile without that flag refuses the run.
// A nil Adapter means the agent of the runner has no adapter, and the run does not start.
type Spec struct {
	Adapter     Adapter
	Runner      model.Runner
	Dir         string
	Prompt      string
	Env         map[string]string
	Token       string
	Timeout     time.Duration
	IdleTimeout time.Duration
	Model       string
	Resume      string
	// MaxTurns and MaxBudget cap one run. 0 means no cap. A profile without the cost flag refuses a set MaxBudget, unless it sets StreamsCost:
	// the runner then sums the dollars on the output and ends the run with OutcomeBudgetCap when the sum passes MaxBudget. That run can pass the cap by the cost of one step.
	// A profile without the turn flag has no turns of its own: the runner counts the shell calls of the run, and a call past MaxTurns ends the run with OutcomeTurnCap.
	MaxTurns  int
	MaxBudget float64
	Log       io.Writer
	OnStart   func(pid int)
	// Guard, when set, is the shell command that the CLI runs before each shell command of the agent. An adapter with no such option ignores it.
	Guard string
	// NoEdits, when set, leaves out the arguments of the profile that let the CLI edit files (Profile.EditArgs) and adds the ones that turn editing off
	// (Profile.NoEditArgs and Profile.NoEditEnv).
	NoEdits bool
	// Sandbox, when set, confines the shell of the run by the plan. The run does not start if the adapter has no sandbox, the system is Windows or the CLI is too old.
	// The environment of the CLI then holds no proxy variable and no agent socket, and git reads no global or system config.
	Sandbox *SandboxPlan
}

// Result is what a run left behind. Stderr is the redacted tail of the error stream.
// Session, Transcript, Text, Skills and Violations describe the finished session: its id, the path of its transcript, its last message, the skills it called
// and one line for each Bash call that broke a forbidden kind. The Runner leaves them empty. The caller that owns the Adapter fills them from the output lines.
// Spend is the cost of the run, filled the same way: the cost that the CLI reported on its result line, or an estimate from the tokens, or the mark
// that the cost is unknown.
// Streamed is the sum of the dollars that single output lines reported (Seen.Spent), which the runner adds for a profile with StreamsCost, and 0 for any other.
// The runner is the one reader of those dollars; the caller takes Streamed and does not read Seen.Spent itself.
type Result struct {
	Spend
	Streamed   float64
	Outcome    Outcome
	ExitCode   int
	Stderr     string
	Note       string
	Session    string
	Transcript string
	Text       string
	Skills     []string
	Violations []string
	// GuardMissing is true when the launch named a marker file for its hook (Launch.Loaded) and the file was not there when the run ended: the hook did not load,
	// so the CLI ran its shell commands with no check. The runner sets it; the caller turns it into a violation.
	GuardMissing bool
}

// Timer is the part of time.Timer that the idle limit and the total limit use.
type Timer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

// Runner starts agent CLIs. AfterFunc starts the idle timer and the total timer; the zero value uses time.AfterFunc.
// Version reads the version of a CLI before a sandboxed run, and OS names the operating system. Their zero values run the CLI with --version and use this system.
type Runner struct {
	AfterFunc func(d time.Duration, fire func()) Timer
	Version   func(ctx context.Context, binary string, env []string) (string, error)
	OS        string
}

// Run starts the agent, sends the prompt on stdin and copies redacted stdout lines to Spec.Log.
// The agent runs in its own process group, which dies on a timeout, a cancel or the end of the run.
func (r Runner) Run(ctx context.Context, spec Spec) Result {
	p, refusal := prepare(spec, cmp.Or(r.OS, hostOS()))
	if refusal != "" {
		return Result{Outcome: OutcomeNotStarted, Note: refusal}
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	after := r.after()

	defer startTotalTimeout(cmp.Or(spec.Timeout, p.DefaultTimeout), after, cancel)()

	idle := newIdleTimer(spec.IdleTimeout, after, cancel)
	defer idle.stop()

	return stalled(execute(ctx, spec, p, idle, r.version()), p)
}

// stalled adds the StallNote of the profile to a run that a total or idle limit ended.
func stalled(result Result, p Profile) Result {
	if p.StallNote != "" && (result.Outcome == OutcomeTimedOut || result.Outcome == OutcomeIdle) {
		result.Note = strings.TrimSpace(result.Note + " " + p.StallNote)
	}

	return result
}

// after returns the timer function of the runner, or the one of the standard library when the runner sets none.
func (r Runner) after() func(time.Duration, func()) Timer {
	if r.AfterFunc != nil {
		return r.AfterFunc
	}

	return func(d time.Duration, fire func()) Timer { return time.AfterFunc(d, fire) }
}

// version returns the function that reads the version of the CLI, or runVersion when the runner sets none.
func (r Runner) version() versionOf {
	if r.Version != nil {
		return r.Version
	}

	return runVersion
}

func prepare(spec Spec, goos string) (Profile, string) {
	if spec.Adapter == nil {
		return Profile{}, fmt.Sprintf("agent %q cannot be started yet. Set the runner to agent claude.", spec.Runner.Agent)
	}

	p := spec.Adapter.Profile()

	if problem := optionProblem(spec, p); problem != "" {
		return p, problem
	}

	if problem := cmp.Or(sandboxProblem(spec, p, goos), guardProblem(spec, p, goos)); problem != "" {
		return p, problem
	}

	if info, err := os.Stat(spec.Runner.ConfigDir); err != nil || !info.IsDir() {
		return p, fmt.Sprintf("the config_dir %q of runner %s is not a folder, so %s is not started. Log in to the runner first.",
			spec.Runner.ConfigDir, spec.Runner.Name, p.Binary)
	}

	return p, ""
}

// optionProblem says why the profile cannot set an option that the spec asks for, or returns "" when it can set each one.
func optionProblem(spec Spec, p Profile) string {
	if spec.Model != "" && p.ModelFlag == "" {
		return fmt.Sprintf("%s has no option to set a model, so model %q is not used. Remove the model from the step.", p.Binary, spec.Model)
	}

	if spec.Resume != "" && p.Resume == nil {
		return p.Binary + " has no option to resume a session, so the run does not start. Remove resume from the step."
	}

	if spec.MaxBudget > 0 && p.BudgetFlag == "" && !p.StreamsCost {
		return p.Binary + " has no option to cap the cost, so the run does not start. Set agents.max_budget_usd to 0."
	}

	return ""
}

// startTotalTimeout cancels the run when the total limit passes. It returns the function that stops the timer.
func startTotalTimeout(limit time.Duration, after func(time.Duration, func()) Timer, cancel context.CancelCauseFunc) func() {
	if limit <= 0 {
		return func() {}
	}

	timer := after(limit, func() { cancel(errTotalTimeout) })

	return func() { timer.Stop() }
}

func execute(ctx context.Context, spec Spec, p Profile, idle *idleTimer, version versionOf) Result {
	binary, err := proc.LookPath(p.Binary, spec.Env["PATH"])
	if err != nil {
		return Result{Outcome: OutcomeNotStarted, Note: p.Binary + " is not installed. Install it and put it on PATH."}
	}

	runDir, err := os.MkdirTemp("", "beehave-run-") // mode 0700
	if err != nil {
		return Result{Outcome: OutcomeNotStarted, Note: fmt.Sprintf("cannot make the run folder for %s: %v", p.Binary, err)}
	}

	defer os.RemoveAll(runDir)

	return launchRun(ctx, spec, p, binary, runDir, idle, version)
}

// launchRun writes the files of the launch into runDir, starts the CLI with the arguments and the variables of the launch, and waits for it.
func launchRun(ctx context.Context, spec Spec, p Profile, binary, runDir string, idle *idleTimer, version versionOf) Result {
	launch := launchOf(spec, runDir)

	if problem := writeFiles(runDir, launch.Files); problem != "" {
		return Result{Outcome: OutcomeNotStarted, Note: problem}
	}

	env := envFor(spec, p, launch.Env)

	if problem := versionProblem(ctx, spec, p, binary, env, version); problem != "" {
		return Result{Outcome: OutcomeNotStarted, Note: problem}
	}

	cmd := proc.GroupCommand(ctx, binary, argsOf(spec, p, launch), spec.Dir)

	cmd.Env = env
	cmd.Stdin = strings.NewReader(spec.Prompt)

	return withGuardProof(runCommand(ctx, spec, cmd, idle), runDir, launch.Loaded)
}

// withGuardProof sets GuardMissing on the result when the launch named a marker file and the file is not in the run folder.
func withGuardProof(result Result, runDir, marker string) Result {
	result.GuardMissing = marker != "" && !loaded(runDir, marker)

	return result
}

// loaded is true when the hook of the launch created its marker file in the run folder. The caller has checked that the path is local.
func loaded(runDir, name string) bool {
	if !filepath.IsLocal(name) {
		return false
	}

	_, err := os.Stat(filepath.Join(runDir, name))

	return err == nil
}

// runCommand starts the command, waits for it and returns what it left.
func runCommand(ctx context.Context, spec Spec, cmd *exec.Cmd, idle *idleTimer) Result {
	stdout, stderr, tail, spend := newStreams(spec, idle.cancel)
	cmd.Stdout, cmd.Stderr = idle.watch(stdout), idle.watch(stderr)

	idle.start()

	runErr := startAndWait(cmd, spec.OnStart)

	killLeftovers(cmd)

	return withSpend(finish(ctx, cmd, runErr, errors.Join(stdout.flush(), stderr.flush()), tail), spend)
}

// withSpend adds the dollars that the spend counter read to the result. A run that exited, with a cost cap that the counter enforces, ends as a budget cap
// when no line reported a cost or the sum is past the cap, because nothing then says the run stayed under the cap.
// A run that exited with a non-zero code and reported no cost keeps OutcomeExited and its exit code: the crash is the outcome, not the budget.
// The conversion keeps the earlier note of the run. The caller still describes a run that ends as a budget cap (session, text, violations).
func withSpend(result Result, counter *spendCounter) Result {
	if counter == nil {
		return result
	}

	result.Streamed = counter.spent

	note := counter.capNote()
	if note == "" || result.Outcome != OutcomeExited || (!counter.priced && result.ExitCode != 0) {
		return result
	}

	result.Outcome = OutcomeBudgetCap
	result.Note = strings.Join(slices.DeleteFunc([]string{result.Note, note}, func(s string) bool { return s == "" }), "; ")

	return result
}

// argsOf returns the arguments of the profile, then the model, the session to resume, the caps and the arguments of the launch.
// A resume that opens a subcommand (Profile.ResumeLast) comes after all of them.
func argsOf(spec Spec, p Profile, launch Launch) []string {
	args := slices.Clone(p.Args)
	args = append(args, editArgs(spec, p)...)

	if spec.Model != "" {
		args = append(args, p.ModelFlag, spec.Model)
	}

	if spec.Resume != "" && !p.ResumeLast {
		args = append(args, p.Resume(spec.Resume)...)
	}

	args = append(args, capArgs(spec, p)...)
	args = append(args, launch.Args...)

	if spec.Resume != "" && p.ResumeLast {
		args = append(args, p.Resume(spec.Resume)...)
	}

	return args
}

// launchOf returns what the adapter adds to the start: the sandbox of the spec, or the command guard when the spec has no sandbox.
func launchOf(spec Spec, runDir string) Launch {
	switch {
	case spec.Sandbox != nil:
		return spec.Adapter.Sandbox(*spec.Sandbox, spec.Guard, runDir)
	case spec.Guard != "":
		return spec.Adapter.Guard(spec.Guard, runDir)
	}

	return Launch{}
}

// writeFiles writes the files of a launch into runDir. It returns why it cannot, or "" when every file is written.
// A path that is not local (absolute, empty or with "..") is refused, because the file would land outside the run folder.
func writeFiles(runDir string, files map[string]string) string {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !filepath.IsLocal(name) {
			return fmt.Sprintf("the launch of the agent puts the file %q outside its run folder, so the run does not start.", name)
		}

		path := filepath.Join(runDir, name)

		if err := os.MkdirAll(filepath.Dir(path), runDirMode); err != nil {
			return fmt.Sprintf("cannot make the folder of the launch file %q: %v", name, err)
		}

		if err := os.WriteFile(path, []byte(files[name]), launchFileMode); err != nil {
			return fmt.Sprintf("cannot write the launch file %q: %v", name, err)
		}
	}

	return ""
}

// editArgs returns the arguments that let the CLI edit files, or for a spec with NoEdits the arguments that turn editing off.
func editArgs(spec Spec, p Profile) []string {
	if spec.NoEdits {
		return p.NoEditArgs
	}

	return p.EditArgs
}

// capArgs returns the options for the turn cap and the cost cap that the spec sets.
func capArgs(spec Spec, p Profile) []string {
	var args []string

	if spec.MaxTurns > 0 {
		args = append(args, p.TurnsFlag, strconv.Itoa(spec.MaxTurns))
	}

	if spec.MaxBudget > 0 && p.BudgetFlag != "" {
		args = append(args, p.BudgetFlag, strconv.FormatFloat(spec.MaxBudget, 'f', -1, floatBits))
	}

	return args
}

// killLeftovers kills the group of a command that started.
func killLeftovers(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// The group may hold no member now; the start-time guard against a reused group id comes later.
		_ = proc.KillGroup(cmd) //nolint:errcheck // ESRCH means nothing was left to kill
	}
}

// startAndWait starts the command, gives its pid to onStart when that is set, and waits for it to end.
func startAndWait(cmd *exec.Cmd, onStart func(pid int)) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	if onStart != nil {
		onStart(cmd.Process.Pid)
	}

	return cmd.Wait()
}

// newStreams returns the redacting writers for stdout (to the log) and stderr (to a tail buffer).
// When the runner enforces the turn cap, the log goes through a stepCounter that calls cancel on the call past the cap.
// A profile with StreamsCost always gets a spendCounter, which is the one reader of the dollars on the output. It also returns that counter, or nil.
// The counter calls cancel when the dollars pass the cap, and only when the profile has no cost flag of its own.
func newStreams(spec Spec, cancel context.CancelCauseFunc) (*redactWriter, *redactWriter, *proc.Tail, *spendCounter) {
	log := spec.Log
	if log == nil {
		log = io.Discard
	}

	if spec.MaxTurns > 0 && spec.Adapter.Profile().TurnsFlag == "" {
		log = &stepCounter{out: log, adapter: spec.Adapter, limit: spec.MaxTurns, cancel: cancel, seen: map[string]bool{}}
	}

	counter := newSpendCounter(spec, log, cancel)
	if counter != nil {
		log = counter
	}

	secrets := newRedactor(spec.Token)
	tail := proc.NewTail(tailBytes)

	return &redactWriter{out: log, redactor: secrets, limit: maxLineBytes},
		&redactWriter{out: tail, redactor: secrets, limit: maxLineBytes}, tail, counter
}

func finish(ctx context.Context, cmd *exec.Cmd, runErr, flushErr error, tail *proc.Tail) Result {
	result := Result{Outcome: OutcomeExited, Stderr: tail.Text()}

	switch {
	case runErr != nil && context.Cause(ctx) != nil:
		result.Outcome, result.ExitCode = outcomeOf(context.Cause(ctx)), -1
	case cmd.ProcessState != nil:
		result.ExitCode, result.Note = cmd.ProcessState.ExitCode(), errorText(flushErr)
	default:
		result.Outcome, result.Note = OutcomeNotStarted, runErr.Error()
	}

	return result
}

func outcomeOf(cause error) Outcome {
	switch {
	case errors.Is(cause, errIdleTimeout):
		return OutcomeIdle
	case errors.Is(cause, errTotalTimeout), errors.Is(cause, context.DeadlineExceeded):
		return OutcomeTimedOut
	case errors.Is(cause, errTurnCap):
		return OutcomeTurnCap
	case errors.Is(cause, errBudgetCap):
		return OutcomeBudgetCap
	}

	return OutcomeCancelled
}

func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// idleTimer cancels a run when nothing is written for the idle limit.
type idleTimer struct {
	limit  time.Duration
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	after  func(d time.Duration, fire func()) Timer
	timer  Timer
}

func newIdleTimer(limit time.Duration, after func(time.Duration, func()) Timer, cancel context.CancelCauseFunc) *idleTimer {
	return &idleTimer{limit: limit, cancel: cancel, after: after}
}

func (t *idleTimer) start() {
	if t.limit <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.timer = t.after(t.limit, func() { t.cancel(errIdleTimeout) })
}

func (t *idleTimer) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.timer != nil {
		t.timer.Stop()
	}
}

func (t *idleTimer) touch() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.timer != nil {
		t.timer.Reset(t.limit)
	}
}

// watch returns a writer that touches the timer before it passes data on.
func (t *idleTimer) watch(out io.Writer) io.Writer {
	return activityWriter{out: out, touch: t.touch}
}

type activityWriter struct {
	out   io.Writer
	touch func()
}

func (w activityWriter) Write(data []byte) (int, error) {
	w.touch()

	return w.out.Write(data)
}
