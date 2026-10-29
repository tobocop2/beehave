package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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
)

// agents runs an agent request with the adapter of the runner it names.
type agents struct {
	runners []model.Runner
	trees   treeFinder
	adapter func(model.Agent) agent.Adapter
	env     map[string]string
	run     func(ctx context.Context, spec agent.Spec) agent.Result
	// guard is the command line of `obee check command`, without its flags. Empty means the agent runs with no hook, and the check after the run is the only one.
	guard string
}

// newAgents runs the agent CLIs with the environment of the process.
func newAgents(cfg config.Config, opts flow.Options, trees treeFinder, table adapters) agents {
	return agents{
		runners: cfg.Runners,
		trees:   trees,
		adapter: table.agent,
		env:     opts.Env,
		run:     func(ctx context.Context, spec agent.Spec) agent.Result { return agent.Runner{}.Run(ctx, spec) },
		guard:   guardCommand(os.Executable),
	}
}

// guardCommand returns the shell command line that starts `obee check command` from this binary, or "" when the path of the binary is unknown.
func guardCommand(executable func() (string, error)) string {
	path, err := executable()
	if err != nil {
		return ""
	}

	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "' check command"
}

// guardFor returns the hook command of a run that forbids kinds, or "" when the run forbids none or has no guard.
func (a agents) guardFor(forbid []model.FindingKind) string {
	if a.guard == "" || len(forbid) == 0 {
		return ""
	}

	names := make([]string, 0, len(forbid))
	for _, kind := range forbid {
		names = append(names, string(kind))
	}

	return a.guard + " --forbid " + strings.Join(names, ",")
}

func (a agents) specOf(request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter, tap *sessionTap, guard string) agent.Spec {
	return agent.Spec{
		Adapter: adapter, Runner: runner, Dir: dir, Prompt: request.Prompt, Env: a.env,
		Timeout: request.Timeout, IdleTimeout: request.IdleTimeout, Model: request.Model, Resume: request.Resume,
		Log: tap, OnStart: request.Started, Guard: guard,
	}
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
	guard := a.guardFor(request.Forbid)

	if note := noGuardNote(adapter, guard, request.Forbid); note != "" {
		return agent.Result{Outcome: agent.OutcomeNotStarted, Note: note}, nil
	}

	tap := newSessionTap(adapter)
	result := a.run(ctx, a.specOf(request, runner, dir, adapter, tap, guard))

	return describe(adapter, runner, dir, tap, result, request.Forbid)
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

// describe adds the session, the transcript path, the last message, the skill calls and the violations of the forbidden kinds to the result of a run that exited with code 0.
// It reads them from the output lines that the tap kept. The violations are the second line of defense: the hook of the run refuses a forbidden command before the call.
func describe(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result, forbid []model.FindingKind) (agent.Result, error) {
	if result.Outcome != agent.OutcomeExited || result.ExitCode != 0 {
		return result, nil
	}

	result.Session, result.Text = tap.found()
	if result.Session == "" {
		return result, errNoSession
	}

	result.Transcript = adapter.TranscriptPath(runner.ConfigDir, dir, result.Session)
	result.Skills = tap.skillCalls()

	if len(forbid) > 0 {
		result.Violations = violations(tap.shellCalls(), forbid, tap.incomplete())
	}

	return result, nil
}

// violations scans each Bash call and returns one line for each hit of a listed kind: the kind, a colon and the command that matched.
// The notes say that the check did not see the whole output. They always stay, and they count in the cap.
// The hits and notes together fill at most maxViolationLines lines, and one line gives the number of hits left out.
func violations(calls []string, forbid []model.FindingKind, notes []string) []string {
	var lines []string

	for _, call := range calls {
		for _, finding := range shell.Scan(call) {
			if slices.Contains(forbid, finding.Kind) {
				lines = append(lines, fmt.Sprintf("%s: %s", finding.Kind, snippet(finding.Text)))
			}
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

func (t *sessionTap) found() (string, string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.session, t.text
}
