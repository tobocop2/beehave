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
	"time"
	"unicode"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/check/shell"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
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
	// errDenied means the provider refused a run that exited with code 0, for a reason that no wait cures.
	errDenied = errors.New("the provider refused the run")
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
	// guard is the command line of `beehave check command`, without its flags. Empty means the agent runs with no hook, and the check after the run is the only one.
	guard string
	// maxTurns and maxBudget are agents.max_turns and agents.max_budget_usd. maxRunBudget is agents.max_run_budget_usd. 0 means no cap.
	maxTurns     int
	maxBudget    float64
	maxRunBudget float64
	// maxTreeBudget is agents.max_tree_budget_usd. 0 means no cap.
	maxTreeBudget float64
	// prices is the price table that estimates the cost of a CLI that reports tokens and no dollars.
	prices agent.Prices
	// home returns the home folder of the owner, which a sandboxed step cannot read.
	home func() (string, error)
	// identity returns the git identity that git reads at the work folder of a step, which the commits of a sandboxed step carry.
	identity func(ctx context.Context, git gitexec.Runner) agent.GitIdentity
	// repos finds the repository of a worktree of a project with a repository, whose identity and hooks folder the git of the engine reads.
	repos gitexec.Repositories
}

// newAgents runs the agent CLIs with the environment of the process.
func newAgents(cfg config.Config, opts flow.Options, trees treeFinder, table adapters) agents {
	repos := gitexec.NewRepositories(reposFolder(opts.StateDir))

	return agents{
		runners:  cfg.Runners,
		trees:    trees,
		projects: cfg.Projects,
		adapter:  table.agent,
		env:      opts.Env,
		run:      func(ctx context.Context, spec agent.Spec) agent.Result { return agent.Runner{}.Run(ctx, spec) },
		guard:    guardCommand(os.Executable),
		home:     os.UserHomeDir,
		identity: gitIdentity,
		repos:    repos,

		maxTurns: cfg.Agents.MaxTurns, maxBudget: cfg.Agents.MaxBudgetUSD, maxRunBudget: cfg.Agents.MaxRunBudgetUSD, maxTreeBudget: cfg.Agents.MaxTreeBudgetUSD,
		prices: agent.DefaultPrices(),
	}
}

// guardCommand returns the shell command line that starts `beehave check command` from this binary, or "" when the path of the binary is unknown.
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

func (a agents) specOf(request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter, tap *sessionTap, guard string, box *agent.SandboxPlan, skills []string) agent.Spec {
	return agent.Spec{
		Sandbox: box, Skills: skills,
		Adapter: adapter, Runner: runner, Dir: dir, Prompt: request.Prompt, Env: a.env,
		Timeout: request.Timeout, IdleTimeout: request.IdleTimeout, Model: request.Model, Resume: request.Resume,
		MaxTurns: a.maxTurns, MaxBudget: lowerCap(a.maxBudget, request.MaxBudget),
		Log: tap, OnStart: request.Started, Guard: guard, NoEdits: request.NoEdits, Write: outputWrites(request),
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
// A step that asks for forbidden commands to be refused does not start without its guard. An adapter that has no hook returns an empty launch for a
// guard command, so the empty launch is the sign of no hook; an adapter that adds settings with no hook would break this check.
func noGuardNote(adapter agent.Adapter, guard string, forbid []model.FindingKind) string {
	switch {
	case len(forbid) == 0:
		return ""
	case guard == "":
		return "The step lists forbid_commands, but beehave cannot find the path of its own program to start the check of the commands. The step did not start."
	case adapter.Guard(guard, "", nil, nil).Empty():
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

	return a.runIn(ctx, request, runner, dir)
}

// runIn starts the agent of the runner in the folder of the item. The step does not start when its sandbox or its skills cannot be made.
func (a agents) runIn(ctx context.Context, request flow.AgentRequest, runner model.Runner, dir string) (agent.Result, error) {
	adapter, protect := a.adapter(runner.Agent), a.protection(request, dir)
	guard := a.guardFor(request.Forbid, protect)

	box, note := a.boxOf(ctx, request, runner, dir, adapter, guard)
	skills, missing := a.skillsOf(request, runner)

	if note = cmp.Or(note, missing); note != "" {
		return agent.Result{Outcome: agent.OutcomeNotStarted, Note: note}, nil
	}

	request, resumed := replayOf(request, runner, dir, adapter)
	tap := a.tapOf(adapter, request)

	result, err := endOf(adapter, runner, dir, tap, a.runCosted(ctx, tap, a.specOf(request, runner, dir, adapter, tap, guard, box, skills), request.Model), request.Forbid, protect)
	result.Resumed, result.Denials = resumed, markClosed(result.Denials, box)

	return result, err
}

// tapOf returns the log of a run. It gives each new session id to the request, and each message text and tool call.
func (a agents) tapOf(adapter agent.Adapter, request flow.AgentRequest) *sessionTap {
	tap := newSessionTap(adapter, a.prices, request.Named)
	tap.said = request.Said

	return tap
}

// replayOf returns the request to run for a start that follows a lost run, and whether it resumes the session of that run.
// The session resumes when the CLI has a resume option and the transcript file of the session exists: the run then continues the session with the short prompt.
// Otherwise the run starts as the request says, with the note after the prompt. A request that follows no lost run stays as it is.
func replayOf(request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter) (flow.AgentRequest, bool) {
	lost := request.Lost
	if lost == nil {
		return request, false
	}

	if _, err := os.Stat(adapter.TranscriptPath(runner.ConfigDir, dir, lost.Session)); err == nil && adapter.Profile().Resume != nil {
		request.Resume, request.Prompt = lost.Session, lost.Continue

		return request, true
	}

	request.Prompt += "\n\n" + lost.Note

	return request, false
}

// runCosted runs the spec and gives the result its cost: the one the CLI reported, or an estimate from the tokens of the run for the model.
func (a agents) runCosted(ctx context.Context, tap *sessionTap, spec agent.Spec, modelName string) agent.Result {
	result := a.run(ctx, spec)
	result.Spend = tap.spent(modelName, result.Streamed)

	return result
}

// unpricedCapNote returns the reason why a run cannot start, or "" when it can. A dollar cap on a CLI that does not cap dollars itself is checked against the
// cost that beehave adds up after each run, and a model with no price gives no cost to add: the cap could not be checked, so the run does not start.
// A CLI that reports the dollars of each step (Profile.StreamsCost) needs no price: the runner caps the sum of those dollars.
func (a agents) unpricedCapNote(adapter agent.Adapter, request flow.AgentRequest) string {
	capped := a.maxRunBudget > 0 || a.maxTreeBudget > 0 || lowerCap(a.maxBudget, request.MaxBudget) > 0
	if !capped || a.prices.Has(request.Model) {
		return ""
	}

	if profile := adapter.Profile(); profile.BudgetFlag != "" || profile.StreamsCost {
		return ""
	}

	name := "the default model"
	if request.Model != "" {
		name = "the model " + request.Model
	}

	return fmt.Sprintf("The run has a cost cap, but the agent CLI cannot cap dollars itself and %s has no price in the price table, so beehave cannot check the cap. "+
		"The step did not start. Add a price for the model, or set agents.max_run_budget_usd, agents.max_tree_budget_usd and agents.max_budget_usd to 0.", name)
}

// protection returns the paths that the step must not change, and whether the project is private.
func (a agents) protection(request flow.AgentRequest, dir string) shell.Protection {
	protect := protectionOf(request.Protect, dir)
	protect.Private = a.projects[request.Project].IsPrivate()

	return protect
}

// boxOf returns the sandbox plan of a step, or nil when the step has no sandbox. A step that cannot start, for want of its guard, its sandbox or a price to check its cost cap, returns the reason.
func (a agents) boxOf(ctx context.Context, request flow.AgentRequest, runner model.Runner, dir string, adapter agent.Adapter, guard string) (*agent.SandboxPlan, string) {
	note := cmp.Or(a.unpricedCapNote(adapter, request), noGuardNote(adapter, guard, request.Forbid), a.outputNote(request, runner))
	if note != "" {
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

// skillsOf returns the folder of each skill that the prompt of the step declares, in the config folder of the runner, or the reason why the step cannot start.
// A step that declares no skill needs no folder.
func (a agents) skillsOf(request flow.AgentRequest, runner model.Runner) ([]string, string) {
	if len(request.Skills) == 0 {
		return nil, ""
	}

	home, err := a.home()
	if err != nil {
		return nil, "the prompt names skills, and beehave cannot find the home folder to look for them in. The step did not start."
	}

	return skillFolders(request.Skills, a.configFolder(runner), home)
}

// configFolder returns the config folder of the runner. A runner with none gets the default folder of its CLI under the home folder, because the launch
// sets the config variable to the empty text and the CLI then reads its default folder. The result is empty when the CLI has no such folder.
func (a agents) configFolder(runner model.Runner) string {
	if runner.ConfigDir != "" {
		return runner.ConfigDir
	}

	if home := a.adapter(runner.Agent).Profile().ConfigHome; home != "" {
		return filepath.Join("~", home)
	}

	return ""
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

// limitedBy gives a run that exited the outcome, the limit and a note of the provider limit that refused more work, and reports whether a limit did.
// The note of the limit comes after the note of the runner, such as an error that wrote the output, as the budget cap adds its note.
// A run that timed out, was canceled or did not start keeps its outcome.
func limitedBy(result agent.Result, limit *agent.Limit) (agent.Result, bool) {
	if limit == nil || result.Outcome != agent.OutcomeExited {
		return result, false
	}

	result.Outcome, result.Limit = agent.OutcomeLimited, limit

	return result.AddNote(limitNote(*limit)), true
}

// limitNote says which provider limit refused more work and when it resets.
func limitNote(limit agent.Limit) string {
	name := limit.Name()
	if limit.ResetsAt.IsZero() {
		return "the provider refused more work at its " + name + ", and the agent gave no reset time"
	}

	return fmt.Sprintf("the provider refused more work at its %s until %s", name, limit.ResetsAt.Format(time.RFC3339))
}

// endOf returns the result of a run with what its output says about its end. A run that exited and that the provider refused for a reason that no wait
// cures fails (deniedRun), even when a line before the refusal reported a limit. describe reads the end of any other run, so a run that timed out,
// was canceled, did not start or ended at the budget cap keeps its outcome.
func endOf(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result, forbid []model.FindingKind, protect shell.Protection) (agent.Result, error) {
	if reason := tap.denial(); reason != "" && result.Outcome == agent.OutcomeExited {
		return deniedRun(result, reason)
	}

	return describe(adapter, runner, dir, tap, result, forbid, protect)
}

// deniedRun fails a run that the provider refused for a reason that no wait cures. The reason is a note that comes after the note of the runner.
// A run that exited with another code than 0 fails by that code. A run that exited with code 0 fails with an error that names the reason.
func deniedRun(result agent.Result, reason string) (agent.Result, error) {
	result = result.AddNote(reason)
	if result.ExitCode != 0 {
		return result, nil
	}

	return result, fmt.Errorf("%w: %s", errDenied, reason)
}

// sessionOf adds the cap outcome and the session of a run to the result. A run that the runner ended at the budget cap, or that a provider limit stopped,
// has its outcome already (ended) and may not have named a session: that is no error.
func sessionOf(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result, stop agent.Stop, ended bool) (agent.Result, error) {
	if ended {
		result, err := withSession(adapter, runner, dir, tap, result)
		if errors.Is(err, errNoSession) {
			err = nil
		}

		return result, err
	}

	result, err := capOrFail(result, stop, tap.failure())
	if err != nil {
		return result, err
	}

	return withSession(adapter, runner, dir, tap, result)
}

// describe adds the session, the transcript path, the last message, the skill calls and the violations of the forbidden kinds to the result of a run that exited with code 0,
// that the runner ended at the budget cap, or that a provider limit stopped.
// It reads them from the output lines that the tap kept. The violations are the second line of defense: the hook of the run refuses a forbidden command before the call.
func describe(adapter agent.Adapter, runner model.Runner, dir string, tap *sessionTap, result agent.Result, forbid []model.FindingKind, protect shell.Protection) (agent.Result, error) {
	// The result line says whether a cap stopped the run. The CLI may exit with any code then, so the stop decides, not the exit code.
	stop := tap.stopped()
	// The runner ends a run as a budget cap when the dollars on the output pass the cap, or when a capped run reported none. The cap is an outcome: the run
	// still produced a session, a message and commands, and the violations check must see them. The run may have been killed before it named a session.
	budgetCap := result.Outcome == agent.OutcomeBudgetCap
	// A provider limit is an outcome in the same way, whatever the exit code, and its error result is no failure of the agent.
	result, limited := limitedBy(result, tap.limited())

	if !budgetCap && !limited && (result.Outcome != agent.OutcomeExited || (result.ExitCode != 0 && stop == "")) {
		return result, nil
	}

	result, err := sessionOf(adapter, runner, dir, tap, result, stop, budgetCap || limited)
	if err != nil {
		return result, err
	}

	if len(forbid) > 0 {
		result.Violations = violations(tap.shellCalls(), forbid, protect, notesOf(tap, result))
	}

	result.Denials = tap.deniedCalls()

	return result, nil
}

// deniedCalls returns the tool calls that the permission check of the CLI refused.
func (t *sessionTap) deniedCalls() []agent.Denial {
	t.mu.Lock()
	defer t.mu.Unlock()

	return slices.Clone(t.denials)
}

// notesOf returns the violation lines that say the check did not see the whole run: the gaps in the output, and a guard that did not load.
func notesOf(tap *sessionTap, result agent.Result) []string {
	notes := tap.incomplete()
	if result.GuardMissing {
		notes = append(notes, guardNotLoaded)
	}

	return notes
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
	skills  []agent.Call
	refused map[string]bool
	// succeeded holds the ids of the calls whose result reported no error.
	succeeded map[string]bool
	// denials are the tool calls that the permission check of the CLI refused, from the result line.
	denials []agent.Denial
	// dropped counts the lines that the runner cut for length. invalid counts the lines that the adapter cannot read. ended is true after the line that ends the run.
	dropped, invalid int
	ended            bool
	stop             agent.Stop
	failed           string
	// limit is the first provider limit that a line reported.
	limit *agent.Limit
	// denied is the first reason that a line gave for a refusal that no wait cures.
	denied string
	// cost is the total cost in dollars of the last result line that reported one.
	cost float64
	// usage is the sum of the tokens that the lines reported.
	usage  agent.Tokens
	prices agent.Prices
	// named receives each new session id at the line that holds it, or is nil.
	named func(session string)
	// said receives each message text and tool call at the line that holds it, or is nil.
	said func(agent.Said)
}

// newSessionTap returns a tap that estimates the cost of a run from the price table, and gives each new session id to named, when named is set.
func newSessionTap(adapter agent.Adapter, prices agent.Prices, named func(session string)) *sessionTap {
	return &sessionTap{adapter: adapter, refused: map[string]bool{}, succeeded: map[string]bool{}, prices: prices, named: named}
}

// The violation lines that say the check did not see the whole output of the run.
const (
	lineTooLong     = "a line of the output was too long to check"
	lineNotReadable = "the output of the run was not complete: it holds a line that is not valid JSON"
	noResultLine    = "the output of the run was not complete: it has no result line"
	guardNotLoaded  = "the command guard did not load, so the shell commands of the run were not checked before they ran"
)

func (t *sessionTap) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, line := range bytes.Split(data, []byte("\n")) {
		session, text := t.adapter.Line(string(line))
		if session != "" && session != t.session {
			t.session = session
			t.name(session)
		}

		if text != "" {
			t.text = text
		}

		t.see(string(line))
	}

	return len(data), nil
}

// name gives a new session id to the callback of the tap, when it has one. The caller holds the lock, so the ids arrive in the order of the lines.
func (t *sessionTap) name(session string) {
	if t.named != nil {
		t.named(session)
	}
}

// see records the calls and the state of the stream that one line holds. The caller holds the lock.
func (t *sessionTap) see(line string) {
	if strings.TrimSpace(line) == agent.DroppedLine {
		t.dropped++

		return
	}

	seen := t.adapter.Calls(line)
	t.shell, t.skills = append(t.shell, seen.Shell...), append(t.skills, seen.Skills...)
	t.speak(seen.Said)

	mark(t.refused, seen.Refused)
	mark(t.succeeded, seen.Succeeded)

	t.addEnd(seen)
	t.addCost(seen)

	if seen.Invalid {
		t.invalid++
	}
}

// speak gives each message text and tool call of a line to the callback of the tap, when it has one. The caller holds the lock, so they arrive in the order of the lines.
func (t *sessionTap) speak(said []agent.Said) {
	if t.said == nil {
		return
	}

	for _, one := range said {
		t.said(one)
	}
}

// mark adds each id to the set.
func mark(set map[string]bool, ids []string) {
	for _, id := range ids {
		set[id] = true
	}
}

// addEnd records how one line says the run ended: the result line, its cap, its error, the tool calls that the permission check refused, the first
// provider limit, and the first refusal that no wait cures. The caller holds the lock.
func (t *sessionTap) addEnd(seen agent.Seen) {
	t.ended = t.ended || seen.Result
	t.denials = append(t.denials, seen.Denials...)
	t.stop, t.failed = cmp.Or(seen.Stop, t.stop), cmp.Or(seen.Failed, t.failed)
	t.denied = cmp.Or(t.denied, seen.Denied)

	if t.limit == nil {
		t.limit = seen.Limit
	}
}

// addCost records the dollars and the tokens that one line holds. The caller holds the lock.
func (t *sessionTap) addCost(seen agent.Seen) {
	t.cost = cmp.Or(seen.Cost, t.cost)
	t.usage = t.usage.Add(seen.Usage)
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

// skillCalls returns the skill of each skill call whose result reported no error, so a skill that did not load is left out.
func (t *sessionTap) skillCalls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var skills []string

	for _, call := range t.skills {
		if t.succeeded[call.ID] {
			skills = append(skills, call.Text)
		}
	}

	return skills
}

// spent returns the cost of the run: the dollars that the result line reported, else streamed (the sum of the dollars that single lines reported, which the
// runner read, Result.Streamed), or 0 when it reported none and no tokens. The tap does not read the dollars of a line itself.
// A run whose CLI reported no cost but did report tokens gets an estimate for the model from the price table, marked Estimated.
// If the model has no price, the cost is marked Unknown, which is not the same as a cost of 0.
func (t *sessionTap) spent(modelName string, streamed float64) agent.Spend {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch dollars, estimated := t.prices.Estimate(modelName, t.usage); {
	case t.cost > 0:
		return agent.Spend{Cost: t.cost}
	case streamed > 0:
		return agent.Spend{Cost: streamed}
	case estimated:
		return agent.Spend{Cost: dollars, Estimated: true}
	case t.usage != agent.Tokens{}:
		return agent.Spend{Unknown: true}
	}

	return agent.Spend{}
}

// stopped returns the cap that the result line reported, or "" when it reported none.
func (t *sessionTap) stopped() agent.Stop {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.stop
}

// limited returns the first provider limit that the output reported, or nil when it reported none or the run went on to a result line with no error.
// A result line with no error is a success or a cap: the CLI took more work after the limit, so the limit did not stop the run.
func (t *sessionTap) limited() *agent.Limit {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.wentOn() {
		return nil
	}

	return t.limit
}

// denial returns the first reason that the output gave for a refusal that no wait cures, or "" when it gave none or the run went on to a result line with
// no error, as limited does.
func (t *sessionTap) denial() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.wentOn() {
		return ""
	}

	return t.denied
}

// wentOn reports whether the run reached a result line with no error, so no refusal before that line stopped the run. The caller holds the lock.
func (t *sessionTap) wentOn() bool {
	return t.ended && t.failed == ""
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
