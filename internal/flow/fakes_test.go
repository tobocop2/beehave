package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/review"
)

const (
	testVersion  = "v1"
	testLogin    = "alice"
	testItem     = "12"
	testPull     = 7
	testProject  = "p"
	testOwner    = "owner-"
	eventTimeout = 30 * time.Second
	pollEvery    = 5 * time.Millisecond
	// storePoll is the interval at which the two queues of an engine and a result wait read the store. Each poll parses a statement, which
	// costs most of the CPU of the package under the race detector, and the engines of the parallel tests all poll at once.
	storePoll = 25 * time.Millisecond
)

var errFake = errors.New("fake failure")

// fakeAgents records each request and counts the agents in flight, in all and for each login.
type fakeAgents struct {
	mu       sync.Mutex
	requests []AgentRequest
	inFlight int
	maxTotal int
	perLogin map[string]int
	maxLogin int
	gate     chan struct{}
	pid      int
	result   func(AgentRequest) agent.Result
	err      error
}

// Run holds each agent in flight until the test closes gate, when gate is set. It reports pid as the process of the agent, when pid is set.
// Like the command of the real runner, it starts nothing under a context that is done, and it kills the group of its process when the context ends.
func (a *fakeAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	if err := ctx.Err(); err != nil {
		return agent.Result{Outcome: agent.OutcomeNotStarted, Note: err.Error()}, err
	}

	if a.pid != 0 {
		request.Started(a.pid)

		// The kill is never stopped: a stop that wins the race against the start of the function would leave the group alive, as no release kills it now.
		// It stands in for the end of the agent: the real runner kills the group when the context ends (the cancel function of the command) and again
		// after every wait, so a phase end that leaves no group is proved here through the context only.
		context.AfterFunc(ctx, func() { _ = proc.KillGroupID(a.pid) }) //nolint:errcheck // ESRCH means the group has no member left
	}

	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.inFlight++
	a.perLogin[request.Login]++
	a.maxTotal = max(a.maxTotal, a.inFlight)
	a.maxLogin = max(a.maxLogin, a.perLogin[request.Login])
	a.mu.Unlock()

	if a.gate != nil {
		select {
		case <-a.gate:
		case <-ctx.Done():
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.inFlight--
	a.perLogin[request.Login]--

	if a.result != nil {
		return a.result(request), a.err
	}

	return agent.Result{Outcome: agent.OutcomeExited}, a.err
}

// running counts the agents in flight now.
func (a *fakeAgents) running() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.inFlight
}

func (a *fakeAgents) seen() []AgentRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]AgentRequest(nil), a.requests...)
}

// logins returns the login of each request, in order.
func (a *fakeAgents) logins() []string {
	var logins []string

	for _, request := range a.seen() {
		logins = append(logins, request.Login)
	}

	return logins
}

// fakeGate answers each call from results in order; the last result repeats.
type fakeGate struct {
	mu      sync.Mutex
	results []GateResult
	calls   int
	err     error
	via     gate
	// keys are the answers of Key in order; keyReads counts them.
	keys     []GateKey
	keyReads int
	keyErr   error
}

// Key answers from keys in order, the last repeating. With no keys it gives an empty key, so no gate reuses a pass.
func (g *fakeGate) Key(context.Context, string, string) (GateKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.keys) == 0 {
		return GateKey{}, g.keyErr
	}

	index := min(g.keyReads, len(g.keys)-1)
	g.keyReads++

	return g.keys[index], g.keyErr
}

// The gates below run a tree of their own and give no key, so the engine never reuses a pass for them.

func (*treeGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (packageGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (zeroTestGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (deadlineGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (slowGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (cancelGate) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (g *fakeGate) Run(ctx context.Context, project, item string) (GateResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.via != nil {
		g.calls++

		return g.via.Run(ctx, project, item)
	}

	index := min(g.calls, len(g.results)-1)
	g.calls++

	result := g.results[index]
	if result.Steps == nil {
		result.Steps = commandStep(result.Passed)
	}

	return result, g.err
}

// commandStep is the one step that a real gate gives from the exit code of its command.
func commandStep(passed bool) []GateStep {
	if passed {
		return []GateStep{{Name: "command", Status: "pass"}}
	}

	return []GateStep{{Name: "command", Status: "fail"}}
}

// fakeLogins picks byItem[item] when set and login otherwise.
type fakeLogins struct {
	login  string
	byItem map[string]string
	err    error
}

func (l fakeLogins) Pick(_ context.Context, _, item string, _ []string) (string, error) {
	if login, ok := l.byItem[item]; ok {
		return login, l.err
	}

	return l.login, l.err
}

// loginPick is one answer of loginSequence.
type loginPick struct {
	login string
	err   error
}

// loginSequence answers each Pick from picks in order. The last answer repeats. It counts the calls.
type loginSequence struct {
	mu    sync.Mutex
	calls int
	picks []loginPick
}

func (l *loginSequence) Pick(context.Context, string, string, []string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	pick := l.picks[min(l.calls, len(l.picks)-1)]
	l.calls++

	return pick.login, pick.err
}

func (l *loginSequence) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.calls
}

// fakeLifecycle records each step it runs and fails the steps named in failing.
type fakeLifecycle struct {
	mu       sync.Mutex
	steps    []string
	projects []string
	failing  map[string]bool
	takes    map[string]int
	released []string
	// failErr replaces errFake as the error of a failing step.
	failErr error
	// onClaim, when set, runs at the start of each Claim: a test uses it to move the clock during the take.
	onClaim func()
	// hang makes Release wait for its context to end, and expired counts the releases that the context ended.
	hang    bool
	expired int
}

func (l *fakeLifecycle) step(name, project, item string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.steps = append(l.steps, name+":"+item)
	l.projects = append(l.projects, project)

	if l.failing[name] {
		return cmp.Or(l.failErr, errFake)
	}

	return nil
}

// hangs reports whether Release waits for its context.
func (l *fakeLifecycle) hangs() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.hang
}

// expire counts a release that its context ended.
func (l *fakeLifecycle) expire() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.expired++
}

// expiries counts the releases that their context ended.
func (l *fakeLifecycle) expiries() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.expired
}

// Claim returns testOwner+item for the first take of an item and a numbered token for each later take.
func (l *fakeLifecycle) Claim(_ context.Context, project, item string) (string, error) {
	if l.onClaim != nil {
		l.onClaim()
	}

	err := l.step("claim", project, item)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.takes == nil {
		l.takes = map[string]int{}
	}

	l.takes[item]++

	if l.takes[item] == 1 {
		return testOwner + item, err
	}

	return fmt.Sprintf("%s%s#%d", testOwner, item, l.takes[item]), err
}

func (l *fakeLifecycle) Release(ctx context.Context, project, item, owner string) error {
	l.mu.Lock()
	l.released = append(l.released, owner)
	l.mu.Unlock()

	if l.hangs() {
		<-ctx.Done()
		l.expire()

		return ctx.Err()
	}

	return l.step("release", project, item)
}

// releasedBy lists the owner token of each release, in order.
func (l *fakeLifecycle) releasedBy() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.released...)
}

// projectsSeen lists the project of each lifecycle call, in order.
func (l *fakeLifecycle) projectsSeen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.projects...)
}

// fakeBeats is the fake tracker: it records each renewal and fails the owner named in failFor, with failure text or an error.
// onBeat, when set, runs in each renewal before it answers: a test uses it to move the clock during the call.
type fakeBeats struct {
	mu      sync.Mutex
	calls   []string
	failFor string
	text    string
	err     error
	onBeat  func()
}

func (b *fakeBeats) Beat(_ context.Context, project, owner string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, project+"/"+owner)
	hook, failing, text, err := b.onBeat, owner == b.failFor, b.text, b.err
	b.mu.Unlock()

	if hook != nil {
		hook()
	}

	if failing {
		return text, err
	}

	return "", nil
}

// hook sets the function that runs in each renewal.
func (b *fakeBeats) hook(f func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.onBeat = f
}

// fail makes every later renewal for owner fail with text or err.
func (b *fakeBeats) fail(owner, text string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.failFor, b.text, b.err = owner, text, err
}

func (b *fakeBeats) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]string(nil), b.calls...)
}

func (l *fakeLifecycle) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.steps...)
}

// harness holds the fakes and the options of one test. Each engine of the test shares one state folder.
type harness struct {
	agents    *fakeAgents
	gate      *fakeGate
	logins    fakeLogins
	picker    logins // replaces logins when set
	life      *fakeLifecycle
	merge     *fakeMerge
	preflight preflighter // nil unless a test sets it
	forge     *fakeForge
	watch     *fakeWatch
	trees     *fakeTrees
	beats     *fakeBeats
	commands  *fakeCommands
	items     *fakeItems
	writer    writer      // nil unless a test sets it
	hosts     hostStopper // nil unless a test sets it
	queue     queueReader // nil unless a test sets it
	poll      pollReader  // nil unless a test sets it
	// sandboxDefault is the project default for a step that sets no sandbox. It is nil, and so off, unless a test sets it.
	sandboxDefault func(project string) bool
	ticks          chan time.Time
	sleep          time.Duration
	opts           Options
	// freshStore makes the engine and the client create the database themselves, for a test of the files that the first open makes.
	freshStore bool
}

// defaultFlow is the workflow file of a harness that a test does not replace: one agent step.
const defaultFlow = "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n"

// defaultFlowName is the name of the default workflow file, which sits beside the state folder of the harness.
const defaultFlowName = "workflow.star"

// waitFlow is a workflow file with one agent step, then a question for the owner. A run of it waits for obee answer after its agent ends.
const waitFlow = "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n    wait(name = \"ask\", question = \"What now?\")\n"

// flowIs replaces the default workflow file of the harness. Call it before the first run.
func (h *harness) flowIs(t *testing.T, text string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName), []byte(text), 0o600))
}

// Run walks the default workflow file of the harness. It is a helper of the tests: the production engine runs only a file named by the caller.
func (e *Engine) Run(item, project string) (ItemResult, error) {
	return e.RunFile(item, project, filepath.Join(filepath.Dir(e.dir), defaultFlowName))
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	tree, _ := gitTree(t)
	folder := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(folder, defaultFlowName), []byte(defaultFlow), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "prompts", "work.md"), []byte("Work on the item.\n"), 0o600))

	life := &fakeLifecycle{failing: map[string]bool{}}

	return &harness{
		agents:   &fakeAgents{perLogin: map[string]int{}},
		gate:     &fakeGate{results: []GateResult{{Passed: true}}},
		logins:   fakeLogins{login: testLogin},
		life:     life,
		forge:    &fakeForge{life: life, state: review.Standing{Number: testPull}},
		watch:    &fakeWatch{outcomes: []review.Outcome{{Kind: review.Merged}}},
		trees:    &fakeTrees{dir: tree},
		beats:    &fakeBeats{},
		commands: &fakeCommands{},
		items:    &fakeItems{fields: map[string]string{"id": testItem, "title": "a title", "body": "a body"}},
		ticks:    make(chan time.Time),
		opts: Options{
			StateDir:      filepath.Join(folder, "state"),
			Version:       testVersion,
			MaxConcurrent: 2, MaxReviewConcurrent: 2,
			Env: map[string]string{},
		},
	}
}

func (h *harness) deps() Deps {
	deps := Deps{
		Agents: h.agents, Gate: h.gate, Logins: h.logins, Lifecycle: h.life, Forge: h.forge, Watch: h.watch, Trees: h.trees, Beats: h.beats, Commands: h.commands, Items: h.items, Writer: h.writer,
		Kept:  []error{errFake},
		Clock: clock{Tick: h.ticks, Poll: storePoll, Watch: cmp.Or(h.sleep, time.Millisecond), Settle: cmp.Or(h.sleep, time.Millisecond)},
	}
	if h.merge != nil {
		deps.Merge = h.merge
	}

	deps.Preflight = h.preflight
	deps.Hosts = h.hosts
	deps.Queue = h.queue
	deps.Poll = h.poll
	deps.SandboxDefault = h.sandboxDefault

	if h.picker != nil {
		deps.Logins = h.picker
	}

	return deps
}

// tick runs one status tick and returns when it has finished. The second send is taken only after the first tick ended.
func (h *harness) tick(t *testing.T) {
	t.Helper()

	for range 2 {
		h.ticks <- time.Time{}
	}
}

// migratedDatabase is the database file of a new state folder after the migrations of the workflow store, built once for the test process.
// The migrations parse and run many SQL statements, which costs more than the rest of a test under the race detector.
var migratedDatabase = sync.OnceValues(func() ([]byte, error) {
	dir, err := os.MkdirTemp("", "flow-template-")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(dir)

	opts := Options{StateDir: filepath.Join(dir, "state"), Version: testVersion, MaxConcurrent: minMaxConcurrent, MaxReviewConcurrent: minMaxConcurrent, Env: map[string]string{}}

	engine, err := NewEngine(context.Background(), opts, Deps{})
	if err != nil {
		return nil, err
	}

	if err = engine.Shutdown(); err != nil {
		return nil, err
	}

	return os.ReadFile(filepath.Join(opts.StateDir, databaseFile))
})

// migrated gives the state folder of the harness a database that has its migrations, when the folder has none. The first open of a folder
// without a database is tested by the tests that call NewEngine or OpenClient on their own.
func (h *harness) migrated(t *testing.T) {
	t.Helper()

	path := filepath.Join(h.opts.StateDir, databaseFile)
	if _, err := os.Stat(path); err == nil || h.freshStore {
		return
	}

	text, err := migratedDatabase()
	require.NoError(t, err)
	require.NoError(t, config.EnsureStateDir(h.opts.StateDir))
	require.NoError(t, os.WriteFile(path, text, privateMode))
}

// engine builds and launches an engine over the harness. The cleanup shuts it down.
func (h *harness) engine(t *testing.T) *Engine {
	t.Helper()

	h.migrated(t)

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// client opens a client over the harness state folder with the given build version.
func (h *harness) client(t *testing.T, version string) *Client {
	t.Helper()

	h.migrated(t)

	opts := h.opts
	opts.Version = version

	client, err := OpenClient(t.Context(), opts, ReleaseDeps{Lifecycle: h.life, Trees: h.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// eventually polls check until it returns true or eventTimeout passes.
func eventually(t *testing.T, check func() bool) {
	t.Helper()

	require.Eventually(t, check, eventTimeout, pollEvery)
}

// waitForTheSleep waits until the run has started a durable sleep. The sleep records its step when it starts, so a cancel sent after this lands inside the sleep and not in the step before it.
func waitForTheSleep(t *testing.T, engine *Engine) {
	t.Helper()

	eventually(t, func() bool {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)

		return err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" })
	})
}

// waitForTheOwner waits until the open run shows its question. The last verdict is recorded before the run asks, so a wait on the verdict count alone can read the run in between.
func waitForTheOwner(t *testing.T, h *harness) {
	t.Helper()

	client := h.client(t, testVersion)
	eventually(t, func() bool {
		infos, err := client.Status()

		return err == nil && len(infos) == 1 && infos[0].Question != ""
	})
}

// runInBackground runs an item on the engine and returns the channel that gets the outcome.
func runInBackground(engine *Engine, item string) <-chan runOutcome {
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.Run(item, testProject)
		done <- runOutcome{result: result, err: err}
	}()

	return done
}

type runOutcome struct {
	result ItemResult
	err    error
}

// fakeForge answers every read with state, or fails with err. It records the items it was asked about.
type fakeForge struct {
	mu    sync.Mutex
	life  *fakeLifecycle // the log that a Ready call goes to, in order with the claim and the release
	state review.Standing
	err   error
	asked []string
	// rewritten makes Reviewed report a head other than the reviewed commit as a rewrite of it.
	rewritten bool
	// failFrom, when above zero, makes the read of that number and every later read fail with errFake.
	failFrom int
	// pushAfterReady, when set, moves the head of the pull request to it once Ready has run: a push between the standing read and the ready call.
	pushAfterReady string
	// closing is the closing issues that each read of ClosingIssues gives, in order; the last repeats. closingReads counts the reads.
	closing      [][]int
	closingReads int
	closingErr   error // fails every read after the first closingOK reads
	closingOK    int
	noPullAfter  int
	noPull       bool // reports no pull request after the first noPullAfter reads
}

func (f *fakeForge) Pull(_ context.Context, _, item string) (review.Standing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, item)

	if f.failFrom > 0 && len(f.asked) >= f.failFrom {
		return review.Standing{}, errFake
	}

	return f.state, f.err
}

// ClosingIssues answers with the next of closing and then repeats the last. A set of nil is the empty set. noPull makes it report no pull request. closingErr fails every read.
func (f *fakeForge) ClosingIssues(_ context.Context, _, _ string) ([]int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closingReads++

	if f.closingErr != nil && f.closingReads > f.closingOK {
		return nil, false, f.closingErr
	}

	gone := f.noPull && f.closingReads > f.noPullAfter

	if gone || len(f.closing) == 0 {
		return nil, !gone, nil
	}

	return f.closing[min(f.closingReads, len(f.closing))-1], true, nil
}

// Reviewed answers as Pull does. When rewritten is set and the head is not the reviewed commit, the standing says the head is a rewrite of it.
func (f *fakeForge) Reviewed(ctx context.Context, project, item, reviewed string) (review.Standing, error) {
	standing, err := f.Pull(ctx, project, item)
	if f.rewritten && standing.Head != reviewed {
		standing.Rewritten = reviewed
	}

	return standing, err
}

// Ready records the call in the log of the lifecycle fake, and fails when a test sets failing["ready"].
func (f *fakeForge) Ready(_ context.Context, project, item string) error {
	err := f.life.step("ready", project, item)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pushAfterReady != "" {
		f.state.Head = f.pushAfterReady
	}

	return err
}

// fakeWatch answers each read with the next of outcomes and then repeats the last. A non-nil err fails every read.
// It records the pull requests it read and the draft calls.
type fakeWatch struct {
	mu       sync.Mutex
	outcomes []review.Outcome
	err      error
	errs     []error // the error of read n, the last one repeats; it replaces err
	draftErr error
	reads    []int
	drafts   []int
}

func (f *fakeWatch) Watch(_ context.Context, _ string, pr int) (review.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.reads = append(f.reads, pr)

	next := f.outcomes[min(len(f.reads), len(f.outcomes))-1]

	if len(f.errs) > 0 {
		return next, f.errs[min(len(f.reads), len(f.errs))-1]
	}

	return next, f.err
}

func (f *fakeWatch) SetDraft(_ context.Context, _ string, pr int, draft bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if draft {
		f.drafts = append(f.drafts, pr)
	}

	return f.draftErr
}

func (f *fakeWatch) seen() (reads, drafts []int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]int(nil), f.reads...), append([]int(nil), f.drafts...)
}

// fakeTrees answers every lookup with dir, or fails with err. A fakeTrees with late set has no folder until Ensure makes it.
type fakeTrees struct {
	dir  string
	err  error
	late *lateFolder
}

// lateFolder is a worktree that exists only after Ensure.
type lateFolder struct {
	mu   sync.Mutex
	dir  string
	made bool
}

func (l *lateFolder) path() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.made {
		return l.dir
	}

	return ""
}

func (l *lateFolder) make() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.made = true

	return l.dir
}

func (f fakeTrees) Path(context.Context, string, string) (string, error) {
	if f.late != nil {
		return f.late.path(), f.err
	}

	return f.dir, f.err
}

func (f fakeTrees) Ensure(context.Context, string, string) (string, error) {
	if f.late != nil {
		return f.late.make(), f.err
	}

	return f.dir, f.err
}
