package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/check/shell"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

// snippetRunes is the longest command text in one violation line.
const snippetRunes = 160

// maxViolationLines is the most violation lines that a run records. One more line says how many the run left out.
const maxViolationLines = 20

var (
	errNoRunner  = errors.New("the runners file has no runner of this name")
	errNoFolder  = errors.New("the item has no worktree to run the agent in")
	errNoPrompt  = errors.New("the agent request has no prompt")
	errNoSession = errors.New("the agent left no session id in its output")
	// errAgentFailed means the result line of a run that exited with code 0 reports an error that is no cap.
	errAgentFailed = errors.New("the agent ended with an error result")
)

// agents runs an agent request with the adapter of the runner it names.
type agents struct {
	runners []model.Runner
	trees   treeFinder
	// projects holds the visibility of each repository. A project that is not here counts as public.
	projects map[string]config.Project
	adapter  func(model.Agent) agent.Adapter
	env      map[string]string
	run      func(ctx context.Context, spec agent.Spec) agent.Result
	// guard is the command line of `obee check command`, without its flags. Empty means the agent runs with no hook, and the check after the run is the only one.
	guard string
	// maxTurns and maxBudget are agents.max_turns and agents.max_budget_usd. 0 means no cap.
	maxTurns  int
	maxBudget float64
	// home returns the home folder of the owner, which a sandboxed step cannot read.
	home func() (string, error)
}

// newAgents runs the agent CLIs with the environment of the process.
func newAgents(cfg config.Config, opts flow.Options, trees treeFinder, table adapters) agents {
	return agents{
		runners:  cfg.Runners,
		trees:    trees,
		projects: cfg.Projects,
		adapter:  table.agent,
		env:      opts.Env,
		run:      func(ctx context.Context, spec agent.Spec) agent.Result { return agent.Runner{}.Run(ctx, spec) },
		guard:    guardCommand(os.Executable),
		home:     os.UserHomeDir,

		maxTurns: cfg.Agents.MaxTurns, maxBudget: cfg.Agents.MaxBudgetUSD,
	}
}

// guardCommand returns the shell command line that starts `obee check command` from this binary, or "" when the path of the binary is unknown.
func guardCommand(executable func() (string, error)) string {
	path, err := executable()
	if err != nil {
		return ""
	}

	return model.ShellQuote(path) + " check command"
}

// protectionOf returns the protection of a request: its directories, absolute, a relative one taken from the folder of the step.
func protectionOf(protect []string, dir string) shell.Protection {
	dirs := make([]string, 0, len(protect))

	for _, name := range protect {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}

		dirs = append(dirs, filepath.Clean(name))
	}

	return shell.Protection{Dirs: dirs, Base: dir}
}

// guardFor returns the hook command of a run that forbids kinds, or "" when the run forbids none or has no guard.
func (a agents) guardFor(forbid []model.FindingKind, protect shell.Protection) string {
	if a.guard == "" || len(forbid) == 0 {
		return ""
	}

	names := make([]string, 0, len(forbid))
	for _, kind := range forbid {
		names = append(names, string(kind))
	}

	words := []string{a.guard, "--forbid", strings.Join(names, ",")}
	for _, dir := range protect.Dirs {
		words = append(words, "--protect", model.ShellQuote(dir))
	}

	if protect.Private {
		words = append(words, "--visibility", string(model.VisibilityPrivate))
	}

	return strings.Join(words, " ")
}

func (a agents) specOf(request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter, tap *sessionTap, guard string, box *agent.SandboxPlan) agent.Spec {
	return agent.Spec{
		Sandbox: box,
		Adapter: adapter, Runner: runner, Dir: dir, Prompt: request.Prompt, Env: a.env,
		Timeout: request.Timeout, IdleTimeout: request.IdleTimeout, Model: request.Model, Resume: request.Resume,
		MaxTurns: a.maxTurns, MaxBudget: lowerCap(a.maxBudget, request.MaxBudget),
		Log: tap, OnStart: request.Started, Guard: guard, NoEdits: request.NoEdits,
	}
}

// lowerCap returns the lower of two caps in dollars, where 0 means no cap. The cap of a step can lower the global cap and never raise it.
func lowerCap(global, step float64) float64 {
	if global <= 0 {
		return step
	}

	if step <= 0 {
		return global
	}

	return min(global, step)
}

// noGuardNote returns the reason why a step that lists forbid_commands cannot start, or "" when the step can start.
// A step that asks for forbidden commands to be refused does not start without its guard.
func noGuardNote(adapter agent.Adapter, guard string, forbid []model.FindingKind) string {
	switch {
	case len(forbid) == 0:
		return ""
	case guard == "":
		return "The step lists forbid_commands, but obee cannot find the path of its own program to start the check of the commands. The step did not start."
	case adapter.Guard(guard) == nil:
		return "The step lists forbid_commands, but the agent CLI of the runner has no hook to refuse a command before it runs. The step did not start."
	}

	return ""
}

// Run starts the agent of the login in the worktree of the item and returns what it left: its session, its last message and the skills it called.
func (a agents) Run(ctx context.Context, request flow.AgentRequest) (agent.Result, error) {
	runner, dir, err := a.target(ctx, request)
	if err != nil {
		return agent.Result{}, err
	}

	adapter := a.adapter(runner.Agent)
	protect := a.protection(request, dir)
	guard := a.guardFor(request.Forbid, protect)

	box, note := a.boxOf(ctx, request, runner, dir, adapter, guard)
	if note != "" {
		return agent.Result{Outcome: agent.OutcomeNotStarted, Note: note}, nil
	}

	tap := newSessionTap(adapter)
	result := a.run(ctx, a.specOf(request, runner, dir, adapter, tap, guard, box))
	result.Cost = tap.spent()

	return describe(adapter, runner, dir, tap, result, request.Forbid, protect)
}

// protection returns the paths that the step must not change, and whether the project is private.
func (a agents) protection(request flow.AgentRequest, dir string) shell.Protection {
	protect := protectionOf(request.Protect, dir)
	protect.Private = a.projects[request.Project].IsPrivate()

	return protect
}

// boxOf returns the sandbox plan of a step, or nil when the step has no sandbox. A step that cannot start, for want of its guard or its sandbox, returns the reason.
func (a agents) boxOf(ctx context.Context, request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter, guard string) (*agent.SandboxPlan, string) {
	if note := noGuardNote(adapter, guard, request.Forbid); note != "" {
		return nil, note
	}

	if request.Sandbox == nil {
		return nil, ""
	}

	plan, note := a.sandboxPlan(ctx, request, runner, dir)
	if note != "" {
		return nil, note
	}

	return &plan, ""
}

// target returns the runner of the login and the worktree of the item. A request with no prompt, an unknown login or no worktree is refused.
func (a agents) target(ctx context.Context, request flow.AgentRequest) (model.Runner, string, error) {
	if request.Prompt == "" {
		return model.Runner{}, "", fmt.Errorf("%w: item %s", errNoPrompt, request.Item)
	}

	runner, found := a.runnerOf(request.Login)
	if !found {
		return model.Runner{}, "", fmt.Errorf("%w: %q", errNoRunner, request.Login)
	}

	dir, err := a.trees.Path(ctx, request.Project, request.Item)
	if err != nil {
		return model.Runner{}, "", fmt.Errorf("find the folder of item %s: %w", request.Item, err)
	}

	if dir == "" {
		return model.Runner{}, "", fmt.Errorf("%w: item %s of project %s", errNoFolder, request.Item, request.Project)
	}

	return runner, dir, nil
}

func (a agents) runnerOf(name string) (model.Runner, bool) {
	for _, runner := range a.runners {
		if runner.Name == name {
			return runner, true
		}
	}

	return model.Runner{}, false
}

// capped gives the result the outcome of the cap that the result line reports, and leaves the result as it is when no cap stopped the run.
func capped(result agent.Result, stop agent.Stop) agent.Result {
	if stop == agent.StopTurns {
		result.Outcome = agent.OutcomeTurnCap
	}

	if stop == agent.StopBudget {
		result.Outcome = agent.OutcomeBudgetCap
	}

	return result
}

// capOrFail gives the result the outcome of the cap that stopped the run. An error result that is no cap fails the run, whatever the exit code of the CLI.
func capOrFail(result agent.Result, stop agent.Stop, failed string) (agent.Result, error) {
	if failed != "" && stop == "" {
		return result, fmt.Errorf("%w: %s", errAgentFailed, failed)
	}

	return capped(result, stop), nil
}

// withSession adds the session, the transcript path, the last message and the skill calls that the tap kept. A run with no session id is an error.
func withSession(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result) (agent.Result, error) {
	result.Session, result.Text = tap.found()
	if result.Session == "" {
		return result, errNoSession
	}

	result.Transcript = adapter.TranscriptPath(runner.ConfigDir, dir, result.Session)
	result.Skills = tap.skillCalls()

	return result, nil
}

// describe adds the session, the transcript path, the last message, the skill calls and the violations of the forbidden kinds to the result of a run that exited with code 0.
// It reads them from the output lines that the tap kept. The violations are the second line of defense: the hook of the run refuses a forbidden command before the call.
func describe(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result, forbid []model.FindingKind, protect shell.Protection) (agent.Result, error) {
	// The result line says whether a cap stopped the run. The CLI may exit with any code then, so the stop decides, not the exit code.
	stop := tap.stopped()
	if result.Outcome != agent.OutcomeExited || (result.ExitCode != 0 && stop == "") {
		return result, nil
	}

	result, err := capOrFail(result, stop, tap.failure())
	if err != nil {
		return result, err
	}

	if result, err = withSession(adapter, runner, dir, tap, result); err != nil {
		return result, err
	}

	if len(forbid) > 0 {
		result.Violations = violations(tap.shellCalls(), forbid, protect, tap.incomplete())
	}

	return result, nil
}

// violations scans each Bash call and returns one line for each hit of a listed kind: the kind, a colon and the command that matched.
// The notes say that the check did not see the whole output. They always stay, and they count in the cap.
// The hits and notes together fill at most maxViolationLines lines, and one line gives the number of hits left out.
func violations(calls []string, forbid []model.FindingKind, protect shell.Protection, notes []string) []string {
	var lines []string

	for _, call := range calls {
		for _, finding := range shell.ScanWith(call, forbid, protect) {
			lines = append(lines, fmt.Sprintf("%s: %s", finding.Kind, snippet(finding.Text)))
		}
	}

	room := max(maxViolationLines-len(notes), 0)

	if left := len(lines) - room; left == 1 {
		lines = append(lines[:room], "1 more violation is not shown")
	} else if left > 1 {
		lines = append(lines[:room], fmt.Sprintf("%d more violations are not shown", left))
	}

	return append(lines, notes...)
}

// snippet collapses the white space of a command to single spaces, so a hit stays on one line, and keeps its first runes.
func snippet(text string) string {
	collapsed := []rune(strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " "))

	return string(collapsed[:min(len(collapsed), snippetRunes)])
}

// sessionTap is the log of a run. It feeds each output line to the adapter and keeps the last session id and message it finds, and every shell call and skill call.
// The runner writes one whole line at a time, so no line is split across two writes.
type sessionTap struct {
	adapter agent.Adapter
	mu      sync.Mutex
	session string
	text    string
	shell   []agent.Call
	skills  []string
	refused map[string]bool
	// dropped counts the lines that the runner cut for length. invalid counts the lines that the adapter cannot read. ended is true after the line that ends the run.
	dropped, invalid int
	ended            bool
	stop             agent.Stop
	failed           string
	// cost is the total cost in dollars of the last result line that reported one.
	cost float64
}

func newSessionTap(adapter agent.Adapter) *sessionTap {
	return &sessionTap{adapter: adapter, refused: map[string]bool{}}
}

// The violation lines that say the check did not see the whole output of the run.
const (
	lineTooLong     = "a line of the output was too long to check"
	lineNotReadable = "the output of the run was not complete: it holds a line that is not valid JSON"
	noResultLine    = "the output of the run was not complete: it has no result line"
)

func (t *sessionTap) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, line := range bytes.Split(data, []byte("\n")) {
		session, text := t.adapter.Line(string(line))
		if session != "" {
			t.session = session
		}

		if text != "" {
			t.text = text
		}

		t.see(string(line))
	}

	return len(data), nil
}

// see records the calls and the state of the stream that one line holds. The caller holds the lock.
func (t *sessionTap) see(line string) {
	if strings.TrimSpace(line) == agent.DroppedLine {
		t.dropped++

		return
	}

	seen := t.adapter.Calls(line)
	t.shell, t.skills = append(t.shell, seen.Shell...), append(t.skills, seen.Skills...)

	for _, id := range seen.Refused {
		t.refused[id] = true
	}

	t.ended = t.ended || seen.Result
	t.stop, t.failed, t.cost = cmp.Or(seen.Stop, t.stop), cmp.Or(seen.Failed, t.failed), cmp.Or(seen.Cost, t.cost)

	if seen.Invalid {
		t.invalid++
	}
}

// shellCalls returns the command of each shell call that ran. A call that the CLI refused before it ran is left out.
func (t *sessionTap) shellCalls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var commands []string

	for _, call := range t.shell {
		if call.ID == "" || !t.refused[call.ID] {
			commands = append(commands, call.Text)
		}
	}

	return commands
}

// incomplete returns one line for each way in which the check did not see the whole output of the run.
func (t *sessionTap) incomplete() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var lines []string

	if t.dropped > 0 {
		lines = append(lines, lineTooLong)
	}

	if t.invalid > 0 {
		lines = append(lines, lineNotReadable)
	}

	if !t.ended {
		lines = append(lines, noResultLine)
	}

	return lines
}

func (t *sessionTap) skillCalls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return slices.Clone(t.skills)
}

// spent returns the cost in dollars that the result line reported, or 0 when it reported none.
func (t *sessionTap) spent() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.cost
}

// stopped returns the cap that the result line reported, or "" when it reported none.
func (t *sessionTap) stopped() agent.Stop {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.stop
}

// failure returns the error subtype that the result line reported, or "" when it reported none.
func (t *sessionTap) failure() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.failed
}

func (t *sessionTap) found() (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.session, t.text
}
