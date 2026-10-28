package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
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
)

var (
	errTotalTimeout = errors.New("total timeout")
	errIdleTimeout  = errors.New("idle timeout")
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
	Log         io.Writer
	OnStart     func(pid int)
}

// Result is what a run left behind. Stderr is the redacted tail of the error stream.
// Session, Transcript, Text and Skills describe the finished session: its id, the path of its transcript, its last message and the skills it called.
// The Runner leaves them empty. The caller that owns the Adapter fills them from the transcript.
type Result struct {
	Outcome    Outcome
	ExitCode   int
	Stderr     string
	Note       string
	Session    string
	Transcript string
	Text       string
	Skills     []string
}

// Timer is the part of time.Timer that the idle limit uses.
type Timer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

// Runner starts agent CLIs. AfterFunc starts the idle timer; the zero value uses time.AfterFunc.
type Runner struct {
	AfterFunc func(d time.Duration, fire func()) Timer
}

// Run starts the agent, sends the prompt on stdin and copies redacted stdout lines to Spec.Log.
// The agent runs in its own process group, which dies on a timeout, a cancel or the end of the run.
func (r Runner) Run(ctx context.Context, spec Spec) Result {
	p, refusal := prepare(spec)
	if refusal != "" {
		return Result{Outcome: OutcomeNotStarted, Note: refusal}
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	ctx, stop := withTotalTimeout(ctx, spec.Timeout)
	defer stop()

	idle := newIdleTimer(spec.IdleTimeout, r.AfterFunc, cancel)
	defer idle.stop()

	return execute(ctx, spec, p, idle)
}

func prepare(spec Spec) (Profile, string) {
	if spec.Adapter == nil {
		return Profile{}, fmt.Sprintf("agent %q cannot be started yet. Set the runner to agent claude.", spec.Runner.Agent)
	}

	p := spec.Adapter.Profile()

	if spec.Model != "" && p.ModelFlag == "" {
		return p, fmt.Sprintf("%s has no option to set a model, so model %q is not used. Remove the model from the step.", p.Binary, spec.Model)
	}

	if spec.Resume != "" && p.ResumeFlag == "" {
		return p, p.Binary + " has no option to resume a session, so the run does not start. Remove resume_from from the step."
	}

	if info, err := os.Stat(spec.Runner.ConfigDir); err != nil || !info.IsDir() {
		return p, fmt.Sprintf("the config_dir %q of runner %s is not a folder, so %s is not started. Log in to the runner first.",
			spec.Runner.ConfigDir, spec.Runner.Name, p.Binary)
	}

	return p, ""
}

func withTotalTimeout(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if limit <= 0 {
		return ctx, func() {}
	}

	return context.WithTimeoutCause(ctx, limit, errTotalTimeout)
}

func execute(ctx context.Context, spec Spec, p Profile, idle *idleTimer) Result {
	binary, err := proc.LookPath(p.Binary, spec.Env["PATH"])
	if err != nil {
		return Result{Outcome: OutcomeNotStarted, Note: p.Binary + " is not installed. Install it and put it on PATH."}
	}

	cmd := proc.GroupCommand(ctx, binary, argsOf(spec, p), spec.Dir)

	cmd.Env = buildEnv(spec.Env, p, spec.Runner.ConfigDir, spec.Token)
	cmd.Stdin = strings.NewReader(spec.Prompt)

	stdout, stderr, tail := newStreams(spec)
	cmd.Stdout, cmd.Stderr = idle.watch(stdout), idle.watch(stderr)

	idle.start()

	runErr := startAndWait(cmd, spec.OnStart)

	killLeftovers(cmd)

	return finish(ctx, cmd, runErr, errors.Join(stdout.flush(), stderr.flush()), tail)
}

// argsOf returns the arguments of the profile, then the model and the session to resume when the spec sets them.
func argsOf(spec Spec, p Profile) []string {
	args := slices.Clone(p.Args)

	if spec.Model != "" {
		args = append(args, p.ModelFlag, spec.Model)
	}

	if spec.Resume != "" {
		args = append(args, p.ResumeFlag, spec.Resume)
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
func newStreams(spec Spec) (*redactWriter, *redactWriter, *tailBuffer) {
	log := spec.Log
	if log == nil {
		log = io.Discard
	}

	secrets := newRedactor(spec.Token)
	tail := &tailBuffer{}

	return &redactWriter{out: log, redactor: secrets, limit: maxLineBytes},
		&redactWriter{out: tail, redactor: secrets, limit: maxLineBytes}, tail
}

func finish(ctx context.Context, cmd *exec.Cmd, runErr, flushErr error, tail *tailBuffer) Result {
	result := Result{Outcome: OutcomeExited, Stderr: string(tail.data)}

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
	if after == nil {
		after = func(d time.Duration, fire func()) Timer { return time.AfterFunc(d, fire) }
	}

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
